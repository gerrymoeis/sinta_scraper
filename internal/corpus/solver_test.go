package corpus

import (
	"strings"
	"testing"

	"sinta-scraper/internal/storage"
)

// mockSolver = SolverGetter tiruan (C3b-C): panggilan → body/status tetap.
type mockSolver struct {
	hits   int
	body   string
	status int
	err    error
}

func (m *mockSolver) Get(_, _ string) ([]byte, int, error) {
	m.hits++
	return []byte(m.body), m.status, m.err
}

// TestGetRetrySolverGate — GATE E7g: jalur normal (SolverMode=false) TIDAK
// PERNAH menyentuh solver walau 403. SolverMode=true → 403/468 → solver;
// status normal (200) → solver tetap tak dipanggil; solver gagal → balasan
// jujur (403).
func TestGetRetrySolverGate(t *testing.T) {
	const u = "https://j1.test/oai?verb=ListRecords"

	t.Run("mode-mati-403-tanpa-solver", func(t *testing.T) {
		m := &mockGetter{}
		m.set(u, "<html>Just a moment</html>", 403)
		sol := &mockSolver{body: "SOLVED", status: 200}
		cfg := corpusCfg(m)
		cfg.Solver = sol // ADA solver tapi SolverMode=false
		_, st, _, _ := getRetry(cfg, u, "")
		if st != 403 {
			t.Errorf("status = %d — mau 403 (E7g: jalur normal tanpa solver)", st)
		}
		if sol.hits != 0 {
			t.Errorf("solver dipanggil %d× — mau 0", sol.hits)
		}
	})

	t.Run("mode-aktif-403-solver-menang", func(t *testing.T) {
		m := &mockGetter{}
		m.set(u, "<html>Just a moment</html>", 403)
		sol := &mockSolver{body: "<?xml solved", status: 200}
		cfg := corpusCfg(m)
		cfg.Solver, cfg.SolverMode = sol, true
		b, st, _, err := getRetry(cfg, u, "")
		if err != nil || st != 200 || string(b) != "<?xml solved" {
			t.Fatalf("b/st/err = %q/%d/%v — mau hasil solver 200", b, st, err)
		}
		if sol.hits != 1 {
			t.Errorf("solver hits = %d — mau 1", sol.hits)
		}
	})

	t.Run("mode-aktif-safeline-468", func(t *testing.T) {
		m := &mockGetter{}
		m.set(u, "safeline", 468)
		sol := &mockSolver{body: "ok", status: 200}
		cfg := corpusCfg(m)
		cfg.Solver, cfg.SolverMode = sol, true
		_, st, _, _ := getRetry(cfg, u, "")
		if st != 200 || sol.hits != 1 {
			t.Errorf("st/hits = %d/%d — mau 200/1 (468 = WAF SafeLine)", st, sol.hits)
		}
	})

	t.Run("mode-aktif-200-tanpa-solver", func(t *testing.T) {
		m := &mockGetter{}
		m.set(u, "<?xml normal", 200)
		sol := &mockSolver{body: "SOLVED", status: 200}
		cfg := corpusCfg(m)
		cfg.Solver, cfg.SolverMode = sol, true
		b, st, _, _ := getRetry(cfg, u, "")
		if st != 200 || string(b) != "<?xml normal" || sol.hits != 0 {
			t.Errorf("st/b/hits = %d/%q/%d — mau 200/normal/0 (host normal = GetPlain)", st, b, sol.hits)
		}
	})

	t.Run("mode-aktif-solver-gagal-jujur", func(t *testing.T) {
		m := &mockGetter{}
		m.set(u, "challenge", 403)
		sol := &mockSolver{body: "masih 403", status: 403}
		cfg := corpusCfg(m)
		cfg.Solver, cfg.SolverMode = sol, true
		_, st, _, _ := getRetry(cfg, u, "")
		if st != 403 {
			t.Errorf("st = %d — mau 403 jujur (solve gagal)", st)
		}
	})
}

// TestStageCorpusSolverFallback — j1: OAI 403 → API kosong → landing
// deny (host asing) → SOLVER sukses → OAI ASLI jadi primer: 3 artikel
// source="oai" + provenance ber-URL OAI (bukan "api"/landing).
func TestStageCorpusSolverFallback(t *testing.T) {
	st := seedCorpusDB(t, false, false)
	m := &mockGetter{}
	m.set(oaiJ1, "<html><title>Just a moment...</title></html>", 403) // GetPlain selalu 403
	m.set(oaiJ1p2, oaiPage2J1, 200)                                   // halaman resumption lanjut (cookie lolos / konten normal)
	m.set(oaiJ4, noRecordsXML, 200)
	// Crossref primer & DOAJ tak di-set → 404 default (API kosong)
	// landing: j1.test = host asing → deny (0 GET landing)
	sol := &mockSolver{body: oaiPage1J1, status: 200}

	cfg := corpusCfg(m)
	cfg.Ranks = []int{1}
	cfg.Solver = sol
	rep, err := RunCorpusStage(st, cfg)
	if err != nil {
		t.Fatalf("RunCorpusStage: %v", err)
	}
	if rep.SolverOK != 1 || rep.OAIMatik != 0 || rep.APIFallback != 0 || rep.LandingOK != 0 {
		t.Errorf("solver/matik/api/landing = %d/%d/%d/%d — mau 1/0/0/0",
			rep.SolverOK, rep.OAIMatik, rep.APIFallback, rep.LandingOK)
	}
	if rep.LandingGET != 0 {
		t.Errorf("LandingGET = %d — mau 0 (host asing ditolak tanpa GET)", rep.LandingGET)
	}
	if rep.CrossrefGET != 2 || rep.DoajGET != 1 {
		t.Errorf("crossref/doaj GET = %d/%d — mau 2/1 (1 utk API-fallback + 1 pelengkap lisensi jalur OAI)",
			rep.CrossrefGET, rep.DoajGET)
	}
	if sol.hits == 0 {
		t.Fatal("solver tak pernah dipanggil")
	}
	arts, err := st.Articles(1)
	if err != nil || len(arts) != 3 {
		t.Fatalf("artikel = %d err=%v — mau 3 (jalur OAI penuh, sama dgn happy path)", len(arts), err)
	}
	for _, a := range arts {
		if a.Source != storage.ArticleSourceOAI {
			t.Errorf("source = %q — mau oai (OAI asli terselamatkan)", a.Source)
		}
		if !strings.Contains(a.Provenance, "j1.test/index.php/j1/oai") {
			t.Errorf("provenance tanpa jejak OAI: %s", a.Provenance)
		}
		if strings.Contains(a.Provenance, `"api"`) {
			t.Errorf("provenance tak boleh bawa api utk jalur solver: %s", a.Provenance)
		}
	}
}

// TestStageCorpusSolverGagalJujur — semua jalur mati (OAI 403, API 404,
// landing deny, solver gagal) → OAI MATI jujur tanpa baris.
func TestStageCorpusSolverGagalJujur(t *testing.T) {
	st := seedCorpusDB(t, false, false)
	m := &mockGetter{}
	m.set(oaiJ1, "<html>challenge</html>", 403)
	m.set(oaiJ4, noRecordsXML, 200)
	sol := &mockSolver{body: "tetap diblokir", status: 403}

	cfg := corpusCfg(m)
	cfg.Ranks = []int{1}
	cfg.Solver = sol
	rep, err := RunCorpusStage(st, cfg)
	if err != nil {
		t.Fatalf("RunCorpusStage: %v", err)
	}
	if rep.OAIMatik != 1 || rep.SolverOK != 0 || rep.Processed != 0 {
		t.Errorf("matik/solver/processed = %d/%d/%d — mau 1/0/0", rep.OAIMatik, rep.SolverOK, rep.Processed)
	}
	if sol.hits == 0 {
		t.Error("solver harus dicoba dulu sebelum menyerah")
	}
	arts, err := st.Articles(1)
	if err != nil || len(arts) != 0 {
		t.Errorf("artikel = %d err=%v — mau 0 (semua jalur gagal = tanpa baris)", len(arts), err)
	}
}
