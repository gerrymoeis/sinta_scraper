package sinta

import (
	"os"
	"strings"
	"testing"
)

func TestParsePageFixture(t *testing.T) {
	f, err := os.Open("testdata/page1.html")
	if err != nil {
		t.Fatalf("buka fixture: %v", err)
	}
	defer f.Close()

	res, err := ParsePage(f, 1)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}

	// Pagination — nilai dibekukan dari live 27 Sep 2026
	if res.CurrentPage != 1 {
		t.Errorf("CurrentPage = %d, want 1", res.CurrentPage)
	}
	if res.TotalPages != 1678 {
		t.Errorf("TotalPages = %d, want 1678", res.TotalPages)
	}
	if res.TotalJournals != 16772 {
		t.Errorf("TotalJournals = %d, want 16772", res.TotalJournals)
	}

	if len(res.Journals) != 10 {
		t.Fatalf("jumlah kartu = %d, want 10", len(res.Journals))
	}

	// Kartu #1 — diverifikasi dari markup asli
	j := res.Journals[0]
	if j.ID != 671 {
		t.Errorf("kartu1 ID = %d, want 671", j.ID)
	}
	if !strings.Contains(j.Name, "Jurnal Pendidikan IPA Indonesia") {
		t.Errorf("kartu1 Name = %q", j.Name)
	}
	if j.SINTAProfileURL != "https://sinta.kemdiktisaintek.go.id/journals/profile/671" {
		t.Errorf("kartu1 SINTAProfileURL = %q", j.SINTAProfileURL)
	}
	if j.OJSURL != "https://journal.unnes.ac.id/journals/jpii" {
		t.Errorf("kartu1 OJSURL = %q", j.OJSURL)
	}
	if j.EditorURL != "https://journal.unnes.ac.id/journals/jpii/about/editorialTeam" {
		t.Errorf("kartu1 EditorURL = %q", j.EditorURL)
	}
	if j.GoogleScholarURL == "" {
		t.Error("kartu1 GoogleScholarURL kosong")
	}
	if j.PrintISSN != "23391286" || j.ElectronicISSN != "20894392" {
		t.Errorf("kartu1 ISSN = %q / %q, want 23391286 / 20894392", j.PrintISSN, j.ElectronicISSN)
	}
	if j.SubjectArea != "" {
		t.Errorf("kartu1 SubjectArea = %q, want kosong (tidak ada di listing)", j.SubjectArea)
	}
	if j.SintaRank != 1 {
		t.Errorf("kartu1 SintaRank = %d, want 1 (badge S1)", j.SintaRank)
	}
	if !j.IsScopus || !j.IsGaruda {
		t.Errorf("kartu1 scopus/garuda = %v/%v, want true/true", j.IsScopus, j.IsGaruda)
	}
	if j.GarudaURL != "https://garuda.kemdiktisaintek.go.id/journal/view/37972" {
		t.Errorf("kartu1 GarudaURL = %q", j.GarudaURL)
	}
	if j.Impact != 104 {
		t.Errorf("kartu1 Impact = %v, want 104", j.Impact)
	}
	if !strings.Contains(j.AffiliationName, "Universitas Negeri Semarang") {
		t.Errorf("kartu1 AffiliationName = %q", j.AffiliationName)
	}
	if j.SourcePage != 1 {
		t.Errorf("kartu1 SourcePage = %d, want 1", j.SourcePage)
	}

	// Subject Area per kartu — dipetakan penuh dari fixture: hanya kartu
	// 3,5,7,9,10 yang punya; 1,2,4,6,8 kosong (uji dua arah)
	subjWant := map[int]string{
		2: "Engineering",                                     // kartu #3  (Jambi)
		4: "Religion, Education, Social",                     // kartu #5  (Nazhruna)
		6: "Economy, Humanities, Science, Education, Social", // kartu #7  (APTISI)
		8: "Religion, Education",                             // kartu #9  (Peuradeun)
		9: "Social",                                          // kartu #10 (SAMARAH)
	}
	for i, jj := range res.Journals {
		want, punya := subjWant[i]
		if punya && jj.SubjectArea != want {
			t.Errorf("kartu %d SubjectArea = %q, want %q", i+1, jj.SubjectArea, want)
		}
		if !punya && jj.SubjectArea != "" {
			t.Errorf("kartu %d SubjectArea = %q, want kosong", i+1, jj.SubjectArea)
		}
	}

	// Semua kartu: ID valid + badge konsisten (page1 = sepenuhnya S1)
	for i, jj := range res.Journals {
		if jj.ID == 0 {
			t.Errorf("kartu %d: ID = 0", i+1)
		}
		if jj.SintaRank != 1 {
			t.Errorf("kartu %d: SintaRank = %d, want 1", i+1, jj.SintaRank)
		}
	}
}
