package storage

import (
	"path/filepath"
	"testing"

	"sinta-scraper/internal/sinta"
)

// TestStageGarudaScope = helper scope stage garuda produksi (doc 40):
// HarvestTargetsRank (filter sinta_rank), SyncTargets (bahan fill-if-absent),
// ViewTargets (matched + resume flag VIEW_DONE), CountMatchStatus (verifikasi).
// Titik rawan = konkatenasi klausul WHERE dari rankWhere.
func TestStageGarudaScope(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	j := []sinta.Journal{
		{ID: 1, Name: "A", SintaRank: 1, SourcePage: 1},
		{ID: 2, Name: "B", SintaRank: 2, SourcePage: 1},
		{ID: 3, Name: "C", SintaRank: 1, SourcePage: 1},
	}
	if _, err := st.UpsertJournals(j); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}

	// ---- HarvestTargetsRank --------------------------------------------
	all, err := st.HarvestTargets()
	if err != nil || len(all) != 3 {
		t.Fatalf("HarvestTargets: n=%d err=%v (harus 3, nil)", len(all), err)
	}
	r1, err := st.HarvestTargetsRank([]int{1})
	if err != nil || len(r1) != 2 || r1[0].ID != 1 || r1[1].ID != 3 {
		t.Fatalf("HarvestTargetsRank([1]): %+v err=%v (harus id 1,3)", r1, err)
	}

	// ---- capture: j1 matched, j3 not_found ------------------------------
	if err := st.UpsertGarudaMatch(GarudaMatch{
		JournalID: 1, Status: "matched", GarudaID: 111,
		GarudaURL: "https://garuda.test/journal/view/111",
		Subject:   "Ilmu Komputer", MatchedBy: "eissn", Confidence: 1.0,
	}); err != nil {
		t.Fatalf("UpsertGarudaMatch: %v", err)
	}
	if err := st.UpsertGarudaMatch(GarudaMatch{JournalID: 3, Status: "not_found"}); err != nil {
		t.Fatalf("UpsertGarudaMatch not_found: %v", err)
	}
	if err := st.SetSubjectCanonical(1, "Ilmu Komputer"); err != nil {
		t.Fatalf("SetSubjectCanonical: %v", err)
	}

	// ---- SyncTargets -----------------------------------------------------
	sync, err := st.SyncTargets([]int{1})
	if err != nil || len(sync) != 2 {
		t.Fatalf("SyncTargets([1]): n=%d err=%v (harus 2)", len(sync), err)
	}
	if sync[0].ID != 1 || sync[0].Canonical != "Ilmu Komputer" || sync[0].URLCap == "" {
		t.Errorf("sync j1: %+v (canonical & URLCap harus terisi)", sync[0])
	}
	if sync[1].ID != 3 || sync[1].Canonical != "" || sync[1].URLCap != "" {
		t.Errorf("sync j3: %+v (not_found = tanpa canonical & URLCap)", sync[1])
	}

	// fill-if-absent pola stageSync: terisi → 1; ulang → 0 (idempoten)
	n, err := st.UpdateJournalsE7(1, "subject_area", "Ilmu Komputer")
	if err != nil || n != 1 {
		t.Fatalf("UpdateJournalsE7 fill: n=%d err=%v (harus 1)", n, err)
	}
	if n, _ := st.UpdateJournalsE7(1, "subject_area", "Ilmu Komputer"); n != 0 {
		t.Errorf("UpdateJournalsE7 ulang: n=%d (harus 0)", n)
	}

	// ---- ViewTargets -----------------------------------------------------
	vt, err := st.ViewTargets([]int{1})
	if err != nil || len(vt) != 1 {
		t.Fatalf("ViewTargets([1]): n=%d err=%v (harus 1: j1 matched dgn id)", len(vt), err)
	}
	if vt[0].ID != 1 || vt[0].GarudaID != 111 || vt[0].ViewDone || vt[0].LinkNow {
		t.Errorf("view target j1: %+v", vt[0])
	}
	if vt[0].SubjNow != "Ilmu Komputer" {
		t.Errorf("SubjNow = %q (harus sudah terisi utk guard fill-if-absent)", vt[0].SubjNow)
	}
	// resume: flag VIEW_DONE → terminal
	if err := st.SetPhase2(1, "", []string{FlagViewDoneTest}, nil, ""); err != nil {
		t.Fatalf("SetPhase2 flag: %v", err)
	}
	vt, _ = st.ViewTargets([]int{1})
	if len(vt) != 1 || !vt[0].ViewDone {
		t.Errorf("ViewTargets setelah flag: %+v (ViewDone harus true)", vt)
	}
	// link sudah ada → LinkNow true (jalur resume kedua)
	if err := st.UpdateViewLinks(1, "https://ojs.test", ""); err != nil {
		t.Fatalf("UpdateViewLinks: %v", err)
	}
	vt, _ = st.ViewTargets([]int{1})
	if len(vt) != 1 || !vt[0].LinkNow {
		t.Errorf("ViewTargets setelah link: %+v (LinkNow harus true)", vt)
	}

	// ---- CountMatchStatus -------------------------------------------------
	if n, err := st.CountMatchStatus([]int{1}); err != nil || n != 2 {
		t.Errorf("CountMatchStatus([1]) = %d, %v (harus 2: j1+j3)", n, err)
	}
	if n, err := st.CountMatchStatus(nil); err != nil || n != 2 {
		t.Errorf("CountMatchStatus(nil) = %d, %v (harus 2: j2 tanpa enrichment)", n, err)
	}
}

// FlagViewDoneTest = nilai flag resume view (padanan garuda.FlagViewDone —
// konstan sama; disalin di sini utk hindari import silang paket test).
const FlagViewDoneTest = "VIEW_DONE"
