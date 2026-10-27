package corpus

import (
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"sinta-scraper/internal/storage"
	"strings"
	"testing"
	"time"
)

// mockGetter = PlainGetter tiruan: URL → body/status (pola ojs stage_test).
type mockGetter struct {
	hits  map[string]int
	resps map[string]struct {
		body   string
		status int
	}
}

func (m *mockGetter) GetPlain(u, _ string) ([]byte, int, string, error) {
	if m.hits == nil {
		m.hits = map[string]int{}
	}
	m.hits[u]++
	r, ok := m.resps[u]
	if !ok {
		return nil, 404, u, nil
	}
	return []byte(r.body), r.status, u, nil
}

func (m *mockGetter) set(url, body string, status int) {
	if m.resps == nil {
		m.resps = map[string]struct {
			body   string
			status int
		}{}
	}
	m.resps[url] = struct {
		body   string
		status int
	}{body, status}
}

const oaiPage1J1 = `<?xml version="1.0" encoding="UTF-8"?>
<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/">
  <ListRecords>
    <record>
      <header><identifier>oai:j1.test:123</identifier></header>
      <metadata><oai_dc:dc xmlns:oai_dc="http://www.openarchives.org/OAI/2.0/oai_dc/" xmlns:dc="http://purl.org/dc/elements/1.1/">
        <dc:title>Analisis Stunting</dc:title>
        <dc:subject>stunting</dc:subject>
        <dc:subject>gizi balita</dc:subject>
        <dc:description>Abstrak: Penelitian ini menganalisis stunting di kabupaten X.</dc:description>
        <dc:date>2025-03-15</dc:date>
        <dc:identifier>https://j1.test/index.php/j1/article/view/123</dc:identifier>
        <dc:identifier>doi:10.1/x</dc:identifier>
        <dc:source>Jurnal Gizi, Vol(12), Iss(1)</dc:source>
        <dc:language>id</dc:language>
        <dc:rights>https://creativecommons.org/licenses/by/4.0/</dc:rights>
      </oai_dc:dc></metadata>
    </record>
    <record>
      <header><identifier>oai:j1.test:125</identifier></header>
      <metadata><oai_dc:dc xmlns:oai_dc="http://www.openarchives.org/OAI/2.0/oai_dc/" xmlns:dc="http://purl.org/dc/elements/1.1/">
        <dc:title>Studi Terbaru Ekologi</dc:title>
        <dc:description>Abstrak: Data lapangan tahun 2025 tentang ekologi dan hasilnya.</dc:description>
        <dc:date>2025-06-01</dc:date>
        <dc:identifier>https://j1.test/index.php/j1/article/view/125</dc:identifier>
        <dc:identifier>doi:10.1/z</dc:identifier>
        <dc:source>Jurnal Gizi, Vol(12), Iss(1)</dc:source>
        <dc:rights>Copyright (c) 2025 Penerbit</dc:rights>
      </oai_dc:dc></metadata>
    </record>
    <record>
      <header><identifier>oai:j1.test:118</identifier></header>
      <metadata><oai_dc:dc xmlns:oai_dc="http://www.openarchives.org/OAI/2.0/oai_dc/" xmlns:dc="http://purl.org/dc/elements/1.1/">
        <dc:title>Artikel Volume Lama</dc:title>
        <dc:date>2024-09-01</dc:date>
        <dc:identifier>https://j1.test/index.php/j1/article/view/118</dc:identifier>
        <dc:source>Jurnal Gizi, Vol(11), Iss(2)</dc:source>
      </oai_dc:dc></metadata>
    </record>
    <record><header status="deleted"><identifier>oai:j1.test:90</identifier></header></record>
    <resumptionToken cursor="0">hal2</resumptionToken>
  </ListRecords>
</OAI-PMH>`

const oaiPage2J1 = `<?xml version="1.0" encoding="UTF-8"?>
<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/">
  <ListRecords>
    <record>
      <header><identifier>oai:j1.test:124</identifier></header>
      <metadata><oai_dc:dc xmlns:oai_dc="http://www.openarchives.org/OAI/2.0/oai_dc/" xmlns:dc="http://purl.org/dc/elements/1.1/">
        <dc:title>Stunting Analysis</dc:title>
        <dc:subject>ekologi</dc:subject>
        <dc:description>Abstract: This study analyzes stunting data from the field.</dc:description>
        <dc:date>2025-05-10</dc:date>
        <dc:identifier>https://j1.test/index.php/j1/article/view/124</dc:identifier>
        <dc:identifier>doi:10.1/y</dc:identifier>
        <dc:source>Jurnal Gizi, Vol(12), Iss(1)</dc:source>
        <dc:language>en</dc:language>
      </oai_dc:dc></metadata>
    </record>
  </ListRecords>
</OAI-PMH>`

const crossrefJ1 = `{"status":"ok","message":{"total-results":2,"items":[
	{"DOI":"10.1/y","license":[{"URL":"https://creativecommons.org/licenses/by/4.0/","start":{"date-time":"2020-01-01T00:00:00Z"}}],"subject":["Public Health"]},
	{"DOI":"10.1/z","license":[{"URL":"https://example.org/lic","start":{"date-time":"2027-01-01T00:00:00Z"}}]}
]}}`

const (
	oaiJ1   = "https://j1.test/index.php/j1/oai?verb=ListRecords&metadataPrefix=oai_dc"
	oaiJ1p2 = "https://j1.test/index.php/j1/oai?verb=ListRecords&resumptionToken=hal2"
	crossJ1 = "https://api.crossref.org/journals/12345678/works?rows=1000&select=DOI,license,subject,abstract"
	oaiJ3   = "https://j3.test/index.php/j3/oai?verb=ListRecords&metadataPrefix=oai_dc"
	oaiJ4   = "https://j4.test/index.php/j4/oai?verb=ListRecords&metadataPrefix=oai_dc"
)

// seedCorpusDB = seed DB korpus utk test — pilih jurnal mana yg dibuat:
// j1 OAI OK (2 halaman+crossref) · j2 tanpa kanonik · j3 OAI 404 ·
// j4 noRecordsMatch.
func seedCorpusDB(t *testing.T, withJ2, withJ3 bool) *storage.Store {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "corpus.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	js := []sinta.Journal{
		{ID: 1, Name: "Jurnal Gizi", SintaRank: 1, SourcePage: 1, PrintISSN: "1234-5678"},
		{ID: 4, Name: "OAI Kosong", SintaRank: 1, SourcePage: 1},
	}
	profs := []storage.SourceProfile{
		{JournalID: 1, Platform: "ojs", Evidence: "marker", CanonicalURL: "https://j1.test/index.php/j1"},
		{JournalID: 4, Platform: "ojs", Evidence: "marker", CanonicalURL: "https://j4.test/index.php/j4"},
	}
	if withJ2 {
		js = append(js, sinta.Journal{ID: 2, Name: "Tanpa Kanonik", SintaRank: 1, SourcePage: 1})
	}
	if withJ3 {
		js = append(js, sinta.Journal{ID: 3, Name: "OAI Mati", SintaRank: 1, SourcePage: 1})
		profs = append(profs, storage.SourceProfile{JournalID: 3, Platform: "ojs", Evidence: "marker",
			CanonicalURL: "https://j3.test/index.php/j3"})
	}
	if _, err := st.UpsertJournals(js); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}
	for _, p := range profs {
		if err := st.UpsertSourceProfile(p); err != nil {
			t.Fatalf("UpsertSourceProfile: %v", err)
		}
	}
	return st
}

func corpusMock() *mockGetter {
	m := &mockGetter{}
	m.set(oaiJ1, oaiPage1J1, 200)
	m.set(oaiJ1p2, oaiPage2J1, 200)
	m.set(crossJ1, crossrefJ1, 200)
	m.set(oaiJ4, noRecordsXML, 200)
	// oaiJ3 tak di-set → 404 default
	return m
}

func corpusCfg(m *mockGetter) StageConfig {
	return StageConfig{
		Client: m,
		VolN:   1,
		Now:    time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC),
	}
}

// TestSanitizeOAIBody — O-C4: body bersih = utuh tanpa alokasi; body dgn
// control-char illegal (U+0000/U+0002) bersih, \t\n\r dipertahankan.
func TestSanitizeOAIBody(t *testing.T) {
	bersih := []byte("<r>teks\taman\nbaris\r</r>")
	if got := sanitizeOAIBody(bersih); string(got) != string(bersih) {
		t.Errorf("body bersih berubah: %q → %q", bersih, got)
	}
	kotor := []byte("<r>liar\x00karakter\x02akhir\x1f</r>")
	want := "<r>liarkarakterakhir</r>"
	if got := string(sanitizeOAIBody(kotor)); got != want {
		t.Errorf("sanitize = %q — mau %q", got, want)
	}
	// Putaran ke-2: U+FFFE/U+FFFF (noncharacter) dibuang — U+FFFD TETAP (legal).
	nonChar := "<r>sisa\uFFFEhingga\uFFFFakhir\uFFFD</r>"
	got := string(sanitizeOAIBody([]byte(nonChar)))
	if got != "<r>sisahinggaakhir\uFFFD</r>" {
		t.Errorf("U+FFFE/FFFF = %q — mau buang keduanya, U+FFFD dipertahankan", got)
	}
	if got := string(sanitizeOAIBody([]byte("tanpa ilegal \uFFFD"))); got != "tanpa ilegal \uFFFD" {
		t.Errorf("U+FFFD legal ikut terbuang: %q", got)
	}
}

// TestFetchListRecordsHTTPSUpgrade — C6-D: halaman-1 http gagal (404) →
// basis di-upgrade ke https sekali → token lanjut https. http TANPA GET
// tambahan bila langsung sukses (dicek di test cap dgn URL https).
func TestFetchListRecordsHTTPSUpgrade(t *testing.T) {
	const httpBase = "http://j1.test/index.php/j1/oai"
	httpP1 := httpBase + "?verb=ListRecords&metadataPrefix=oai_dc"
	httpsP1 := "https://j1.test/index.php/j1/oai?verb=ListRecords&metadataPrefix=oai_dc"

	m := &mockGetter{}              // httpP1 tak di-set → 404 default → trigger upgrade
	m.set(httpsP1, oaiPage1J1, 200) // token hal2
	m.set(oaiJ1p2, oaiPage2J1, 200) // halaman 2 (sudah https) tanpa token → selesai

	rep := &StageReport{}
	recs, err := fetchListRecords(corpusCfg(m), rep, httpBase)
	if err != nil {
		t.Fatalf("upgrade http→https harus sukses: %v", err)
	}
	if len(recs) != 5 { // 4 (page1) + 1 (page2)
		t.Errorf("record = %d — mau 5", len(recs))
	}
	if m.hits[httpP1] != 1 {
		t.Errorf("GET http = %d× — mau 1 (404 non-transien: tanpa retry, lalu upgrade)", m.hits[httpP1])
	}
	if m.hits[httpsP1] != 1 || m.hits[oaiJ1p2] != 1 {
		t.Errorf("GET https p1/p2 = %d/%d — mau 1/1 (token lanjut di scheme baru)", m.hits[httpsP1], m.hits[oaiJ1p2])
	}
	if rep.OAIGet != 3 {
		t.Errorf("OAIGet = %d — mau 3 (1 http gagal + 2 https sukses)", rep.OAIGet)
	}
}

// TestFetchListRecordsCap — O-C4: cap dr cfg.MaxPages (bukan const) + body
// NUL tetap parse sukses (integrasi sanitasi dlm loop).
func TestFetchListRecordsCap(t *testing.T) {
	m := &mockGetter{}
	// Page awal + token-URL: SELALU balik halaman ber-token → loop tak
	// berujung → kena cap.
	m.set(oaiJ1, oaiPage1J1, 200) // token hal2
	m.set(oaiJ1p2, oaiPage1J1, 200)
	cfg := corpusCfg(m)
	cfg.MaxPages = 2

	rep := &StageReport{}
	_, err := fetchListRecords(cfg, rep, "https://j1.test/index.php/j1/oai")
	if err == nil || !strings.Contains(err.Error(), "resumption > 2 halaman") {
		t.Fatalf("cap MaxPages=2 → err = %v — mau error cap '2'", err)
	}
	if rep.OAIGet != 2 {
		t.Errorf("OAIGet = %d — mau 2 (cap berhenti di halaman ke-2)", rep.OAIGet)
	}

	// Integrasi sanitasi: XML valid dgn NUL di tengah → parse sukses.
	m2 := &mockGetter{}
	kotor := strings.Replace(oaiPage1J1, "Analisis Stunting", "Anali\x00sis Stunting", 1)
	m2.set(oaiJ1, kotor, 200)
	m2.set(oaiJ1p2, oaiPage2J1, 200) // halaman 2 tanpa token → selesai
	rep2 := &StageReport{}
	recs, err := fetchListRecords(corpusCfg(m2), rep2, "https://j1.test/index.php/j1/oai")
	if err != nil {
		t.Fatalf("body dgn NUL harus sukses setelah sanitasi: %v", err)
	}
	if len(recs) != 5 { // 4 (page1: 3 + 1 deleted) + 1 (page2)
		t.Errorf("record = %d — mau 5 (NUL tak membuang record)", len(recs))
	}
}

// TestStageCorpusHappyPath — j1 OAI resumable 2 halaman + merge crossref ·
// j4 noRecordsMatch jujur → VERIFIED=true (OAIEmpty bukan masalah).
func TestStageCorpusHappyPath(t *testing.T) {
	st := seedCorpusDB(t, false, false) // j1 + j4 saja (j2/j3 = test kategori jujur)
	m := corpusMock()

	rep, err := RunCorpusStage(st, corpusCfg(m))
	if err != nil {
		t.Fatalf("RunCorpusStage: %v", err)
	}
	if !rep.Verified {
		t.Errorf("Verified = false (%s) — mau true", rep.VerifyMsg)
	}
	if rep.Processed != 1 || rep.OAIEmpty != 1 || rep.ArticlesWritten != 3 {
		t.Errorf("processed/oaiEmpty/articles = %d/%d/%d — mau 1/1/3",
			rep.Processed, rep.OAIEmpty, rep.ArticlesWritten)
	}
	if rep.OAIGet != 3 || rep.CrossrefGET != 1 {
		t.Errorf("oai_get/crossref_get = %d/%d — mau 3/1", rep.OAIGet, rep.CrossrefGET)
	}
	if rep.EmbargoFound != 1 {
		t.Errorf("embargo = %d — mau 1 (r125 start 2027)", rep.EmbargoFound)
	}

	rows, err := st.Articles(1)
	if err != nil || len(rows) != 3 {
		t.Fatalf("Articles j1 = %d err=%v — mau 3 (vol11 & deleted di-skip)", len(rows), err)
	}
	byID := map[string]storage.ArticleRow{}
	for _, r := range rows {
		byID[r.RecordID] = r
	}
	// r123: license OAI CC (DOI tak ada di crossref) + keywords author id.
	r := byID["oai:j1.test:123"]
	if r.LicenseStatus != LicenseOpen || !strings.Contains(r.LicenseURL, "creativecommons") {
		t.Errorf("r123 license = %q/%q — mau open CC via OAI", r.LicenseStatus, r.LicenseURL)
	}
	if r.Language != LanguageID || r.AbstractID == "" || r.AbstractEN != "" {
		t.Errorf("r123 lang/abs = %q / id=%q en=%q — mau id dgn abstrak id", r.Language, r.AbstractID, r.AbstractEN)
	}
	if r.VolumeNo != 12 || r.IssueNo != 1 || r.Year != 2025 {
		t.Errorf("r123 vol/no/year = %d/%d/%d — mau 12/1/2025", r.VolumeNo, r.IssueNo, r.Year)
	}
	if !strings.Contains(r.KeywordsJSON, `"src":"author"`) || !strings.Contains(r.KeywordsJSON, "stunting") {
		t.Errorf("r123 keywords = %q — mau author (stunting, gizi balita)", r.KeywordsJSON)
	}
	// r125: embargo Crossref menang atas OAI copyright.
	r = byID["oai:j1.test:125"]
	if r.LicenseStatus != LicenseFreeNotOA || !strings.Contains(r.Provenance, "embargo") {
		t.Errorf("r125 license = %q prov=%s — mau free_not_open + catatan embargo", r.LicenseStatus, r.Provenance)
	}
	// r124: crossref open + language en + abstract en.
	r = byID["oai:j1.test:124"]
	if r.LicenseStatus != LicenseOpen || r.Language != LanguageEN || r.AbstractEN == "" {
		t.Errorf("r124 = lang%q lic%q en=%q — mau en + open crossref", r.Language, r.LicenseStatus, r.AbstractEN)
	}

	// ── Resume: j1 complete → Skip; j4 tanpa baris → re-GET (jujur) ──
	j4Hits := m.hits[oaiJ4]
	rep2, err := RunCorpusStage(st, corpusCfg(m))
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if rep2.Skip != 1 || rep2.ArticlesWritten != 0 {
		t.Errorf("rerun skip/articles = %d/%d — mau 1/0", rep2.Skip, rep2.ArticlesWritten)
	}
	if m.hits[oaiJ4] != j4Hits+1 {
		t.Errorf("j4 re-GET = %d (mau +%d — noRecordsMatch bukan complete)", m.hits[oaiJ4], j4Hits)
	}
}

// TestStageCorpusKategoriJujur — j2 tanpa kanonik + j3 OAI 404 → report
// jujur, VERIFIED=false dgn sinyal C3b.
func TestStageCorpusKategoriJujur(t *testing.T) {
	st := seedCorpusDB(t, true, true) // semua 4 jurnal
	m := corpusMock()

	rep, err := RunCorpusStage(st, corpusCfg(m))
	if err != nil {
		t.Fatalf("RunCorpusStage: %v", err)
	}
	if rep.Target != 4 || rep.NoCanonical != 1 || rep.OAIMatik != 1 || rep.Processed != 1 {
		t.Errorf("target/nocanon/oaimatik/processed = %d/%d/%d/%d — mau 4/1/1/1",
			rep.Target, rep.NoCanonical, rep.OAIMatik, rep.Processed)
	}
	if rep.Verified {
		t.Error("Verified=true dgn 1 OAI mati — mau false (sinyal C3b)")
	}
	if !strings.Contains(rep.VerifyMsg, "OAI mati") {
		t.Errorf("VerifyMsg = %q — mau memuat sinyal OAI mati", rep.VerifyMsg)
	}
	// j3 0 GET artikel (mati total) + tanpa baris.
	if rows, _ := st.Articles(3); len(rows) != 0 {
		t.Errorf("j3 baris = %d — mau 0 (OAI mati, antrean C3b)", len(rows))
	}
}

// TestStageCorpusScopeJournal — flag -journal (smoke C6): hanya jurnal
// scope yg diproses; jurnal lain 0 GET & tanpa baris.
func TestStageCorpusScopeJournal(t *testing.T) {
	st := seedCorpusDB(t, false, false) // j1 (OAI OK) + j4 (noRecordsMatch)
	m := corpusMock()
	cfg := corpusCfg(m)
	cfg.JournalIDs = []int64{4} // scope j4 saja

	rep, err := RunCorpusStage(st, cfg)
	if err != nil {
		t.Fatalf("RunCorpusStage: %v", err)
	}
	if rep.Target != 1 || rep.OAIEmpty != 1 || rep.Processed != 0 {
		t.Errorf("target/oaiEmpty/processed = %d/%d/%d — mau 1/1/0",
			rep.Target, rep.OAIEmpty, rep.Processed)
	}
	// j1 TIDAK tersentuh: 0 GET & 0 baris.
	if m.hits[oaiJ1] != 0 || m.hits[oaiJ1p2] != 0 || m.hits[crossJ1] != 0 {
		t.Errorf("j1 di luar scope kena GET: %v", m.hits)
	}
	if rows, _ := st.Articles(1); len(rows) != 0 {
		t.Errorf("j1 baris = %d — mau 0 (di luar scope -journal)", len(rows))
	}
	// Dedupe: ID dobel tak menggandakan target.
	cfg2 := corpusCfg(m)
	cfg2.JournalIDs = []int64{4, 4}
	if _, err := RunCorpusStage(st, cfg2); err != nil {
		t.Fatalf("rerun dgn ID dobel: %v", err)
	}
}

// TestStageCorpusScopeJournalTakDikenal — typo/ID bukan target = error
// hening-harus-nyala (0 GET; bukan scope sunyi).
func TestStageCorpusScopeJournalTakDikenal(t *testing.T) {
	st := seedCorpusDB(t, false, false)
	m := corpusMock()
	cfg := corpusCfg(m)
	cfg.JournalIDs = []int64{999}

	_, err := RunCorpusStage(st, cfg)
	if err == nil || !strings.Contains(err.Error(), "tak ditemukan") {
		t.Errorf("err = %v — mau 'tak ditemukan'", err)
	}
	if len(m.hits) != 0 {
		t.Errorf("GET terlanjur sebelum error: %v", m.hits)
	}
}

// TestStageCorpusPrioritasOAIResmi — fix G1: link resmi garuda_oai_url
// diutamakan atas canonical+"/oai"; jurnal TANPA canonical tp PUNYA link
// resmi tetap diproses (bukan NoCanonical).
func TestStageCorpusPrioritasOAIResmi(t *testing.T) {
	st := seedCorpusDB(t, true, false) // j1 (canonical) + j2 (tanpa canonical) + j4
	const resmi1 = "https://j1.test/index.php/j1/oai-resmi"
	const resmi2 = "https://j2.test/oai-resmi"
	const qList = "?verb=ListRecords&metadataPrefix=oai_dc" // pola fetchListRecords
	for _, id := range []int64{1, 2} {
		// Siapkan baris journal_enrichment (fixture — UpdateViewLinks error bila
		// baris belum ada; canonical subjeknya "" = netral utk test ini).
		if err := st.SetSubjectCanonical(id, ""); err != nil {
			t.Fatalf("siap enrichment j%d: %v", id, err)
		}
	}
	if err := st.UpdateViewLinks(1, "", resmi1); err != nil {
		t.Fatalf("UpdateViewLinks j1: %v", err)
	}
	if err := st.UpdateViewLinks(2, "", resmi2); err != nil {
		t.Fatalf("UpdateViewLinks j2: %v", err)
	}
	m := &mockGetter{}
	m.set(resmi1+qList, noRecordsXML, 200) // OAI valid 0 record (jalur resmi)
	m.set(resmi2+qList, noRecordsXML, 200)

	cfg := corpusCfg(m)
	cfg.JournalIDs = []int64{1, 2}
	rep, err := RunCorpusStage(st, cfg)
	if err != nil {
		t.Fatalf("RunCorpusStage: %v", err)
	}
	// Prioritas: GET ke URL resmi — canonical+"/oai" TIDAK disentuh.
	if m.hits[resmi1+qList] != 1 || m.hits[resmi2+qList] != 1 {
		t.Errorf("hits resmi j1/j2 = %d/%d — mau 1/1 (pakai garuda_oai_url); rep=%+v hits=%v",
			m.hits[resmi1+qList], m.hits[resmi2+qList], rep, m.hits)
	}
	if m.hits[oaiJ1] != 0 {
		t.Errorf("canonical+/oai j1 kena GET %d× — mau 0 (resmi menang)", m.hits[oaiJ1])
	}
	// j2 tanpa canonical tapi ber-link resmi → proses (OAIEmpty), bukan NoCanonical.
	if rep.OAIEmpty != 2 || rep.NoCanonical != 0 {
		t.Errorf("oaiEmpty/noCanonical = %d/%d — mau 2/0 (resmi diprioritaskan; j4 di luar scope)", rep.OAIEmpty, rep.NoCanonical)
	}
	// NoCanonical hanya bila KEDUANYA kosong — tercakup test KategoriJujur (j2 tanpa profile).
}
