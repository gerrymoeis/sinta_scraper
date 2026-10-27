// Program probep4: probe P4 statistik volume (doc 41 §7 opsi 1 — approve
// user 13 Okt 2026, pagu ≤120 GET). Tiap jurnal OJS: GET kanonik → link
// archive → parse issue (vol/no/tanggal) → Crossref works (kelengkapan
// volume/issue/date vs OJS) → 2 TOC issue (jumlah artikel — silang D2).
// Custom/unknown: GET kanonik saja (ukur ketiadaan archive OJS = fakta).
// Alat diagnosis; BUKAN pipeline.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"

	_ "modernc.org/sqlite"
)

// Sampel campuran 18 (fakta DB 13 Okt: 14 ojs tersebar id (ada/absen e-issn,
// 679 http-only uji upgrade) + 2 custom + 2 unknown — platform hasil Fase 3).
var ids = []int64{
	16, 94, 107, 185, 679, 783, 900, 3539, 4644, 4748, 6126, 6672, 7933, 9792,
	689, 741, 5, 131,
}

var (
	reHrefAll     = regexp.MustCompile(`(?i)href="([^"]+)"`)
	reIssueView   = regexp.MustCompile(`(?i)href="([^"]*issue/view/(\d+)[^"]*)"`)
	reVolNo       = regexp.MustCompile(`(?i)Vol\.?\s*(\d+)\s*(?:No\.?|,)\s*(\d+)`)
	reArticleView = regexp.MustCompile(`(?i)article/view/(\d+)`)
	reOffset      = regexp.MustCompile(`(?i)(?:offset|page)=\d+`)
	reTag         = regexp.MustCompile(`<[^>]+>`)
	reSpace       = regexp.MustCompile(`\s+`)
)

type target struct {
	jid       int64
	name      string
	platform  string
	issn      string
	eissn     string
	canonical string
}

func main() {
	pagu := flag.Int("pagu", 120, "pagu GET")
	tocPer := flag.Int("toc", 2, "TOC issue per jurnal utk silang D2")
	dbPath := flag.String("db", "data/stage2/sinta-r2-enrich.db", "db enrich")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)"
	c := garuda.NewClient("probep4", ua, time.Second, 3*time.Second)

	fmt.Printf("P4 probe: %d jurnal, pagu %d GET, TOC/jurnal=%d\n", len(ids), *pagu, *tocPer)
	gets := 0

	for _, id := range ids {
		if gets >= *pagu {
			fmt.Fprintf(os.Stderr, "PAGU %d tercapai — berhenti\n", *pagu)
			break
		}
		t, err := loadTarget(db, id)
		if err != nil {
			fmt.Printf("JID=%d|load_err=%v|SKIP\n", id, err)
			continue
		}
		if t.platform != "ojs" {
			// Non-OJS: 1 GET kanonik — ukur ketiadaan archive OJS (fakta).
			_, stt, _, e := c.GetPlain(t.canonical, "")
			gets++
			fmt.Printf("JID=%d|name=%q|platform=%s|issn=%s|home_status=%d|err=%v|NON_OJS_ARCH=none\n",
				t.jid, t.name, t.platform, t.issn, stt, e)
			continue
		}
		gets = probeOJS(c, t, *tocPer, &gets, *pagu)
	}
	fmt.Fprintf(os.Stderr, "SELESAI: GET=%d solves=%d\n", gets, c.Solves())
}

func loadTarget(db *sql.DB, id int64) (target, error) {
	var t target
	var eissn, canonical, platform sql.NullString
	err := db.QueryRow(`
		SELECT j.name, COALESCE(j.print_issn,''), j.electronic_issn,
		       sp.canonical_url, sp.platform
		FROM journals j LEFT JOIN journal_source_profile sp ON sp.journal_id = j.id
		WHERE j.id = ?`, id).Scan(&t.name, &t.issn, &eissn, &canonical, &platform)
	if err != nil {
		return t, err
	}
	t.jid = id
	t.eissn = eissn.String
	t.canonical = canonical.String
	t.platform = platform.String
	return t, nil
}

// probeOJS = archive + Crossref + TOC silang. Return GET kumulatif.
func probeOJS(c *garuda.Client, t target, tocPer int, gets *int, pagu int) int {
	// 1) GET kanonik → cari link archive.
	body, status, final, err := c.GetPlain(t.canonical, "")
	*gets++
	if err != nil || status != 200 {
		fmt.Printf("JID=%d|name=%q|platform=%s|issn=%s|home_status=%d|err=%v|HOME_GAGAL\n",
			t.jid, t.name, t.platform, t.issn, status, err)
		return *gets
	}
	archiveURL := findArchive(final, string(body))
	if archiveURL == "" {
		fmt.Printf("JID=%d|name=%q|platform=%s|issn=%s|home_status=200|archive=NONE|TANPA_LINK_ARCHIVE\n",
			t.jid, t.name, t.platform, t.issn)
		return *gets
	}

	// 2) GET archive → daftar issue.
	aBody, aStatus, aFinal, aErr := c.GetPlain(archiveURL, final)
	*gets++
	if aErr != nil || aStatus != 200 {
		fmt.Printf("JID=%d|name=%q|issn=%s|archive_status=%d|err=%v|url=%s|ARCHIVE_GAGAL\n",
			t.jid, t.name, t.issn, aStatus, aErr, archiveURL)
		return *gets
	}
	// Dedupe issue view id (urut kemunculan = terbaru dulu di OJS default).
	seenIssue := map[string]bool{}
	var issueURLs []string
	for _, m := range reIssueView.FindAllStringSubmatch(string(aBody), -1) {
		u := absolutize(aFinal, m[1])
		if u == "" || seenIssue[m[2]] {
			continue
		}
		seenIssue[m[2]] = true
		issueURLs = append(issueURLs, u)
	}
	aText := stripHTML(string(aBody))
	nVolNo := len(reVolNo.FindAllString(aText, -1))
	paginasi := reOffset.MatchString(string(aBody))

	// 3) Crossref works (rows=1000 — kelengkapan field utk D2).
	cv, ci, cd, cTotal, cRows, distinctVI, yMin, yMax, cErr := crossref(t.issn)

	// 4) TOC issue (jumlah artikel) — silang dgn Crossref per (vol,issue).
	var tocs []string
	var tocCounts []int
	var tocVNs [][2]string
	for i := 0; i < tocPer && i < len(issueURLs) && *gets < pagu; i++ {
		tBody, tStatus, tFinal, tErr := c.GetPlain(issueURLs[i], aFinal)
		*gets++
		if tErr != nil || tStatus != 200 {
			tocs = append(tocs, fmt.Sprintf("toc%d_status=%d", i+1, tStatus))
			continue
		}
		arts := countUniqueSubmatch(reArticleView, string(tBody))
		tocCounts = append(tocCounts, arts)
		vn := reVolNo.FindStringSubmatch(stripHTML(string(tBody)))
		if len(vn) == 3 {
			tocVNs = append(tocVNs, [2]string{vn[1], vn[2]})
		}
		tocs = append(tocs, fmt.Sprintf("toc%d_articles=%d|%s", i+1, arts, tFinal))
	}

	fmt.Printf("JID=%d|name=%q|platform=%s|issn=%s|home=200|archive=%s|issues_page1=%d|volno_parsed=%d|paginasi=%v|cv_pct=%d|ci_pct=%d|cd_pct=%d|crossref_total=%d|crossref_rows=%d|distinct_vi=%d|year=%d-%d|cErr=%v",
		t.jid, t.name, t.platform, t.issn, archiveURL, len(issueURLs), nVolNo, paginasi,
		cv, ci, cd, cTotal, cRows, distinctVI, yMin, yMax, cErr)
	for i, s := range tocs {
		fmt.Printf("|%s", s)
		if i < len(tocVNs) {
			fmt.Printf("|toc_v%d=%s.%s", i+1, tocVNs[i][0], tocVNs[i][1])
		}
	}
	fmt.Println()
	return *gets
}

// findArchive = URL absolut link archive dari body home. Filter "issue/archive"
// pada href utuh (bukan regex gabungan — bug P4 run-1: alternatif `/archive"`
// menelan kutip penutup di tengah capture → URL 404). Fallback: base + "/issue/archive".
func findArchive(base, body string) string {
	for _, m := range reHrefAll.FindAllStringSubmatch(body, -1) {
		if !strings.Contains(strings.ToLower(m[1]), "issue/archive") {
			continue
		}
		if u := absolutize(base, m[1]); u != "" {
			return u
		}
	}
	return strings.TrimRight(base, "/") + "/issue/archive"
}

// crossref = kelengkapan field volume/issue/date pada works (rows≤1000).
// Return persen kelengkapan + total works + rows terbaca + distinct (vol,issue)
// + rentang tahun. ISSN print utama; e-ISSN dicoba bila print kosong.
func crossref(issn string) (cv, ci, cd, total, rows, distinctVI, yMin, yMax int, err error) {
	if strings.TrimSpace(issn) == "" {
		return 0, 0, 0, 0, 0, 0, 0, 0, fmt.Errorf("tanpa issn")
	}
	issn = strings.ReplaceAll(issn, "-", "")
	u := fmt.Sprintf("https://api.crossref.org/journals/%s/works?rows=1000&select=volume,issue,published&mailto=gerry.23164%%40mhs.unesa.ac.id", issn)
	resp, err := http.Get(u) // polite pool Crossref — timeout default client
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		err = fmt.Errorf("crossref status %d", resp.StatusCode)
		return
	}
	var payload struct {
		Message struct {
			TotalResults int `json:"total-results"`
			Items        []struct {
				Volume    any `json:"volume"`
				Issue     any `json:"issue"`
				Published struct {
					DateParts [][]int `json:"date-parts"`
				} `json:"published"`
			} `json:"items"`
		} `json:"message"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return
	}
	total = payload.Message.TotalResults
	rows = len(payload.Message.Items)
	yMin, yMax = 9999, 0
	vi := map[string]bool{}
	nV, nI, nD := 0, 0, 0
	for _, it := range payload.Message.Items {
		if strAny(it.Volume) != "" {
			nV++
		}
		if strAny(it.Issue) != "" {
			nI++
		}
		if len(it.Published.DateParts) > 0 && len(it.Published.DateParts[0]) > 0 {
			nD++
			y := it.Published.DateParts[0][0]
			if y < yMin {
				yMin = y
			}
			if y > yMax {
				yMax = y
			}
		}
		vi[strAny(it.Volume)+"/"+strAny(it.Issue)] = true
	}
	if rows > 0 {
		cv, ci, cd = nV*100/rows, nI*100/rows, nD*100/rows
	}
	distinctVI = len(vi)
	if yMin == 9999 {
		yMin = 0
	}
	return
}

func strAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		var parts []string
		for _, e := range x {
			parts = append(parts, strAny(e))
		}
		return strings.Join(parts, ".")
	}
	return ""
}

func absolutize(base, href string) string {
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "mailto:") {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	return b.ResolveReference(u).String()
}

func countUniqueSubmatch(re *regexp.Regexp, s string) int {
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		seen[m[1]] = true
	}
	return len(seen)
}

func stripHTML(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}
