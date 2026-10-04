package garuda

import (
	"bytes"
	"fmt"
	stdhtml "html"
	"io"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// Parser halaman Garuda (doc 30 §13.4 langkah 3–4). Dibangun dari markup
// live 3 Okt 2026 (testdata: search-single/search-multi/area):
//   - /area      : <li><a class="ui tiny tag label " href="/area/index/N">Label</a></li>  (40 entri)
//   - /journal?q=: tabel 10 baris/halaman; per baris:
//     a.title-journal href=/journal/view/N + <xmp>judul</xmp>;
//     a.subtitle-journal dgn href = publisher;
//     baris meta "ISSN : <xmp>…</xmp> EISSN : <xmp>…</xmp>" (nilai mentah,
//     placeholder "-");
//     a.label-journal href=/area/index/N = label area (0..n).

type AreaLabel struct {
	ID    int64
	Label string
}

type SearchRow struct {
	GarudaID  int64
	Title     string
	Publisher string
	PISSN     string // RAW apa adanya (K1); "-" → string biasa, filter di pemanggil
	EISSN     string
	Areas     []AreaLabel
}

type SearchPage struct {
	Page    int
	OfPages int
	Total   int // "Total Record : N"
	Found   int // blok "… N Journals / Conference found" (dgn koma ribuan); -1 bila tak ada
	Rows    []SearchRow
}

var (
	// found: "… : <b>15,957</b> Journals / Conference found" (koma ribuan).
	reFound = regexp.MustCompile(`:\s*<b>([\d,]+)</b>\s*Journals / Conference found`)
	// pagination-info: "Page 1 of 1596 | Total Record : 15957"
	rePageOf = regexp.MustCompile(`Page\s+(\d+)\s+of\s+(\d+)`)
	reTotal  = regexp.MustCompile(`Total Record\s*:\s*([\d,]+)`)
	// "ISSN : - … EISSN : 24428620" — [^E] mencegah "ISSN" di dalam "EISSN"
	// (RE2 tanpa lookbehind) dan memaksa urutan [p, e] pada teks baris meta.
	rePISSN = regexp.MustCompile(`(?:^|[^E])ISSN\s*:\s*(\S+)`)
	reEISSN = regexp.MustCompile(`EISSN\s*:\s*(\S+)`)
	reView  = regexp.MustCompile(`/journal/view/(\d+)`)
	reArea  = regexp.MustCompile(`/area/index/(\d+)`)
)

// ParseAreaList mem-parsing daftar taxonomi /area (entri li > a.tag-label;
// link label-journal di dalam baris jurnal BUKAN taxonomi — dibuang).
func ParseAreaList(r io.Reader) ([]AreaLabel, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("gagal parse HTML /area: %w", err)
	}
	var out []AreaLabel
	for _, a := range findElements(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "a" &&
			classHas(n, "tag") && classHas(n, "label") &&
			parentName(n) == "li"
	}) {
		href := getAttr(a, "href")
		m := reArea.FindStringSubmatch(href)
		if m == nil {
			continue
		}
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			continue
		}
		label := strings.TrimSpace(cleanText(a))
		if label == "" {
			continue
		}
		out = append(out, AreaLabel{ID: id, Label: label})
	}
	return out, nil
}

// ParseSearchPage mem-parsing satu halaman hasil /journal?q=…
func ParseSearchPage(r io.Reader) (*SearchPage, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("baca HTML search: %w", err)
	}
	doc, err := html.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("gagal parse HTML search: %w", err)
	}

	page := &SearchPage{Found: -1}
	if m := reFound.FindSubmatch(raw); m != nil {
		if n, err := strconv.Atoi(strings.ReplaceAll(string(m[1]), ",", "")); err == nil {
			page.Found = n
		}
	}
	if m := rePageOf.FindSubmatch(raw); m != nil {
		page.Page, _ = strconv.Atoi(string(m[1]))
		page.OfPages, _ = strconv.Atoi(string(m[2]))
	}
	if m := reTotal.FindSubmatch(raw); m != nil {
		page.Total, _ = strconv.Atoi(strings.ReplaceAll(string(m[1]), ",", ""))
	}

	for _, tr := range findElements(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "tr"
	}) {
		row, ok := parseSearchRow(tr)
		if ok {
			page.Rows = append(page.Rows, row)
		}
	}
	return page, nil
}

// parseSearchRow mengeluarkan satu baris hasil; ok=false bila tanpa
// a.title-journal (baris bukan data — dilewati tanpa menggagalkan halaman).
func parseSearchRow(tr *html.Node) (SearchRow, bool) {
	var row SearchRow

	titleA := firstElement(tr, func(n *html.Node) bool {
		return n.Data == "a" && classHas(n, "title-journal")
	})
	if titleA == nil {
		return row, false
	}
	if m := reView.FindStringSubmatch(getAttr(titleA, "href")); m != nil {
		row.GarudaID, _ = strconv.ParseInt(m[1], 10, 64)
	}
	row.Title = cleanText(titleA)

	// publisher = a.subtitle-journal YANG punya href (baris meta tanpa href).
	pubA := firstElement(tr, func(n *html.Node) bool {
		return n.Data == "a" && classHas(n, "subtitle-journal") && getAttr(n, "href") != ""
	})
	if pubA != nil {
		row.Publisher = cleanText(pubA)
	}

	// baris meta ISSN/EISSN = a.subtitle-journal tanpa href.
	metaA := firstElement(tr, func(n *html.Node) bool {
		return n.Data == "a" && classHas(n, "subtitle-journal") && getAttr(n, "href") == ""
	})
	if metaA != nil {
		meta := cleanText(metaA)
		if m := rePISSN.FindStringSubmatch(meta); m != nil {
			row.PISSN = m[1]
		}
		if m := reEISSN.FindStringSubmatch(meta); m != nil {
			row.EISSN = m[1]
		}
	}

	for _, a := range findElements(tr, func(n *html.Node) bool {
		return n.Data == "a" && classHas(n, "label-journal")
	}) {
		m := reArea.FindStringSubmatch(getAttr(a, "href"))
		if m == nil {
			continue
		}
		id, _ := strconv.ParseInt(m[1], 10, 64)
		label := strings.TrimSpace(cleanText(a))
		if label != "" {
			row.Areas = append(row.Areas, AreaLabel{ID: id, Label: label})
		}
	}
	return row, true
}

// ---------- util DOM ----------

func findElements(root *html.Node, pred func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if pred(n) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

func firstElement(root *html.Node, pred func(*html.Node) bool) *html.Node {
	for _, n := range findElements(root, pred) {
		return n
	}
	return nil
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func classHas(n *html.Node, cls string) bool {
	for _, c := range strings.Fields(getAttr(n, "class")) {
		if c == cls {
			return true
		}
	}
	return false
}

func parentName(n *html.Node) string {
	if n.Parent == nil {
		return ""
	}
	return n.Parent.Data
}

// cleanText = teks rekursif + TrimSpace; UnescapeString HANYA bila mengandung
// entitas "&amp;" (isi <xmp> live = teks literal `&`, li = sudah didekode
// parser — keduanya aman; ini pengaman bila server berubah).
func cleanText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	t := b.String()
	if strings.Contains(t, "&amp;") {
		t = stdhtml.UnescapeString(t)
	}
	return strings.TrimSpace(t)
}
