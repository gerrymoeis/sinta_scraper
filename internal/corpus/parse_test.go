package corpus

import (
	"strings"
	"testing"
)

const listRecordsXML = `<?xml version="1.0" encoding="UTF-8"?>
<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/"
  xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
  xsi:schemaLocation="http://www.openarchives.org/OAI/2.0/
  http://www.openarchives.org/OAI/2.0/OAI-PMH.xsd">
  <responseDate>2026-10-14T00:00:00Z</responseDate>
  <ListRecords>
    <record>
      <header><identifier>oai:jurnalku.test:123</identifier><datestamp>2025-03-15</datestamp></header>
      <metadata>
        <oai_dc:dc xmlns:oai_dc="http://www.openarchives.org/OAI/2.0/oai_dc/"
          xmlns:dc="http://purl.org/dc/elements/1.1/">
          <dc:title>Analisis Stunting di Kabupaten X</dc:title>
          <dc:creator>Budi Santoso</dc:creator>
          <dc:creator>Siti Aminah</dc:creator>
          <dc:subject>stunting</dc:subject>
          <dc:subject>gizi balita</dc:subject>
          <dc:description>Abstrak: Penelitian ini menganalisis stunting. Abstract: This study analyzes stunting.</dc:description>
          <dc:description>Kata Kunci: stunting, gizi balita</dc:description>
          <dc:publisher>Universitas X</dc:publisher>
          <dc:date>2025-03-15</dc:date>
          <dc:type>article</dc:type>
          <dc:identifier>https://jurnalku.test/index.php/j/article/view/123</dc:identifier>
          <dc:identifier>doi:10.1234/jurnalku.123</dc:identifier>
          <dc:source>Jurnal Gizi, Vol(12), Iss(1), Pg(10-20)</dc:source>
          <dc:language>id</dc:language>
          <dc:rights>https://creativecommons.org/licenses/by/4.0/</dc:rights>
        </oai_dc:dc>
      </metadata>
    </record>
    <record>
      <header><identifier>oai:jurnalku.test:118</identifier><datestamp>2024-09-01</datestamp></header>
      <metadata>
        <oai_dc:dc xmlns:oai_dc="http://www.openarchives.org/OAI/2.0/oai_dc/"
          xmlns:dc="http://purl.org/dc/elements/1.1/">
          <dc:title>Prior Article</dc:title>
          <dc:date>2024-09-01</dc:date>
          <dc:identifier>https://jurnalku.test/index.php/j/article/view/118</dc:identifier>
          <dc:source>Jurnal Gizi, Vol(11), Iss(2), Pg(5-9)</dc:source>
          <dc:language>en</dc:language>
          <dc:rights>Copyright (c) 2024</dc:rights>
        </oai_dc:dc>
      </metadata>
    </record>
    <record>
      <header status="deleted"><identifier>oai:jurnalku.test:90</identifier></header>
    </record>
    <resumptionToken cursor="0">token-b8f2</resumptionToken>
  </ListRecords>
</OAI-PMH>`

const noRecordsXML = `<?xml version="1.0" encoding="UTF-8"?>
<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/">
  <responseDate>2026-10-14T00:00:00Z</responseDate>
  <error code="noRecordsMatch">The combination of values results in an empty list.</error>
</OAI-PMH>`

// TestParseListRecords — multi-element (subject/description/identifier/
// language/creator) + resumption token + header deleted.
func TestParseListRecords(t *testing.T) {
	recs, token, err := ParseListRecords(listRecordsXML)
	if err != nil {
		t.Fatalf("ParseListRecords: %v", err)
	}
	if token != "token-b8f2" {
		t.Errorf("resumption = %q — mau token-b8f2", token)
	}
	if len(recs) != 3 {
		t.Fatalf("records = %d — mau 3 (termasuk deleted)", len(recs))
	}
	r := recs[0]
	if r.Title != "Analisis Stunting di Kabupaten X" {
		t.Errorf("title = %q", r.Title)
	}
	if len(r.Creators) != 2 || len(r.Subjects) != 2 || len(r.Descriptions) != 2 {
		t.Errorf("creators=%d subjects=%d descriptions=%d — mau 2/2/2 (multi-element)",
			len(r.Creators), len(r.Subjects), len(r.Descriptions))
	}
	if r.Source == "" || !strings.Contains(r.Source, "Vol(12)") {
		t.Errorf("source = %q — mau citation Vol(12)", r.Source)
	}
	if r.Languages[0] != "id" || r.Rights[0] == "" {
		t.Errorf("language/rights = %q/%q", r.Languages[0], r.Rights[0])
	}
	if !recs[2].Deleted {
		t.Error("record ke-3 status deleted harusnya ditandai")
	}
	// noRecordsMatch → error bernama.
	if _, _, err := ParseListRecords(noRecordsXML); err == nil || !strings.Contains(err.Error(), "noRecordsMatch") {
		t.Errorf("noRecordsMatch harus error bernama, got %v", err)
	}
	if !OAIErrorIs(noRecordsXML, "noRecordsMatch") {
		t.Error("OAIErrorIs(noRecordsMatch) = false — mau true")
	}
}

// TestParseCitationVol — format OJS 3 "Vol(12), Iss(1)" + gaya "Vol. 11 No. 2".
func TestParseCitationVol(t *testing.T) {
	cases := []struct {
		cite    string
		vol, no int64
	}{
		{"Jurnal Gizi, Vol(12), Iss(1), Pg(10-20)", 12, 1},
		{"Vol. 11 No. 2 (2024)", 11, 2},
		{"Volume 3, Number 4, 2025", 3, 4},
		{"Vol 7", 7, 0},
		{"tanpa nomor sama sekali", 0, 0},
	}
	for _, c := range cases {
		v, n := ParseCitationVol(c.cite)
		if v != c.vol || n != c.no {
			t.Errorf("ParseCitationVol(%q) = %d/%d — mau %d/%d", c.cite, v, n, c.vol, c.no)
		}
	}
}

// TestPickDOIURL — identifier DOI vs landing.
func TestPickDOIURL(t *testing.T) {
	ids := []string{"https://j.test/index.php/j/article/view/123", "doi:10.1234/abc"}
	if doi := PickDOI(ids); doi != "10.1234/abc" {
		t.Errorf("PickDOI = %q — mau 10.1234/abc", doi)
	}
	if u := PickURL(ids); u != "https://j.test/index.php/j/article/view/123" {
		t.Errorf("PickURL = %q — mau landing URL", u)
	}
	if PickDOI([]string{"https://j.test/x"}) != "" {
		t.Error("tanpa DOI harus ''")
	}
	if PickDOI([]string{"https://doi.org/10.555/x"}) != "10.555/x" {
		t.Error("https://doi.org/ prefix harus ke-detect")
	}
	if PickURL([]string{"https://doi.org/10.555/x"}) != "" {
		t.Error("doi.org bukan landing")
	}
}

// TestFilterOwnRecords — G16: guard identitas endpoint aggregate.
func TestFilterOwnRecords(t *testing.T) {
	mk := func(src string) OAIRecord { return OAIRecord{Source: src} }
	const namaPanjang = "Kesmas: Jurnal Kesehatan Masyarakat Nasional (National Public Health Journal)"

	// 1. Campuran (kasus scholarhub): 1 milik + 2 asing → buang 2 (match dua arah:
	//    name mengandung source "Kesmas: Jurnal Kesehatan Masyarakat Nasional").
	recs := []OAIRecord{
		mk("Jurnal Studi Hubungan Internasional"), // asing (jsht)
		mk("Kesmas: Jurnal Kesehatan Masyarakat Nasional"),
		mk("Makara Journal of Health Research"), // asing
	}
	out, dropped := FilterOwnRecords(recs, namaPanjang)
	if dropped != 2 || len(out) != 1 {
		t.Errorf("campuran: dropped=%d out=%d — mau 2/1", dropped, len(out))
	}

	// 2. Semua milik (jurnal normal, source = "Nama, Vol(12)") → tak disentuh.
	normal := []OAIRecord{mk("Jurnal Gizi X, Vol(12), Iss(1)"), mk("Jurnal Gizi X, Vol(11), Iss(2)")}
	out2, d2 := FilterOwnRecords(normal, "Jurnal Gizi X")
	if d2 != 0 || len(out2) != 2 {
		t.Errorf("normal: dropped=%d out=%d — mau 0/2 (tanpa filter)", d2, len(out2))
	}

	// 3. Nol match (nama source beda total) → SEMUA dipertahankan (perilaku lama).
	beda := []OAIRecord{mk("Indonesian Journal of Nutrition"), mk("IJN")}
	out3, d3 := FilterOwnRecords(beda, "Jurnal Gizi dan Dietetik Indonesia")
	if d3 != 0 || len(out3) != 2 {
		t.Errorf("nol-match: dropped=%d out=%d — mau 0/2 (mode aman)", d3, len(out3))
	}

	// 4. Source "" = netral (bukan asing) → kombinasi 1-milik+1-kosong TANPA
	//    asing → filter tak aktif (record tanpa dc:source tak terbuang).
	kosong := []OAIRecord{mk(""), mk("Jurnal A")}
	out4, d4 := FilterOwnRecords(kosong, "Jurnal A")
	if d4 != 0 || len(out4) != 2 {
		t.Errorf("source-kosong: dropped=%d out=%d — mau 0/2 (netral, tanpa asing)", d4, len(out4))
	}

	// 5. Kasus j916 (G16b): source "Wacana**,** Journal…" vs nama SINTA
	//    "Wacana**:** Journal…" — normalisasi tanda baca → match ✓.
	j916 := []OAIRecord{
		mk("Jurnal Sosial Humaniora Terapan"), // asing (jsht)
		mk("Wacana, Journal of the Humanities of Indonesia"),
	}
	out5, d5 := FilterOwnRecords(j916, "Wacana: Journal of the Humanities of Indonesia")
	if d5 != 1 || len(out5) != 1 {
		t.Errorf("j916 koma-vs-titikdua: dropped=%d out=%d — mau 1/1", d5, len(out5))
	}
}

// TestFilterLatestVolume — vol terbesar utk N=1 · N=2 · fallback tahun ·
// flag vol_missing utk record tanpa vol.
func TestFilterLatestVolume(t *testing.T) {
	mk := func(id, cite, date string) OAIRecord {
		return OAIRecord{Identifier: id, Source: cite, Dates: []string{date}}
	}
	recs := []OAIRecord{
		mk("r1", "Jurnal, Vol(12), Iss(1)", "2025-03-15"),
		mk("r2", "Jurnal, Vol(12), Iss(2)", "2025-07-01"),
		mk("r3", "Jurnal, Vol(11), Iss(2)", "2024-09-01"),
		mk("r4", "", "2025-05-01"), // vol gagal — tahun 2025 = tahun vol terpilih
	}

	// N=1 → vol 12 (r1, r2 + r4 ikut dgn flag vol_missing).
	f := FilterLatestVolume(recs, 1)
	if f.Vol != 12 || f.Year != 2025 {
		t.Errorf("N=1 vol/year = %d/%d — mau 12/2025", f.Vol, f.Year)
	}
	if len(f.Picked) != 3 {
		t.Errorf("N=1 picked = %d — mau 3 (r1,r2,r4)", len(f.Picked))
	}
	if f.Flag != "vol_missing" {
		t.Errorf("flag = %q — mau vol_missing", f.Flag)
	}

	// N=2 → vol 11 (r3 saja — r4 tahun 2025 ≠ 2024).
	f = FilterLatestVolume(recs, 2)
	if f.Vol != 11 || len(f.Picked) != 1 || f.Picked[0].Identifier != "r3" {
		t.Errorf("N=2 = vol %d picked %d — mau vol 11, 1 baris r3", f.Vol, len(f.Picked))
	}

	// N=3 melebihi tersedia → vol terlama (11).
	f = FilterLatestVolume(recs, 3)
	if f.Vol != 11 {
		t.Errorf("N=3 vol = %d — mau 11 (cap ke vol tersedia)", f.Vol)
	}

	// Semua tanpa vol → fallback tahun max + vol_fallback.
	novol := []OAIRecord{
		mk("a", "", "2025-01-01"),
		mk("b", "", "2024-01-01"),
	}
	f = FilterLatestVolume(novol, 1)
	if f.Vol != 0 || f.Year != 2025 || f.Flag != "vol_fallback" || len(f.Picked) != 1 {
		t.Errorf("fallback = vol%d year%d flag%q picked%d — mau 0/2025/vol_fallback/1",
			f.Vol, f.Year, f.Flag, len(f.Picked))
	}

	// Tanpa vol & tanpa tahun → vol_unparsed, 0 picked (jujur).
	none := []OAIRecord{mk("c", "", "tanpa tahun")}
	f = FilterLatestVolume(none, 1)
	if f.Flag != "vol_unparsed" || len(f.Picked) != 0 {
		t.Errorf("none = flag%q picked%d — mau vol_unparsed/0", f.Flag, len(f.Picked))
	}
}

// TestRecordYear — multi date & date tanpa tahun.
func TestRecordYear(t *testing.T) {
	r := OAIRecord{Dates: []string{"2025-03-15"}}
	if r.RecordYear() != 2025 {
		t.Errorf("year = %d — mau 2025", r.RecordYear())
	}
	if (OAIRecord{Dates: []string{"tak jelas"}}).RecordYear() != 0 {
		t.Error("tanpa tahun harus 0")
	}
}
