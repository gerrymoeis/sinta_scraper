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

// Langkah 22 (doc 18 Bagian 3.1): unit test normalisasi ISSN — varian
// hyphen/spasi terbukti live di kartu SINTA (hal.8 "0216-1370", hal.14
// "2088 351X"), plus X check-digit dan x huruf kecil.
func TestNormalizeISSN(t *testing.T) {
	cases := map[string]string{
		"0216-1370": "02161370", // hyphen (Cakrawala, live hal.8)
		"2088 351X": "2088351X", // spasi di tengah (Formatif, live hal.14)
		"2615790X":  "2615790X", // X check-digit utuh
		"2406825x":  "2406825X", // x huruf kecil → uppercase
		"23391286":  "23391286", // polos tidak berubah
		"0":         "",         // placeholder SINTA "tanpa ISSN" (live: JEBIS) → kosong
		"":          "",         // e-only / kosong
		"12345":     "12345",    // tidak 8 karakter → dipertahankan agar terlihat di validasi
	}
	for in, want := range cases {
		if got := normalizeISSN(in); got != want {
			t.Errorf("normalizeISSN(%q) = %q, want %q", in, got, want)
		}
	}
}

// End-to-end parser: fixture dimodifikasi in-memory menjadi varian
// hyphen + spasi; nilai akhir harus sama dengan ekspektasi ternormalisasi.
// File fixture tidak diubah agar tetap mewakili page-1 live yang polos.
func TestParseISSNNormalisasiVarian(t *testing.T) {
	raw, err := os.ReadFile("testdata/page1.html")
	if err != nil {
		t.Fatalf("baca fixture: %v", err)
	}
	mod := strings.Replace(string(raw), "P-ISSN : 23391286", "P-ISSN : 2339-1286", 1)
	mod = strings.Replace(mod, "E-ISSN :  20894392", "E-ISSN :  2089 4392", 1)

	res, err := ParsePage(strings.NewReader(mod), 1)
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if len(res.Journals) != 10 {
		t.Fatalf("jumlah kartu = %d, want 10", len(res.Journals))
	}
	j := res.Journals[0]
	if j.PrintISSN != "23391286" || j.ElectronicISSN != "20894392" {
		t.Errorf("kartu1 ISSN ternormalisasi = %q / %q, want 23391286 / 20894392",
			j.PrintISSN, j.ElectronicISSN)
	}
}
