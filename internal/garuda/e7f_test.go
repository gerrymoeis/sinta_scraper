package garuda

import "testing"

// HitungE7f: found / nol / kendala http — klasifikasi jujur utk laporan.
func TestHitungE7f(t *testing.T) {
	items := []E7fHasil{
		{Doaj: E6DOAJ{Tersedia: true, Subjek: []string{"Nursing"}, HTTP: 200}},
		{Doaj: E6DOAJ{Tersedia: true, HTTP: 200}},     // found tanpa subject
		{Doaj: E6DOAJ{HTTP: 404}},                     // tak terdaftar
		{Doaj: E6DOAJ{Tersedia: false, HTTP: 200}},    // total=0 / ISSN mismatch
		{Doaj: E6DOAJ{HTTP: 0, Error: "unreachable"}}, // kendala
	}
	r := HitungE7f(items)
	if r.Total != 5 || r.Found != 2 || r.DenganSubjek != 1 ||
		r.Nol != 2 || r.KendalaHTTP != 1 {
		t.Errorf("ringkas = %+v", r)
	}
}

func TestDoajTocURL(t *testing.T) {
	if got := DoajTocURL("23562641"); got != "https://doaj.org/toc/2356-2641" {
		t.Errorf("toc = %q", got)
	}
	if got := DoajTocURL("2088351X"); got != "https://doaj.org/toc/2088-351X" {
		t.Errorf("toc X = %q", got)
	}
	if got := DoajTocURL("bukan-issn"); got != "" {
		t.Errorf("invalid = %q, want \"\"", got)
	}
}

// Alur E7f: parse DOAJ search (LCC bibjson.subject) → MergeSubjectArea fill.
func TestE7fParseLaluMerge(t *testing.T) {
	body := []byte(`{"total":1,"results":[{"bibjson":{"title":"X",
	  "eissn":"2356-2641","pissn":"0215-773X",
	  "subject":[{"code":"P","scheme":"LCC","term":"Language and Literature"},
	            {"code":"P1-1091","scheme":"LCC","term":"Philology. Linguistics"}]}}]}`)
	d, err := ParseDOAJSearch(body, "23562641")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !d.Tersedia || len(d.Subjek) != 2 {
		t.Fatalf("subjek = %v", d.Subjek)
	}
	if got := MergeSubjectArea("Education", d.Subjek); got !=
		"Education, Language and Literature, Philology. Linguistics" {
		t.Errorf("merge fill = %q", got)
	}
	if got := MergeSubjectArea("", d.Subjek); got !=
		"Language and Literature, Philology. Linguistics" {
		t.Errorf("merge kosong = %q", got)
	}
}
