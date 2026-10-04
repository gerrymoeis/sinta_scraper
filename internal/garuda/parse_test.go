package garuda

import (
	"os"
	"path/filepath"
	"testing"
)

func bukaTestdata(t *testing.T, nama string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", nama))
	if err != nil {
		t.Fatalf("buka testdata %s: %v", nama, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestParseAreaList(t *testing.T) {
	labels, err := ParseAreaList(bukaTestdata(t, "area.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 40 {
		t.Errorf("jumlah label area = %d, want 40 (fakta riset §13.0)", len(labels))
	}
	// kasus label multi-koma, entitas HTML, typo resmi (D1: apa adanya),
	// spasi ekor
	kasus := map[int64]string{
		97:  "Education",
		95:  "Economics, Econometrics & Finance",
		135: "Languange, Linguistic, Communication & Media", // typo resmi TIDAK dikoreksi
		80:  "Decision Sciences, Operations Research & Management",
		500: "Other",
		1:   "Religion",
	}
	byID := map[int64]string{}
	for _, l := range labels {
		byID[l.ID] = l.Label
	}
	for id, want := range kasus {
		if got := byID[id]; got != want {
			t.Errorf("area %d = %q, want %q", id, got, want)
		}
	}
	// ID unik
	seen := map[int64]bool{}
	for _, l := range labels {
		if seen[l.ID] {
			t.Errorf("duplikat area id %d", l.ID)
		}
		seen[l.ID] = true
	}
}

func TestParseSearchPageSingle(t *testing.T) {
	page, err := ParseSearchPage(bukaTestdata(t, "search-single.html"))
	if err != nil {
		t.Fatal(err)
	}
	if page.Found != 1 || page.Total != 1 || page.Page != 1 || page.OfPages != 1 {
		t.Errorf("meta = found%d total%d page%d/%d, want 1/1/1/1",
			page.Found, page.Total, page.Page, page.OfPages)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(page.Rows))
	}
	r := page.Rows[0]
	if r.GarudaID != 445 {
		t.Errorf("garuda_id = %d, want 445", r.GarudaID)
	}
	if r.Title != "Cakrawala Pendidikan" {
		t.Errorf("title = %q", r.Title)
	}
	if r.Publisher != "Universitas Negeri Yogyakarta" {
		t.Errorf("publisher = %q", r.Publisher)
	}
	if r.EISSN != "24428620" {
		t.Errorf("eissn = %q, want 24428620", r.EISSN)
	}
	if r.PISSN != "-" {
		t.Errorf("pissn = %q, want '-' (placeholder e-only)", r.PISSN)
	}
	if len(r.Areas) != 1 || r.Areas[0].ID != 97 || r.Areas[0].Label != "Education" {
		t.Errorf("areas = %+v, want [{97 Education}]", r.Areas)
	}
}

func TestParseSearchPageMulti(t *testing.T) {
	page, err := ParseSearchPage(bukaTestdata(t, "search-multi.html"))
	if err != nil {
		t.Fatal(err)
	}
	// found pakai koma ribuan: "15,957"
	if page.Found != 15957 {
		t.Errorf("found = %d, want 15957 (koma ribuan dinormalkan)", page.Found)
	}
	if page.Total != 15957 || page.Page != 1 || page.OfPages != 1596 {
		t.Errorf("meta = total%d page%d/%d", page.Total, page.Page, page.OfPages)
	}
	if len(page.Rows) != 10 {
		t.Fatalf("rows = %d, want 10 (10/halaman, fakta live)", len(page.Rows))
	}
	// baris multi-label + ISSN dgn X checksum + row tanpa area
	var multi, dgnX, tanpaArea int
	for _, r := range page.Rows {
		if r.GarudaID == 0 || r.Title == "" {
			t.Errorf("baris rusak: %+v", r)
		}
		if len(r.Areas) > 1 {
			multi++
		}
		if len(r.Areas) == 0 {
			tanpaArea++
		}
		if len(r.EISSN) == 8 && (r.EISSN[7] == 'X' || r.EISSN[7] == 'x') {
			dgnX++
		}
	}
	if multi == 0 {
		t.Error("tak ada baris multi-label pada hasil 'jurnal'")
	}
	t.Logf("multi-label=%d, tanpa-area=%d, eissn-X=%d", multi, tanpaArea, dgnX)
	// baris label-journal yang ID-nya tak ada di taxonomi (contoh: 18899)
	byID := map[int64]string{}
	if labels, err := ParseAreaList(bukaTestdata(t, "area.html")); err == nil {
		for _, l := range labels {
			byID[l.ID] = l.Label
		}
	}
	var baru int
	for _, r := range page.Rows {
		for _, a := range r.Areas {
			if _, ok := byID[a.ID]; !ok {
				baru++
			}
		}
	}
	t.Logf("label baris di luar daftar /area (perlu refresh on-demand): %d", baru)
}
