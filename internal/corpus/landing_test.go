package corpus

import (
	"strings"
	"testing"

	"sinta-scraper/internal/storage"
)

// ---- canonical ----

func TestCanonicalOrigin(t *testing.T) {
	if got, err := canonicalOrigin("https://criticalcareshock.com/"); err != nil || got != "https://criticalcareshock.com" {
		t.Errorf("got=%q err=%v — mau origin tanpa trailing slash", got, err)
	}
	if _, err := canonicalOrigin(""); err == nil {
		t.Error("canonical kosong harus error")
	}
	if _, err := canonicalOrigin("/index.php/x"); err == nil {
		t.Error("canonical tanpa scheme+host harus error")
	}
}

// ---- OSCM: homepage ----

func TestParseOSCMTOCHome(t *testing.T) {
	home := `
	<a href="/publication/toc/oscm-volume-19-issue-4-2026">Vol 19 iss 4</a>
	<a href="https://journal.oscm-forum.org/publication/toc/oscm-volume-19-issue-3-2026">absolut</a>
	<a href="/publication/toc/oscm-volume-18-issue-4-2025">lama</a>
	<a href="/publication/toc/oscm-volume-19-issue-4-2026">duplikat</a>
	<a href="/publication/article/bukan-toc">bukan TOC</a>`
	got := ParseOSCMTOCHome([]byte(home))
	if len(got) != 3 {
		t.Fatalf("TOC = %d (%v) — mau 3 (duplikat & article dibuang)", len(got), got)
	}
	if got[0] != "/publication/toc/oscm-volume-19-issue-4-2026" ||
		got[1] != "/publication/toc/oscm-volume-19-issue-3-2026" {
		t.Errorf("urutan/path salah: %v", got)
	}
}

// ---- OSCM: TOC ----

const oscmTOCPage = `
<ol>
<li class="toc">
  <a class="toc-title" href="/publication/article/role-supply-chain">Exploring the Role of Supply Chain Leadership</a>
  <ul class="list-inline"><li class="list-inline-item">Sean P. Goffnett</li></ul>
  <div class="toc-guides doi">
    <a href="#" data-toggle="abstract-936">Abstract</a>
    <a href="/publication/pdf/role-supply-chain">Full PDF</a>
    <a href="https://doi.org/10.31387/oscm0670542">DOI</a>
  </div>
</li>
<li class="toc">
  <a class="toc-title" href="/publication/article/humanitarian-model">A Humanitarian Logistics Model</a>
  <a href="#" data-toggle="abstract-1231">Abstract</a>
  <a href="https://doi.org/10.31387/oscm0670543">DOI</a>
</li>
</ol>
<div id="abstract-936" style="margin-top:10px;"><div class="card card-body article-abstract-card">This paper tests supply chain leadership on security.</div></div>
<div id="abstract-1231" style="margin-top:10px;"><div class="card card-body article-abstract-card">Second abstract with <b>markup</b> inside.</div></div>`

// TestParseOSCMTOC — vol/iss/thn dari SLUG (bukan tebakan) · judul+URL
// absolut · DOI per-segmen · abstrak by index toggle (urutan = urutan judul) ·
// strip markup.
func TestParseOSCMTOC(t *testing.T) {
	tocURL := "https://journal.oscm-forum.org/publication/toc/oscm-volume-19-issue-4-2026"
	recs, err := ParseOSCMTOC([]byte(oscmTOCPage), tocURL, "Jurnal OSCM")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("record = %d — mau 2", len(recs))
	}
	a := recs[0]
	if a.Identifier != "https://journal.oscm-forum.org/publication/article/role-supply-chain" {
		t.Errorf("identifier = %q — mau URL absolut artikel", a.Identifier)
	}
	if a.Title != "Exploring the Role of Supply Chain Leadership" {
		t.Errorf("title = %q", a.Title)
	}
	if a.Source != "Jurnal OSCM, Vol(19), Iss(4)" {
		t.Errorf("source = %q — mau citation dr slug TOC", a.Source)
	}
	if vol, no := ParseCitationVol(a.Source); vol != 19 || no != 4 {
		t.Errorf("vol/iss = %d/%d — mau 19/4", vol, no)
	}
	if a.RecordYear() != 2026 {
		t.Errorf("year = %d — mau 2026", a.RecordYear())
	}
	if PickDOI(a.Identifiers) != "10.31387/oscm0670542" {
		t.Errorf("doi = %q (segmen artikel-1)", PickDOI(a.Identifiers))
	}
	if len(a.Descriptions) != 1 || a.Descriptions[0] != "This paper tests supply chain leadership on security." {
		t.Errorf("abstrak = %v", a.Descriptions)
	}
	// artikel ke-2: DOI segmen ke-2 + abstrak by index (toggle urut == judul).
	if PickDOI(recs[1].Identifiers) != "10.31387/oscm0670543" {
		t.Errorf("doi ke-2 = %q", PickDOI(recs[1].Identifiers))
	}
	if len(recs[1].Descriptions) != 1 || strings.Contains(recs[1].Descriptions[0], "<b>") {
		t.Errorf("abstrak ke-2 = %v — mau markup di-strip", recs[1].Descriptions)
	}
	// URL TOC tanpa slug = error jujur.
	if _, err := ParseOSCMTOC([]byte(oscmTOCPage), "https://journal.oscm-forum.org/x", "J"); err == nil {
		t.Error("slug TOC hilang harus error (tanpa tebakan vol)")
	}
	// 0 artikel = error jujur.
	if _, err := ParseOSCMTOC([]byte("<html>kosong</html>"), tocURL, "J"); err == nil {
		t.Error("TOC tanpa toc-title harus error")
	}
}

// ---- CCS: arsip sidebar ----

func TestParseCCSArchive(t *testing.T) {
	home := `
	<li class="e"><a href="/2026/08/"><span class="t"><strong>August 2026</strong> Volume 29 Number 4</span></a></li>
	<li class="e"><a href="/2026/02/"><span class="t"><strong>February 2026</strong> Volume 29 Number 1</span></a></li>
	<li class="e"><a href="/2025/12/"><span class="t"><strong>December 2025</strong> Volume 28 Number 6</span></a></li>
	<a href="/2026/10/">October 2026</a>
	<a href="/2026/08/"><span class="t"><strong>August 2026</strong> Volume 29 Number 4</span></a>`
	got, err := ParseCCSArchive([]byte(home))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("entri = %d (%v) — mau 3 (link tanpa label & duplikat dibuang)", len(got), got)
	}
	if got[0] != (ccsBulan{Y: 2026, M: 8, Vol: 29, Iss: 4}) ||
		got[1] != (ccsBulan{Y: 2026, M: 2, Vol: 29, Iss: 1}) ||
		got[2] != (ccsBulan{Y: 2025, M: 12, Vol: 28, Iss: 6}) {
		t.Errorf("isi = %v", got)
	}
	if _, err := ParseCCSArchive([]byte("<html>tanpa arsip</html>")); err == nil {
		t.Error("tanpa entri ber-label harus error")
	}
	// vol max + after = 1 detik sebelum bulan awal vol terakhir.
	volMax, after := ccsAfter(got)
	if volMax != 29 || after != "2026-01-31T23:59:59" {
		t.Errorf("volMax/after = %d/%q — mau 29/2026-01-31T23:59:59", volMax, after)
	}
	// cek URL posts (encoding konsisten).
	u := ccsPostsURL("https://criticalcareshock.com", after)
	for _, w := range []string{"wp-json/wp/v2/posts", "per_page=100", "after=2026-01-31T23"} {
		if !strings.Contains(u, w) {
			t.Errorf("URL tanpa %q: %s", w, u)
		}
	}
}

// ---- CCS: abstrak ----

func TestCCSAbstract(t *testing.T) {
	content := `<p class="wp-block-paragraph"><strong>Abstract</strong></p>
<p class="wp-block-paragraph"><strong>Background</strong>: Cardiac surgery monitoring. <strong>Conclusion</strong>: iloprost works.</p>
<p class="has-white-color has-text-color has-link-color wp-elements-5 wp-block-paragraph" style="font-size:0px">John Doe, Jane Roe</p>
<div class="sws_supernormalaction"><button class="b"><svg><path d="x"/></svg><span>Share</span></button></div>`
	got := ccsAbstract(content)
	if !strings.Contains(got, "Cardiac surgery monitoring") || !strings.Contains(got, "iloprost works") {
		t.Errorf("abstrak = %q — inti hilang", got)
	}
	if strings.Contains(got, "Abstract") && strings.Contains(got, "Background :") == false {
		t.Errorf("label Abstract harus terpotong: %q", got)
	}
	for _, junk := range []string{"John Doe", "Share", "svg", "<p"} {
		if strings.Contains(got, junk) {
			t.Errorf("abstrak mengandung %q: %q", junk, got)
		}
	}
	// editorial tanpa label Abstract → konten apa adanya.
	if got2 := ccsAbstract("<p>Introduction This editorial honours the legacy.</p>"); !strings.Contains(got2, "Introduction This editorial") {
		t.Errorf("editorial = %q", got2)
	}
}

// ---- CCS: heading current issue ----

func TestParseCCSCurrent(t *testing.T) {
	h := `<h2 class="elementor-heading-title">Current Issue Highlights: October 2026, Volume 29 Number 5</h2>`
	got, ok := ParseCCSCurrent([]byte(h))
	if !ok {
		t.Fatal("heading current issue harus terdeteksi")
	}
	if got != (ccsBulan{Y: 2026, M: 10, Vol: 29, Iss: 5}) {
		t.Errorf("entri = %v — mau 2026-10 v29 n5", got)
	}
	if _, ok := ParseCCSCurrent([]byte("<p>halaman biasa</p>")); ok {
		t.Error("tanpa heading harus ok=false (bukan error)")
	}
}

// ---- CCS: WP-REST ----

const ccsPostsJSON = `[
 {"id":1,"date":"2026-08-01T05:00:00","link":"https://criticalcareshock.com/2026/08/411247/",
  "title":{"rendered":"Pulmonary <em>Hypertension</em> Review"},
  "content":{"rendered":"<p class=\"wp-block-paragraph\"><strong>Abstract</strong></p><p class=\"wp-block-paragraph\"><strong>Background</strong>: Cardiac surgery monitoring. Results: iloprost works.</p><p class=\"has-white-color\" style=\"font-size:0px\">John Doe, Jane Roe</p><div class=\"sws_supernormalaction\"><button><svg><path d=\"x\"/></svg><span>Share</span></button></div>"}},
 {"id":2,"date":"2026-02-15T10:00:00","link":"https://criticalcareshock.com/2026/02/555/",
  "title":{"rendered":"Editorial Introduction"},
  "content":{"rendered":"<p>Introduction This editorial honours the symposium.</p>"}},
 {"id":3,"date":"2024-06-10T10:00:00","link":"https://criticalcareshock.com/2024/06/999/",
  "title":{"rendered":"Old Post"},
  "content":{"rendered":"<p>Old abstract.</p>"}},
 {"id":4,"date":"2026-10-01T05:00:00","link":"https://criticalcareshock.com/2026/10/771128/",
  "title":{"rendered":"October Current Issue Article"},
  "content":{"rendered":"<p class=\"wp-block-paragraph\"><strong>Abstract</strong></p><p class=\"wp-block-paragraph\">October abstract text.</p>"}}
]`

func TestParseCCSPosts(t *testing.T) {
	// termasuk entri hasil-merge heading (2026-10 v29 n5 — TIDAK ada di sidebar).
	entries := []ccsBulan{{Y: 2026, M: 8, Vol: 29, Iss: 4}, {Y: 2026, M: 2, Vol: 29, Iss: 1},
		{Y: 2026, M: 10, Vol: 29, Iss: 5}}
	recs, err := ParseCCSPosts([]byte(ccsPostsJSON), entries, "Jurnal CCS")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("record = %d — mau 3 (bulan 2024-06 tanpa entri arsip = dilewati jujur)", len(recs))
	}
	a := recs[0]
	if a.Title != "Pulmonary Hypertension Review" {
		t.Errorf("title = %q — tag di-strip", a.Title)
	}
	if a.Source != "Jurnal CCS, Vol(29), Iss(4)" {
		t.Errorf("source = %q — mau dari map bulan sidebar", a.Source)
	}
	if vol, no := ParseCitationVol(a.Source); vol != 29 || no != 4 {
		t.Errorf("vol/iss = %d/%d", vol, no)
	}
	if a.RecordYear() != 2026 {
		t.Errorf("year = %d (dari date)", a.RecordYear())
	}
	if len(a.Descriptions) != 1 || strings.Contains(a.Descriptions[0], "John Doe") ||
		strings.Contains(a.Descriptions[0], "Share") {
		t.Errorf("abstrak = %v — penulis/share harus terbuang", a.Descriptions)
	}
	if recs[1].Source != "Jurnal CCS, Vol(29), Iss(1)" {
		t.Errorf("source ke-2 = %q", recs[1].Source)
	}
	if _, err := ParseCCSPosts([]byte("<html>"), entries, "J"); err == nil {
		t.Error("bukan JSON array harus error")
	}
}

// ---- rantai fallback landing ----

func TestFetchLandingFallbackRantai(t *testing.T) {
	cfg := corpusCfg(&mockGetter{})
	rep := &StageReport{}

	t.Run("host-asang-ditolak", func(t *testing.T) {
		_, err := fetchLandingFallback(cfg, rep, &storage.CorpusTarget{ID: 9, Name: "Asing",
			Canonical: "https://contoh-asing.test/"})
		if err == nil || !strings.Contains(err.Error(), "tak didukung") {
			t.Fatalf("err = %v — mau deny-list host asing", err)
		}
	})
	t.Run("canonical-rusak", func(t *testing.T) {
		if _, err := fetchLandingFallback(cfg, rep, &storage.CorpusTarget{ID: 9, Canonical: ""}); err == nil {
			t.Error("canonical kosong harus error")
		}
	})
	t.Run("oscm-tocth-last", func(t *testing.T) {
		m := &mockGetter{}
		home := `<a href="/publication/toc/oscm-volume-19-issue-4-2026">a</a>
		         <a href="/publication/toc/oscm-volume-18-issue-4-2025">b</a>`
		m.set("https://journal.oscm-forum.org/", home, 200)
		m.set("https://journal.oscm-forum.org/publication/toc/oscm-volume-19-issue-4-2026", oscmTOCPage, 200)
		// TOC vol-18 TIDAK di-set → bila di-GET = gagal (harusnya tak dipilih)
		r := &StageReport{}
		recs, err := fetchLandingFallback(corpusCfg(m), r,
			&storage.CorpusTarget{ID: 3203, Name: "Jurnal OSCM", Canonical: "https://journal.oscm-forum.org/"})
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if len(recs) != 2 {
			t.Errorf("record = %d — mau 2 (hanya TOC vol terakhir)", len(recs))
		}
		if r.LandingGET != 2 {
			t.Errorf("LandingGET = %d — mau 2 (homepage + 1 TOC vol-19)", r.LandingGET)
		}
	})
	t.Run("ccs-3get", func(t *testing.T) {
		m := &mockGetter{}
		articles := `<li class="e"><a href="/2026/08/"><span class="t"><strong>August 2026</strong> Volume 29 Number 4</span></a></li>
		         <li class="e"><a href="/2026/02/"><span class="t"><strong>February 2026</strong> Volume 29 Number 1</span></a></li>`
		heading := `<h2>Current Issue Highlights: October 2026, Volume 29 Number 5</h2>`
		m.set("https://criticalcareshock.com/articles/", articles, 200)
		m.set("https://criticalcareshock.com/", heading, 200)
		m.set(ccsPostsURL("https://criticalcareshock.com", "2026-01-31T23:59:59"), ccsPostsJSON, 200)
		r := &StageReport{}
		recs, err := fetchLandingFallback(corpusCfg(m), r,
			&storage.CorpusTarget{ID: 687, Name: "Critical Care and Shock", Canonical: "https://criticalcareshock.com/"})
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if len(recs) != 3 {
			t.Errorf("record = %d — mau 3 (vol 29: sidebar + heading merge)", len(recs))
		}
		if r.LandingGET != 3 {
			t.Errorf("LandingGET = %d — mau 3 (/articles/ + homepage + WP-REST)", r.LandingGET)
		}
	})
}

// ---- integrasi end-to-end (C3b tahap B) ----

// TestStageCorpusLandingFallback — j1: OAI 403 → Crossref 404 → DOAJ 404
// → LANDING (CCS) terselamatkan: 2 artikel vol-29 source="landing",
// provenance TANPA "api" (jujur), lisensi unknown (Fase 3.5/C7) ·
// j4 noRecordsMatch tetap jujur TANPA fallback.
func TestStageCorpusLandingFallback(t *testing.T) {
	st := seedCorpusDB(t, false, false)
	// override kanonik j1 → host landing yang didukung (deny-list ketat).
	if err := st.UpsertSourceProfile(storage.SourceProfile{JournalID: 1,
		Platform: "wordpress", Evidence: "marker", CanonicalURL: "https://criticalcareshock.com/"}); err != nil {
		t.Fatalf("UpsertSourceProfile: %v", err)
	}
	m := &mockGetter{}
	m.set(oaiJ1, "<html><title>Just a moment...</title></html>", 403)
	// Crossref primer & DOAJ tak di-set → 404 default (API kosong)
	m.set("https://criticalcareshock.com/articles/",
		`<li class="e"><a href="/2026/08/"><span class="t"><strong>August 2026</strong> Volume 29 Number 4</span></a></li>
         <li class="e"><a href="/2026/02/"><span class="t"><strong>February 2026</strong> Volume 29 Number 1</span></a></li>`, 200)
	m.set("https://criticalcareshock.com/",
		`<h2>Current Issue Highlights: October 2026, Volume 29 Number 5</h2>`, 200)
	m.set(ccsPostsURL("https://criticalcareshock.com", "2026-01-31T23:59:59"), ccsPostsJSON, 200)
	m.set(oaiJ4, noRecordsXML, 200)

	cfg := corpusCfg(m)
	cfg.Ranks = []int{1}
	rep, err := RunCorpusStage(st, cfg)
	if err != nil {
		t.Fatalf("RunCorpusStage: %v", err)
	}
	if rep.LandingOK != 1 || rep.APIFallback != 0 || rep.OAIMatik != 0 {
		t.Errorf("landing/api/matik = %d/%d/%d — mau 1/0/0", rep.LandingOK, rep.APIFallback, rep.OAIMatik)
	}
	if rep.Processed != 1 || rep.OAIEmpty != 1 {
		t.Errorf("processed/empty = %d/%d — mau 1/1", rep.Processed, rep.OAIEmpty)
	}
	if rep.LandingGET != 3 || rep.CrossrefGET != 1 || rep.DoajGET != 1 {
		t.Errorf("landing/crossref/doaj GET = %d/%d/%d — mau 3/1/1 (rantai penuh)",
			rep.LandingGET, rep.CrossrefGET, rep.DoajGET)
	}
	arts, err := st.Articles(1)
	if err != nil {
		t.Fatalf("Articles: %v", err)
	}
	if len(arts) != 3 {
		t.Fatalf("artikel = %d — mau 3 (vol-29: sidebar 2 + heading 1; 2024-06 dilewati)", len(arts))
	}
	for _, a := range arts {
		if a.Source != storage.ArticleSourceLand {
			t.Errorf("source = %q — mau landing", a.Source)
		}
		if a.VolumeNo != 29 || !a.IsComplete {
			t.Errorf("vol/complete = %d/%v — mau 29/true", a.VolumeNo, a.IsComplete)
		}
		if a.LicenseStatus != storage.LicenseUnknown {
			t.Errorf("lisensi = %q — landing tanpa data lisensi → unknown (C7)", a.LicenseStatus)
		}
		if strings.Contains(a.Provenance, `"api"`) {
			t.Errorf("provenance tak boleh bawa \"api\" utk landing: %s", a.Provenance)
		}
		if !strings.Contains(a.Provenance, `"oai":""`) {
			t.Errorf("provenance oai harus \"\" (jujur): %s", a.Provenance)
		}
	}
}
