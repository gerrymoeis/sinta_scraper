package sinta

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

func (f *fakeStore) UpsertJournals(js []Journal) error {
	for _, j := range js {
		f.journals[j.ID] = j
	}
	return nil
}

func (f *fakeStore) MarkPageCompleted(runKey string, page int) error {
	if f.checklist[runKey] == nil {
		f.checklist[runKey] = map[int]bool{}
	}
	f.checklist[runKey][page] = true
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
