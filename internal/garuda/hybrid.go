package garuda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// Jalur anti-WAF hybrid (approve user 4 Okt 2026 — doc 35, doktrin doc 34 §6c):
// jalur std (UA riset D2) menangani host normal; balasan 403 di-fallback ke
// tls-client (profil Chrome_152 — menyelesaikan kelas WAF TLS) + solver
// ON-DEMAND MURNI-GO (BrowserSolve: chromedp + browser bawaan-OS — fakta
// doc 35 §7 r3: Edge LIVE SOLVED 11,3 dtk + confirm 200; fallback legacy
// python/nodriver via env SINTA_SOLVER_IMPL=python) bila body = challenge
// Cloudflare → cf_clearance+UA dipakai ulang per-host utk request berikutnya.
const (
	// chromeUA konsisten dgn profil Chrome_152 (paket terbukti E5-eksp:
	// tls-client 200 utk kelas TLS & 200 dgn cookie utk kelas challenge).
	// Host TANPA WAF tak pernah melihat UA ini — jalur std tetap uaDefault.
	chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"

	solverScript = "scripts/solve_cf.py"
	solverWait   = 120 * time.Second
)

// challengeFetcher = satu GET via transport anti-WAF. Implementasi produksi:
// tlsFetcher (tls-client). Dlm test: fake bertingkat.
type challengeFetcher interface {
	get(rawURL, referer, ua, cookie string) (status int, body []byte, err error)
}

// solverFunc = solve challenge Cloudflare sekali-jalan → kredensial per-host.
// Implementasi: BrowserSolve (Go, default) / runSolver (python, env legacy);
// dlm test: fake.
type solverFunc func(rawURL string) (cookie, ua string, err error)

// hostCred = cf_clearance + UA persis yg dipegang cookie (ikat IP+UA).
type hostCred struct {
	cookie string
	ua     string
}

// Hybrid = lapis fallback 403 pada Client. Aman dipakai berbarengan:
// seluruh Fetch diserialisasi mutex (fallback langka — hanya host ber-WAF;
// juga mencegah >1 instance Chrome solver hidup bersamaan).
type Hybrid struct {
	mu     sync.Mutex
	fetch  challengeFetcher // lazy — tlsFetcher saat pertama dipakai
	solve  solverFunc       // lazy — runSolver saat pertama dipakai
	cache  map[string]hostCred
	solves int // observability utk laporan/test
}

// NewHybrid membuat lapis hybrid kosong (transport/solver diinisialisasi lazy).
func NewHybrid() *Hybrid {
	return &Hybrid{cache: map[string]hostCred{}}
}

// Fetch = GET penuh jalur anti-WAF:
//  1. tls-client dgn kredensial cache host bila ada (cookie mati → kebaca
//     sebagai challenge → otomatis ke langkah 2), tanpa cookie → chromeUA.
//  2. bila balasan = challenge Cloudflare → solver (sekali) → cache kredensial
//     → retry SEKALI dgn cookie+UA solver. Hasil retry dikembalikan apa adanya
//     (tanpa loop) — klasifikasi retry tetap di Client.Get.
//
// Status non-403 non-200 (404 dsb.) TIDAK memicu solve — solver hanya utk
// challenge (body "Just a moment"/cdn-cgi/cf_chl — fakta fixture §6c).
func (h *Hybrid) Fetch(rawURL, referer string) (int, []byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.fetch == nil {
		f, err := newTLSFetcher()
		if err != nil {
			return 0, nil, fmt.Errorf("init tls-client: %w", err)
		}
		h.fetch = f
	}
	if h.solve == nil {
		h.solve = pickSolver()
	}

	host := hostOf(rawURL)
	cred := h.cache[host]
	ua, cookie := chromeUA, cred.cookie
	if cookie != "" {
		ua = cred.ua
	}

	st, body, err := h.fetch.get(rawURL, referer, ua, cookie)
	if err != nil || !isChallenge(st, body) {
		return st, body, err
	}

	// Challenge: cookie lama (bila dipakai) sudah mati / belum ada → solve.
	newCookie, newUA, serr := h.solve(rawURL)
	if serr != nil {
		return st, body, fmt.Errorf("challenge %s: solver gagal: %w", host, serr)
	}
	h.solves++
	if newUA == "" {
		newUA = chromeUA
	}
	if newCookie != "" {
		h.cache[host] = hostCred{cookie: newCookie, ua: newUA}
	}
	return h.fetch.get(rawURL, referer, newUA, newCookie)
}

// Solves = berapa kali solver dijalankan (observability).
func (h *Hybrid) Solves() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.solves
}

// isChallenge = deteksi balasan challenge Cloudflare. Fakta fixture e5waf-*:
// body memuat "Just a moment" (judul), jejak skrip /cdn-cgi/challenge-platform/,
// atau variabel cf_chl_opt. Status WAJIB 403 — halaman200 normal juga memuat
// beacon cdn-cgi (fakta e5waf-tls-j911home) jadi status jadi penjaga utama.
func isChallenge(status int, body []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	s := string(body)
	return strings.Contains(s, "Just a moment") ||
		strings.Contains(s, "/cdn-cgi/challenge-platform/") ||
		strings.Contains(s, "cf_chl_opt") ||
		strings.Contains(s, "cf-chl-")
}

// hostOf = kunci cache per-host (port diabaikan — cookie discope host).
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// tlsFetcher = challengeFetcher via bogdanfinn/tls-client profil Chrome_152:
// TLS/H2 fingerprint Chrome (ClientHello), header set = setiap proyek (doc 14
// Bagian 3) + UA profil — identitas browser utuh hanya di jalur WAF.
type tlsFetcher struct {
	cli tls_client.HttpClient
}

func newTLSFetcher() (*tlsFetcher, error) {
	cli, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(),
		tls_client.WithTimeoutSeconds(30),
		tls_client.WithClientProfile(profiles.Chrome_152),
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
	)
	if err != nil {
		return nil, err
	}
	return &tlsFetcher{cli: cli}, nil
}

func (t *tlsFetcher) get(rawURL, referer, ua, cookie string) (int, []byte, error) {
	req, err := fhttp.NewRequest("GET", rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	if ua == "" {
		ua = chromeUA
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en;q=0.8")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := t.cli.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("baca body: %w", err)
	}
	return resp.StatusCode, body, nil
}

// pickSolver = pemilih solver: default = BrowserSolve (murni-Go, browser
// Chromium ADAPTIF bawaan-OS — Chrome→Edge→Chromium, tanpa Python; fakta r3
// doc 35 §7). Env SINTA_SOLVER_IMPL=python → runSolver (fallback legacy
// scripts/solve_cf.py, dipertahankan utk diagnosa/regresi — jangan dihapus
// tanpa approve).
func pickSolver() solverFunc {
	if os.Getenv("SINTA_SOLVER_IMPL") == "python" {
		return runSolver
	}
	return BrowserSolve
}

// solverResult = kontrak JSON stdout scripts/solve_cf.py (satu baris).
type solverResult struct {
	OK     bool   `json:"ok"`
	Cookie string `json:"cookie"`
	UA     string `json:"ua"`
	Status int    `json:"status"`
	MS     int64  `json:"ms"`
	Err    string `json:"err"`
}

// runSolver = eksekusi subproses solver LEGACY (headed Chrome via nodriver,
// ±8 s bila challenge; timeout 120 s) — aktif hanya bila
// SINTA_SOLVER_IMPL=python (default kini BrowserSolve, Go murni).
// Env: SINTA_PYTHON (default "python"), SINTA_SOLVER (default scripts/solve_cf.py).
func runSolver(rawURL string) (string, string, error) {
	python := os.Getenv("SINTA_PYTHON")
	if python == "" {
		python = "python"
	}
	script := os.Getenv("SINTA_SOLVER")
	if script == "" {
		script = solverScript
	}
	ctx, cancel := context.WithTimeout(context.Background(), solverWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, script, rawURL)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("solver jalan: %w (stderr: %s)", err, trunc(errb.String(), 300))
	}
	return parseSolverOut(out.String())
}

// parseSolverOut = ambil objek JSON terakhir dari stdout (tahan noise).
func parseSolverOut(s string) (cookie, ua string, err error) {
	line := strings.TrimSpace(s)
	if i := strings.LastIndex(line, "{"); i >= 0 {
		if j := strings.LastIndex(line, "}"); j > i {
			line = line[i : j+1]
		}
	}
	var r solverResult
	if jerr := json.Unmarshal([]byte(line), &r); jerr != nil {
		return "", "", fmt.Errorf("parse output solver: %w (stdout: %s)", jerr, trunc(s, 300))
	}
	if !r.OK {
		msg := strings.TrimSpace(r.Err)
		if msg == "" {
			msg = "ok=false tanpa keterangan"
		}
		return "", "", fmt.Errorf("solver: %s", msg)
	}
	return r.Cookie, r.UA, nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
