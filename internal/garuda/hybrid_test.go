package garuda

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"sinta-scraper/internal/sinta"
)

// fakeFetch = challengeFetcher bertingkat: jawaban diambil berurutan dari
// steps (bila habis, ulangi terakhir) + mencatat tiap panggilan.
type fakeFetch struct {
	mu    sync.Mutex
	steps []fetchStep
	calls []string // "ua|cookie" tiap panggilan
}

type fetchStep struct {
	st   int
	body string
	err  error
}

func (f *fakeFetch) get(rawURL, referer, ua, cookie string) (int, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ua+"|"+cookie)
	i := len(f.calls) - 1
	if i >= len(f.steps) {
		i = len(f.steps) - 1
	}
	s := f.steps[i]
	return s.st, []byte(s.body), s.err
}

type fakeSolver struct {
	mu      sync.Mutex
	calls   int
	cookie  string
	ua      string
	err     error
	lastURL string
}

func (s *fakeSolver) run(rawURL string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastURL = rawURL
	return s.cookie, s.ua, s.err
}

func newTestHybrid(f *fakeFetch, s *fakeSolver) *Hybrid {
	h := NewHybrid()
	h.fetch = f
	h.solve = s.run
	return h
}

func TestIsChallenge(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"403 just-a-moment", 403, "<title>Just a moment...</title>", true},
		{"403 cdn-cgi", 403, `<script src="/cdn-cgi/challenge-platform/x"></script>`, true},
		{"403 cf_chl_opt", 403, `var cf_chl_opt = {cType: 'managed'}`, true},
		{"403 cf-chl-", 403, `id="cf-chl-widget"`, true},
		{"403 polos", 403, "Forbidden", false},
		{"200 dgn beacon cdn-cgi", 200, "/cdn-cgi/challenge-platform/", false},
		{"200 dgn teks just a moment", 200, "Just a moment", false},
		{"404 challenge-ish", 404, "Just a moment", false},
	}
	for _, c := range cases {
		if got := isChallenge(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%s: isChallenge = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://joiv.org/index.php/joiv/oai?verb=Identify": "joiv.org",
		"http://jtrolis.ub.ac.id:8080/x":                    "jtrolis.ub.ac.id",
		"bukan-url":                                         "",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSolverOut(t *testing.T) {
	// kasus sukses
	ck, ua, err := parseSolverOut(`{"ok":true,"cookie":"cf_clearance=abc","ua":"Chrome/154","status":200,"ms":7500,"err":""}`)
	if err != nil || ck != "cf_clearance=abc" || ua != "Chrome/154" {
		t.Fatalf("sukses: ck=%q ua=%q err=%v", ck, ua, err)
	}
	// sukses dgn noise sebelum JSON (stdout boleh kotor)
	ck, _, err = parseSolverOut("warning: blah\n{\"ok\":true,\"cookie\":\"c=1\",\"ua\":\"u\"}")
	if err != nil || ck != "c=1" {
		t.Fatalf("noise: ck=%q err=%v", ck, err)
	}
	// solver melapor gagal
	_, _, err = parseSolverOut(`{"ok":false,"cookie":"","ua":"","status":0,"ms":1000,"err":"challenge tak lolos"}`)
	if err == nil || !strings.Contains(err.Error(), "challenge tak lolos") {
		t.Fatalf("ok=false: err=%v", err)
	}
	// ok=false tanpa keterangan → pesan default
	_, _, err = parseSolverOut(`{"ok":false}`)
	if err == nil || !strings.Contains(err.Error(), "ok=false") {
		t.Fatalf("ok=false kosong: err=%v", err)
	}
	// output bukan JSON
	_, _, err = parseSolverOut("Traceback: something exploded")
	if err == nil || !strings.Contains(err.Error(), "parse output solver") {
		t.Fatalf("bukan JSON: err=%v", err)
	}
}

// Kelas TLS: 403 challenge tanpa cookie → solve → retry dgn cookie+UA solver → 200.
func TestFetchSolveRetry(t *testing.T) {
	f := &fakeFetch{steps: []fetchStep{
		{st: 403, body: "<title>Just a moment...</title>"},
		{st: 200, body: "HALAMAN-ASLI"},
	}}
	s := &fakeSolver{cookie: "cf_clearance=z", ua: "Chrome/154-ua"}
	h := newTestHybrid(f, s)

	st, body, err := h.Fetch("https://joiv.org/index.php/joiv", DefaultReferer)
	if err != nil {
		t.Fatal(err)
	}
	if st != 200 || string(body) != "HALAMAN-ASLI" {
		t.Fatalf("st=%d body=%q", st, string(body))
	}
	if s.calls != 1 || h.Solves() != 1 {
		t.Fatalf("solve calls=%d solves=%d, want 1", s.calls, h.Solves())
	}
	if len(f.calls) != 2 {
		t.Fatalf("fetch calls=%d, want 2", len(f.calls))
	}
	if !strings.HasSuffix(f.calls[0], "|") {
		t.Errorf("panggilan-1 tanpa cookie: %q", f.calls[0])
	}
	if f.calls[1] != "Chrome/154-ua|cf_clearance=z" {
		t.Errorf("panggilan-2 = %q, want cookie+UA solver", f.calls[1])
	}
	// kredensial ter-cache utk host → request berikutnya langsung bawa cookie
	if got := h.cache["joiv.org"]; got.cookie != "cf_clearance=z" || got.ua != "Chrome/154-ua" {
		t.Errorf("cache = %+v", got)
	}
}

// Cache terisi tapi cookie sudah mati → tetap challenge → re-solve.
func TestFetchCookieMatiResolve(t *testing.T) {
	f := &fakeFetch{steps: []fetchStep{
		{st: 403, body: "Just a moment..."},
		{st: 200, body: "OK"},
	}}
	s := &fakeSolver{cookie: "cf_clearance=baru", ua: "Chrome/154-ua"}
	h := newTestHybrid(f, s)
	h.cache["joiv.org"] = hostCred{cookie: "cf_clearance=mati", ua: "Chrome/154-ua"}

	st, _, err := h.Fetch("https://joiv.org/x", "")
	if err != nil || st != 200 {
		t.Fatalf("st=%d err=%v", st, err)
	}
	if f.calls[0] != "Chrome/154-ua|cf_clearance=mati" {
		t.Errorf("panggilan-1 harus bawa cookie lama: %q", f.calls[0])
	}
	if s.calls != 1 {
		t.Errorf("solve calls=%d, want 1 (re-solve)", s.calls)
	}
	if got := h.cache["joiv.org"]; got.cookie != "cf_clearance=baru" {
		t.Errorf("cache belum diperbarui: %+v", got)
	}
}

// 403 TANPA penanda challenge → jangan panggil solver.
func TestFetch403TanpaMarkerTanpaSolve(t *testing.T) {
	f := &fakeFetch{steps: []fetchStep{{st: 403, body: "Forbidden by policy"}}}
	s := &fakeSolver{}
	h := newTestHybrid(f, s)

	st, _, err := h.Fetch("https://x.org/a", "")
	if err != nil || st != 403 {
		t.Fatalf("st=%d err=%v", st, err)
	}
	if s.calls != 0 || h.Solves() != 0 {
		t.Errorf("solve calls=%d, want 0", s.calls)
	}
	if len(f.calls) != 1 {
		t.Errorf("fetch calls=%d, want 1 (tanpa retry)", len(f.calls))
	}
}

// 200 (kelas TLS selesai di lapis tls-client) → tanpa solve, satu panggilan.
func TestFetchLangsung200(t *testing.T) {
	f := &fakeFetch{steps: []fetchStep{{st: 200, body: "halo"}}}
	s := &fakeSolver{}
	h := newTestHybrid(f, s)

	st, body, err := h.Fetch("https://jtrolis.ub.ac.id/x", "ref")
	if err != nil || st != 200 || string(body) != "halo" {
		t.Fatalf("st=%d body=%q err=%v", st, body, err)
	}
	if s.calls != 0 {
		t.Errorf("solve calls=%d, want 0", s.calls)
	}
	if f.calls[0] != chromeUA+"|" {
		t.Errorf("panggilan-1 = %q, want chromeUA tanpa cookie", f.calls[0])
	}
}

// Solver gagal → error berstatus 403 (terminal di Client.Get), tanpa retry fetch.
func TestFetchSolverGagal(t *testing.T) {
	f := &fakeFetch{steps: []fetchStep{{st: 403, body: "Just a moment..."}}}
	s := &fakeSolver{err: errors.New("nodriver tak terpasang")}
	h := newTestHybrid(f, s)

	st, _, err := h.Fetch("https://joiv.org/x", "")
	if st != 403 {
		t.Fatalf("st=%d, want 403", st)
	}
	if err == nil || !strings.Contains(err.Error(), "solver gagal") ||
		!strings.Contains(err.Error(), "nodriver") {
		t.Fatalf("err=%v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("fetch calls=%d, want 1 (tanpa retry)", len(f.calls))
	}
}

// Transport error langsung diteruskan (tanpa solve).
func TestFetchTransportError(t *testing.T) {
	f := &fakeFetch{steps: []fetchStep{{st: 0, err: errors.New("connection reset")}}}
	s := &fakeSolver{}
	h := newTestHybrid(f, s)

	_, _, err := h.Fetch("https://x.org/", "")
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("err=%v", err)
	}
	if s.calls != 0 {
		t.Errorf("solve calls=%d, want 0", s.calls)
	}
}

// ---------- level Client (httptest) ----------

func newTestClient(hyb *Hybrid, tr http.RoundTripper) *Client {
	return &Client{
		http:     &http.Client{Transport: tr},
		ua:       "ua-riset-test",
		stage:    "test-hybrid",
		limiter:  sinta.NewLimiter(0, 0),
		backoffs: []time.Duration{time.Millisecond},
		hyb:      hyb,
	}
}

// std 403 → fallback hybrid sukses → body hybrid dikembalikan; std hanya 1×.
func TestGetFallbackHybrid(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	f := &fakeFetch{steps: []fetchStep{{st: 200, body: "LEWAT-HYBRID"}}}
	h := newTestHybrid(f, &fakeSolver{})
	c := newTestClient(h, http.DefaultTransport)

	body, st, err := c.Get(srv.URL+"/jurnal", "https://ref.example/")
	if err != nil || st != 200 || string(body) != "LEWAT-HYBRID" {
		t.Fatalf("st=%d body=%q err=%v", st, body, err)
	}
	if n != 1 {
		t.Errorf("std GET = %d, want 1 (fallback tidak mengulang std)", n)
	}
	if f.calls[0] != chromeUA+"|" {
		t.Errorf("hybrid call = %q", f.calls[0])
	}
}

// std 200 → jalur hybrid TIDAK dipanggil sama sekali.
func TestGet200TanpaHybrid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "langsung-ok")
	}))
	defer srv.Close()

	f := &fakeFetch{}
	h := newTestHybrid(f, &fakeSolver{})
	c := newTestClient(h, http.DefaultTransport)

	body, st, err := c.Get(srv.URL, "")
	if err != nil || st != 200 || string(body) != "langsung-ok" {
		t.Fatalf("st=%d body=%q err=%v", st, body, err)
	}
	if len(f.calls) != 0 {
		t.Errorf("hybrid calls=%d, want 0", len(f.calls))
	}
}

// Gagal solve = terminal: tidak di-backoff (std tetap 1×), error menyebut solver.
func TestGetSolverGagalTerminal(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	f := &fakeFetch{steps: []fetchStep{{st: 403, body: "Just a moment..."}}}
	h := newTestHybrid(f, &fakeSolver{err: errors.New("python tak ada")})
	c := newTestClient(h, http.DefaultTransport)

	_, st, err := c.Get(srv.URL, "")
	if st != 403 {
		t.Fatalf("st=%d, want 403", st)
	}
	if err == nil || !strings.Contains(err.Error(), "python tak ada") {
		t.Fatalf("err=%v", err)
	}
	if n != 1 {
		t.Errorf("std GET = %d, want 1 (terminal — tanpa backoff)", n)
	}
}

// 404 → tidak menyentuh hybrid, tidak di-retry.
func TestGet404TanpaHybrid(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		http.NotFound(w, r)
	}))
	defer srv.Close()

	f := &fakeFetch{}
	c := newTestClient(newTestHybrid(f, &fakeSolver{}), http.DefaultTransport)

	_, st, err := c.Get(srv.URL, "")
	if st != 404 || err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("st=%d err=%v", st, err)
	}
	if n != 1 {
		t.Errorf("std GET = %d, want 1", n)
	}
	if len(f.calls) != 0 {
		t.Errorf("hybrid calls=%d, want 0", len(f.calls))
	}
}

// pickSolver: default = BrowserSolve (Go murni, tanpa Python — approve r3);
// SINTA_SOLVER_IMPL=python → runSolver (fallback legacy).
func TestPickSolver(t *testing.T) {
	t.Setenv("SINTA_SOLVER_IMPL", "")
	if got := reflect.ValueOf(pickSolver()).Pointer(); got != reflect.ValueOf(BrowserSolve).Pointer() {
		t.Errorf("default solver bukan BrowserSolve (ptr=%x)", got)
	}
	t.Setenv("SINTA_SOLVER_IMPL", "python")
	if got := reflect.ValueOf(pickSolver()).Pointer(); got != reflect.ValueOf(runSolver).Pointer() {
		t.Errorf("SINTA_SOLVER_IMPL=python bukan runSolver (ptr=%x)", got)
	}
}

// tlsFetcher: scheme http:// (cleartext) wajib lewat getCleartext - tls-client
// (fhttp + profil TLS) menolak cleartext dgn "http2: unsupported scheme"
// (fakta run E7gh 7 Okt 2026: 11 host http:// gagal total walau std 403).
func TestTLSFetcherCleartextHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("tanpa User-Agent")
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden"))
	}))
	defer srv.Close()

	f, err := newTLSFetcher()
	if err != nil {
		t.Fatalf("newTLSFetcher: %v", err)
	}
	st, body, err := f.get(srv.URL, "", chromeUA, "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if st != http.StatusForbidden || string(body) != "Forbidden" {
		t.Errorf("st=%d body=%q, want 403 Forbidden", st, body)
	}
}
