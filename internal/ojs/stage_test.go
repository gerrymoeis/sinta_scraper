package ojs

import (
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"sinta-scraper/internal/storage"
	"strings"
	"testing"
)

// ==== stage_test.go — driver F3.3 (fake PlainGetter — tanpa jaringan) ====
//
// 5 kasus: happy path (canonical+fees+provenance+flag) · resume skip ·
// NO_FEE_PAGE · tanpa kandidat · semua network-fail (Err, tanpa terminal).

// fakeGetter = PlainGetter tiruan: map URL → (body, status, final, err).
// GetCount memantau panggilan GET (assert resume = 0 GET, pagu kandidat).
type fakeGetter struct {
	responses map[string]fakeResp
	gets      int
}

type fakeResp struct {
	body   []byte
	status int
	final  string
	err    error
}

func (f *fakeGetter) GetPlain(rawURL, referer string) ([]byte, int, string, error) {
	f.gets++
	r, ok := f.responses[rawURL]
	if !ok {
		return nil, 404, rawURL, nil // URL tak dikenal = 404 jujur
	}
	return r.body, r.status, r.final, r.err
}

// homeOJS = halaman home OJS dgn link fee (≥1KB utk ambang canonical).
func homeOJS(withFeeLink bool) []byte {
	links := `<a href="/index.php/about/fees">Author Fees</a><a href="/index.php/about">About</a>`
	if !withFeeLink {
		links = `<a href="/index.php/about">About</a>`
	}
	body := `<html><head><title>Jurnal</title></head><body>
<meta name="citation_journal_title" content="Jurnal Contoh"/>
<p>Open Journal Systems — PKP. Portal jurnal contoh dgn cukup banyak teks
supaya body melewati ambang 1 KB canonical resolve. ` + strings.Repeat("isi halaman yang cukup panjang untuk ambang. ", 40) + `
</p>` + links + `</body></html>`
	return []byte(body)
}

const feePageMillah = `<html><body><h2>Author Fees</h2><p>
This journal charges only article processing charge of Rp9,000,000.00.
</p></body></html>`

// seedJournal = buat jurnal + kandidat URL (pola test storage existing).
func seedJournal(t *testing.T, st *storage.Store, id int64, urls map[string]string, homeURL string) {
	t.Helper()
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: int(id), Name: "Jurnal Test", SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}
	for u, src := range urls {
		if _, err := st.UpsertJournalURL(id, u, "ojs", src, nil, "", ""); err != nil {
			t.Fatalf("UpsertJournalURL %s: %v", u, err)
		}
	}
	if homeURL != "" {
		if err := st.UpdateViewLinks(id, homeURL, ""); err != nil {
			t.Fatalf("UpdateViewLinks: %v", err)
		}
	}
}

// TestStageHappyPath — canonical ketemu (2 kandidat: sinta kalah, garuda menang)
// + Detect ojs + policy fee page + ExtractFees Millah → author_fees terisi,
// provenance source "ojs", SOURCE_RESOLVED terminal, Verified=true.
func TestStageHappyPath(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "stage.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	// 2 kandidat: sinta http (stub kecil — kalah) + garuda https (OJS penuh).
	seedJournal(t, st, 1, map[string]string{
		"http://j1.sinta.test/":   "sinta",
		"https://j1.garuda.test/": "garuda",
	}, "")

	g := &fakeGetter{responses: map[string]fakeResp{
		"http://j1.sinta.test/": {body: []byte("stub"), status: 200, final: "http://j1.sinta.test/"},
		"https://j1.garuda.test/": {
			body:   homeOJS(true),
			status: 200,
			final:  "https://j1.garuda.test/",
		},
		"https://j1.garuda.test/index.php/about/fees": {
			body:   []byte(feePageMillah),
			status: 200,
			final:  "https://j1.garuda.test/index.php/about/fees",
		},
	}}

	rep, err := RunOJSStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunOJSStage: %v", err)
	}
	if !rep.Verified {
		t.Errorf("Verified = false (%s), want true", rep.VerifyMsg)
	}
	if rep.Resolved != 1 || rep.FeesFound != 1 || rep.Target != 1 {
		t.Errorf("resolved=%d fees=%d target=%d (want 1/1/1)", rep.Resolved, rep.FeesFound, rep.Target)
	}

	// author_fees terisi + provenance source "ojs" + SOURCE_RESOLVED flag.
	fees, err := st.AuthorFees(1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fees, "9000000") {
		t.Errorf("author_fees = %q, want memuat 9000000", fees)
	}
	prov, err := st.Provenance(1)
	if err != nil {
		t.Fatal(err)
	}
	pf, ok := prov["journals_author_fees"]
	if !ok || pf.Source != "ojs" {
		t.Errorf("provenance journals_author_fees = %+v (want source ojs)", pf)
	}
	flags, err := st.Phase2Flags(1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flags, FlagSourceResolved) {
		t.Errorf("flags = %q, want SOURCE_RESOLVED", flags)
	}
}

// TestStageResumeSkip — SOURCE_RESOLVED lama → skip, 0 GET.
func TestStageResumeSkip(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	seedJournal(t, st, 1, map[string]string{"https://j1.test/": "sinta"}, "")
	// Terminal dulu.
	if err := st.SetPhase2(1, "", []string{FlagSourceResolved}, nil, ""); err != nil {
		t.Fatal(err)
	}
	g := &fakeGetter{responses: map[string]fakeResp{}}
	rep, err := RunOJSStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunOJSStage: %v", err)
	}
	if rep.Skip != 1 || rep.Resolved != 0 {
		t.Errorf("skip=%d resolved=%d (want 1/0)", rep.Skip, rep.Resolved)
	}
	if g.gets != 0 {
		t.Errorf("GET = %d, want 0 (resume)", g.gets)
	}
	if !rep.Verified {
		t.Errorf("Verified = false (%s)", rep.VerifyMsg)
	}
}

// TestStageNoFeePage — canonical ketemu tapi tanpa link fee → NO_FEE_PAGE
// jujur + SOURCE_RESOLVED terminal, author_fees tetap NULL.
func TestStageNoFeePage(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "nofee.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	seedJournal(t, st, 1, map[string]string{"https://j1.test/": "sinta"}, "")

	g := &fakeGetter{responses: map[string]fakeResp{
		"https://j1.test/": {body: homeOJS(false), status: 200, final: "https://j1.test/"},
	}}
	rep, err := RunOJSStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunOJSStage: %v", err)
	}
	if rep.Resolved != 1 || rep.NoFeePage != 1 || rep.FeesFound != 0 {
		t.Errorf("resolved=%d noFeePage=%d fees=%d (want 1/1/0)", rep.Resolved, rep.NoFeePage, rep.FeesFound)
	}
	flags, err := st.Phase2Flags(1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flags, FlagNoFeePage) || !strings.Contains(flags, FlagSourceResolved) {
		t.Errorf("flags = %q, want NO_FEE_PAGE + SOURCE_RESOLVED", flags)
	}
	fees, err := st.AuthorFees(1)
	if err != nil {
		t.Fatal(err)
	}
	if fees != "" {
		t.Errorf("author_fees = %q, want kosong", fees)
	}
	if !rep.Verified {
		t.Errorf("Verified = false (%s)", rep.VerifyMsg)
	}
}

// TestStageTanpaKandidat — jurnal tanpa journal_urls & tanpa home → 0 GET,
// NoCandidate jujur, Verified=true (tak ada yg bisa diproses).
func TestStageTanpaKandidat(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "nocand.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	seedJournal(t, st, 1, nil, "")

	g := &fakeGetter{responses: map[string]fakeResp{}}
	rep, err := RunOJSStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunOJSStage: %v", err)
	}
	if rep.NoCandidate != 1 || rep.Resolved != 0 {
		t.Errorf("noCandidate=%d resolved=%d (want 1/0)", rep.NoCandidate, rep.Resolved)
	}
	if g.gets != 0 {
		t.Errorf("GET = %d, want 0", g.gets)
	}
	if !rep.Verified {
		t.Errorf("Verified = false (%s)", rep.VerifyMsg)
	}
}

// TestStageSemuaNetworkFail — semua kandidat err (status 0) → Err (murni
// network, transient), TANPA SOURCE_RESOLVED, Verified=false (rerun wajib).
func TestStageSemuaNetworkFail(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "netfail.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	seedJournal(t, st, 1, map[string]string{
		"https://j1.test/":        "sinta",
		"https://j1-garuda.test/": "garuda",
	}, "")

	g := &fakeGetter{responses: map[string]fakeResp{
		"https://j1.test/":        {err: errNet{}},
		"https://j1-garuda.test/": {err: errNet{}},
	}}
	rep, err := RunOJSStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunOJSStage: %v", err)
	}
	if rep.Err != 1 || rep.Resolved != 0 || rep.NoCanonical != 0 {
		t.Errorf("err=%d resolved=%d noCanonical=%d (want 1/0/0)", rep.Err, rep.Resolved, rep.NoCanonical)
	}
	if rep.Verified {
		t.Error("Verified = true, want false (murni network — rerun wajib)")
	}
	// Retry 1× per kandidat = 4 GET (2 kandidat × 2 percobaan).
	if g.gets != 4 {
		t.Errorf("GET = %d, want 4 (retry 1× per kandidat)", g.gets)
	}
	// TANPA SOURCE_RESOLVED (tanpa terminal).
	flags, err := st.Phase2Flags(1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(flags, FlagSourceResolved) {
		t.Errorf("flags = %q — SOURCE_RESOLVED tidak boleh tertulis (network fail)", flags)
	}
}

// errNet = error jaringan tiruan (GetPlain network gagal → status 0).
type errNet struct{}

func (errNet) Error() string { return "network down" }

// TestStageUnreachablePersisten — bukti kategori opsi B (approve 13 Okt):
// run-1 semua kandidat status 0 & belum pernah di-GET → Err (transien,
// Verified=false). Run-2: journal_urls kini checked_at terisi + http_status
// NULL (tulis run-1 — konvensi urls.go) & masih gagal → Unreachable persisten
// (kategori FINAL jujur) → Verified=true, TETAP tanpa SOURCE_RESOLVED (run
// depan tetap mencoba ulang — pemulihan otomatis bila host bangkit).
func TestStageUnreachablePersisten(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "unreach.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	seedJournal(t, st, 1, map[string]string{"https://j1.test/": "sinta"}, "")

	g := &fakeGetter{responses: map[string]fakeResp{
		"https://j1.test/": {err: errNet{}},
	}}

	// Run-1: kandidat belum pernah di-GET (checked_at "") → Err transien.
	rep1, err := RunOJSStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("run-1: %v", err)
	}
	if rep1.Err != 1 || rep1.Unreachable != 0 || rep1.Verified {
		t.Errorf("run-1: err=%d unreachable=%d verified=%v (want 1/0/false)", rep1.Err, rep1.Unreachable, rep1.Verified)
	}

	// Run-2: checked_at terisi + http_status NULL dari run-1 → persisten.
	getsBefore := g.gets
	rep2, err := RunOJSStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("run-2: %v", err)
	}
	if rep2.Unreachable != 1 || rep2.Err != 0 {
		t.Errorf("run-2: unreachable=%d err=%d (want 1/0)", rep2.Unreachable, rep2.Err)
	}
	if !rep2.Verified {
		t.Errorf("Verified = false (%s), want true (Unreachable = kategori final jujur)", rep2.VerifyMsg)
	}
	// TETAP di-GET ulang (pemulihan) — bukan terminal; TANPA SOURCE_RESOLVED.
	if g.gets == getsBefore {
		t.Error("GET run-2 = 0, want >0 (Unreachable tetap dicoba ulang tiap run)")
	}
	flags, err := st.Phase2Flags(1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(flags, FlagSourceResolved) {
		t.Errorf("flags = %q — SOURCE_RESOLVED tidak boleh tertulis (Unreachable bukan terminal)", flags)
	}
	if !strings.Contains(flags, FlagUnreachable) {
		t.Errorf("flags = %q, want memuat UNREACHABLE", flags)
	}
}

// TestStagePrevRunAllZero — guard deteksi persistensi: kandidat baru (belum
// pernah di-GET) / ada respons final / daftar kosong → belum persisten.
func TestStagePrevRunAllZero(t *testing.T) {
	st200 := 200
	cases := []struct {
		name  string
		cands []storage.OjsCand
		want  bool
	}{
		{"semua status-0 pernah di-GET", []storage.OjsCand{
			{URL: "https://a.test/", CheckedAt: "2026-10-13T00:00:00Z"},
			{URL: "https://b.test/", CheckedAt: "2026-10-13T00:00:00Z"},
		}, true},
		{"kandidat baru belum pernah di-GET", []storage.OjsCand{
			{URL: "https://a.test/", CheckedAt: "2026-10-13T00:00:00Z"},
			{URL: "https://b.test/"},
		}, false},
		{"ada respons final (bukan network)", []storage.OjsCand{
			{URL: "https://a.test/", CheckedAt: "2026-10-13T00:00:00Z", HTTPStatus: &st200},
		}, false},
		{"daftar kosong", nil, false},
	}
	for _, tc := range cases {
		if got := prevRunAllZero(tc.cands); got != tc.want {
			t.Errorf("%s: prevRunAllZero = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestStagePolicyPartialPersisten — bukti Opsi 2 (approve 13 Okt): canonical
// ketemu + 1 link policy network + 1 link 200. Run-1: link network BELUM
// pernah di-GET → PolicyErr (belum terminal, Verified=false). Run-2: link
// itu http_status 0 + checked_at terisi dari run-1 (journal_policy) →
// POLICY_PARTIAL persisten → terminal jujur: SOURCE_RESOLVED + POLICY_PARTIAL,
// fees nil → TANPA NO_FEE_PAGE (sebagian halaman belum terevaluasi),
// Verified=true.
func TestStagePolicyPartialPersisten(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "partial.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	seedJournal(t, st, 1, map[string]string{"https://j1.test/": "sinta"}, "")

	g := &fakeGetter{responses: map[string]fakeResp{
		"https://j1.test/": {body: homeOJS(true), status: 200, final: "https://j1.test/"},
		// fee page: selalu network (dgn retry = 2 GET/run).
		"https://j1.test/index.php/about/fees": {err: errNet{}},
		// about: 200 tapi tanpa frasa fee → fees nil dari link sukses.
		"https://j1.test/index.php/about": {
			body:   []byte("<html><body><h2>About</h2><p>Tentang jurnal.</p></body></html>"),
			status: 200,
			final:  "https://j1.test/index.php/about",
		},
	}}
	cfg := StageConfig{Client: g, RetryDelay: 0}

	// Run-1: link fee belum pernah di-GET → persisten=false → PolicyErr.
	rep1, err := RunOJSStage(st, cfg)
	if err != nil {
		t.Fatalf("run-1: %v", err)
	}
	if rep1.PolicyErr != 1 || rep1.Resolved != 0 || rep1.Verified {
		t.Errorf("run-1: policyErr=%d resolved=%d verified=%v (want 1/0/false)",
			rep1.PolicyErr, rep1.Resolved, rep1.Verified)
	}

	// Run-2: journal_policy utk fee = status 0 + checked_at (tulis run-1).
	rep2, err := RunOJSStage(st, cfg)
	if err != nil {
		t.Fatalf("run-2: %v", err)
	}
	if rep2.PolicyPartial != 1 || rep2.PolicyErr != 0 || rep2.Resolved != 1 {
		t.Errorf("run-2: partial=%d policyErr=%d resolved=%d (want 1/0/1)",
			rep2.PolicyPartial, rep2.PolicyErr, rep2.Resolved)
	}
	if !rep2.Verified {
		t.Errorf("Verified = false (%s), want true (POLICY_PARTIAL = terminal jujur)", rep2.VerifyMsg)
	}
	flags, err := st.Phase2Flags(1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flags, FlagPolicyPartial) || !strings.Contains(flags, FlagSourceResolved) {
		t.Errorf("flags = %q, want POLICY_PARTIAL + SOURCE_RESOLVED", flags)
	}
	if strings.Contains(flags, FlagNoFeePage) {
		t.Errorf("flags = %q — NO_FEE_PAGE tidak boleh tertulis (sebagian halaman belum terevaluasi)", flags)
	}
	if rep2.NoFeePage != 0 || rep2.FeesFound != 0 {
		t.Errorf("noFeePage=%d fees=%d (want 0/0)", rep2.NoFeePage, rep2.FeesFound)
	}
	fees, err := st.AuthorFees(1)
	if err != nil {
		t.Fatal(err)
	}
	if fees != "" {
		t.Errorf("author_fees = %q, want kosong", fees)
	}
}
