package volume

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---- fixture builders (murni, tanpa GET) ----

func crJSON(items ...string) string {
	return `{"message":{"items":[` + strings.Join(items, ",") + `]}}`
}

func art(vol, issue, y, m int) string {
	return `{"volume":"` + itoa(vol) + `","issue":"` + itoa(issue) +
		`","published":{"date-parts":[[` + itoa(y) + `,` + itoa(m) + `,1]]}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

const archiveHTML = `
<div class="toc">
 <ul>
  <li><a href="https://j.example.org/index.php/x/issue/view/9">Vol. 11 No. 1 (2025)</a></li>
  <li><a href="https://j.example.org/index.php/x/issue/view/8">Vol. 10 No. 2 (Desember 2024)</a></li>
  <li><a href="https://j.example.org/index.php/x/issue/view/7">Vol. 10 No. 1 (Juni 2024)</a></li>
  <li><a href="https://j.example.org/index.php/x/issue/archive">Archive</a></li>
 </ul>
</div>`

const tocHTML = `
<html><head><title>Vol. 11 No. 1 (2025)</title></head><body>
 <h1>Vol. 11 No. 1 (2025)</h1>
 <a href="https://j.example.org/index.php/x/article/view/101">A</a>
 <a href="https://j.example.org/index.php/x/article/view/102">B</a>
 <a href="https://j.example.org/index.php/x/article/view/102#anchor">B-dup</a>
 <a href="https://j.example.org/index.php/x/article/view/103">C</a>
</body></html>`

// ---- ParseArchiveIssues ----

func TestParseArchiveIssues(t *testing.T) {
	got := ParseArchiveIssues(archiveHTML, "https://j.example.org/index.php/x/issue/archive")
	if len(got) != 3 {
		t.Fatalf("issue count = %d, mau 3 (archive link dibuang)", len(got))
	}
	// Baris terbaru dulu (urutan dokumen).
	if got[0].Vol != 11 || got[0].No != 1 || got[0].Year != 2025 {
		t.Errorf("issue#1 = %+v, mau Vol11 No1 Year2025", got[0])
	}
	if got[1].Month != 12 || got[1].Year != 2024 {
		t.Errorf("issue#2 = %+v, mau Month12 Year2024 (bulan Indonesia)", got[1])
	}
	if !strings.HasSuffix(got[0].IssueURL, "/issue/view/9") {
		t.Errorf("issue_url = %q", got[0].IssueURL)
	}
}

// ---- ParseCrossref ----

func TestParseCrossref(t *testing.T) {
	got := ParseCrossref(crJSON(
		art(10, 2, 2024, 11), art(10, 2, 2024, 12), // min month = 11
		art(10, 1, 2024, 6),
	))
	if got.Rows != 3 {
		t.Fatalf("rows = %d, mau 3", got.Rows)
	}
	ci := got.Issues[[2]int64{10, 2}]
	if ci.Count != 2 || ci.MinMonth != 11 {
		t.Errorf("(10,2) = %+v, mau Count2 MinMonth11 (refine-D min)", ci)
	}
	if got.MaxYear != 2024 {
		t.Errorf("MaxYear = %d, mau 2024", got.MaxYear)
	}
}

// ---- ParseTOC ----

func TestParseTOC(t *testing.T) {
	got := ParseTOC(tocHTML)
	if got.Vol != 11 || got.No != 1 {
		t.Errorf("volno = %d/%d, mau 11/1", got.Vol, got.No)
	}
	if got.ArticleCount != 3 {
		t.Errorf("articles = %d, mau 3 (href dup dide-dup)", got.ArticleCount)
	}
}

// ---- Compute: gap-A (anchor chain j185-case) ----

func TestComputeGapA_AnchorChainJ185(t *testing.T) {
	// Issue terbaru (Vol11No1, 2025) BELUM ada di Crossref (deposit delay) —
	// tanpa archive-year, anchor = 2024 → window geser mundur. Gap-A fix:
	// anchor = max(Crossref 2024, archive 2025) = 2025.
	in := Input{
		Archive: ParseArchiveIssues(archiveHTML, "https://j.example.org/index.php/x/issue/archive"),
		Crossref: ParseCrossref(crJSON(
			art(10, 2, 2024, 12), art(10, 1, 2024, 6),
		)),
		TOC: map[string]int{
			"https://j.example.org/index.php/x/issue/view/9": 3,
			"https://j.example.org/index.php/x/issue/view/8": 2,
			"https://j.example.org/index.php/x/issue/view/7": 1,
		},
	}
	r := Compute(in)
	if r.AnchorYear != 2025 {
		t.Fatalf("anchor = %d, mau 2025 (gap-A: archive-year mengalahkan Crossref)", r.AnchorYear)
	}
	if r.AnchorSource != "archive" {
		t.Errorf("anchor_source = %q, mau archive", r.AnchorSource)
	}
	if r.WindowStart != 2023 || r.WindowEnd != 2025 {
		t.Errorf("window = %d..%d, mau 2023..2025", r.WindowStart, r.WindowEnd)
	}
	// Vol11No1 masuk window dgn TOC count 3.
	if r.WindowIssues < 1 {
		t.Fatalf("window issues = %d, mau ≥1", r.WindowIssues)
	}
	if r.WindowCounted != r.WindowIssues {
		t.Errorf("window counted = %d / %d, mau sama semua", r.WindowCounted, r.WindowIssues)
	}
}

// ---- Compute: gap-B (DOI-tipis j679-case) ----

func TestComputeGapB_CrossrefKosong(t *testing.T) {
	// Crossref 404 total → anchor HARUS dari archive-year; tak ada asumsi.
	in := Input{
		Archive:  ParseArchiveIssues(archiveHTML, "https://j.example.org/index.php/x/issue/archive"),
		Crossref: ParseCrossref(`{}`), // kosong
		TOC:      map[string]int{},
	}
	r := Compute(in)
	if r.AnchorYear != 2025 {
		t.Fatalf("anchor = %d, mau 2025 (gap-B: archive-year fallback)", r.AnchorYear)
	}
	if r.AnchorSource != "archive" {
		t.Errorf("anchor_source = %q, mau archive", r.AnchorSource)
	}
	if r.ArtPerVol != 0 || r.IsComplete {
		t.Errorf("D2 = %v / complete=%v — mau 0/false (jujur: tanpa TOC)", r.ArtPerVol, r.IsComplete)
	}
}

func TestComputeGapB_TotalGagal(t *testing.T) {
	// Tanpa archive-year & tanpa Crossref → anchor null, is_complete false,
	// IssuesJSON tetap "[]" VALID (G4 — writer tak menolak).
	r := Compute(Input{Archive: []ArchiveIssue{{IssueURL: "u"}}})
	if r.AnchorYear != 0 || r.IsComplete {
		t.Fatalf("anchor=%d complete=%v — mau 0/false (jujur)", r.AnchorYear, r.IsComplete)
	}
	if r.IssuesJSON != "[]" {
		t.Errorf("IssuesJSON = %q, mau \"[]\" valid (G4)", r.IssuesJSON)
	}
	if err := json.Unmarshal([]byte(r.IssuesJSON), new([]any)); err != nil {
		t.Errorf("IssuesJSON bukan array valid: %v (G4)", err)
	}
}

// ---- G1: early-view Crossref TIDAK menarik anchor ----

func TestComputeG1_EarlyViewAnchor(t *testing.T) {
	// Crossref punya (12,1) dgn tahun 2026 (online-first, issue BELUM di
	// archive). Tanpa G1: anchor global=2026 → window geser. Dgn G1: hanya
	// VI match archive → anchor=2025.
	in := Input{
		Archive: ParseArchiveIssues(archiveHTML, "https://j.example.org/index.php/x/issue/archive"),
		Crossref: ParseCrossref(crJSON(
			art(11, 1, 2025, 2), art(12, 1, 2026, 1), // (12,1) early-view
		)),
		TOC: map[string]int{
			"https://j.example.org/index.php/x/issue/view/9": 3,
			"https://j.example.org/index.php/x/issue/view/8": 2,
			"https://j.example.org/index.php/x/issue/view/7": 1,
		},
	}
	r := Compute(in)
	if r.AnchorYear != 2025 {
		t.Fatalf("anchor = %d, mau 2025 (G1: early-view 2026 diabaikan utk anchor)", r.AnchorYear)
	}
	if r.WindowEnd != 2025 {
		t.Errorf("window_end = %d, mau 2025", r.WindowEnd)
	}
}

// ---- G2: baris archive Vol=0 tak double-count ----

func TestComputeG2_Vol0AntiDoubleCount(t *testing.T) {
	// Baris archive #1: volno GAGAL parse (Vol=0), tapi year=2025 terbaca.
	// Crossref (11,1) 2025 = issue SAMA → tanpa G2: dihitung 2× (baris
	// archive + crossref-only) → D1 over. Dgn G2: baris Vol=0 tak dihitung
	// WindowIssues; crossref-only (11,1) dihitung 1×.
	archive := []ArchiveIssue{
		{IssueURL: "u1", Vol: 0, No: 0, Year: 2025}, // unparsed volno
		{IssueURL: "u2", Vol: 10, No: 2, Year: 2024},
	}
	in := Input{
		Archive: archive,
		Crossref: ParseCrossref(crJSON(
			art(11, 1, 2025, 2), art(10, 2, 2024, 12),
		)),
		TOC: map[string]int{"u2": 2}, // D1: by URL — u1 (Vol=0) & crossref-only (11,1) tanpa URL tak di-TOC
	}
	r := Compute(in)
	if r.UnparsedVI != 1 {
		t.Errorf("unparsed = %d, mau 1 (baris Vol=0)", r.UnparsedVI)
	}
	// Window 2023-2025: (11,1) via crossref-only + (10,2) via archive = 2
	// — BUKAN 3 (tanpa G2, baris archive Vol=0 dgn year 2025 ikut terhitung).
	if r.WindowIssues != 2 {
		t.Errorf("window issues = %d, mau 2 (G2: tanpa double-count)", r.WindowIssues)
	}
}

// ---- G3: early-view tahun sebelumnya tak menurunkan bulan issue ----

func TestParseCrossrefG3_EarlyViewMonth(t *testing.T) {
	// Issue (10,2): artikel utama Des 2024 + artikel early-view Mar 2023.
	// Tanpa G3: MinMonth=3 (salah). Dgn G3: MinMonth dari thn MaxYear (2024)
	// = 12.
	got := ParseCrossref(crJSON(
		art(10, 2, 2024, 12), art(10, 2, 2023, 3),
	))
	ci := got.Issues[[2]int64{10, 2}]
	if ci.MinMonth != 12 {
		t.Errorf("MinMonth = %d, mau 12 (G3: filter early-view thn lalu)", ci.MinMonth)
	}
	if ci.MaxYear != 2024 {
		t.Errorf("MaxYear = %d, mau 2024", ci.MaxYear)
	}
}

// ---- H11: non-OJS (archive nil, Crossref penuh) — 30 jurnal ----

func TestComputeNonOJS_CrossrefOnly(t *testing.T) {
	// Driver non-OJS: Archive=nil — anchor fallback global MaxYear (G1),
	// issue dlm window = crossref-only loop, TOCable=0 → complete=true
	// (retry tak berdaya; D2=0 jujur, driver set source='crossref').
	r := Compute(Input{
		Archive: nil,
		Crossref: ParseCrossref(crJSON(
			art(11, 1, 2025, 2), art(10, 2, 2024, 12), art(10, 1, 2024, 6),
		)),
	})
	if r.AnchorYear != 2025 {
		t.Fatalf("anchor = %d, mau 2025 (global fallback utk archive nil)", r.AnchorYear)
	}
	if r.WindowIssues != 3 || r.WindowTOCable != 0 {
		t.Errorf("window issues=%d tocable=%d — mau 3/0 (semua crossref-only)",
			r.WindowIssues, r.WindowTOCable)
	}
	if !r.IsComplete {
		t.Error("is_complete = false — mau true (H18: TOCable=0, retry tak berdaya)")
	}
	if r.ArtPerVol != 0 {
		t.Errorf("D2 = %v, mau 0 (tanpa TOC)", r.ArtPerVol)
	}
}

// ---- H18: crossref-only TIDAK menuntut TOC (anti infinite-retry) ----

func TestComputeH18_CrossrefOnlyAntiRetry(t *testing.T) {
	// Window: (10,2) archive dgn URL + (11,1) crossref-only (tanpa URL —
	// archive baris issue terbaru Vol=0, volno gagal parse → G2 + gap-A
	// archive-year 2025 tarik anchor). Driver TAK BISA TOC utk (11,1) →
	// tanpa H18: counted(1) < issues(2) → parsial FOREVER. Dgn H18:
	// TOCable=1 counted=1 → complete=true.
	archive := []ArchiveIssue{
		{IssueURL: "https://j.example.org/issue/view/9", Vol: 0, No: 0, Year: 2025}, // unparsed
		{IssueURL: "https://j.example.org/issue/view/8", Vol: 10, No: 2, Year: 2024},
	}
	in := Input{
		Archive: archive,
		Crossref: ParseCrossref(crJSON(
			art(11, 1, 2025, 2), art(10, 2, 2024, 12),
		)),
		TOC: map[string]int{"https://j.example.org/issue/view/8": 2}, // (11,1) tak bisa di-TOC
	}
	r := Compute(in)
	if r.WindowIssues != 2 || r.WindowTOCable != 1 || r.WindowCounted != 1 {
		t.Fatalf("issues=%d tocable=%d counted=%d — mau 2/1/1",
			r.WindowIssues, r.WindowTOCable, r.WindowCounted)
	}
	if !r.IsComplete {
		t.Error("is_complete = false — mau true (H18: TOCable terpenuhi semua)")
	}
	if r.ArtPerVol != 2 {
		t.Errorf("D2 = %v, mau 2 (rata2 dari 1 issue TOC)", r.ArtPerVol)
	}
}

// ---- H10: month di luar 1-12 diabaikan ----

func TestParseCrossrefH10_InvalidMonth(t *testing.T) {
	// date-parts dgn month 15 (corrupt) — H10: tak dihitung sbg MinMonth.
	got := ParseCrossref(`{"message":{"items":[
		{"volume":"10","issue":"2","published":{"date-parts":[[2024,15,1]]}}
	]}}`)
	ci := got.Issues[[2]int64{10, 2}]
	if ci.MinMonth != 0 {
		t.Errorf("MinMonth = %d, mau 0 (month 15 invalid — H10)", ci.MinMonth)
	}
	if ci.MaxYear != 2024 {
		t.Errorf("MaxYear = %d, mau 2024", ci.MaxYear)
	}
}

func TestParseArchiveIssuesG6_MultiLine(t *testing.T) {
	html := `<a href="https://j.example.org/index.php/x/issue/view/9">
Vol. 11 No. 1
(2025)
</a>`
	got := ParseArchiveIssues(html, "https://j.example.org/index.php/x/issue/archive")
	if len(got) != 1 {
		t.Fatalf("count = %d, mau 1 (G6 (?s) multi-line)", len(got))
	}
	if got[0].Vol != 11 || got[0].No != 1 || got[0].Year != 2025 {
		t.Errorf("got %+v — mau Vol11 No1 Year2025 dari teks multi-line", got[0])
	}
}

// ---- refine-D: bulan issue = min(bulan artikel) ----

func TestComputeRefineD_MinBulan(t *testing.T) {
	in := Input{
		Archive: ParseArchiveIssues(archiveHTML, "https://j.example.org/index.php/x/issue/archive"),
		Crossref: ParseCrossref(crJSON(
			art(11, 1, 2025, 3), art(11, 1, 2025, 1), // min = 1
			art(10, 2, 2024, 12), art(10, 1, 2024, 6),
		)),
		TOC: map[string]int{
			"https://j.example.org/index.php/x/issue/view/9": 3,
			"https://j.example.org/index.php/x/issue/view/8": 2,
		},
	}
	r := Compute(in)
	if r.ReleaseMonths == "" {
		t.Fatal("release_months kosong")
	}
	// Window 2023-2025: (11,1) month=1 · (10,2) month=12 · (10,1) month=6 → "1,6,12"
	if r.ReleaseMonths != "1,6,12" {
		t.Errorf("release_months = %q, mau \"1,6,12\" (min-bulan, terurut)", r.ReleaseMonths)
	}
}

// ---- IsComplete parsial (refine-C input) ----

func TestComputeIsCompleteParsial(t *testing.T) {
	// 2 issue dlm window; TOC hanya utk 1 → parsial.
	in := Input{
		Archive: ParseArchiveIssues(archiveHTML, "https://j.example.org/index.php/x/issue/archive"),
		Crossref: ParseCrossref(crJSON(
			art(11, 1, 2025, 2), art(10, 2, 2024, 12), art(10, 1, 2024, 6),
		)),
		TOC: map[string]int{"https://j.example.org/index.php/x/issue/view/9": 3}, // (10,2) & (10,1) miss
	}
	r := Compute(in)
	if r.IsComplete {
		t.Error("is_complete = true — mau FALSE (window miss TOC count)")
	}
	if r.WindowCounted >= r.WindowIssues {
		t.Errorf("counted=%d issues=%d — mau counted < issues", r.WindowCounted, r.WindowIssues)
	}
}

// ---- issues JSON nullable jujur ----

func TestComputeIssuesJSONJujur(t *testing.T) {
	r := Compute(Input{
		Archive:  ParseArchiveIssues(archiveHTML, "https://j.example.org/index.php/x/issue/archive"),
		Crossref: ParseCrossref(crJSON(art(11, 1, 2025, 2))),
	})
	var arr []map[string]any
	if err := json.Unmarshal([]byte(r.IssuesJSON), &arr); err != nil {
		t.Fatalf("issues bukan JSON array: %v", err)
	}
	if len(arr) < 3 {
		t.Fatalf("issues len = %d, mau ≥3 (raw all-year)", len(arr))
	}
	// Vol11No1 ada di Crossref → year_source=crossref; sisanya archive.
	if arr[0]["year_source"] != "crossref" {
		t.Errorf("issues[0].year_source = %v, mau crossref", arr[0]["year_source"])
	}
}
