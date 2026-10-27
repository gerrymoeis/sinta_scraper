package corpus

// ==== landing.go — fallback LANDING resmi jurnal (doc 43 §5.3 — C3b tahap B,
// di-approve user 27 Okt 2026) ====
//
// Posisi rantai: OAI ─gagal→ Crossref ─kosong→ DOAJ ─kosong→ LANDING
// ─gagal→ OAI MATI (jujur). Landing HANYA utk 2 jurnal khusus yang OAI-404
// + TANPA API apa pun:
//
//	j687  criticalcareshock.com  — WordPress: GET homepage (sidebar arsip
//	      bulan → map bulan→Volume/Number) + WP-REST /wp-json/wp/v2/posts
//	      (content = ABSTRAK LENGKAP, bukan fulltext) — 2 GET utk vol terakhir.
//	j3203 journal.oscm-forum.org — GET homepage (daftar TOC slug
//	      "oscm-volume-19-issue-4-2026" → vol/iss/thn TANPA tebakan) + GET
//	      tiap TOC vol terakhir: judul/URL/DOI/abstrak per blok — 5 GET.
//
// Fakta verifikasi 27 Okt (GET eksternal; 0 GET DB):
//   - CCS: label arsip = `<a href="/2026/08/"><span ...><strong>August 2026</strong>
//     Volume 29 Number 4</span></a>` · WP-REST 200 (X-WP-Total=626,
//     per_page=100 → 1 halaman utk 1 vol) · content.rendered = abstrak utuh
//     ("Abstract … Conclusion …") + penulis dlm `<p style="font-size:0px">`
//     (tersembunyi) + tombol share `<div class="sws_supernormalaction">` →
//     keduanya DIBUANG · excerpt = terpotong "…" → DITOLAK (pilih content) ·
//     vol TIDAK ada di content → map bulan homepage WAJIB.
//   - OSCM: TOC slug = vol/iss/thn eksplisit · per TOC: 9 <li class="toc">,
//     `a.toc-title` (judul+URL relatif), DOI doi.org urut, toggle abstract
//     id urut == id di DOM (936,1231,761,962) → zip by index · abstrak di
//     `div#abstract-N > .article-abstract-card` (hidden via JS, ADA di DOM).
//
// Semua hasil = []OAIRecord → pipeline SAMA (FilterOwnRecords netral ·
// FilterLatestVolume · deriveArticle). source="landing" (whitelist storage).

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"

	"sinta-scraper/internal/storage"
)

// ---- helpers ----

// canonicalOrigin = canonical → "scheme://host" (tanpa path/trailing slash).
// "" / parse gagal = error jujur (tak ada landing tanpa dasar).
func canonicalOrigin(canonical string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(canonical))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("canonical %q tak berisi scheme+host", canonical)
	}
	return u.Scheme + "://" + u.Host, nil
}

var (
	reHTMLTag = regexp.MustCompile(`<[^>]+>`)
	reWS      = regexp.MustCompile(`\s+`)
)

// stripHTML = tag → teks, entity di-unescape, whitespace collapse.
func stripHTML(s string) string {
	s = reHTMLTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(reWS.ReplaceAllString(s, " "))
}

// ---- OSCM (j3203) ----

var (
	reOSCMTocPath = regexp.MustCompile(`href="(?:https?://[^"]+)?(/publication/toc/[^"]+)"`)
	reOSCMSlug    = regexp.MustCompile(`volume-(\d+)-issue-(\d+)-(\d{4})`)
	reOSCMTitle   = regexp.MustCompile(`toc-title" href="([^"]+)">([^<]*)</a>`)
	reOSCMToggle  = regexp.MustCompile(`data-toggle="abstract-(\d+)"`)
	reOSCMAB      = regexp.MustCompile(`(?s)id="abstract-(\d+)"[^>]*>\s*<div class="card card-body article-abstract-card">\s*(.*?)\s*</div>`)
	reOSCMDOI     = regexp.MustCompile(`https://doi\.org/[^"\s]+`)
)

// ParseOSCMTOCHome = homepage → path TOC urut kemunculan, unik.
func ParseOSCMTOCHome(body []byte) []string {
	ms := reOSCMTocPath.FindAllSubmatch(body, -1)
	seen := map[string]bool{}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		p := string(m[1])
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// ParseOSCMTOC = 1 halaman TOC → []OAIRecord. vol/iss/thn dari SLUG TOC
// (bukan tebakan). DOI diambil per-segmen [title[i], title[i+1]) agar tetap
// terpasangkan walau jumlah DOI ≠ jumlah judul. Abstrak by index toggle
// (fakta: urutan toggle == urutan id == urutan judul).
func ParseOSCMTOC(body []byte, tocURL, journalName string) ([]OAIRecord, error) {
	// slug TOC → vol/iss/thn
	var vol, iss, yr int
	if m := reOSCMSlug.FindSubmatch([]byte(tocURL)); m != nil {
		fmt.Sscanf(string(m[1]), "%d", &vol)
		fmt.Sscanf(string(m[2]), "%d", &iss)
		fmt.Sscanf(string(m[3]), "%d", &yr)
	} else {
		return nil, fmt.Errorf("OSCM: slug TOC %q tak berisi volume-issue-tahun", tocURL)
	}

	origin, err := canonicalOrigin(tocURL)
	if err != nil {
		return nil, fmt.Errorf("OSCM: TOC URL: %w", err)
	}

	titles := reOSCMTitle.FindAllSubmatchIndex(body, -1)
	if len(titles) == 0 {
		return nil, fmt.Errorf("OSCM: TOC %q tanpa artikel (0 toc-title)", tocURL)
	}

	// abstrak id → teks
	absMap := map[string]string{}
	for _, m := range reOSCMAB.FindAllSubmatch(body, -1) {
		if t := stripHTML(string(m[2])); t != "" {
			absMap[string(m[1])] = t
		}
	}
	toggles := reOSCMToggle.FindAllSubmatch(body, -1)

	out := make([]OAIRecord, 0, len(titles))
	for i, idx := range titles {
		href := string(body[idx[2]:idx[3]])
		title := strings.TrimSpace(html.UnescapeString(string(body[idx[4]:idx[5]])))
		if title == "" {
			continue
		}
		if !strings.HasPrefix(href, "http") {
			href = origin + href
		}

		// segmen artikel: [end judul ini, start judul berikutnya)
		segEnd := len(body)
		if i+1 < len(titles) {
			segEnd = titles[i+1][0]
		}
		doi := ""
		if m := reOSCMDOI.Find(body[idx[1]:segEnd]); m != nil {
			doi = strings.TrimPrefix(strings.ToLower(string(m)), "https://doi.org/")
		}

		ids := []string{href}
		if doi != "" {
			ids = append(ids, "doi:"+doi)
		}
		rec := OAIRecord{
			Identifier:  href,
			Title:       title,
			Identifiers: ids,
			Source:      fmt.Sprintf("%s, Vol(%d), Iss(%d)", journalName, vol, iss),
			Publishers:  []string{journalName},
			Dates:       []string{fmt.Sprintf("%04d", yr)},
		}
		if i < len(toggles) {
			if a := absMap[string(toggles[i][1])]; a != "" {
				rec.Descriptions = []string{a}
			}
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("OSCM: TOC %q — semua artikel tanpa judul", tocURL)
	}
	return out, nil
}

// oscmFetch = landing OSCM: homepage → TOC vol terakhir (cap 6) → merge.
func oscmFetch(cfg StageConfig, rep *StageReport, journalName, origin string) ([]OAIRecord, error) {
	body, st, _, err := getRetry(cfg, origin+"/", "")
	rep.LandingGET++
	if err != nil || st != 200 {
		return nil, fmt.Errorf("OSCM homepage st=%d/%v", st, err)
	}
	paths := ParseOSCMTOCHome(body)
	if len(paths) == 0 {
		return nil, fmt.Errorf("OSCM homepage tanpa link TOC")
	}
	// pilih TOC dgn volume tertinggi (scope vol-N = vol terakhir; cap 6 utk
	// batasi GET — vol biasanya 4 issue).
	best, pick := -1, []string(nil)
	for _, p := range paths {
		m := reOSCMSlug.FindStringSubmatch(p)
		if m == nil {
			continue
		}
		var v int
		fmt.Sscanf(m[1], "%d", &v)
		if v > best {
			best, pick = v, []string{p}
		} else if v == best && len(pick) < 6 {
			pick = append(pick, p)
		}
	}
	if len(pick) == 0 {
		return nil, fmt.Errorf("OSCM: 0 TOC dgn slug volume valid (dari %d link)", len(paths))
	}
	var all []OAIRecord
	for _, p := range pick {
		b, s, _, e := getRetry(cfg, origin+p, origin+"/")
		rep.LandingGET++
		if e != nil || s != 200 {
			return nil, fmt.Errorf("OSCM TOC %s st=%d/%v", p, s, e)
		}
		recs, perr := ParseOSCMTOC(b, origin+p, journalName)
		if perr != nil {
			return nil, perr
		}
		all = append(all, recs...)
	}
	return all, nil
}

// ---- CCS / WordPress (j687) ----

var (
	reCCSBulan  = regexp.MustCompile(`href="/(\d{4})/(\d{2})/"`)
	reCCSVol    = regexp.MustCompile(`Volume\s+(\d+)\s+Number\s+(\d+)`)
	reCCSCurIss = regexp.MustCompile(`Current Issue[^<]*?([A-Z][a-z]+)\s+(\d{4}),\s*Volume\s+(\d+)\s+Number\s+(\d+)`)
	reCCSAbTag  = regexp.MustCompile(`(?i)<strong>\s*Abstract\s*</strong>`)
	reCCSShare  = regexp.MustCompile(`(?s)<div class="sws_supernormalaction">.*?</div>`)
	reCCSName   = regexp.MustCompile(`(?s)<p class="has-white-color[^"]*"[^>]*>.*?</p>`)
	reCCSBtn    = regexp.MustCompile(`(?s)<button.*?</button>`)
	reCCSSvg    = regexp.MustCompile(`(?s)<svg.*?</svg>`)
)

// ccsMonthNo = nama bulan EN → nomor (heading "October 2026, Volume 29
// Number 5" = SATU-SATUNYA teks situs berlabel issue TERKINI; sidebar
// /articles/ berhenti di 2026-08 / Vol 29 No 4 — fakta verifikasi 27 Okt).
var ccsMonthNo = map[string]int{
	"January": 1, "February": 2, "March": 3, "April": 4, "May": 5, "June": 6,
	"July": 7, "August": 8, "September": 9, "October": 10, "November": 11, "December": 12,
}

// ParseCCSCurrent = homepage CCS → entri "current issue" (Month YYYY,
// Volume V Number N). ok=false = heading tak ditemukan (bukan kegagalan —
// sidebar bisa sudah memuat bulan tsb).
func ParseCCSCurrent(body []byte) (ccsBulan, bool) {
	m := reCCSCurIss.FindSubmatch(body)
	if m == nil {
		return ccsBulan{}, false
	}
	mo, ok := ccsMonthNo[string(m[1])]
	if !ok {
		return ccsBulan{}, false
	}
	var y, v, n int
	fmt.Sscanf(string(m[2]), "%d", &y)
	fmt.Sscanf(string(m[3]), "%d", &v)
	fmt.Sscanf(string(m[4]), "%d", &n)
	if y == 0 || v == 0 {
		return ccsBulan{}, false
	}
	return ccsBulan{Y: y, M: mo, Vol: v, Iss: n}, true
}

// ccsBulan = 1 entri arsip sidebar: bulan publikasi → Volume & Number.
type ccsBulan struct {
	Y, M, Vol, Iss int
}

// ParseCCSArchive = homepage CCS → daftar arsip bulan (urut kemunculan,
// unik per bulan; entri TANPA label Volume/Number dilewati — sidebar umum
// juga memuat link bulan polos).
func ParseCCSArchive(body []byte) ([]ccsBulan, error) {
	ms := reCCSBulan.FindAllSubmatchIndex(body, -1)
	seen := map[string]bool{}
	out := []ccsBulan(nil)
	for _, m := range ms {
		y, mi := 0, 0
		fmt.Sscanf(string(body[m[2]:m[3]]), "%d", &y)
		fmt.Sscanf(string(body[m[4]:m[5]]), "%d", &mi)
		key := fmt.Sprintf("%04d-%02d", y, mi)
		if seen[key] {
			continue
		}
		// segmen link: s/d </a> berikutnya (cap 400 char) utk baca label
		segEnd := m[1] + 400
		if segEnd > len(body) {
			segEnd = len(body)
		}
		if j := strings.Index(string(body[m[1]:segEnd]), "</a>"); j >= 0 {
			segEnd = m[1] + j
		}
		vm := reCCSVol.FindSubmatch(body[m[1]:segEnd])
		if vm == nil {
			continue // link bulan tanpa label vol — bukan arsip ber-label
		}
		var v, n int
		fmt.Sscanf(string(vm[1]), "%d", &v)
		fmt.Sscanf(string(vm[2]), "%d", &n)
		seen[key] = true
		out = append(out, ccsBulan{Y: y, M: mi, Vol: v, Iss: n})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("CCS: homepage tanpa arsip bulan ber-label Volume/Number")
	}
	return out, nil
}

// ccsAfter = bulan AWAL pertama utk vol terakhir → parameter `after` WP-REST.
// 1 detik SEBELUM bulan pertama (eksklusif) → post bulan pertama ikut —
// dihitung via time (tanpa tanggal 30/31 yang bisa tak valid).
func ccsAfter(entries []ccsBulan) (volMax int, after string) {
	for _, e := range entries {
		if e.Vol > volMax {
			volMax = e.Vol
		}
	}
	minBulan := ""
	for _, e := range entries {
		if e.Vol != volMax {
			continue
		}
		k := fmt.Sprintf("%04d-%02d", e.Y, e.M)
		if minBulan == "" || k < minBulan {
			minBulan = k
		}
	}
	var y, mo int
	fmt.Sscanf(minBulan[:4], "%d", &y)
	fmt.Sscanf(minBulan[5:], "%d", &mo)
	t0 := time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC).Add(-time.Second)
	return volMax, t0.Format("2006-01-02T15:04:05")
}

// ccsPostsURL = WP-REST 1 GET utk vol terakhir (per_page=100 ≥ kapasitas
// 1 vol ≈ 30 post; _fields = hemati body).
func ccsPostsURL(origin, after string) string {
	q := url.Values{}
	q.Set("_fields", "id,date,link,title,content")
	q.Set("per_page", "100")
	if after != "" {
		q.Set("after", after)
	}
	return origin + "/wp-json/wp/v2/posts?" + q.Encode()
}

type ccsPost struct {
	ID    int    `json:"id"`
	Date  string `json:"date"`
	Link  string `json:"link"`
	Title struct {
		Rendered string `json:"rendered"`
	} `json:"title"`
	Content struct {
		Rendered string `json:"rendered"`
	} `json:"content"`
}

// ccsAbstract = content WP → abstrak bersih: buang tombol share + blok
// penulis tersembunyi + sisa button/svg → potong pada label "Abstract"
// (bila ada — editorial tanpa label = konten apa adanya) → strip tag.
func ccsAbstract(content string) string {
	s := reCCSShare.ReplaceAllString(content, " ")
	s = reCCSName.ReplaceAllString(s, " ")
	s = reCCSBtn.ReplaceAllString(s, " ")
	s = reCCSSvg.ReplaceAllString(s, " ")
	if m := reCCSAbTag.FindStringIndex(s); m != nil {
		s = s[m[1]:]
	}
	s = stripHTML(s)
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "Share"))
	return s
}

// ParseCCSPosts = body WP-REST → []OAIRecord. Post dgn bulan TANPA entri
// arsip = dilewati (jujur — vol/number tak diketahui, tak boleh ditebak).
func ParseCCSPosts(body []byte, entries []ccsBulan, journalName string) ([]OAIRecord, error) {
	var posts []ccsPost
	if err := json.Unmarshal(body, &posts); err != nil {
		return nil, fmt.Errorf("CCS posts: bukan JSON array: %w", err)
	}
	byBulan := map[string]ccsBulan{}
	for _, e := range entries {
		byBulan[fmt.Sprintf("%04d-%02d", e.Y, e.M)] = e
	}
	out := make([]OAIRecord, 0, len(posts))
	for _, p := range posts {
		if len(p.Date) < 7 {
			continue
		}
		e, ok := byBulan[p.Date[:7]]
		if !ok {
			continue // bulan di luar arsip ber-label — dilewati (jujur)
		}
		title := strings.TrimSpace(stripHTML(p.Title.Rendered))
		if title == "" || p.Link == "" {
			continue
		}
		rec := OAIRecord{
			Identifier:  p.Link,
			Title:       title,
			Identifiers: []string{p.Link},
			Source:      fmt.Sprintf("%s, Vol(%d), Iss(%d)", journalName, e.Vol, e.Iss),
			Publishers:  []string{journalName},
			Dates:       []string{p.Date[:4]},
		}
		if a := ccsAbstract(p.Content.Rendered); a != "" {
			rec.Descriptions = []string{a}
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("CCS: 0 post layak (posts=%d, entri arsip=%d)", len(posts), len(entries))
	}
	return out, nil
}

// ccsFetch = landing CCS: /articles/ (sidebar 105 entri ber-label) +
// homepage (heading "Current Issue … Month YYYY, Volume V Number N" —
// SATU-SATUNYA tempat label issue terkini) + WP-REST vol terakhir — 3 GET.
func ccsFetch(cfg StageConfig, rep *StageReport, journalName, origin string) ([]OAIRecord, error) {
	body, st, _, err := getRetry(cfg, origin+"/articles/", origin+"/")
	rep.LandingGET++
	if err != nil || st != 200 {
		return nil, fmt.Errorf("CCS /articles/ st=%d/%v", st, err)
	}
	entries, aerr := ParseCCSArchive(body)
	if aerr != nil {
		return nil, aerr
	}
	// heading current-issue (merge — bulan sudah ada = menang entri sidebar)
	bodyH, stH, _, errH := getRetry(cfg, origin+"/", origin+"/articles/")
	rep.LandingGET++
	if errH != nil || stH != 200 {
		return nil, fmt.Errorf("CCS homepage st=%d/%v", stH, errH)
	}
	if cur, ok := ParseCCSCurrent(bodyH); ok {
		dup := false
		for _, e := range entries {
			if e.Y == cur.Y && e.M == cur.M {
				dup = true
				break
			}
		}
		if !dup {
			entries = append(entries, cur)
		}
	}
	volMax, after := ccsAfter(entries)
	body2, st2, _, err2 := getRetry(cfg, ccsPostsURL(origin, after), origin+"/articles/")
	rep.LandingGET++
	if err2 != nil || st2 != 200 {
		return nil, fmt.Errorf("CCS WP-REST st=%d/%v (vol %d after=%s)", st2, err2, volMax, after)
	}
	recs, perr := ParseCCSPosts(body2, entries, journalName)
	if perr != nil {
		return nil, perr
	}
	return recs, nil
}

// ---- rantai fallback ke-3 ----

// fetchLandingFallback = C3b tahap B (approve 27 Okt): API Crossref+DOAJ
// kosong → landing resmi. Host di-deny ketat (hanya 2 jurnal riset C3b) —
// TANPA menebak strategi utk host asing. Return (records, "landing", err).
func fetchLandingFallback(cfg StageConfig, rep *StageReport, t *storage.CorpusTarget) ([]OAIRecord, error) {
	origin, err := canonicalOrigin(t.Canonical)
	if err != nil {
		return nil, fmt.Errorf("landing: %w", err)
	}
	switch {
	case strings.Contains(origin, "journal.oscm-forum.org"):
		return oscmFetch(cfg, rep, t.Name, origin)
	case strings.Contains(origin, "criticalcareshock.com"):
		return ccsFetch(cfg, rep, t.Name, origin)
	}
	return nil, fmt.Errorf("landing: host %q tak didukung (deny-list ketat — hanya criticalcareshock.com & journal.oscm-forum.org)", origin)
}
