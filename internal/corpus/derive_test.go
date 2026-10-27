package corpus

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSplitAbstract — penanda deterministik id/en (§4.1): prefix · tengah
// ("… . Abstract: …") · dua description terpisah · tanpa penanda (ratio).
func TestSplitAbstract(t *testing.T) {
	cases := []struct {
		name   string
		descs  []string
		id, en string
	}{
		{"prefix id", []string{"Abstrak: Penelitian stunting di Kabupaten X."}, "Penelitian stunting di Kabupaten X.", ""},
		{"prefix en", []string{"Abstract: This study analyzes stunting."}, "", "This study analyzes stunting."},
		{"tengah id-lalu-en", []string{"Abstrak: Analisis stunting. Abstract: Stunting analysis."},
			"Analisis stunting", "Stunting analysis."}, // titik = pemisah (ikut regex — deterministik)
		{"dua desc terpisah", []string{"Abstrak: Data gizi balita 2024.", "Abstract: Nutritional data 2024."},
			"Data gizi balita 2024.", "Nutritional data 2024."},
		{"tanpa penanda ratio id", []string{"Penelitian ini menganalisis data yang diambil dari survei dan hasilnya menunjukkan penurunan stunting."},
			"Penelitian ini menganalisis data yang diambil dari survei dan hasilnya menunjukkan penurunan stunting.", ""},
		{"tanpa penanda ratio en", []string{"The study analyzes data which were collected from the survey and the results show a reduction in stunting."},
			"", "The study analyzes data which were collected from the survey and the results show a reduction in stunting."},
		{"kosong", []string{"", "  "}, "", ""},
		{"kata kunci diabaikan", []string{"Kata Kunci: stunting, gizi"}, "", ""}, // ratio 0 → bukan abstrak
	}
	for _, c := range cases {
		id, en := SplitAbstract(c.descs)
		if id != c.id || en != c.en {
			t.Errorf("%s:\n  id = %q\n  en = %q\n  mau id=%q en=%q", c.name, id, en, c.id, c.en)
		}
	}
}

// TestDetectLanguage — O-B1 (G12): fakta dua-abstrak dulu · kontradiksi
// tag-vs-isi → isi menang · dc:language first · ratio · unknown.
func TestDetectLanguage(t *testing.T) {
	if got := DetectLanguage([]string{"id"}, "", "", ""); got != LanguageID {
		t.Errorf("tag id = %q", got)
	}
	if got := DetectLanguage([]string{"en-US"}, "", "", ""); got != LanguageEN {
		t.Errorf("tag en-us = %q (normalisasi)", got)
	}
	if got := DetectLanguage([]string{"id", "en"}, "", "", ""); got != LanguageMixed {
		t.Errorf("dua tag = %q — mau mixed", got)
	}
	if got := DetectLanguage(nil, "Abstrak isi.", "Abstract text.", ""); got != LanguageMixed {
		t.Errorf("dua abstrak tanpa tag = %q — mau mixed", got)
	}
	// O-B1: dua abstrak MENANG atas tag tunggal (audit G12: 147 bilingual).
	if got := DetectLanguage([]string{"en"}, "Abstrak isi.", "Abstract text.", ""); got != LanguageMixed {
		t.Errorf("dua abstrak + tag en = %q — mau mixed (O-B1)", got)
	}
	// O-B1: kontradiksi tag-vs-isi SATU abstrak → isi menang.
	if got := DetectLanguage([]string{"en"}, "Abstrak isi.", "", ""); got != LanguageID {
		t.Errorf("absID-saja + tag en = %q — mau id (O-B1, isi menang)", got)
	}
	if got := DetectLanguage([]string{"id"}, "", "Abstract text.", ""); got != LanguageEN {
		t.Errorf("absEN-saja + tag id = %q — mau en (O-B1, isi menang)", got)
	}
	// O-B1: satu abstrak TANPA kontradiksi → tag tetap first (§4.1).
	if got := DetectLanguage([]string{"en"}, "", "Abstract text.", ""); got != LanguageEN {
		t.Errorf("absEN-saja + tag en = %q — tetap en (§4.1)", got)
	}
	// Ratio: teks Indonesia jelas.
	idText := "Penelitian ini adalah analisis yang dilakukan di lapangan dengan hasil yang menunjukkan"
	if got := DetectLanguage(nil, idText, "", ""); got != LanguageID {
		t.Errorf("ratio id = %q", got)
	}
	if got := DetectLanguage(nil, "", "", "This is the study of the results and the analysis in the field"); got != LanguageEN {
		t.Errorf("ratio en = %q", got)
	}
	if got := DetectLanguage(nil, "", "", "xyz abc"); got != LanguageUnknown {
		t.Errorf("tanpa petunjuk = %q — mau unknown", got)
	}
}

// TestLicenseFromOAI — rules §5.5 sumber dc:rights.
func TestLicenseFromOAI(t *testing.T) {
	s, u, ok := licenseFromOAI([]string{"https://creativecommons.org/licenses/by/4.0/"})
	if !ok || s != LicenseOpen || !strings.Contains(u, "creativecommons") {
		t.Errorf("CC → %q/%v (mau open)", s, ok)
	}
	if s, _, ok := licenseFromOAI([]string{"Open Access Journal"}); !ok || s != LicenseFreeNotOA {
		t.Errorf("open access statement → %q/%v (mau free_not_open)", s, ok)
	}
	if s, _, ok := licenseFromOAI([]string{"Berbayar. Langganan tahunan."}); !ok || s != LicensePaid {
		t.Errorf("berbayar → %q/%v (mau paid)", s, ok)
	}
	if _, _, ok := licenseFromOAI([]string{"Copyright (c) 2024 Penerbit"}); ok {
		t.Error("copyright biasa → ok=true (mau false → unknown jujur)")
	}
	if _, _, ok := licenseFromOAI(nil); ok {
		t.Error("rights kosong → ok=true (mau false)")
	}
}

// TestKeywordJSON — chain author → crossref → [] (antrean TF-IDF pass 2).
func TestKeywordJSON(t *testing.T) {
	kw := KeywordJSON([]string{"stunting", "Gizi Balita"}, []string{"public health"}, LanguageID)
	var arr []map[string]string
	if err := json.Unmarshal([]byte(kw), &arr); err != nil || len(arr) != 2 {
		t.Fatalf("author keywords = %q err=%v — mau 2", kw, err)
	}
	if arr[0]["src"] != "author" || arr[0]["kw"] != "stunting" {
		t.Errorf("kw[0] = %v — mau author/stunting", arr[0])
	}
	// Author kosong → crossref.
	kw = KeywordJSON(nil, []string{"Public Health"}, LanguageEN)
	if err := json.Unmarshal([]byte(kw), &arr); err != nil || len(arr) != 1 || arr[0]["src"] != "crossref" {
		t.Errorf("crossref fallback = %q (mau 1 item src=crossref)", kw)
	}
	// Dua-duanya kosong → [].
	if kw := KeywordJSON(nil, nil, LanguageID); kw != "[]" {
		t.Errorf("kosong = %q — mau []", kw)
	}
	// Dedup case-insensitive.
	kw = KeywordJSON([]string{"Stunting", "stunting"}, nil, LanguageID)
	if err := json.Unmarshal([]byte(kw), &arr); err != nil || len(arr) != 1 {
		t.Errorf("dedup = %q — mau 1 item", kw)
	}
}

// TestNormLangTags — tag aneh → "" (lanjut tahap berikut — jujur).
func TestNormLangTags(t *testing.T) {
	if got := normLangTags([]string{"de"}); got != "" {
		t.Errorf("tag de = %q — mau ''", got)
	}
	if got := normLangTags([]string{"Indonesian"}); got != LanguageID {
		t.Errorf("tag Indonesian = %q — mau id", got)
	}
}
