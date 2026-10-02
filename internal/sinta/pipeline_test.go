package sinta

import (
	"fmt"
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

func (f *fakeStore) RankCounts() (map[int]int, error) {
	out := map[int]int{}
	for _, j := range f.journals {
		out[j.SintaRank]++
	}
	return out, nil
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
		BaseURL:       srv.URL,
		FilterData:    form,
		RunKey:        "rank-1",
		Logf:          t.Logf,
		ExpectedRanks: []int{1}, // jalur sehat: sanity rank harus TIDAK mengganggu OK
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
	if res.UniqueIDs != 20 {
		t.Errorf("UniqueIDs = %d, want 20", res.UniqueIDs)
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
	if res.UniqueIDs != 40 {
		t.Errorf("UniqueIDs = %d, want 40", res.UniqueIDs)
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

// TestRunSintaStageRepair: page2 sengaja salah (mengulang kartu page1) pada
// pass utama → defisit unik + dup terdeteksi → repair re-fetch {1,2} → page2
// benar → 20/20 unik = Verified. (Simulasi defisit kecil doc 19 Bagian 9.5.)
func TestRunSintaStageRepair(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	page1 := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		"Page 1 of 2 | Total Records 20")
	page2 := strings.ReplaceAll(page1, "/profile/", "/profile/9")

	var page2Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
			w.WriteHeader(http.StatusSeeOther)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			page2Hits++
			if page2Hits == 1 {
				w.Write([]byte(page1)) // pass utama: page2 SALAH (dup) → defisit
				return
			}
			w.Write([]byte(page2)) // setelah repair: page2 benar
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
		t.Fatalf("RunSintaStage: %v", err)
	}
	if res.RepairRounds != 1 || res.RepairPages != 2 {
		t.Errorf("repair = %d round/%d halaman, want 1/2 (halaman duplikat 2 ±1 → {1,2})",
			res.RepairRounds, res.RepairPages)
	}
	if res.RepairStop != "verifikasi-ok" {
		t.Errorf("RepairStop = %q, want verifikasi-ok", res.RepairStop)
	}
	if !res.Verified || res.UniqueIDs != 20 {
		t.Errorf("Verified=%v unik=%d, want true/20 (VerifyMsg=%q)", res.Verified, res.UniqueIDs, res.VerifyMsg)
	}
	if res.PagesSaved != 2 || res.JournalsSaved != 20 {
		t.Errorf("pass utama harus tetap 2 halaman/20 jurnal (repair tak boleh dobel), got %d/%d",
			res.PagesSaved, res.JournalsSaved)
	}
	if len(store.journals) != 20 {
		t.Errorf("jurnal unik di store = %d, want 20", len(store.journals))
	}
}

// TestRunSintaStageUnresolved: page2 selalu dup → repair round 1 memberi
// gain +0 (plateau) → fixpoint berhenti jujur (doc 24 I1), bukan klaim lengkap.
func TestRunSintaStageUnresolved(t *testing.T) {
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
		w.Write([]byte(page1)) // page=2 juga selalu page1 → defisit permanen
	}))
	defer srv.Close()

	store := newFakeStore()
	form, _ := BuildFilterForm("1")
	res, err := RunSintaStage(newTestSession(t), store, StageConfig{
		BaseURL: srv.URL, FilterData: form, RunKey: "rank-1", Logf: t.Logf,
	})
	if err != nil {
		t.Fatalf("RunSintaStage: %v", err)
	}
	if res.Verified {
		t.Error("defisit permanen tidak boleh Verified=true")
	}
	if !strings.HasPrefix(res.VerifyMsg, "INCOMPLETE") {
		t.Errorf("VerifyMsg = %q, harus diawali INCOMPLETE (kontrak I4, doc 24)", res.VerifyMsg)
	}
	if res.UniqueIDs != 10 || res.TotalRecords != 20 {
		t.Errorf("unik/server = %d/%d, want 10/20", res.UniqueIDs, res.TotalRecords)
	}
	if res.RepairRounds != 3 {
		t.Errorf("RepairRounds = %d, want 3 (L5 F1: plateau → ±2 → span → stop; alt mati, doc 29)", res.RepairRounds)
	}
	if res.RepairStop != "plateau" {
		t.Errorf("RepairStop = %q, want plateau", res.RepairStop)
	}
}

// TestRunSintaStageOnePass: -max-repair-rounds -1 = pass-1 murni tanpa
// repair/alt-sort (mode ukur eksperimen superset shielding, doc 28 L4).
// Subtest defisit: page2 selalu dup tapi round TIDAK jalan → INCOMPLETE +
// RepairStop "one-pass"; subtest lengkap: unik==T0 di pass-1 → VERIFIED.
func TestRunSintaStageOnePass(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}

	t.Run("defisit", func(t *testing.T) {
		page1 := strings.ReplaceAll(string(raw),
			"Page 1 of 1.678 | Total Records 16.772",
			"Page 1 of 2 | Total Records 20")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
				w.WriteHeader(http.StatusSeeOther)
				return
			}
			w.Write([]byte(page1)) // page=2 juga selalu page1 → defisit 10, tapi repair dilarang
		}))
		defer srv.Close()

		store := newFakeStore()
		form, _ := BuildFilterForm("1")
		res, err := RunSintaStage(newTestSession(t), store, StageConfig{
			BaseURL: srv.URL, FilterData: form, RunKey: "rank-1", Logf: t.Logf,
			MaxRepairRounds: -1,
		})
		if err != nil {
			t.Fatalf("RunSintaStage: %v", err)
		}
		if res.RepairRounds != 0 || res.RepairPages != 0 {
			t.Errorf("repair = %d round/%d halaman, want 0/0 (one-pass dilarang repair)", res.RepairRounds, res.RepairPages)
		}
		if res.RepairStop != "one-pass" {
			t.Errorf("RepairStop = %q, want one-pass", res.RepairStop)
		}
		if res.Verified || !strings.HasPrefix(res.VerifyMsg, "INCOMPLETE") {
			t.Errorf("Verified=%v msg=%q, want false + INCOMPLETE (kontrak I4)", res.Verified, res.VerifyMsg)
		}
		if res.UniqueIDs != 10 || res.TotalRecords != 20 {
			t.Errorf("unik/server = %d/%d, want 10/20", res.UniqueIDs, res.TotalRecords)
		}
		if res.T0Recheck != "" {
			t.Errorf("T0Recheck = %q, want \"\" (one-pass di luar jendela recheck L2)", res.T0Recheck)
		}
	})

	t.Run("lengkap", func(t *testing.T) {
		page1 := strings.ReplaceAll(string(raw),
			"Page 1 of 1.678 | Total Records 16.772",
			"Page 1 of 1 | Total Records 10")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
				w.WriteHeader(http.StatusSeeOther)
				return
			}
			w.Write([]byte(page1))
		}))
		defer srv.Close()

		store := newFakeStore()
		form, _ := BuildFilterForm("1")
		res, err := RunSintaStage(newTestSession(t), store, StageConfig{
			BaseURL: srv.URL, FilterData: form, RunKey: "rank-1", Logf: t.Logf,
			MaxRepairRounds: -1,
		})
		if err != nil {
			t.Fatalf("RunSintaStage: %v", err)
		}
		if res.RepairStop != "one-pass" {
			t.Errorf("RepairStop = %q, want one-pass", res.RepairStop)
		}
		if !res.Verified || res.UniqueIDs != 10 {
			t.Errorf("Verified=%v unik=%d, want true/10 (msg=%q)", res.Verified, res.UniqueIDs, res.VerifyMsg)
		}
	})
}

// TestRunSintaStageRegionEscalation (L5 F1, doc 29): plateau TIDAK langsung
// menyerah — region eskalasi bertingkat ±1 → ±2 → span component → stop (alt
// mati di test ini). Fixture 5 halaman: pass awal dup hanya di p3 (unik 40/50);
// semua fetch di repair phase mengembalikan konten dup → 3 plateau beruntun.
// Ekspektasi: 3 round, RepairPages 3+5+5 = 13, RepairStop "plateau", log tier.
func TestRunSintaStageRegionEscalation(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	base := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		"Page 1 of 5 | Total Records 50")
	p2 := strings.ReplaceAll(base, "/profile/", "/profile/2")
	p4 := strings.ReplaceAll(base, "/profile/", "/profile/4")
	p5 := strings.ReplaceAll(base, "/profile/", "/profile/5")

	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
			w.WriteHeader(http.StatusSeeOther)
			return
		}
		gets++
		if gets > 5 {
			w.Write([]byte(base)) // phase repair: semua hal dup (konten p1)
			return
		}
		switch r.URL.Query().Get("page") {
		case "2":
			w.Write([]byte(p2))
		case "3":
			w.Write([]byte(base)) // dup dgn p1 → dupPages={3}
		case "4":
			w.Write([]byte(p4))
		case "5":
			w.Write([]byte(p5))
		default:
			w.Write([]byte(base))
		}
	}))
	defer srv.Close()

	var logs []string
	store := newFakeStore()
	form, _ := BuildFilterForm("1")
	res, err := RunSintaStage(newTestSession(t), store, StageConfig{
		BaseURL: srv.URL, FilterData: form, RunKey: "rank-1",
		MaxRepairRounds: 6, RepairFreeze: 0, AltSort: 0, // alt mati → berhenti di tier terakhir
		Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
	})
	if err != nil {
		t.Fatalf("RunSintaStage: %v", err)
	}
	if res.RepairRounds != 3 || res.RepairPages != 13 {
		t.Errorf("repair = %d round/%d halaman, want 3/13 (±1=3hal → ±2=5hal → span=5hal)",
			res.RepairRounds, res.RepairPages)
	}
	if res.RepairStop != "plateau" {
		t.Errorf("RepairStop = %q, want plateau (tier habis, alt mati)", res.RepairStop)
	}
	if res.UniqueIDs != 40 || res.TotalRecords != 50 {
		t.Errorf("unik/server = %d/%d, want 40/50", res.UniqueIDs, res.TotalRecords)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"±1 → ±2", "→ span component"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log eskalasi %q tidak ditemukan; logs:\n%s", want, joined)
		}
	}
}

// TestRunSintaStageRepairBatchParalel (L5 F3, doc 29): repair round memakai
// pool cfg.Workers (fetchRepairBatch) — hasil WAJIB identik dgn skenario
// sekuensial TestRunSintaStageRepair (unik 20 = T0, 1 round, 2 halaman).
func TestRunSintaStageRepairBatchParalel(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	page1 := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		"Page 1 of 2 | Total Records 20")
	page2 := strings.ReplaceAll(page1, "/profile/", "/profile/9")

	var page2Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
			w.WriteHeader(http.StatusSeeOther)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			page2Hits++
			if page2Hits == 1 {
				w.Write([]byte(page1)) // pass utama: page2 dup → defisit; repair: benar
				return
			}
			w.Write([]byte(page2))
			return
		}
		w.Write([]byte(page1))
	}))
	defer srv.Close()

	store := newFakeStore()
	form, _ := BuildFilterForm("1")
	res, err := RunSintaStage(newTestSession(t), store, StageConfig{
		BaseURL: srv.URL, FilterData: form, RunKey: "rank-1",
		Workers: 4, // pool repair paralel (fetchRepairBatch)
		Logf:    t.Logf,
	})
	if err != nil {
		t.Fatalf("RunSintaStage: %v", err)
	}
	if !res.Verified || res.UniqueIDs != 20 {
		t.Errorf("Verified=%v unik=%d, want true/20 (msg=%q)", res.Verified, res.UniqueIDs, res.VerifyMsg)
	}
	if res.RepairRounds != 1 || res.RepairPages != 2 {
		t.Errorf("repair = %d round/%d halaman, want 1/2", res.RepairRounds, res.RepairPages)
	}
	if len(store.journals) != 20 {
		t.Errorf("jurnal unik di store = %d, want 20", len(store.journals))
	}
}

// TestRunSintaStageRepairMultiRound: fixpoint butuh 2 round — page2 benar
// pada hit ke-2, page3 benar pada hit ke-3; round 1 masih defisit (gain +10)
// → round 2 jalan → 30/30 unik = verifikasi-ok (doc 24 Tahap I1).
func TestRunSintaStageRepairMultiRound(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	base := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		"Page 1 of 3 | Total Records 30")
	pageB := strings.ReplaceAll(base, "/profile/", "/profile/9")
	pageC := strings.ReplaceAll(base, "/profile/", "/profile/8")

	var hit2, hit3 atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
			w.WriteHeader(http.StatusSeeOther)
			return
		}
		switch r.URL.Query().Get("page") {
		case "2":
			if hit2.Add(1) == 1 {
				w.Write([]byte(base)) // pass: salah (dup page1)
				return
			}
			w.Write([]byte(pageB)) // hit ke-2: benar
		case "3":
			if hit3.Add(1) <= 2 {
				w.Write([]byte(base)) // pass & round 1: masih salah
				return
			}
			w.Write([]byte(pageC)) // hit ke-3 (round 2): benar
		default:
			w.Write([]byte(base))
		}
	}))
	defer srv.Close()

	store := newFakeStore()
	form, _ := BuildFilterForm("1")
	res, err := RunSintaStage(newTestSession(t), store, StageConfig{
		BaseURL: srv.URL, FilterData: form, RunKey: "rank-1", Logf: t.Logf,
	})
	if err != nil {
		t.Fatalf("RunSintaStage: %v", err)
	}
	if res.TotalRecords != 30 || res.UniqueIDs != 30 {
		t.Errorf("unik/server = %d/%d, want 30/30 (VerifyMsg=%q)", res.UniqueIDs, res.TotalRecords, res.VerifyMsg)
	}
	if res.RepairRounds != 2 || res.RepairPages != 6 {
		t.Errorf("repair = %d round/%d halaman, want 2/6 (round 1: p1-3, round 2: p1-3)",
			res.RepairRounds, res.RepairPages)
	}
	if res.RepairStop != "verifikasi-ok" {
		t.Errorf("RepairStop = %q, want verifikasi-ok", res.RepairStop)
	}
	if !res.Verified {
		t.Errorf("Verified=false, VerifyMsg=%q", res.VerifyMsg)
	}
	if res.PagesSaved != 3 {
		t.Errorf("PagesSaved = %d, want 3 (repair tidak menghitung ganda pass)", res.PagesSaved)
	}
}

// TestRunSintaStageRepairSoftFreeze — Tahap I2 (doc 24): freeze p1..N
// menahan halaman beku dari refetch normal; saat plateau + masih ada
// kandidat tertahan → freeze dibuka (thaw) untuk round cadangan region
// penuh; lalu konvergen → verifikasi-ok. Skenario 4 halaman (T0=40),
// freeze=1: p1 tertahan di round 1-2 (bukti beku), p3 salah sampai hit-4
// (hanya round thaw yang bisa memulihkannya).
func TestRunSintaStageRepairSoftFreeze(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	base := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		"Page 1 of 4 | Total Records 40")
	pageB := strings.ReplaceAll(base, "/profile/", "/profile/9")
	pageC := strings.ReplaceAll(base, "/profile/", "/profile/8")
	pageD := strings.ReplaceAll(base, "/profile/", "/profile/7")

	var hit1, hit2, hit3 atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
			w.WriteHeader(http.StatusSeeOther)
			return
		}
		switch r.URL.Query().Get("page") {
		case "1":
			hit1.Add(1)
			w.Write([]byte(base)) // selalu dup (A) — tak pernah jadi penyumbang unik
		case "2":
			if hit2.Add(1) == 1 {
				w.Write([]byte(base)) // pass: salah (dup page1)
				return
			}
			w.Write([]byte(pageB)) // hit ke-2 (round 1): benar
		case "3":
			if hit3.Add(1) <= 3 {
				w.Write([]byte(base)) // pass, round 1 & 2: masih salah
				return
			}
			w.Write([]byte(pageC)) // hit ke-4 (round thaw): benar — TIDAK tercapai tanpa thaw
		default:
			w.Write([]byte(pageD)) // selalu benar sejak pass (D)
		}
	}))
	defer srv.Close()

	store := newFakeStore()
	form, _ := BuildFilterForm("1")
	res, err := RunSintaStage(newTestSession(t), store, StageConfig{
		BaseURL: srv.URL, FilterData: form, RunKey: "rank-1", Logf: t.Logf,
		RepairFreeze: 1,
	})
	if err != nil {
		t.Fatalf("RunSintaStage: %v", err)
	}
	if res.TotalRecords != 40 || res.UniqueIDs != 40 {
		t.Errorf("unik/server = %d/%d, want 40/40 (VerifyMsg=%q)", res.UniqueIDs, res.TotalRecords, res.VerifyMsg)
	}
	// round 1 {2,3,4} gain +10 → 30; round 2 {2,3,4} plateau + tertahan p1
	// → thaw; round 3 {1,2,3,4} p3 hit-4 benar → 40.
	if res.RepairRounds != 3 || res.RepairPages != 10 {
		t.Errorf("repair = %d round/%d halaman, want 3/10 (3+3+4 halaman)",
			res.RepairRounds, res.RepairPages)
	}
	if res.RepairStop != "verifikasi-ok" {
		t.Errorf("RepairStop = %q, want verifikasi-ok", res.RepairStop)
	}
	if !res.Verified {
		t.Errorf("Verified=false, VerifyMsg=%q", res.VerifyMsg)
	}
	if got := hit1.Load(); got != 2 {
		t.Errorf("p1 di-fetch %d kali, want 2 (pass + round thaw saja — freeze menahan round 1-2)", got)
	}
	if got := hit3.Load(); got != 4 {
		t.Errorf("p3 di-fetch %d kali, want 4 (pass + 2 round freeze + 1 round thaw)", got)
	}
	if res.PagesSaved != 4 {
		t.Errorf("PagesSaved = %d, want 4 (repair tidak menghitung ganda pass)", res.PagesSaved)
	}
}

// TestRunSintaStageRegionRecompute — Tahap L1 (doc 25): dua mode region.
// kumulatif (default): dupPages menumpuk dari pass → round 2 region =
// ±1 dari SEMUA dup pass ∪ round 1. recompute: tracker di-reset tiap round →
// round 2 region HANYA dari dup round 1. Skenario 4 halaman (T0=40):
// pass dup {2,3,4} → round 1 {1,2,3,4} (p1,p2 dup; p3,p4 baru +10 → 30) →
// round 2: recompute {1,2,3} (p4 TIDAK di-refetch) vs kumulatif {1,2,3,4}.
// p1 di round 2 memberi set terakhir → T0 di kedua mode.
func TestRunSintaStageRegionRecompute(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	base := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		"Page 1 of 4 | Total Records 40")
	pageB := strings.ReplaceAll(base, "/profile/", "/profile/9")
	pageC := strings.ReplaceAll(base, "/profile/", "/profile/8")
	pageD := strings.ReplaceAll(base, "/profile/", "/profile/7")

	tests := []struct {
		name      string
		mode      string
		wantPages int   // RepairPages: kumulatif 4+4, recompute 4+3
		wantHit4  int64 // p4 di-fetch: kumulatif 3 (pass+round1+round2), recompute 2
	}{
		{"kumulatif", "kumulatif", 8, 3},
		{"recompute", "recompute", 7, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hit1, hit2, hit3, hit4 atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
					w.WriteHeader(http.StatusSeeOther)
					return
				}
				switch r.URL.Query().Get("page") {
				case "1":
					if hit1.Add(1) <= 2 {
						w.Write([]byte(base)) // pass: A unik pertama; round 1: dup
						return
					}
					w.Write([]byte(pageD)) // round 2: set terakhir (+10 → T0)
				case "2":
					hit2.Add(1)
					w.Write([]byte(base)) // selalu dup (A) — pass, round 1 & 2
				case "3":
					if hit3.Add(1) == 1 {
						w.Write([]byte(base)) // pass: dup
						return
					}
					w.Write([]byte(pageB)) // round 1: +10 unik; round 2: dup
				case "4":
					if hit4.Add(1) == 1 {
						w.Write([]byte(base)) // pass: dup
						return
					}
					w.Write([]byte(pageC)) // round 1: +10 unik; round 2 (kumulatif): dup
				default:
					w.Write([]byte(base))
				}
			}))
			defer srv.Close()

			store := newFakeStore()
			form, _ := BuildFilterForm("1")
			res, err := RunSintaStage(newTestSession(t), store, StageConfig{
				BaseURL: srv.URL, FilterData: form, RunKey: "rank-1", Logf: t.Logf,
				RegionMode: tc.mode,
			})
			if err != nil {
				t.Fatalf("RunSintaStage: %v", err)
			}
			if res.TotalRecords != 40 || res.UniqueIDs != 40 {
				t.Errorf("unik/server = %d/%d, want 40/40 (VerifyMsg=%q)",
					res.UniqueIDs, res.TotalRecords, res.VerifyMsg)
			}
			if res.RepairRounds != 2 {
				t.Errorf("RepairRounds = %d, want 2", res.RepairRounds)
			}
			if res.RepairPages != tc.wantPages {
				t.Errorf("RepairPages = %d, want %d (round1=4 + round2=%d)",
					res.RepairPages, tc.wantPages, tc.wantPages-4)
			}
			if res.RepairStop != "verifikasi-ok" || !res.Verified {
				t.Errorf("RepairStop=%q Verified=%v, want verifikasi-ok/true", res.RepairStop, res.Verified)
			}
			if got := hit4.Load(); got != tc.wantHit4 {
				t.Errorf("p4 di-fetch %d kali, want %d (recompute TIDAK me-refetch p4 di round 2)",
					got, tc.wantHit4)
			}
		})
	}
}

// TestRunSintaStageT0Recheck — L2 (R7 doc 23 + doc 24): re-check T0 (+1
// request p1) HANYA saat repair berhenti plateau dgn defisit tersisa — guard
// deteksi perubahan total server di tengah run, bukan blocker round baru.
// Kontrak: T0 sama → catatan konfirmasi pada VerifyMsg; T0 berubah → TotalRecords
// di-update (verifier memakai angka terbaru), termasuk kasus T0 turun di bawah
// unik (unik > T0 baru → GAGAL + konteks berubah); flag mati → tanpa request ke-3.
func TestRunSintaStageT0Recheck(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	base := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		"Page 1 of 4 | Total Records 40")
	t0Naik := strings.ReplaceAll(base, "Total Records 40", "Total Records 45")
	t0Turun := strings.ReplaceAll(base, "Total Records 40", "Total Records 8")

	tests := []struct {
		name          string
		recheck       bool
		htmlRecheck   string // konten p1 pada re-check (fetch TERAKHIR, setelah loop L5: pass + 3 round repair)
		wantHit1      int64  // p1 di-fetch: pass + round {±1,±2,span} (+ recheck bila aktif)
		wantT0        int    // TotalRecords hasil verifier
		wantPrefix    string // prefix VerifyMsg
		wantSub       string // substring VerifyMsg
		wantRecheck   string // substring StageResult.T0Recheck ("" = cek kosong saja)
		wantEmptyRech bool   // true → T0Recheck harus kosong
	}{
		{"konfirmasi", true, base, 5, 40, "INCOMPLETE",
			"re-check T0: server masih 40", "konfirmasi", false},
		{"T0-naik", true, t0Naik, 5, 45, "INCOMPLETE",
			"T0 berubah 40→45 saat re-check plateau", "BERUBAH", false},
		{"T0-turun", true, t0Turun, 5, 8, "GAGAL:",
			"T0 berubah 40→8 saat re-check plateau", "BERUBAH", false},
		{"dimatikan", false, base, 4, 40, "INCOMPLETE",
			"ID unik 10 < 40 server", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hit1 atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
					w.WriteHeader(http.StatusSeeOther)
					return
				}
				switch r.URL.Query().Get("page") {
				case "1":
					// hit 1 = pass (unik); hit 2..4 = round repair L5 (±1, ±2,
					// span) → dup; hit ≥5 = re-check T0 pasca-loop.
					if hit1.Add(1) <= 4 {
						w.Write([]byte(base))
						return
					}
					w.Write([]byte(tc.htmlRecheck)) // re-check (fetch terakhir)
				default:
					w.Write([]byte(base)) // p2..p4 selalu dup (A) → round1 gain 0 → plateau
				}
			}))
			defer srv.Close()

			store := newFakeStore()
			form, _ := BuildFilterForm("1")
			res, err := RunSintaStage(newTestSession(t), store, StageConfig{
				BaseURL: srv.URL, FilterData: form, RunKey: "rank-1", Logf: t.Logf,
				T0Recheck: tc.recheck,
			})
			if err != nil {
				t.Fatalf("RunSintaStage: %v", err)
			}
			if res.RepairStop != "plateau" {
				t.Errorf("RepairStop=%q, want plateau", res.RepairStop)
			}
			if res.UniqueIDs != 10 || res.TotalRecords != tc.wantT0 {
				t.Errorf("unik/T0 = %d/%d, want 10/%d (VerifyMsg=%q)",
					res.UniqueIDs, res.TotalRecords, tc.wantT0, res.VerifyMsg)
			}
			if got := hit1.Load(); got != tc.wantHit1 {
				t.Errorf("p1 di-fetch %d kali, want %d", got, tc.wantHit1)
			}
			if !strings.HasPrefix(res.VerifyMsg, tc.wantPrefix) || !strings.Contains(res.VerifyMsg, tc.wantSub) {
				t.Errorf("VerifyMsg=%q, want prefix %q + substring %q",
					res.VerifyMsg, tc.wantPrefix, tc.wantSub)
			}
			if res.Verified {
				t.Errorf("Verified=true, want false (defisit tersisa)")
			}
			if tc.wantEmptyRech {
				if res.T0Recheck != "" {
					t.Errorf("T0Recheck=%q, want kosong saat flag mati", res.T0Recheck)
				}
			} else if !strings.Contains(res.T0Recheck, tc.wantRecheck) {
				t.Errorf("T0Recheck=%q, want substring %q", res.T0Recheck, tc.wantRecheck)
			}
		})
	}
}

// TestRunSintaStageT0RecheckMaksRound — L2 diperluas (keputusan user 2 Okt):
// jendela re-check T0 bukan hanya plateau, tapi SEMUA titik berhenti fixpoint
// dgn defisit. Live Run A (doc 26) berhenti maks-round dgn defisit 1 setelah
// fallback I2/I3 habis — kasus ini wajib ter-guard. Skenario: round1 gain >0
// tapi -max-repair-rounds 1 → maks-round + defisit → re-check p1 terpicu.
func TestRunSintaStageT0RecheckMaksRound(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	base := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		"Page 1 of 4 | Total Records 40")
	pageB := strings.ReplaceAll(base, "/profile/", "/profile/9")

	var hit1, hit2 atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
			w.WriteHeader(http.StatusSeeOther)
			return
		}
		switch r.URL.Query().Get("page") {
		case "1":
			if hit1.Add(1) <= 2 {
				w.Write([]byte(base)) // pass: unik; round 1: dup
				return
			}
			w.Write([]byte(base)) // re-check (fetch ke-3): T0 tetap 40
		case "2":
			if hit2.Add(1) == 1 {
				w.Write([]byte(base)) // pass: dup
				return
			}
			w.Write([]byte(pageB)) // round 1: +10 unik → gain >0 → lalu maks-round
		default:
			w.Write([]byte(base))
		}
	}))
	defer srv.Close()

	store := newFakeStore()
	form, _ := BuildFilterForm("1")
	res, err := RunSintaStage(newTestSession(t), store, StageConfig{
		BaseURL: srv.URL, FilterData: form, RunKey: "rank-1", Logf: t.Logf,
		MaxRepairRounds: 1, T0Recheck: true,
	})
	if err != nil {
		t.Fatalf("RunSintaStage: %v", err)
	}
	if res.RepairStop != "maks-round" {
		t.Errorf("RepairStop=%q, want maks-round", res.RepairStop)
	}
	if res.UniqueIDs != 20 || res.TotalRecords != 40 {
		t.Errorf("unik/T0 = %d/%d, want 20/40 (VerifyMsg=%q)",
			res.UniqueIDs, res.TotalRecords, res.VerifyMsg)
	}
	if got := hit1.Load(); got != 3 {
		t.Errorf("p1 di-fetch %d kali, want 3 (pass + round1 + recheck)", got)
	}
	if !strings.HasPrefix(res.VerifyMsg, "INCOMPLETE") ||
		!strings.Contains(res.VerifyMsg, "re-check T0: server masih 40") {
		t.Errorf("VerifyMsg=%q, want prefix INCOMPLETE + catatan recheck", res.VerifyMsg)
	}
	if !strings.Contains(res.T0Recheck, "konfirmasi") {
		t.Errorf("T0Recheck=%q, want contains konfirmasi", res.T0Recheck)
	}
}

// TestRunSintaStagePublisherRecovery — L3 (R9 doc 23 + doc 27): publisher
// recovery sebagai BACKSTOP. Katalog ekspektasi (id→affid) disuplai via
// StageConfig.RecoverCatalog; missing = katalog \ run → crawl partisi
// /journals/index/{affid} dengan sesi Sibling (sesi segar) → verifier ulang.
// Skenario: global 2 halaman identik (defisit = jumlah kartu fixture) →
// katalog semua ID pageB (affid 9) → partisi menyediakannya → VERIFIED_COMPLETE.
// Subtest kedua: tanpa katalog → skip (first-run buta, R9) & partisi tak disentuh.
func TestRunSintaStagePublisherRecovery(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	// Hitung kartu lewat PARSER (regex /profile/ menipu: link nav/footer ikut).
	parseIDs := func(s string) []int {
		pr, err := ParsePage(strings.NewReader(s), 1)
		if err != nil {
			t.Fatalf("ParsePage: %v", err)
		}
		ids := make([]int, 0, len(pr.Journals))
		for _, j := range pr.Journals {
			ids = append(ids, j.ID)
		}
		return ids
	}

	// Skenario disusun dari jumlah kartu fixture (tahan perubahan fixture):
	// global = 2 halaman identik (unik = kartu, p2 dup → plateau), T0 = 2×kartu
	// → defisit = kartu; katalog = semua ID pageB (affid 9); partisi
	// menyediakannya kembali → unik 2×kartu = T0 → VERIFIED_COMPLETE.
	nKartu := len(parseIDs(string(raw)))
	if nKartu == 0 {
		t.Fatal("fixture tanpa kartu")
	}
	t0 := 2 * nKartu
	base := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		fmt.Sprintf("Page 1 of 2 | Total Records %d", t0))
	pageB := strings.ReplaceAll(base, "/profile/", "/profile/9")
	partisi := strings.ReplaceAll(pageB,
		fmt.Sprintf("Page 1 of 2 | Total Records %d", t0),
		fmt.Sprintf("Page 1 of 1 | Total Records %d", nKartu))

	// Katalog: semua ID pageB (belum terlihat run global) → affID 9.
	katalog := map[int]int{}
	for _, id := range parseIDs(pageB) {
		katalog[id] = 9
	}
	if len(katalog) != nKartu {
		t.Fatalf("katalog ID = %d, want %d", len(katalog), nKartu)
	}

	tests := []struct {
		name      string
		katalog   map[int]int
		wantVerif bool // true → VERIFIED_COMPLETE setelah recovery
		wantPart  int  // partisi di-crawl
	}{
		{"dengan-katalog", katalog, true, 1},
		{"tanpa-katalog", nil, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hitGlobal, hitPartisi atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
					w.WriteHeader(http.StatusSeeOther)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/journals/index/") {
					hitPartisi.Add(1)
					w.Write([]byte(partisi)) // 10 kartu ID baru (pageB) — seluruh missing ketemu
					return
				}
				hitGlobal.Add(1)
				w.Write([]byte(base)) // p1 & p2 identik → dup → defisit → plateau
			}))
			defer srv.Close()

			store := newFakeStore()
			form, _ := BuildFilterForm("1")
			res, err := RunSintaStage(newTestSession(t), store, StageConfig{
				BaseURL: srv.URL + "/journals", FilterData: form, RunKey: "rank-1", Logf: t.Logf,
				RecoverCatalog: tc.katalog,
			})
			if err != nil {
				t.Fatalf("RunSintaStage: %v", err)
			}
			if res.RepairStop != "plateau" {
				t.Errorf("RepairStop=%q, want plateau", res.RepairStop)
			}
			if got := hitPartisi.Load(); got != int64(tc.wantPart) {
				t.Errorf("partisi di-hit %d kali, want %d", got, tc.wantPart)
			}
			if tc.wantVerif {
				if !res.Verified || !strings.HasPrefix(res.VerifyMsg, "VERIFIED_COMPLETE") {
					t.Errorf("Verified=%v msg=%q, want VERIFIED_COMPLETE", res.Verified, res.VerifyMsg)
				}
				if res.UniqueIDs != t0 || res.TotalRecords != t0 {
					t.Errorf("unik/T0 = %d/%d, want %d/%d", res.UniqueIDs, res.TotalRecords, t0, t0)
				}
				if res.RecoveryMissing != nKartu || res.RecoveryFound != nKartu ||
					res.RecoveryPartisi != 1 || res.RecoveryPages != 1 {
					t.Errorf("Recovery = missing %d found %d partisi %d pages %d, want %d/%d/1/1",
						res.RecoveryMissing, res.RecoveryFound, res.RecoveryPartisi, res.RecoveryPages, nKartu, nKartu)
				}
			} else {
				if res.Verified {
					t.Errorf("Verified=true tanpa katalog, want INCOMPLETE")
				}
				if !strings.HasPrefix(res.VerifyMsg, "INCOMPLETE") {
					t.Errorf("VerifyMsg=%q, want prefix INCOMPLETE", res.VerifyMsg)
				}
				if res.RecoveryMissing != 0 {
					t.Errorf("RecoveryMissing=%d tanpa katalog, want 0", res.RecoveryMissing)
				}
			}
		})
	}
}

// TestRunSintaStageRepairAltSort — Tahap I3 (doc 24): plateau pada sort
// awal → ChangeSort in-place (POST changesort di sesi HIDUP, tanpa sesi
// baru) → round cadangan dengan urutan baru → konvergen. Server test
// mensimulasikan server asli: perubahan sort TANPA cookie baru, dan isi
// halaman 2/3 berubah hanya setelah sort aktif (fase B probe live:
// VERDICT BERUBAH — doc 24 Tahap I3).
func TestRunSintaStageRepairAltSort(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	base := strings.ReplaceAll(string(raw),
		"Page 1 of 1.678 | Total Records 16.772",
		"Page 1 of 3 | Total Records 30")
	pageB := strings.ReplaceAll(base, "/profile/", "/profile/9")
	pageC := strings.ReplaceAll(base, "/profile/", "/profile/8")

	var altOn atomic.Bool // true setelah POST changesort diterima (sort aktif)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if r.Form.Get("changesort") == "1" {
				altOn.Store(true)
				w.WriteHeader(http.StatusSeeOther) // sesi hidup: TANPA cookie baru
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
			w.WriteHeader(http.StatusSeeOther)
			return
		}
		switch r.URL.Query().Get("page") {
		case "2":
			if altOn.Load() {
				w.Write([]byte(pageB)) // fase alt: benar (+10 unik)
				return
			}
			w.Write([]byte(base)) // fase awal: dup
		case "3":
			if altOn.Load() {
				w.Write([]byte(pageC)) // fase alt: benar (+10 unik)
				return
			}
			w.Write([]byte(base)) // fase awal: dup
		default:
			w.Write([]byte(base)) // p1 selalu dup (A)
		}
	}))
	defer srv.Close()

	store := newFakeStore()
	form, _ := BuildFilterForm("1")
	res, err := RunSintaStage(newTestSession(t), store, StageConfig{
		BaseURL: srv.URL, FilterData: form, RunKey: "rank-1", Logf: t.Logf,
		AltSort: 4,
	})
	if err != nil {
		t.Fatalf("RunSintaStage: %v", err)
	}
	if res.TotalRecords != 30 || res.UniqueIDs != 30 {
		t.Errorf("unik/server = %d/%d, want 30/30 (VerifyMsg=%q)", res.UniqueIDs, res.TotalRecords, res.VerifyMsg)
	}
	// L5 F1 (doc 29) — rantai fallback kini bertingkat SEBELUM alt-sort:
	// round 1 {1,2,3} pad ±1: gain 0 → pad ±2;
	// round 2 {1,2,3} pad ±2: gain 0 → pad span;
	// round 3 {1,2,3} span: gain 0 → ChangeSort in-place (pad reset ±1);
	// round 4 {1,2,3} sort baru: p2+p3 benar → +20 → T0.
	if res.RepairRounds != 4 || res.RepairPages != 12 {
		t.Errorf("repair = %d round/%d halaman, want 4/12 (±1 → ±2 → span → alt-sort → verifikasi)",
			res.RepairRounds, res.RepairPages)
	}
	if res.AltRounds != 1 {
		t.Errorf("AltRounds = %d, want 1 (hanya round setelah ganti sort)", res.AltRounds)
	}
	if res.RepairStop != "verifikasi-ok" {
		t.Errorf("RepairStop = %q, want verifikasi-ok", res.RepairStop)
	}
	if !res.Verified {
		t.Errorf("Verified=false, VerifyMsg=%q", res.VerifyMsg)
	}
	if res.PagesSaved != 3 {
		t.Errorf("PagesSaved = %d, want 3 (repair tidak menghitung ganda pass)", res.PagesSaved)
	}
}

// TestRunSintaStageGagalFilter — sanity doc 20 Bagian 2.4 (pakai baris run-ini):
// (a) server kembalikan rank salah (skenario bug value=[N]=1) → GAGAL-FILTER;
// (b) rank diminta hilang saat cakupan penuh tanpa resume → GAGAL-FILTER;
// (c) db punya rank lain dari run berbeda → TIDAK false-positive (sah).
func TestRunSintaStageGagalFilter(t *testing.T) {
	t.Run("salah-sasaran", func(t *testing.T) {
		// fixture selalu S1, tapi kita "minta" S5 → tertulis S1 → asing
		srv, _, _ := serveListing(t)
		store := newFakeStore()
		form, _ := BuildFilterForm("5")
		res, err := RunSintaStage(newTestSession(t), store, StageConfig{
			BaseURL: srv.URL, FilterData: form, RunKey: "rank-5",
			ExpectedRanks: []int{5}, Logf: t.Logf,
		})
		if err != nil {
			t.Fatalf("RunSintaStage: %v", err)
		}
		if res.Verified {
			t.Error("rank salah sasaran tidak boleh lolos Verified")
		}
		if !strings.HasPrefix(res.VerifyMsg, "GAGAL-FILTER") {
			t.Errorf("VerifyMsg = %q, harus diawali GAGAL-FILTER", res.VerifyMsg)
		}
		if !strings.Contains(res.VerifyMsg, "S1×20") {
			t.Errorf("VerifyMsg = %q, harus menyebut pelaku S1×20", res.VerifyMsg)
		}
	})

	t.Run("rank-hilang", func(t *testing.T) {
		srv, _, _ := serveListing(t) // semua kartu S1, tak ada S5
		store := newFakeStore()
		form, _ := BuildFilterForm("1,5")
		res, err := RunSintaStage(newTestSession(t), store, StageConfig{
			BaseURL: srv.URL, FilterData: form, RunKey: "rank-1,5",
			ExpectedRanks: []int{1, 5}, Logf: t.Logf,
		})
		if err != nil {
			t.Fatalf("RunSintaStage: %v", err)
		}
		if res.Verified {
			t.Error("rank hilang (S5) saat cakupan penuh tidak boleh lolos Verified")
		}
		if !strings.HasPrefix(res.VerifyMsg, "GAGAL-FILTER") {
			t.Errorf("VerifyMsg = %q, harus diawali GAGAL-FILTER", res.VerifyMsg)
		}
		if !strings.Contains(res.VerifyMsg, "S5") {
			t.Errorf("VerifyMsg = %q, harus menyebut S5 yang hilang", res.VerifyMsg)
		}
	})

	t.Run("db-lintas-rank-tidak-false-positive", func(t *testing.T) {
		// db sudah berisi rank lain (hasil run berbeda) → run -rank 1 tetap OK
		srv, _, _ := serveListing(t)
		store := newFakeStore()
		store.journals[9999] = Journal{ID: 9999, Name: "Milik Run Lain", SintaRank: 5, SourcePage: 9}
		form, _ := BuildFilterForm("1")
		res, err := RunSintaStage(newTestSession(t), store, StageConfig{
			BaseURL: srv.URL, FilterData: form, RunKey: "rank-1",
			ExpectedRanks: []int{1}, Logf: t.Logf,
		})
		if err != nil {
			t.Fatalf("RunSintaStage: %v", err)
		}
		if !res.Verified || !strings.HasPrefix(res.VerifyMsg, "VERIFIED_COMPLETE") {
			t.Errorf("db multi-rank sah tidak boleh memicu GAGAL-FILTER, got Verified=%v msg=%q",
				res.Verified, res.VerifyMsg)
		}
	})
}

func TestBuildFilterForm(t *testing.T) {
	want := map[string]string{
		"1":     "filter_accreditation[1]=1&filter_journals=1",
		"3":     "filter_accreditation[3]=3&filter_journals=1",
		"2-4":   "filter_accreditation[2]=2&filter_accreditation[3]=3&filter_accreditation[4]=4&filter_journals=1",
		"5":     "filter_accreditation[5]=5&filter_journals=1",
		"1,5":   "filter_accreditation[1]=1&filter_accreditation[5]=5&filter_journals=1",
		"5,1":   "filter_accreditation[1]=1&filter_accreditation[5]=5&filter_journals=1", // selalu urut naik
		"1,3-4": "filter_accreditation[1]=1&filter_accreditation[3]=3&filter_accreditation[4]=4&filter_journals=1",
		"1,1":   "filter_accreditation[1]=1&filter_journals=1", // dedupe
		"all":   "",
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
	for _, bad := range []string{"7", "3-1", "0", "x", "1,", "1-7", "2,8", "-1", "1,,2"} {
		if _, err := BuildFilterForm(bad); err == nil {
			t.Errorf("BuildFilterForm(%q) seharusnya error", bad)
		}
	}
}

func TestParseRankSet(t *testing.T) {
	cases := map[string][]int{
		"1":     {1},
		"5":     {5},
		"1-5":   {1, 2, 3, 4, 5},
		"1,5":   {1, 5},
		"5,1":   {1, 5}, // urut naik
		"1,3-4": {1, 3, 4},
		"2-3,5": {2, 3, 5},
	}
	for rank, expected := range cases {
		got, err := ParseRankSet(rank)
		if err != nil {
			t.Errorf("ParseRankSet(%q): %v", rank, err)
			continue
		}
		if len(got) != len(expected) {
			t.Errorf("ParseRankSet(%q) = %v, want %v", rank, got, expected)
			continue
		}
		for i := range expected {
			if got[i] != expected[i] {
				t.Errorf("ParseRankSet(%q) = %v, want %v", rank, got, expected)
				break
			}
		}
	}
	if got, err := ParseRankSet("all"); err != nil || got != nil {
		t.Errorf("ParseRankSet(\"all\") = %v, %v; want nil, nil", got, err)
	}
	for _, bad := range []string{"7", "0", "x", "2-1"} {
		if _, err := ParseRankSet(bad); err == nil {
			t.Errorf("ParseRankSet(%q) seharusnya error", bad)
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
