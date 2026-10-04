package garuda

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKandidatDariFixture(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join("testdata", "search-single.html"))
	if err != nil {
		t.Fatalf("baca testdata: %v", err)
	}
	// nama file = pola Q3: search-j{id}-*.html
	if err := os.WriteFile(filepath.Join(dir, "search-j42-eissn.html"), src, 0o644); err != nil {
		t.Fatalf("tulis fixture: %v", err)
	}
	cands, total, err := KandidatDariFixture(dir, 42)
	if err != nil {
		t.Fatalf("KandidatDariFixture: %v", err)
	}
	if total != 1 || len(cands) != 1 {
		t.Fatalf("total=%d cands=%d, want 1/1", total, len(cands))
	}
	c := cands[0]
	if c.GarudaID != 445 {
		t.Errorf("GarudaID = %d, want 445", c.GarudaID)
	}
	if c.EISSN != "24428620" {
		t.Errorf("EISSN = %q", c.EISSN)
	}
	if c.PISSN != "-" {
		t.Errorf("PISSN = %q (harus RAW)", c.PISSN)
	}
	if c.Title != "Cakrawala Pendidikan" {
		t.Errorf("Title = %q", c.Title)
	}
	if c.Publisher != "Universitas Negeri Yogyakarta" {
		t.Errorf("Publisher = %q", c.Publisher)
	}
	if c.URL == "" {
		t.Error("URL kosong")
	}
	// Match langsung jalan dgn kandidat fixture (E4a: ladder offline).
	res := Match(Input{Name: "Cakrawala Pendidikan", EISSN: "24428620"}, cands)
	if res.Status != StatusMatched || res.MatchedBy != "eissn" || res.Confidence != 100 {
		t.Errorf("Match = %+v, want matched/eissn/100", res)
	}
}

func TestKandidatDariFixtureKosong(t *testing.T) {
	dir := t.TempDir()
	// fixture tak ada → nol kandidat, tanpa error (butuh live).
	cands, total, err := KandidatDariFixture(dir, 999)
	if err != nil || len(cands) != 0 || total != 0 {
		t.Errorf("tak ada fixture: cands=%d total=%d err=%v", len(cands), total, err)
	}
	// file dengan 0 baris (found=0) → tetap nol, tanpa error.
	empty := `Search results for <b>"x"</b> : <b>0</b> Journals / Conference found`
	if err := os.WriteFile(filepath.Join(dir, "search-j7-alt.html"), []byte(empty), 0o644); err != nil {
		t.Fatalf("tulis fixture: %v", err)
	}
	cands, total, err = KandidatDariFixture(dir, 7)
	if err != nil || len(cands) != 0 || total != 0 {
		t.Errorf("found=0: cands=%d total=%d err=%v", len(cands), total, err)
	}
}
