package volume

// ==== stage.go — driver stage volume (doc 41 §13.7 K6 REVISED + syarat
// H17 — urutan revisi approve user 14 Okt 2026) ====
//
// GET nyata pola stage ojs/garuda: target dari VolumeTargets (canonical+
// platform+ISSN), retry 1× transien, polite via garuda.Client. Alur OJS:
// home → findArchive (pola probep4: href filter "issue/archive" + fallback
// canonical+/issue/archive) → archive + PAGINASI offset (all-year — user
// konfirmasi 14 Okt) → Crossref H17 (sort=published&order=desc — syarat
// #1 utk cap 1000 rows) → Compute#1 (TOC kosong → anchor+window) → TOC GET
// utk issue dlm window (match BY issue_url — D1) → Compute#2 final → tulis.
//
// Non-OJS (platform≠ojs): tanpa archive/TOC — Crossref derive saja dgn label
// source='crossref' (approve user). IsComplete driver override: result &&
// archive pagination tanpa kegagalan transien (parsial = retry resume H18).

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sinta-scraper/internal/storage"
)

// PlainGetter = kontrak GET tunggal tanpa retry (dipenuhi garuda.Client
// GetPlain — main.go menyuntik client; pola ojs.StageConfig).
type PlainGetter interface {
	GetPlain(rawURL, referer string) ([]byte, int, string, error)
}

const (
	// DefaultMaxTOC = safety cap TOC/jurnal. 40 = window V3 (3 tahun) × 12
	// bulan + buffer — jurnal bulanan WindowTOCable=36 HARUS ter-TOC penuh
	// (Gap-C: cap 12 → counted<TOCable → parsial FOREVER utk bulanan; #1
	// di atas #3 — approve user (b′) = TOC SEMUA issue dlm window).
	DefaultMaxTOC          = 40
	DefaultMaxArchivePages = 15 // cap halaman archive (safety — jurnal 20+ thn)
)

// StageConfig = konfigurasi stage volume (diisi main.go).
type StageConfig struct {
	Ranks           []int
	Refresh         bool
	Client          PlainGetter
	RetryDelay      time.Duration // jeda antar retry transien (0 = test)
	MaxTOC          int           // ≤0 → DefaultMaxTOC
	MaxArchivePages int           // ≤0 → DefaultMaxArchivePages
	CrossrefMailto  string
	Logf            func(format string, args ...any)
}

// StageReport = ringkasan jujur (log + verifikasi exit code).
type StageReport struct {
	Target      int
	Skip        int // resume is_complete=1
	NoCanonical int // canonical kosong — 0 GET, tanpa baris (rerun bila resolve menyusul)
	NoISSN      int // non-OJS tanpa ISSN — Crossref gagal total (jujur)
	Processed   int // baris ditulis is_complete=1
	Partial     int // baris ditulis is_complete=0 (retry resume — H18 anti-infinite utk kasus permanen TOCable)
	Err         int // murni network (rerun wajib — TANPA baris)
	ArchiveGET  int
	CrossrefGET int
	TOCGET      int

	Verified  bool
	VerifyMsg string
}

var (
	reHrefAll   = regexp.MustCompile(`(?i)href="([^"]+)"`)
	reOffsetVal = regexp.MustCompile(`(?i)(?:[?&]|&amp;)offset=(\d+)`)
)

// RunVolumeStage = orkestrator stage volume. Error hanya kegagalan fatal
// (db/target kosong); kegagalan per-jurnal masuk report.
func RunVolumeStage(store *storage.Store, cfg StageConfig) (*StageReport, error) {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if cfg.Client == nil {
		return nil, fmt.Errorf("volume: StageConfig.Client wajib (PlainGetter)")
	}
	maxTOC := cfg.MaxTOC
	if maxTOC <= 0 {
		maxTOC = DefaultMaxTOC
	}
	maxPages := cfg.MaxArchivePages
	if maxPages <= 0 {
		maxPages = DefaultMaxArchivePages
	}

	rep := &StageReport{}
	targets, err := store.VolumeTargets(cfg.Ranks)
	if err != nil {
		return nil, fmt.Errorf("baca target: %w", err)
	}
	rep.Target = len(targets)
	if len(targets) == 0 {
		return nil, fmt.Errorf("target kosong — tidak ada baris journals utk scope -rank ini")
	}
	logf("[volume] target = %d baris (refresh=%v maxTOC=%d maxPages=%d)",
		len(targets), cfg.Refresh, maxTOC, maxPages)

	for i := range targets {
		t := &targets[i]
		if !cfg.Refresh && t.Complete {
			rep.Skip++
			continue
		}
		if err := processJournal(store, cfg, rep, t, maxTOC, maxPages, logf); err != nil {
			return nil, err // fatal db — bukan kegagalan per-jurnal
		}
	}

	stageVerify(rep, logf)
	return rep, nil
}

// processJournal = satu jurnal: non-OJS → Crossref derive; OJS → archive
// (+paginasi) → Crossref → Compute#1 → TOC window → Compute#2 → tulis.
func processJournal(store *storage.Store, cfg StageConfig, rep *StageReport,
	t *storage.VolumeTarget, maxTOC, maxPages int, logf func(string, ...any)) error {

	if t.Canonical == "" {
		rep.NoCanonical++
		logf("[volume] j%d %q: TANPA KANONIK (jujur — 0 GET)", t.ID, t.Name)
		return nil
	}
	issn := strings.ReplaceAll(firstNonEmpty(t.ISSN, t.EISSN), "-", "")

	// ── Non-OJS (30 jurnal — approve user): Crossref derive, label jujur ──
	if t.Platform != "ojs" {
		if issn == "" {
			rep.NoISSN++
			logf("[volume] j%d %q: non-OJS TANPA ISSN — Crossref tak bisa (jujur)", t.ID, t.Name)
			return nil
		}
		cr, st, err := getCrossref(cfg, issn, rep)
		rep.CrossrefGET++
		if err != nil || st != 200 {
			rep.Err++
			logf("[volume] j%d %q: crossref status=%d err=%v (rerun)", t.ID, t.Name, st, err)
			return nil
		}
		r := Compute(Input{Crossref: cr})
		prov, _ := json.Marshal(map[string]any{
			"crossref_issn": issn, "crossref_rows": cr.Rows, "h17": "sort=published&order=desc",
		})
		if err := store.UpsertVolumeStats(storage.VolumeStatsRow{
			JournalID: t.ID, AnchorYear: r.AnchorYear, WindowStart: r.WindowStart,
			WindowEnd: r.WindowEnd, VolPerYearAvg: r.VolPerYear,
			ArticlesPerVolumeAvg: r.ArtPerVol, ReleaseMonths: r.ReleaseMonths,
			IssuesJSON: r.IssuesJSON, Source: "crossref",
			IsComplete:     r.IsComplete && r.AnchorYear > 0,
			ProvenanceJSON: string(prov),
		}); err != nil {
			return err
		}
		if r.AnchorYear > 0 && r.IsComplete {
			rep.Processed++
		} else {
			rep.Partial++
		}
		logf("[volume] j%d %q: non-OJS crossref derive — anchor=%d issues=%s",
			t.ID, t.Name, r.AnchorYear, short(r.IssuesJSON))
		return nil
	}

	// ── OJS: home → archive (+paginasi) ──
	homeBody, hSt, hFinal, _ := getRetry(cfg, t.Canonical, "")
	if hSt != 200 {
		rep.Err++
		logf("[volume] j%d %q: home status=%d (rerun)", t.ID, t.Name, hSt)
		return nil
	}
	archiveURL := findArchive(hFinal, string(homeBody))
	archive, archiveParsial := fetchArchive(cfg, rep, archiveURL, hFinal, maxPages, logf)
	if len(archive) == 0 {
		// Archive 0 issue: 403 WAF / 404 / body tak ter-parse — baris tetap
		// ditulis dgn anchor-crossref-only (Compute fallback) bila Crossref
		// ada; parsial jujur. Tanpa archive & tanpa crossref → Err rerun.
		logf("[volume] j%d %q: archive 0 issue (status/body) — lanjut crossref", t.ID, t.Name)
	}

	// ── Crossref (H17: sort=published&order=desc — syarat #1 cap 1000 rows) ──
	var cr CrossrefData
	crossOK := false
	if issn != "" {
		data, st, err := getCrossref(cfg, issn, rep)
		rep.CrossrefGET++
		if err == nil && st == 200 {
			cr, crossOK = data, true
		} else {
			archiveParsial = true // Crossref gagal = anchor/VI sebagian hilang
			logf("[volume] j%d %q: crossref status=%d err=%v", t.ID, t.Name, st, err)
		}
	}
	// Gap-E (H18): OJS tanpa ISSN = crossref MUSTAHIL (bukan gagal sementara)
	// — TIDAK set archiveParsial; archive+TOC tetap bisa lengkap → complete.

	// ── Compute#1 (TOC kosong) → anchor + window + issues raw ──
	r1 := Compute(Input{Archive: archive, Crossref: cr})
	if r1.AnchorYear == 0 {
		rep.Err++
		logf("[volume] j%d %q: ANCHOR GAGAL (archive+crossref tanpa tahun) — rerun", t.ID, t.Name)
		return nil
	}

	// ── TOC GET utk issue dlm window (BY issue_url — D1; cap maxTOC) ──
	toc := map[string]int{}
	tocTarget := tocTargets(r1.IssuesJSON, r1.WindowStart, r1.WindowEnd, maxTOC)
	for _, u := range tocTarget {
		body, st, _, _ := getRetry(cfg, u, archiveURL)
		rep.TOCGET++
		if st != 200 {
			// Gap-D (H18): status NON-TRANSIEN (404/403/410 permanen — issue
			// dihapus) = "sudah dicek, tak tersedia" → key=0 (counted utk
			// completeness, D2 exclude). Transien (0/429/5xx) TANPA key →
			// resume retry run berikut (WAF/transien bisa membaik).
			if !transien(st) {
				toc[u] = 0
				logf("[volume] j%d %q: TOC %s status=%d permanen — ditandai sudah-dicek", t.ID, t.Name, u, st)
			} else {
				archiveParsial = true
				logf("[volume] j%d %q: TOC %s status=%d transien (resume retry)", t.ID, t.Name, u, st)
			}
			continue
		}
		// Key ADA dlm map = "sudah GET & dicek" (H18 anti-resume-loop); nilai 0
		// = issue valid 0 artikel / template beda — D2 exclude, complete tetap.
		toc[u] = ParseTOC(string(body)).ArticleCount
	}

	// ── Compute#2 final + tulis ──
	r2 := Compute(Input{Archive: archive, Crossref: cr, TOC: toc})
	complete := r2.IsComplete && !archiveParsial
	prov, _ := json.Marshal(map[string]any{
		"archive": archiveURL, "archive_issues": len(archive),
		"archive_parsial": archiveParsial, "crossref_issn": issn,
		"crossref_rows": cr.Rows, "crossref_ok": crossOK,
		"toc_got": len(toc), "toc_target": len(tocTarget),
		"h17": "sort=published&order=desc", "notes": r2.Notes,
	})
	if err := store.UpsertVolumeStats(storage.VolumeStatsRow{
		JournalID: t.ID, AnchorYear: r2.AnchorYear, WindowStart: r2.WindowStart,
		WindowEnd: r2.WindowEnd, VolPerYearAvg: r2.VolPerYear,
		ArticlesPerVolumeAvg: r2.ArtPerVol, ReleaseMonths: r2.ReleaseMonths,
		IssuesJSON: r2.IssuesJSON, Source: "ojs", IsComplete: complete,
		ProvenanceJSON: string(prov),
	}); err != nil {
		return err
	}
	if complete {
		rep.Processed++
	} else {
		rep.Partial++
	}
	logf("[volume] j%d %q: anchor=%d (%s) window=%d-%d issues=%d toc=%d/%d complete=%v",
		t.ID, t.Name, r2.AnchorYear, r2.AnchorSource, r2.WindowStart, r2.WindowEnd,
		len(archive), len(toc), len(tocTarget), complete)
	return nil
}

// fetchArchive = GET archive + paginasi offset (all-year; pola P4: offset
// link di body). Stop: offset tak ada lagi · 0 issue baru · non-200 · cap.
// Return issues gabungan (dedup URL) + flag parsial (ada page gagal transien).
func fetchArchive(cfg StageConfig, rep *StageReport, archiveURL, referer string,
	maxPages int, logf func(string, ...any)) ([]ArchiveIssue, bool) {

	seen := map[string]bool{}
	var out []ArchiveIssue
	pageURL := archiveURL
	parsial := false
	for page := 0; page < maxPages; page++ {
		body, st, final, _ := getRetry(cfg, pageURL, referer)
		rep.ArchiveGET++
		if st != 200 {
			parsial = true
			logf("[volume] archive page %d status=%d url=%s", page+1, st, pageURL)
			break
		}
		issues := ParseArchiveIssues(string(body), final)
		barubaru := 0
		for _, iss := range issues {
			if iss.IssueURL == "" || seen[iss.IssueURL] {
				continue
			}
			seen[iss.IssueURL] = true
			out = append(out, iss)
			barubaru++
		}
		if page > 0 && barubaru == 0 {
			break // page dup penuh (OJS offset loop) — stop normal
		}
		next := nextOffset(string(body), final)
		if next == "" || next == pageURL {
			break // offset habis — normal all-year
		}
		pageURL = next
	}
	return out, parsial
}

// nextOffset = URL archive page berikutnya (offset minimal lebih besar dari
// current — pola pagination OJS; "" bila tak ada).
func nextOffset(body, current string) string {
	cur := 0
	if m := reOffsetVal.FindStringSubmatch(current); m != nil {
		cur, _ = strconv.Atoi(m[1])
	}
	best, bestVal := "", -1
	for _, m := range reHrefAll.FindAllStringSubmatch(body, -1) {
		href := m[1]
		if !strings.Contains(strings.ToLower(href), "offset=") {
			continue
		}
		om := reOffsetVal.FindStringSubmatch(href)
		if om == nil {
			continue
		}
		v, _ := strconv.Atoi(om[1])
		if v > cur && (bestVal < 0 || v < bestVal) {
			if u := absURL(current, strings.ReplaceAll(href, "&amp;", "&")); u != "" {
				best, bestVal = u, v
			}
		}
	}
	return best
}

// findArchive = URL absolut link archive dari body home (pola probep4:
// filter "issue/archive" pada href utuh; fallback canonical+/issue/archive).
func findArchive(base, body string) string {
	for _, m := range reHrefAll.FindAllStringSubmatch(body, -1) {
		if !strings.Contains(strings.ToLower(m[1]), "issue/archive") {
			continue
		}
		if u := absURL(base, m[1]); u != "" {
			return u
		}
	}
	return strings.TrimRight(base, "/") + "/issue/archive"
}

// getCrossref = GET works H17 (rows=1000 + sort=published&order=desc — syarat
// #1: items terbaru dulu → cap 1000 = jendela V3 selalu keambil utk jurnal
// besar; P4 TANPA sort = gap H17). Polite via Client (garuda).
func getCrossref(cfg StageConfig, issn string, rep *StageReport) (CrossrefData, int, error) {
	if issn == "" {
		return CrossrefData{}, 0, fmt.Errorf("tanpa issn")
	}
	u := fmt.Sprintf("https://api.crossref.org/journals/%s/works?rows=1000&sort=published&order=desc&select=volume,issue,published",
		issn)
	if cfg.CrossrefMailto != "" {
		u += "&mailto=" + cfg.CrossrefMailto
	}
	// getRetry — 429/5xx = transien Crossref (rate-limit pool polite);
	// tanpa retry = parsial massal palsu utk semua jurnal kena 429.
	body, st, _, err := getRetry(cfg, u, "")
	if err != nil || st != 200 {
		return CrossrefData{}, st, err
	}
	return ParseCrossref(string(body)), st, nil
}

// tocTargets = URL TOC utk issue dlm window (issues JSON Compute#1: year dlm
// [ws,we] & issue_url≠"" & Vol≠"0" — Vol=0 di luar WindowIssues (G2) → GET
// TOC-nya percuma utk D2; urut JSON = archive order; cap maxTOC.
func tocTargets(issuesJSON string, ws, we int64, maxTOC int) []string {
	var arr []struct {
		Vol      int64  `json:"vol"`
		Year     int64  `json:"year"`
		IssueURL string `json:"issue_url"`
	}
	if err := json.Unmarshal([]byte(issuesJSON), &arr); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, it := range arr {
		if it.Vol == 0 || it.Year < ws || it.Year > we || it.IssueURL == "" || seen[it.IssueURL] {
			continue
		}
		seen[it.IssueURL] = true
		out = append(out, it.IssueURL)
		if len(out) >= maxTOC {
			break
		}
	}
	return out
}

// getRetry = GET + retry 1× transien (pola ojs.StageConfig).
func getRetry(cfg StageConfig, rawURL, referer string) ([]byte, int, string, error) {
	body, st, final, err := cfg.Client.GetPlain(rawURL, referer)
	if err == nil && !transien(st) {
		return body, st, final, nil
	}
	if cfg.RetryDelay > 0 {
		time.Sleep(cfg.RetryDelay)
	}
	body2, st2, final2, err2 := cfg.Client.GetPlain(rawURL, referer)
	if err2 == nil && !transien(st2) {
		return body2, st2, final2, nil
	}
	if err2 != nil {
		return body2, st2, final2, err2
	}
	return body2, st2, final2, nil
}

func transien(st int) bool { return st == 0 || st == 429 || st >= 500 }

func stageVerify(rep *StageReport, logf func(string, ...any)) {
	total := rep.Skip + rep.NoCanonical + rep.NoISSN + rep.Processed + rep.Partial + rep.Err
	var masalah []string
	if total != rep.Target {
		masalah = append(masalah, fmt.Sprintf("drift: kategori=%d ≠ target=%d", total, rep.Target))
	}
	if rep.Err > 0 {
		masalah = append(masalah, fmt.Sprintf("%d jurnal network gagal (rerun wajib)", rep.Err))
	}
	if rep.Partial > 0 {
		masalah = append(masalah, fmt.Sprintf("%d jurnal parsial (is_complete=0 — resume retry)", rep.Partial))
	}
	if len(masalah) == 0 {
		rep.Verified = true
		rep.VerifyMsg = fmt.Sprintf("done %d/%d | skip %d | no_canonical %d | no_issn %d | archive_get %d | crossref_get %d | toc_get %d",
			rep.Processed, rep.Target, rep.Skip, rep.NoCanonical, rep.NoISSN,
			rep.ArchiveGET, rep.CrossrefGET, rep.TOCGET)
	} else {
		rep.Verified = false
		rep.VerifyMsg = fmt.Sprintf("%d masalah: %v", len(masalah), masalah)
	}
	logf("[volume] verifikasi: %s → VERIFIED=%v", rep.VerifyMsg, rep.Verified)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func short(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}
