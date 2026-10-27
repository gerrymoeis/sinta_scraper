package corpus

import (
	"strings"
	"testing"

	"sinta-scraper/internal/storage"
)

// ---- URL builders (fakta verifikasi 27 Okt utk G10-variant) ----

// TestCrossrefPrimeraURL — WAJIB sort=published&order=desc (vol terakhir
// masuk rows=1000) + TANPA `language` (route /journals/{issn}/works
// menolaknya dgn 400 validation-failure — fakta probe 27 Okt).
func TestCrossrefPrimeraURL(t *testing.T) {
	u := crossrefPrimeraURL("23019468", "a@b.c")
	for _, want := range []string{
		"/journals/23019468/works", "rows=1000",
		"sort=published", "order=desc",
		"select=DOI,title,abstract,issued,volume,issue,URL,container-title,subject,license,publisher,page",
		"mailto=a@b.c",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("URL tanpa %q: %s", want, u)
		}
	}
	if strings.Contains(u, "language") {
		t.Errorf("URL memuat select language (400 utk route ini): %s", u)
	}
	if crossrefPrimeraURL("23019468", "") == u {
		t.Error("mailto kosong harus menghasilkan URL berbeda (tanpa &mailto)")
	}
}

// TestDoajArticlesURL — ISSN WAJIB format dash (tanpa dash = total 0
// SILENT, fakta probe 27 Okt).
func TestDoajArticlesURL(t *testing.T) {
	u := doajArticlesURL("23019468")
	if !strings.Contains(u, "issn:2301-9468") {
		t.Errorf("ISSN tak di-dash-kan: %s", u)
	}
	if !strings.Contains(u, "pageSize=100") || !strings.Contains(u, "sort=last_updated:desc") {
		t.Errorf("pageSize/sort hilang: %s", u)
	}
	if got := doajArticlesURL("2301-9468"); !strings.Contains(got, "issn:2301-9468") {
		t.Errorf("ISSN ber-dash ganda/berubah: %s", got)
	}
}

// ---- adapter Crossref ----

const crossrefPrimeraSample = `{"status":"ok","message":{"total-results":4,"items":[
	{"DOI":"10.1/a","title":["Stunting di Kabupaten X"],"issued":{"date-parts":[[2025,3,15]]},
	 "volume":"12","issue":"1","URL":"https://j1.test/index.php/j1/article/view/201",
	 "container-title":["Jurnal Gizi"],"subject":["stunting","gizi"],
	 "abstract":"<jats:p>Abstrak <italic>A</italic></jats:p>",
	 "license":[{"URL":"https://creativecommons.org/licenses/by/4.0/","start":{"date-time":"2020-01-01T00:00:00Z"}}],
	 "publisher":"Penerbit X","page":"1-10"},
	{"DOI":"10.1/b","title":["Ekologi Lamun"],"issued":{"date-parts":[[2024]]},
	 "volume":"11","issue":"2","URL":"https://j1.test/index.php/j1/article/view/202",
	 "container-title":["Jurnal Gizi"]},
	{"DOI":"10.1/c","issued":{"date-parts":[[2025,1,1]]},"volume":"12","issue":"1",
	 "URL":"https://j1.test/index.php/j1/article/view/203","container-title":["Jurnal Gizi"]},
	{"title":["Tanpa Identitas"],"issued":{"date-parts":[[2025,1,1]]},"volume":"12",
	 "container-title":["Jurnal Gizi"]}
]}}`

// TestParseCrossrefPrimera — adapter: field → OAIRecord utuh (citation
// rekonstruksi utk ParseCitationVol, date, ids, abstract strip-JATS,
// license utk §5.5). Item tanpa judul / tanpa DOI+URL = dilewati (jujur).
func TestParseCrossrefPrimera(t *testing.T) {
	recs, err := ParseCrossrefPrimera([]byte(crossrefPrimeraSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("record = %d — mau 2 (2 dilewati: tanpa judul, tanpa identitas)", len(recs))
	}
	a := recs[0]
	if a.Identifier != "crossref:10.1/a" {
		t.Errorf("identifier = %q — mau crossref:10.1/a", a.Identifier)
	}
	if a.Title != "Stunting di Kabupaten X" {
		t.Errorf("title = %q", a.Title)
	}
	if a.Source != "Jurnal Gizi, Vol(12), Iss(1)" {
		t.Errorf("source = %q — mau citation OJS rekonstruksi", a.Source)
	}
	if vol, no := ParseCitationVol(a.Source); vol != 12 || no != 1 {
		t.Errorf("ParseCitationVol(source) = %d/%d — mau 12/1 (inti scope vol-N)", vol, no)
	}
	if got := a.RecordYear(); got != 2025 {
		t.Errorf("year = %d — mau 2025", got)
	}
	if PickDOI(a.Identifiers) != "10.1/a" {
		t.Errorf("PickDOI = %q", PickDOI(a.Identifiers))
	}
	if PickURL(a.Identifiers) != "https://j1.test/index.php/j1/article/view/201" {
		t.Errorf("PickURL = %q (URL bukan doi.org = landing sah)", PickURL(a.Identifiers))
	}
	if len(a.Descriptions) != 1 || strings.Contains(a.Descriptions[0], "<jats:p>") ||
		strings.Contains(a.Descriptions[0], "<italic>") {
		t.Errorf("abstract = %q — mau JATS sudah di-strip", a.Descriptions)
	}
	if len(a.Rights) != 1 || a.Rights[0] != "https://creativecommons.org/licenses/by/4.0/" {
		t.Errorf("rights = %v — mau license URL pertama (§5.5)", a.Rights)
	}
	if len(a.Subjects) != 2 {
		t.Errorf("subjects = %v — mau 2", a.Subjects)
	}
	// item ke-2: tahun cuma yyyy.
	if recs[1].RecordYear() != 2024 {
		t.Errorf("year item-2 = %d — mau 2024 (date-parts 1 elemen)", recs[1].RecordYear())
	}
	// guard identitas (G16) netral utk API: source = nama jurnal sendiri.
	if _, dropped := FilterOwnRecords(recs, "Jurnal Gizi"); dropped != 0 {
		t.Errorf("guard membuang %d record API — mau 0 (netral)", dropped)
	}
	if _, err := ParseCrossrefPrimera([]byte("bukan json")); err == nil {
		t.Error("body bukan JSON harus error")
	}
}

// ---- adapter DOAJ ----

const doajSample = `{"total":3,"results":[
	{"id":"abc123","bibjson":{"title":"Ekologi Terumbu Karang","abstract":"Abstrak karang.","year":"2025",
	 "identifier":[{"type":"pissn","id":"1234-5678"},{"type":"doi","id":"10.9/karang"}],
	 "link":[{"type":"fulltext","url":"https://j.test/index.php/k/article/view/9"}],
	 "journal":{"title":"Jurnal Karang","volume":"7","issue":"2"},
	 "subject":[{"term":"ekologi"}],"license":{"type":"CC BY","url":"https://creativecommons.org/licenses/by/4.0/"},
	 "language":"en"}},
	{"id":"def456","bibjson":{"title":"Tahun Number","year":2024,"journal":{"title":"Jurnal Karang"}}},
	{"id":"ghi789","bibjson":{"title":"","year":"2025","journal":{"title":"Jurnal Karang"}}}
]}`

// TestParseDOAJArticles — year STRING (fakta probe 27 Okt) & number sama
// -sama dipakai · journal.volume → citation · doi via identifier[] · item
// tanpa judul dilewati.
func TestParseDOAJArticles(t *testing.T) {
	recs, err := ParseDOAJArticles([]byte(doajSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("record = %d — mau 2 (tanpa judul dilewati)", len(recs))
	}
	a := recs[0]
	if a.Identifier != "doaj:abc123" {
		t.Errorf("identifier = %q", a.Identifier)
	}
	if a.RecordYear() != 2025 {
		t.Errorf("year(string) = %d — mau 2025", a.RecordYear())
	}
	if a.Source != "Jurnal Karang, Vol(7), Iss(2)" {
		t.Errorf("source = %q — mau citation dari journal.volume", a.Source)
	}
	if vol, no := ParseCitationVol(a.Source); vol != 7 || no != 2 {
		t.Errorf("vol/iss = %d/%d — mau 7/2", vol, no)
	}
	if PickDOI(a.Identifiers) != "10.9/karang" {
		t.Errorf("PickDOI = %q (dari identifier[] type=doi)", PickDOI(a.Identifiers))
	}
	if PickURL(a.Identifiers) != "https://j.test/index.php/k/article/view/9" {
		t.Errorf("PickURL = %q", PickURL(a.Identifiers))
	}
	if len(a.Rights) != 1 {
		t.Errorf("rights = %v — mau license URL", a.Rights)
	}
	// year number (bukan string).
	if recs[1].RecordYear() != 2024 {
		t.Errorf("year(number) = %d — mau 2024", recs[1].RecordYear())
	}
	if _, err := ParseDOAJArticles([]byte("<html>")); err == nil {
		t.Error("body bukan JSON harus error")
	}
}

// ---- rantai fallback ----

// TestFetchAPIFallbackRantai — 3 kasus: (1) Crossref sukses → doaj TANPA
// GET · (2) Crossref 404 → DOAJ sukses · (3) keduanya gagal → error (caller
// OAI MATI). Counter GET sesuai.
func TestFetchAPIFallbackRantai(t *testing.T) {
	crURL := crossrefPrimeraURL("12345678", "")
	djURL := doajArticlesURL("12345678")
	target := func() *storage.CorpusTarget {
		return &storage.CorpusTarget{ID: 1, Name: "Jurnal Gizi", ISSN: "1234-5678"}
	}
	_ = target
	t.Run("crossref-sukses", func(t *testing.T) {
		m := &mockGetter{}
		m.set(crURL, crossrefPrimeraSample, 200)
		m.set(djURL, doajSample, 200) // tak boleh dipakai
		rep := &StageReport{}
		recs, works, src, err := fetchAPIFallback(corpusCfg(m), rep, target(), corpusCfg(m).Now)
		if err != nil {
			t.Fatalf("harus sukses: %v", err)
		}
		if len(recs) != 2 || src != "crossref" {
			t.Errorf("recs/src = %d/%q — mau 2/crossref", len(recs), src)
		}
		if len(works.Items) != 3 {
			t.Errorf("works = %d — mau 3 (4 items − 1 tanpa DOI; 1 GET diparse 2 fungsi — lisensi §5.5)", len(works.Items))
		}
		if rep.CrossrefGET != 1 || rep.DoajGET != 0 {
			t.Errorf("crossref/doaj GET = %d/%d — mau 1/0", rep.CrossrefGET, rep.DoajGET)
		}
		if m.hits[djURL] != 0 {
			t.Errorf("DOAJ di-GET %d× — harus 0 (Crossref menang)", m.hits[djURL])
		}
	})
	t.Run("turun-ke-doaj", func(t *testing.T) {
		m := &mockGetter{} // crURL tak di-set → 404 default
		m.set(djURL, doajSample, 200)
		rep := &StageReport{}
		recs, _, src, err := fetchAPIFallback(corpusCfg(m), rep, target(), corpusCfg(m).Now)
		if err != nil {
			t.Fatalf("harus sukses via DOAJ: %v", err)
		}
		if len(recs) != 2 || src != "doaj" {
			t.Errorf("recs/src = %d/%q — mau 2/doaj", len(recs), src)
		}
		if rep.CrossrefGET != 1 || rep.DoajGET != 1 {
			t.Errorf("crossref/doaj GET = %d/%d — mau 1/1", rep.CrossrefGET, rep.DoajGET)
		}
	})
	t.Run("keduanya-gagal", func(t *testing.T) {
		m := &mockGetter{} // dua-duanya 404 default
		rep := &StageReport{}
		_, _, _, err := fetchAPIFallback(corpusCfg(m), rep, target(), corpusCfg(m).Now)
		if err == nil || !strings.Contains(err.Error(), "crossref st=404") ||
			!strings.Contains(err.Error(), "doaj st=404") {
			t.Fatalf("err = %v — mau error dgn status keduanya", err)
		}
	})
	t.Run("tanpa-issn", func(t *testing.T) {
		m := &mockGetter{}
		rep := &StageReport{}
		_, _, _, err := fetchAPIFallback(corpusCfg(m), rep,
			&storage.CorpusTarget{ID: 9, Name: "Tanpa ISSN"}, corpusCfg(m).Now)
		if err == nil || !strings.Contains(err.Error(), "butuh ISSN") {
			t.Fatalf("err = %v — mau 'butuh ISSN' (0 GET)", err)
		}
		if rep.CrossrefGET != 0 || rep.DoajGET != 0 {
			t.Errorf("GET = %d/%d — mau 0/0 (tak ada ISSN = percuma)", rep.CrossrefGET, rep.DoajGET)
		}
	})
}

// ---- integrasi end-to-end (C3b opsi A) ----

// TestStageCorpusAPIFallbackCrossref — j1 OAI 403 (WAF) → API-merge
// Crossref sbg primer: scope vol-N tetap jalan (vol 12 menang), kolom
// source="crossref", provenance bawa kunci "api", lisensi §5.5 dari GET
// YANG SAMA (1 GET 2 guna). j4 noRecordsMatch tetap jujur (TANPA fallback).
func TestStageCorpusAPIFallbackCrossref(t *testing.T) {
	st := seedCorpusDB(t, false, false) // j1 (ISSN 1234-5678) + j4
	m := &mockGetter{}
	m.set(oaiJ1, "<html><title>Just a moment...</title></html>", 403) // WAF challenge
	m.set(crossrefPrimeraURL("12345678", ""), crossrefPrimeraSample, 200)
	m.set(oaiJ4, noRecordsXML, 200)

	cfg := corpusCfg(m)
	cfg.Ranks = []int{1}
	rep, err := RunCorpusStage(st, cfg)
	if err != nil {
		t.Fatalf("RunCorpusStage: %v", err)
	}
	if rep.APIFallback != 1 {
		t.Errorf("APIFallback = %d — mau 1", rep.APIFallback)
	}
	if rep.OAIMatik != 0 || rep.OAIEmpty != 1 || rep.Processed != 1 {
		t.Errorf("matik/empty/processed = %d/%d/%d — mau 0/1/1", rep.OAIMatik, rep.OAIEmpty, rep.Processed)
	}
	if rep.OAIGet != 2 || rep.CrossrefGET != 1 || rep.DoajGET != 0 {
		t.Errorf("oai/crossref/doaj GET = %d/%d/%d — mau 2/1/0 (j1 403 + j4 noRecordsMatch)", rep.OAIGet, rep.CrossrefGET, rep.DoajGET)
	}
	// 1 GET Crossref = primer + lisensi: vol 12 menang → 1 artikel (bukan 2).
	arts, err := st.Articles(1)
	if err != nil {
		t.Fatalf("ArticlesByJournal: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("artikel = %d — mau 1 (vol-12 menang, vol-11 tersingkir, 2 tanpa judul dilewati)", len(arts))
	}
	a := arts[0]
	if a.Source != storage.ArticleSourceCrossref {
		t.Errorf("source = %q — mau crossref", a.Source)
	}
	if a.VolumeNo != 12 || a.IssueNo != 1 || a.Year != 2025 {
		t.Errorf("vol/iss/thn = %d/%d/%d — mau 12/1/2025 (scope vol-N dari API)", a.VolumeNo, a.IssueNo, a.Year)
	}
	if a.LicenseStatus != storage.LicenseOpen || a.LicenseURL == "" {
		t.Errorf("lisensi = %q/%q — mau open dgn URL (works dari GET yang sama)", a.LicenseStatus, a.LicenseURL)
	}
	if !strings.Contains(a.Provenance, `"api":"crossref"`) {
		t.Errorf("provenance tanpa jejak api: %s", a.Provenance)
	}
	if !strings.Contains(a.Provenance, `"oai":""`) {
		t.Errorf("provenance oai harus \"\" (jujur — bukan endpoint OAI): %s", a.Provenance)
	}
	if !strings.Contains(a.Provenance, `"license_src":"crossref"`) {
		t.Errorf("license_src bukan crossref: %s", a.Provenance)
	}
}

// TestStageCorpusAPIFallbackDOAJ — Crossref 404 (tak ada) → DOAJ sbg
// primer · source="doaj" · provenance jujur.
func TestStageCorpusAPIFallbackDOAJ(t *testing.T) {
	st := seedCorpusDB(t, false, false)
	m := &mockGetter{} // crURL tak di-set → 404 default
	m.set(oaiJ1, "<html>Attention Required</html>", 403)
	m.set(doajArticlesURL("12345678"), doajSample, 200)
	m.set(oaiJ4, noRecordsXML, 200)

	cfg := corpusCfg(m)
	cfg.Ranks = []int{1}
	rep, err := RunCorpusStage(st, cfg)
	if err != nil {
		t.Fatalf("RunCorpusStage: %v", err)
	}
	if rep.APIFallback != 1 || rep.DoajGET != 1 || rep.Processed != 1 {
		t.Errorf("fallback/doaj/processed = %d/%d/%d — mau 1/1/1", rep.APIFallback, rep.DoajGET, rep.Processed)
	}
	arts, err := st.Articles(1)
	if err != nil {
		t.Fatalf("ArticlesByJournal: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("artikel = %d — mau 1 (vol 7 dari DOAJ)", len(arts))
	}
	a := arts[0]
	if a.Source != storage.ArticleSourceDOAJ || a.VolumeNo != 7 || a.IssueNo != 2 {
		t.Errorf("source/vol/iss = %q/%d/%d — mau doaj/7/2", a.Source, a.VolumeNo, a.IssueNo)
	}
	if !strings.Contains(a.Provenance, `"api":"doaj"`) {
		t.Errorf("provenance tanpa jejak api: %s", a.Provenance)
	}
}

// TestStageCorpusAPIFallbackGagalJujur — OAI 403 + Crossref 404 + DOAJ
// 404 → TETAP OAI MATI (jujur, tanpa baris) + counter jelas utk antrean
// landing/solver (C3b tahap berikut).
func TestStageCorpusAPIFallbackGagalJujur(t *testing.T) {
	st := seedCorpusDB(t, false, false)
	m := &mockGetter{}
	m.set(oaiJ1, "<html>Just a moment</html>", 403)
	// Crossref primer & DOAJ tak di-set → 404 default
	m.set(oaiJ4, noRecordsXML, 200)

	cfg := corpusCfg(m)
	cfg.Ranks = []int{1}
	rep, err := RunCorpusStage(st, cfg)
	if err != nil {
		t.Fatalf("RunCorpusStage: %v", err)
	}
	if rep.OAIMatik != 1 || rep.APIFallback != 0 || rep.Processed != 0 {
		t.Errorf("matik/fallback/processed = %d/%d/%d — mau 1/0/0", rep.OAIMatik, rep.APIFallback, rep.Processed)
	}
	if rep.CrossrefGET != 1 || rep.DoajGET != 1 {
		t.Errorf("crossref/doaj GET = %d/%d — mau 1/1 (kedua API dicoba dulu)", rep.CrossrefGET, rep.DoajGET)
	}
	arts, err := st.Articles(1)
	if err != nil {
		t.Fatalf("ArticlesByJournal: %v", err)
	}
	if len(arts) != 0 {
		t.Errorf("artikel = %d — mau 0 (semua sumber gagal = tanpa baris)", len(arts))
	}
}
