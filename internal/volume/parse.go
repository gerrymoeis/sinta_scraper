// Package volume = logika stage volume (doc 41 §8.1 + §13.7 K6 REVISED +
// gap-A/B + refine-C/D approve 14 Okt). Parser HTML/JSON FUNGSI MURNI (tanpa
// HTTP) — driver stage (nanti) yang GET + memanggil sini. Hybrid per-field
// per-sumber: archive=issue links · Crossref=date/anchor/volno · TOC=D2 count.
package volume

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ArchiveIssue = SATU baris issue dari halaman archive OJS (raw, semua tahun).
type ArchiveIssue struct {
	IssueURL string `json:"issue_url"`
	Vol      int64  `json:"vol"`                // nullable di JSON — 0 = tak ter-parse
	No       int64  `json:"no"`                 // nullable
	Year     int64  `json:"year"`               // nullable — dari baris archive (gap-A fallback)
	Month    int64  `json:"month"`              // nullable — 1-12 dari baris archive
	TextRaw  string `json:"text_raw,omitempty"` // teks anchor utk audit
}

// CrossrefIssue = agregat per (vol,issue) dari items Crossref (1 item/artikel).
type CrossrefIssue struct {
	Count    int   // jumlah artikel dgn vol+issue ini
	MinMonth int64 // refine-D: min(bulan date artikel) = bulan rilis issue
	MaxYear  int64 // max tahun date artikel (utk anchor chain gap-A)
}

// CrossrefData = hasil ParseCrossref utk SATU jurnal.
type CrossrefData struct {
	Issues  map[[2]int64]CrossrefIssue
	Rows    int // total items terparsing (audit)
	MaxYear int64
}

// TOCIssue = hasil parse halaman TOC/issue OJS.
type TOCIssue struct {
	Vol          int64
	No           int64
	ArticleCount int
}

var (
	// Baris issue archive: teks anchor link issue/view/<id>. G6: (?s) — anchor
	// multi-line tetap terbaca (non-greedy stop di </a> pertama).
	reArchiveHref = regexp.MustCompile(`(?is)href="([^"]*issue/view/(\d+))"[^>]*>(.*?)</a>`)
	reVol         = regexp.MustCompile(`(?i)\bvol(?:ume)?\.?\s*(\d{1,4})`)
	reNo          = regexp.MustCompile(`(?i)\bno(?:mber|mr)?\.?\s*(\d{1,3})\b`)
	reYear        = regexp.MustCompile(`\b((?:19|20)\d{2})\b`)
	reTag         = regexp.MustCompile(`<[^>]+>`)
	reSpace       = regexp.MustCompile(`\s+`)
)

// bulanID = kamus bulan Indonesia utk parse baris archive (opsional).
var bulanID = map[string]int64{
	"januari": 1, "februari": 2, "maret": 3, "april": 4, "mei": 5,
	"juni": 6, "juli": 7, "agustus": 8, "september": 9, "oktober": 10,
	"november": 11, "desember": 12,
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "jun": 6, "jul": 7,
	"agt": 8, "sep": 9, "okt": 10, "nov": 11, "des": 12,
	"january": 1, "february": 2, "march": 3, "may": 5, "june": 6,
	"july": 7, "august": 8, "october": 10,
}

// ParseArchiveIssues mengekstrak baris issue dari halaman archive OJS (fungsi
// murni; base = URL archive utk resolusi relatif issue/view/<id>). Vol/no/
// year/month = parser longgar baris teks — nullable (0) bila tak ter-parse
// (jujur; rate-nya di-log driver utk iterasi #1 — gap-A).
func ParseArchiveIssues(body, base string) []ArchiveIssue {
	var out []ArchiveIssue
	seen := map[string]bool{}
	for _, m := range reArchiveHref.FindAllStringSubmatch(body, -1) {
		href, text := m[1], stripHTML(m[3])
		u := absURL(base, href)
		if seen[u] || strings.Contains(strings.ToLower(u), "issue/archive") {
			continue
		}
		seen[u] = true
		iss := ArchiveIssue{IssueURL: u, TextRaw: text}
		iss.Vol = firstInt(reVol, text)
		iss.No = firstInt(reNo, text)
		if y := firstInt(reYear, text); y != 0 {
			iss.Year = y
		}
		low := strings.ToLower(text)
		for k, mn := range bulanID {
			if strings.Contains(low, k) {
				iss.Month = mn
				break
			}
		}
		out = append(out, iss)
	}
	return out
}

// ParseCrossref mengekstrak agregat (vol,issue) dari response Crossref works
// (items rows≤1000 — pola probep4: volume/issue/published date-parts per
// artikel; fungsi murni, tanpa HTTP). G3: MinMonth dihitung dari artikel
// dgn year == MaxYear issue — early-view tahun sebelumnya TIDAK menurunkan
// bulan rilis issue.
func ParseCrossref(body string) CrossrefData {
	var payload struct {
		Message struct {
			Items []struct {
				Volume  json.RawMessage `json:"volume"`
				Issue   json.RawMessage `json:"issue"`
				Publish struct {
					DateParts [][]int64 `json:"date-parts"`
				} `json:"published"`
			} `json:"items"`
		} `json:"message"`
	}
	out := CrossrefData{Issues: map[[2]int64]CrossrefIssue{}}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return out
	}
	// Tahap 1: kumpul (year→bulan) per issue + MaxYear global.
	ym := map[[2]int64]map[int64][]int64{}
	for _, it := range payload.Message.Items {
		vol := jsonInt64(it.Volume)
		no := jsonInt64(it.Issue)
		if vol == 0 || no == 0 {
			continue
		}
		key := [2]int64{vol, no}
		if ym[key] == nil {
			ym[key] = map[int64][]int64{}
		}
		for _, dp := range it.Publish.DateParts {
			if len(dp) < 1 || dp[0] == 0 {
				continue
			}
			year := dp[0]
			var month int64
			if len(dp) >= 2 {
				month = dp[1]
			}
			ym[key][year] = append(ym[key][year], month)
			if year > out.MaxYear {
				out.MaxYear = year
			}
		}
	}
	// Tahap 2: MaxYear = max tahun; MinMonth = min bulan artikel thn MaxYear (G3).
	for key, byYear := range ym {
		agg := CrossrefIssue{}
		for year := range byYear {
			if year > agg.MaxYear {
				agg.MaxYear = year
			}
			agg.Count += len(byYear[year])
		}
		for _, m := range byYear[agg.MaxYear] {
			if m >= 1 && m <= 12 && (agg.MinMonth == 0 || m < agg.MinMonth) { // H10: guard 1-12
				agg.MinMonth = m
			}
		}
		out.Issues[key] = agg
	}
	out.Rows = len(payload.Message.Items)
	return out
}

// ParseTOC mengekstrak vol/no + jumlah artikel unik dari halaman TOC/issue
// OJS (parser longgar P4-repair: teks Vol X No Y dari title/h1/h2; artikel =
// href article/view unik — pola teruji 10/10 probep4b).
func ParseTOC(body string) TOCIssue {
	var out TOCIssue
	text := stripHTML(body)
	out.Vol = firstInt(reVol, text)
	out.No = firstInt(reNo, text)
	seen := map[string]bool{}
	for _, m := range reTOCArticle.FindAllStringSubmatch(body, -1) {
		k := m[1]
		if i := strings.IndexAny(k, "#?"); i >= 0 {
			k = k[:i] // strip fragment/query utk kunci dedup
		}
		if !seen[k] {
			seen[k] = true
			out.ArticleCount++
		}
	}
	return out
}

var reTOCArticle = regexp.MustCompile(`(?i)href="([^"]*article/view/\d+[^"]*)"`)

// ---- helper ----

func stripHTML(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}

func firstInt(re *regexp.Regexp, s string) int64 {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	for _, g := range m[1:] {
		if g == "" {
			continue
		}
		n, err := strconv.ParseInt(g, 10, 64)
		if err == nil {
			return n
		}
	}
	return 0
}

// absURL: href absolut → pakai; "/x" → origin(base)+x; relatif → dirname(base)+x.
// (P4 buktikan href archive utamanya absolut — fallback utk theme lain.)
func absURL(base, href string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	origin := base
	if n := schemeLen(base); n > 0 {
		rest := base[n:]
		if i := strings.Index(rest, "/"); i >= 0 {
			origin = base[:n+i]
		} else {
			origin = base
		}
	}
	if strings.HasPrefix(href, "/") {
		return origin + href
	}
	dir := base
	if i := strings.LastIndex(base, "/"); i > schemeLen(base) {
		dir = base[:i]
	}
	return dir + "/" + href
}

func schemeLen(u string) int {
	if strings.HasPrefix(u, "https://") {
		return 8
	}
	if strings.HasPrefix(u, "http://") {
		return 7
	}
	return 0
}

func jsonInt64(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, _ := strconv.ParseInt(s, 10, 64)
		return n
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	return 0
}

// sortedVI mengembalikan pasang (vol,no) terurut (determinisme test/log).
func sortedVI(m map[[2]int64]CrossrefIssue) [][2]int64 {
	out := make([][2]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}
