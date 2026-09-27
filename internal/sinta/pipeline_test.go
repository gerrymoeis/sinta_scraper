package sinta

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStore = JournalStore in-memory (storage asli sudah diuji di paket storage).
type fakeStore struct {
	journals  map[int]Journal
	checklist map[string]map[int]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{journals: map[int]Journal{}, checklist: map[string]map[int]bool{}}
}

func (f *fakeStore) UpsertJournals(js []Journal) (UpsertReport, error) {
	var rep UpsertReport
	for _, j := range js {
		old, ok := f.journals[j.ID]
		switch {
		case !ok:
			rep.New++
		case old == j:
			rep.Unchanged++
		default:
			rep.Updated++
			rep.Changes = append(rep.Changes, JournalChange{ID: j.ID, Name: j.Name})
		}
		f.journals[j.ID] = j
	}
	return rep, nil
}

func (f *fakeStore) MarkPageCompleted(runKey string, page int) error {
	if f.checklist[runKey] == nil {
		f.checklist[runKey] = map[int]bool{}
	}
	f.checklist[runKey][page] = true
	return nil
}

func (f *fakeStore) ClearCheckpoint(runKey string) error {
	delete(f.checklist, runKey)
	return nil
}

func (f *fakeStore) CompletedPages(runKey string) (map[int]bool, error) {
	cp := map[int]bool{}
	for p, ok := range f.checklist[runKey] {
		cp[p] = ok
	}
	return cp, nil
}

// serveListing menyiapkan server tiruan: POST → 303 + ci_session; GET page=1
// → fixture dengan pagination "2 halaman / 20 record"; GET page=2 → fixture
// yang sama tapi ID profil diberi awalan 9 (10 jurnal unik berbeda).
func serveListing(t *testing.T) (srv *httptest.Server, posts, gets *int) {
	t.Helper()
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	page1 := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		"Page 1 of 2 | Total Records 20")
	page2 := strings.ReplaceAll(page1, "/profile/", "/profile/9")

	var p, g int
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			p++
			http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
			w.WriteHeader(http.StatusSeeOther)
		case http.MethodGet:
			g++
			if _, err := r.Cookie("ci_session"); err != nil {
				t.Errorf("GET tanpa cookie ci_session — jar tidak bekerja?")
				http.Error(w, "no cookie", http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("page") == "2" {
				w.Write([]byte(page2))
			} else {
				w.Write([]byte(page1))
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &p, &g
}

// serveListingParalel = varian 4 halaman (40 record) + counter atomik + jeda
// 15ms per GET — memaksa fetch halaman 2-4 tumpang tindih (uji pool + -race).
func serveListingParalel(t *testing.T) (srv *httptest.Server, posts, gets *atomic.Int64) {
	t.Helper()
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	base := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		"Page 1 of 4 | Total Records 40")
	pages := map[string]string{
		"1": base,
		"2": strings.ReplaceAll(base, "/profile/", "/profile/9"),
		"3": strings.ReplaceAll(base, "/profile/", "/profile/8"),
		"4": strings.ReplaceAll(base, "/profile/", "/profile/7"),
	}
	var p, g atomic.Int64
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			p.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
			w.WriteHeader(http.StatusSeeOther)
		case http.MethodGet:
			g.Add(1)
			if _, err := r.Cookie("ci_session"); err != nil {
				t.Errorf("GET tanpa cookie ci_session — jar tidak bekerja?")
				http.Error(w, "no cookie", http.StatusUnauthorized)
				return
			}
			time.Sleep(15 * time.Millisecond) // beri ruang tumpang tindih antar worker
			if html, ok := pages[r.URL.Query().Get("page")]; ok {
				w.Write([]byte(html))
			} else {
				w.Write([]byte(base))
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &p, &g
}

func newTestSession(t *testing.T) *Session {
	t.Helper()
	s, err := NewSession("test", "test-ua/1.0", 0, 0)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	s.backoffs = []time.Duration{0, 0, 0}
	return s
}

func TestRunSintaStagePenuh(t *testing.T) {
	srv, posts, gets := serveListing(t)
	store := newFakeStore()
	form, err := BuildFilterForm("1")
	if err != nil {
		t.Fatalf("BuildFilterForm: %v", err)
	}

	res, err := RunSintaStage(newTestSession(t), store, StageConfig{
		BaseURL:    srv.URL,
		FilterData: form,
		RunKey:     "rank-1",
		Logf:       t.Logf,
	})
	if err != nil {
		t.Fatalf("RunSintaStage: %v", err)
	}

	if *posts != 1 {
		t.Errorf("POST filter = %d, want 1", *posts)
	}
	if *gets != 2 {
		t.Errorf("GET = %d, want 2 (auto-detect page 1 + page 2)", *gets)
	}
	if res.TotalPages != 2 || res.TotalRecords != 20 {
		t.Errorf("auto-detect = %d halaman/%d record, want 2/20", res.TotalPages, res.TotalRecords)
	}
	if res.PagesSaved != 2 || res.JournalsSaved != 20 {
		t.Errorf("saved = %d halaman/%d jurnal, want 2/20", res.PagesSaved, res.JournalsSaved)
	}
	if !res.Verified {
		t.Errorf("Verified = false, VerifyMsg = %q", res.VerifyMsg)
	}
	if len(store.journals) != 20 {
		t.Errorf("jurnal unik di store = %d, want 20", len(store.journals))
	}
	if !store.checklist["rank-1"][1] || !store.checklist["rank-1"][2] {
		t.Error("checkpoint halaman 1/2 belum tercentang")
	}
	if j, ok := store.journals[671]; !ok || !strings.Contains(j.Name, "Jurnal Pendidikan IPA Indonesia") {
		t.Errorf("jurnal 671 tidak tersimpan benar (ada=%v)", ok)
	}
}

func TestRunSintaStageResume(t *testing.T) {
	srv, posts, gets := serveListing(t)
	store := newFakeStore()
	form, _ := BuildFilterForm("1")
	cfg := StageConfig{BaseURL: srv.URL, FilterData: form, RunKey: "rank-1", Logf: t.Logf}

	// Run pertama: penuh
	res1, err := RunSintaStage(newTestSession(t), store, cfg)
	if err != nil {
		t.Fatalf("run 1: err=%v", err)
	}
	if !res1.Verified {
		t.Fatalf("run 1: verified=fale, VerifyMsg=%q", res1.VerifyMsg)
	}

	// Run kedua (sesi baru, seperti rerun asli): semua halaman tercentang
	*posts, *gets = 0, 0
	res2, err := RunSintaStage(newTestSession(t), store, cfg)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if res2.PagesSaved != 0 || res2.JournalsSaved != 0 {
		t.Errorf("run 2 menyimpan %d halaman/%d jurnal, want 0/0", res2.PagesSaved, res2.JournalsSaved)
	}
	if res2.PagesSkipped != 2 {
		t.Errorf("run 2 skipped = %d, want 2", res2.PagesSkipped)
	}
	if res2.Verified {
		t.Error("run resume seharusnya tidak memverifikasi (transparan via VerifyMsg)")
	}
	if *gets != 1 {
		t.Errorf("run 2 GET = %d, want 1 (hanya auto-detect)", *gets)
	}
	if *posts != 1 {
		t.Errorf("run 2 POST = %d, want 1 (filter baru per sesi baru)", *posts)
	}
}

func TestRunSintaStageGagalSebagian(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	page1 := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		"Page 1 of 2 | Total Records 20")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
			w.WriteHeader(http.StatusSeeOther)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusServiceUnavailable) // halaman 2 selalu gagal
			return
		}
		w.Write([]byte(page1))
	}))
	defer srv.Close()

	store := newFakeStore()
	form, _ := BuildFilterForm("1")
	res, err := RunSintaStage(newTestSession(t), store, StageConfig{
		BaseURL: srv.URL, FilterData: form, RunKey: "rank-1", Logf: t.Logf,
	})
	if err != nil {
		t.Fatalf("run dengan 1 halaman gagal seharusnya TIDAK abort: %v", err)
	}
	if res.PagesFailed != 1 || res.PagesSaved != 1 {
		t.Errorf("failed=%d saved=%d, want 1/1", res.PagesFailed, res.PagesSaved)
	}
	if res.Verified {
		t.Error("verifikasi harus dilewati saat ada halaman gagal")
	}
	if !strings.Contains(res.VerifyMsg, "gagal") {
		t.Errorf("VerifyMsg = %q, harus menjelaskan kegagalan", res.VerifyMsg)
	}
	if store.checklist["rank-1"][2] {
		t.Error("halaman 2 tidak boleh tercentang")
	}
}

func TestRunSintaStageParalel(t *testing.T) {
	srv, posts, gets := serveListingParalel(t)
	store := newFakeStore()
	form, err := BuildFilterForm("1")
	if err != nil {
		t.Fatalf("BuildFilterForm: %v", err)
	}

	res, err := RunSintaStage(newTestSession(t), store, StageConfig{
		BaseURL:    srv.URL,
		FilterData: form,
		RunKey:     "rank-1",
		Workers:    4,
		Logf:       t.Logf,
	})
	if err != nil {
		t.Fatalf("RunSintaStage: %v", err)
	}

	if posts.Load() != 1 {
		t.Errorf("POST filter = %d, want 1", posts.Load())
	}
	if gets.Load() != 4 {
		t.Errorf("GET = %d, want 4 (halaman 1-4, tanpa retry)", gets.Load())
	}
	if res.PagesSaved != 4 || res.JournalsSaved != 40 || res.PagesFailed != 0 {
		t.Errorf("saved=%d halaman/%d jurnal failed=%d, want 4/40/0",
			res.PagesSaved, res.JournalsSaved, res.PagesFailed)
	}
	if !res.Verified {
		t.Errorf("Verified=false, VerifyMsg=%q", res.VerifyMsg)
	}
	// Invariant (doc 16 Bagian 5): tidak ada kartu yang hilang/dobel antara
	// hitungan panjang slice (JournalsSaved) dan klasifikasi storage.
	if res.JournalsNew+res.JournalsUpdated+res.JournalsUnchanged != res.JournalsSaved {
		t.Errorf("invariant jurnal: %d+%d+%d != %d",
			res.JournalsNew, res.JournalsUpdated, res.JournalsUnchanged, res.JournalsSaved)
	}
	if res.JournalsNew+res.JournalsUpdated+res.JournalsUnchanged != res.JournalsSaved {
		t.Errorf("invariant jurnal: %d+%d+%d != %d",
			res.JournalsNew, res.JournalsUpdated, res.JournalsUnchanged, res.JournalsSaved)
	}
	if len(store.journals) != 40 {
		t.Errorf("jurnal unik = %d, want 40", len(store.journals))
	}
	for p := 1; p <= 4; p++ {
		if !store.checklist["rank-1"][p] {
			t.Errorf("checkpoint halaman %d belum tercentang", p)
		}
	}
}

// TestRunSintaStageRefresh memastikan -refresh: checkpoint di-wipe → semua
// halaman discrape ulang, kartu identik terklasifikasi tidak berubah (doc 16).
func TestRunSintaStageRefresh(t *testing.T) {
	srv, posts, gets := serveListing(t)
	store := newFakeStore()
	form, _ := BuildFilterForm("1")
	cfg := StageConfig{BaseURL: srv.URL, FilterData: form, RunKey: "rank-1", Logf: t.Logf}

	if _, err := RunSintaStage(newTestSession(t), store, cfg); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	*posts, *gets = 0, 0

	cfg.Refresh = true
	res2, err := RunSintaStage(newTestSession(t), store, cfg)
	if err != nil {
		t.Fatalf("run 2 (-refresh): %v", err)
	}
	if res2.PagesSkipped != 0 {
		t.Errorf("refresh: skipped=%d, want 0 (checkpoint di-wipe)", res2.PagesSkipped)
	}
	if res2.PagesSaved != 2 || res2.JournalsSaved != 20 {
		t.Errorf("refresh: saved=%d halaman/%d jurnal, want 2/20", res2.PagesSaved, res2.JournalsSaved)
	}
	if res2.JournalsNew != 0 || res2.JournalsUpdated != 0 || res2.JournalsUnchanged != 20 {
		t.Errorf("refresh: baru=%d diperbarui=%d tidak-berubah=%d, want 0/0/20",
			res2.JournalsNew, res2.JournalsUpdated, res2.JournalsUnchanged)
	}
	if res2.JournalsNew+res2.JournalsUpdated+res2.JournalsUnchanged != res2.JournalsSaved {
		t.Errorf("invariant jurnal: %d != %d",
			res2.JournalsNew+res2.JournalsUpdated+res2.JournalsUnchanged, res2.JournalsSaved)
	}
	if *gets != 2 {
		t.Errorf("refresh: GET=%d, want 2 (semua halaman discrape ulang)", *gets)
	}
	if *posts != 1 {
		t.Errorf("refresh: POST=%d, want 1", *posts)
	}
	if !res2.Verified {
		t.Errorf("refresh: Verified=false, VerifyMsg=%q", res2.VerifyMsg)
	}
}

func TestBuildFilterForm(t *testing.T) {
	want := map[string]string{
		"1":   "filter_accreditation[1]=1&filter_journals=1",
		"3":   "filter_accreditation[3]=1&filter_journals=1",
		"2-4": "filter_accreditation[2]=1&filter_accreditation[3]=1&filter_accreditation[4]=1&filter_journals=1",
		"all": "",
	}
	for rank, expected := range want {
		got, err := BuildFilterForm(rank)
		if err != nil {
			t.Errorf("BuildFilterForm(%q): %v", rank, err)
			continue
		}
		if got != expected {
			t.Errorf("BuildFilterForm(%q) = %q, want %q", rank, got, expected)
		}
	}
	for _, bad := range []string{"7", "3-1", "0", "x", "1,2"} {
		if _, err := BuildFilterForm(bad); err == nil {
			t.Errorf("BuildFilterForm(%q) seharusnya error", bad)
		}
	}
}

func TestRunKeyFor(t *testing.T) {
	if got := RunKeyFor("", "", "1"); got != "rank-1" {
		t.Errorf("rank: %q, want rank-1", got)
	}
	if got := RunKeyFor("", "", "1-3"); got != "rank-1-3" {
		t.Errorf("range: %q, want rank-1-3", got)
	}
	fk := RunKeyFor("filter_accreditation[1]=1", "", "1")
	qk := RunKeyFor("", "sinta=6", "1")
	if !strings.HasPrefix(fk, "filter-") || !strings.HasPrefix(qk, "query-") {
		t.Errorf("hash key = %q / %q, harus diawali filter-/query-", fk, qk)
	}
	if fk == qk {
		t.Error("hash filter dan query tidak boleh sama")
	}
}
