package garuda

import (
	"fmt"
	"html"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// ViewInfo = field terpetakan dari halaman /journal/view/N (E3, doc 31 §2).
// Nilai placeholder Garuda ("-") dinormalkan jadi string kosong.
type ViewInfo struct {
	NotFound  bool     // halaman "Record Not Found" (ID basi/keliru)
	Title     string   // div.j-meta-title
	Publisher string   // "Published by" → <a>
	PrintISSN string   // "ISSN :" → <a> (tanpa lookbehind E)
	EISSN     string   // "EISSN :" → <a>
	DOIURL    string   // "DOI :" → <a> (angka tanpa hyphen)
	HomeURL   string   // link "Home Page" = URL OJS resmi (16/18 halaman)
	OAIURL    string   // link "OAI Link" = URL OAI jurnal (16/18)
	Areas     []string // label <a href="/area/index/N"> (cross-check subject)
	// Blok sidebar "Filter by Year" (embed di HTML, doc 32 §4): tahun awal
	// & akhir artikel terindeks di halaman ini — penentu entri duplikat
	// yang masih AKTIF. 0 = blok tak ada (jurnal tanpa artikel / halaman
	// rusak).
	YearFrom int
	YearTo   int
}

var (
	viewTitleRe = regexp.MustCompile(`(?is)<div class="j-meta-title">\s*(.*?)\s*</div>`)
	viewPubRe   = regexp.MustCompile(`(?is)Published by\s*<a[^>]*>(.*?)</a>`)
	viewEISSNRe = regexp.MustCompile(`(?is)EISSN\s*:\s*<a[^>]*>(.*?)</a>`)
	// RE2 tanpa lookbehind: "ISSN" boleh cocok hanya bila tak diawali huruf
	// (menolak substring "ISSN" di "EISSN") — group 2 = nilai ISSN.
	viewISSNRe   = regexp.MustCompile(`(?is)(^|[^A-Za-z])ISSN\s*:\s*<a[^>]*>(.*?)</a>`)
	viewDOIRe    = regexp.MustCompile(`(?is)DOI\s*:\s*<a[^>]*>(.*?)</a>`)
	viewAreaRe   = regexp.MustCompile(`(?is)<a[^>]*href="/area/index/\d+"[^>]*>(.*?)</a>`)
	viewNotFound = regexp.MustCompile(`(?is)Record Not Found`)
	// Blok "Filter by Year" (lihat user 4 Okt 2026 — doc 31 §2):
	//   <a class="ui top attached label">Filter by Year</a>
	//   <p> 2015 <span style="float: right"> 2026</span></p>
	// group 1 = tahun awal, group 2 = tahun akhir.
	viewYearRe = regexp.MustCompile(`(?is)Filter by Year</a>\s*<p>\s*(\d{4})\s*<span[^>]*>\s*(\d{4})`)
)

// linkView mengambil href dari <a> yang teksnya persis label ("Home Page" /
// "OAI Link") — pola h5: <a href="…" target="_blank"><i …></i>Label</a>.
func linkView(doc, label string) string {
	re := regexp.MustCompile(`(?is)<a[^>]*href="([^"]+)"[^>]*>\s*(?:<i[^>]*></i>)*\s*` +
		regexp.QuoteMeta(label) + `\s*</a>`)
	if m := re.FindStringSubmatch(doc); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// bersihView membuang tag sisa, trim, decode entitas, dan menormalkan
// placeholder "-" menjadi string kosong.
func bersihView(s string) string {
	out := strings.TrimSpace(tagRe.ReplaceAllString(s, " "))
	out = strings.TrimSpace(html.UnescapeString(out))
	if out == "-" {
		return ""
	}
	return out
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

// ParseViewPage memetakan field halaman /journal/view/N dari HTML mentah
// (fixture E3 — doc 31 §2). Baca seluruh reader; error hanya bila konten
// kosong/gagal dibaca.
func ParseViewPage(r io.Reader) (*ViewInfo, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("baca halaman view: %w", err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("halaman view kosong")
	}
	doc := string(b)
	v := &ViewInfo{NotFound: viewNotFound.MatchString(doc)}
	if m := viewTitleRe.FindStringSubmatch(doc); m != nil {
		v.Title = bersihView(m[1])
	}
	if m := viewPubRe.FindStringSubmatch(doc); m != nil {
		v.Publisher = bersihView(m[1])
	}
	if m := viewEISSNRe.FindStringSubmatch(doc); m != nil {
		v.EISSN = bersihView(m[1])
	}
	if m := viewISSNRe.FindStringSubmatch(doc); m != nil {
		v.PrintISSN = bersihView(m[2])
	}
	if m := viewDOIRe.FindStringSubmatch(doc); m != nil {
		v.DOIURL = bersihView(m[1])
	}
	v.HomeURL = linkView(doc, "Home Page")
	v.OAIURL = linkView(doc, "OAI Link")
	if m := viewYearRe.FindStringSubmatch(doc); m != nil {
		v.YearFrom, _ = strconv.Atoi(m[1])
		v.YearTo, _ = strconv.Atoi(m[2])
	}
	seen := map[string]bool{}
	for _, m := range viewAreaRe.FindAllStringSubmatch(doc, -1) {
		lbl := bersihView(m[1])
		if lbl != "" && !seen[lbl] {
			seen[lbl] = true
			v.Areas = append(v.Areas, lbl)
		}
	}
	return v, nil
}
