// Program licfix — DRY-RUN lisensi Fase 3.5 / C7-a (doc 43 §6.x — Opsi A
// primer + Opsi B cross-check/fallback, approve user 27 Okt 2026).
// ALAT REVIEW: default TANPA UPDATE (0 tulis DB) — eksekusi = tahap C7-b/D4
// lewat -apply (tulis PERSIS artefak yg di-approve, tanpa re-probe; apply.go).
//
// Dua fase lewat -phase (jalur terpisah — B-additive):
//
//	-phase lic    (default) = C7-a: probe license_status legacy 4 kelas.
//	-phase attrs  = D3 (doc 43 §6.x.8): probe oa_status + license_type
//	  per jurnal → artefak JSON (-out, default data/licfix-attrs.json)
//	  → REVIEW USER sebelum D4. Isi & rantai sinyal: lihat attrs.go.
//
// D4 (C7-b, approve 27 Okt): -apply -phase lic  → legacy 812 dari
// data/licfix-legacy.json (ditulis dry-run) · -apply -phase attrs → kolom
// baru 41 jurnal dari data/licfix-attrs.json persis hasil review.
//
// REV. C7-a (temuan smoke 27 Okt): match PERTAMA ternyata bisa berupa label
// menu sidebar ("OPEN ACCESS STATEMENT") → rantai tak lagi stop di match
// pertama; semua kandidat di-sweep, lalu keputusan 3-tier:
//  1. CC (kuat, terminal)  — utamakan; stop cari;
//  2. sinyal KUAT sejajar  — 1 status unik → pakai; beda status → AMBIGU
//     (jujur, jangan nebak);
//  3. hanya LEMAH (heading/menu) → unresolved + bukti utk iterasi.
//
// Rantai kandidat per jurnal:
//
//	fase-1: journal_policy 200-OK (urut copyright→other→about→fee);
//	fase-2 (bila belum ketemu CC): GET canonical → home sendiri + link
//	        discovery (filter path copyright/licence/open-access/about/policy,
//	        max 5);
//	Opsi B: DOAJ API (ISSN) — CROSS-CHECK bila A hit (MATCH/DIFF), FALLBACK
//	        bila belum resolvable;
//	sisa   = unresolved/PAGU (jujur) → rekap utk iterasi rules berikutnya.
//
// Cache body per URL (sha1) di -cache → rerun parser = 0 GET (efisiensi #3;
// hanya body 200 yg di-cache supaya retry network tetap hidup).
//
// Pakai:
//
//	go run ./cmd/licfix -db data/stage2/sinta-r2-enrich.db
//	go run ./cmd/licfix -journal 8857,680 -pagu 40   (smoke subset)
package main

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"sinta-scraper/internal/corpus"
	"sinta-scraper/internal/garuda"
	"sinta-scraper/internal/ojs"

	_ "modernc.org/sqlite"
)

// errPagu = pagu GET network habis (cache hit tak dihitung) — target sisa
// dilaporkan PAGU (jujur, bukan unresolved-murni).
var errPagu = errors.New("pagu GET habis")

// Filter discovery: path link home yg nyambung ke lisensi/kebijakan/about.
var (
	reLicPath = regexp.MustCompile(`(?i)(copyright|licen[cs]e|open[-_ ]?access|publication[-_ ]?ethics|\babout\b|polic(?:y|ies)|instruction|guideline)`)
	reLicTop  = regexp.MustCompile(`(?i)(copyright|licen[cs]e|open[-_ ]?access)`)
)

type jrow struct {
	id        int64
	name      string
	unk       int
	issn      string
	doajURL   string
	canonical string
	oaiURL    string // E5 garuda_oai_url — sumber home bila canonical kosong (C7-a rev.)
}

// homeOf = URL home utk fase-2: canonical; bila kosong (j88/686/692 tanpa baris
// source_profile) → derive dari OAI resmi E5 (strip ekor "oai" → halaman
// index.php/<slug>/ = home OJS). "" = benar-benar tanpa sumber (jujur).
func (j *jrow) homeOf() string {
	if j.canonical != "" {
		return j.canonical
	}
	if j.oaiURL != "" && strings.HasSuffix(j.oaiURL, "oai") {
		return strings.TrimSuffix(j.oaiURL, "oai")
	}
	return ""
}

// pageHit = sinyal + asal halaman (untuk keputusan & report).
type pageHit struct {
	corpus.PolicyHit
	src  string // policy:<typ>|home|discovery|doaj
	page string // URL halaman
}

type result struct {
	jid      int64
	name     string
	unk      int
	status   string // open|free_not_open|paid|unresolved|PAGU
	source   string // policy:<typ>|home|discovery|doaj|doaj-diff|AMBIGU|weak-only
	url      string // halaman sumber (jejak audit → provenance.license_page)
	licURL   string // URL lisensi CC (bila ada → articles.license_url)
	evidence string
	doajChk  string // "-"|MATCH|DIFF:<st>|OVERRIDE|FALLBACK|miss|err:<e>|pagu
}

func main() {
	dbPath := flag.String("db", "data/stage2/sinta-r2-enrich.db", "path file SQLite korpus")
	pagu := flag.Int("pagu", 400, "pagu GET network (cache hit gratis)")
	cacheDir := flag.String("cache", "data/licpages", "folder cache body per URL")
	jflag := flag.String("journal", "", "opsional: subset ID jurnal (mis. 8857,680)")
	solverFlag := flag.Bool("solver", true, "fase-3: retry home 403-WAF via solver chromedp (hanya bila semua jalur lain miss)")
	phase := flag.String("phase", "lic", "fase: lic (C7-a, DRY-RUN legacy) | attrs (D3 probe 2 atribut → artefak)")
	out := flag.String("out", "data/licfix-attrs.json", "fase attrs: path artefak JSON per jurnal")
	legacyOut := flag.String("out-legacy", "data/licfix-legacy.json", "fase lic: path artefak keputusan legacy (D4a)")
	applyFlag := flag.Bool("apply", false, "D4: TULIS ke DB dari artefak persis (default = dry-run; wajib backup .preC7; TANPA re-probe)")
	flag.Parse()
	if *phase != "lic" && *phase != "attrs" {
		fatal("phase %q tak dikenal (pilihan: lic | attrs)", *phase)
	}

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		fatal("Open db: %v", err)
	}
	defer db.Close()

	bpath := strings.TrimSuffix(*dbPath, ".db") + ".preC7.bak.db"

	journals, err := loadJournals(db, *jflag)
	if err != nil {
		fatal("load jurnal unknown: %v", err)
	}

	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)"
	f := &fetcher{c: garuda.NewClient("licfix", ua, time.Second, 3*time.Second), dir: *cacheDir, pagu: *pagu, solver: *solverFlag}

	// ==== D4 (C7-b): -apply = tulis PERSIS isi artefak, TANPA re-probe ====
	if *applyFlag {
		switch *phase {
		case "attrs":
			applyAttrs(db, *out, true, bpath) // D4b: kolom baru 41 jurnal
		case "lic":
			applyLegacy(db, *legacyOut, true, bpath) // D4a: legacy 812
		}
		return
	}

	// Fase D3 (attrs): probe 2 atribut → artefak JSON → REVIEW USER (0 tulis DB).
	// Fase lic (C7-a) = jalur lama, tak tersentuh (B-additive).
	if *phase == "attrs" {
		runAttrs(db, journals, f, *out)
		return
	}

	fmt.Printf("licfix DRY-RUN: %d jurnal unknown, pagu %d GET (0 UPDATE)\n\n", len(journals), *pagu)
	var results []result
	for _, j := range journals {
		results = append(results, process(f, db, j))
	}
	report(results, f)
	// D4a: dry-run MENYIMPAN keputusan → -apply menulis persis file ini.
	writeLegacyArtefak(results, *legacyOut)
}

// loadJournals = jurnal dgn baris unknown (+jumlah, ISSN utk DOAJ, doaj_url,
// canonical utk discovery), urut unknown desc. -journal = subset IN (...) —
// smoke kecil dulu sebelum full.
func loadJournals(db *sql.DB, subset string) ([]jrow, error) {
	q := `SELECT a.journal_id, COUNT(*), j.name,
	             COALESCE(NULLIF(TRIM(j.electronic_issn), ''), TRIM(COALESCE(j.print_issn, ''))),
	             COALESCE(j.doaj_url, ''), COALESCE(sp.canonical_url, ''),
	             COALESCE((SELECT je.garuda_oai_url FROM journal_enrichment je
	                       WHERE je.journal_id = a.journal_id), '')
	      FROM articles a
	      JOIN journals j ON j.id = a.journal_id
	      LEFT JOIN journal_source_profile sp ON sp.journal_id = a.journal_id
	      WHERE a.license_status = 'unknown'`
	var args []any
	if s := strings.TrimSpace(subset); s != "" {
		parts := strings.Split(s, ",")
		marks := make([]string, 0, len(parts))
		for _, p := range parts {
			id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("-journal %q: %w", p, err)
			}
			args = append(args, id)
			marks = append(marks, "?")
		}
		q += " AND a.journal_id IN (" + strings.Join(marks, ",") + ")"
	}
	q += " GROUP BY a.journal_id ORDER BY 2 DESC"

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []jrow
	for rows.Next() {
		var j jrow
		if err := rows.Scan(&j.id, &j.unk, &j.name, &j.issn, &j.doajURL, &j.canonical, &j.oaiURL); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// fetcher = GET dgn cache per-URL (sha1) + pagu. Body 200 di-cache → rerun
// parser tanpa network; body gagal TIDAK di-cache (retry tetap hidup).
type fetcher struct {
	c         *garuda.Client
	dir       string
	pagu      int
	gets      int
	exhausted bool // pagu pernah habis (lapor PAGU utk sisa tanpa bukti)
	solver    bool // fase-3 solver diizinkan (-solver=false utk matikan)
}

func (f *fetcher) get(rawURL string) ([]byte, int, error) {
	sum := sha1.Sum([]byte(rawURL))
	p := filepath.Join(f.dir, hex.EncodeToString(sum[:])+".body")
	if b, err := os.ReadFile(p); err == nil {
		return b, 200, nil
	}
	if f.gets >= f.pagu {
		f.exhausted = true
		return nil, 0, errPagu
	}
	b, st, _, err := f.c.GetPlain(rawURL, "")
	f.gets++
	if err != nil || st != 200 {
		return b, st, err
	}
	_ = os.MkdirAll(f.dir, 0o755)
	_ = os.WriteFile(p, b, 0o644)
	return b, st, nil
}

// process = satu jurnal: sweep kandidat (fase-1 policy → fase-2 home+discovery
// bila belum CC) → decide 3-tier → cross-check/fallback DOAJ → result.
func process(f *fetcher, db *sql.DB, j jrow) result {
	r := result{jid: j.id, name: j.name, unk: j.unk, status: "unresolved", doajChk: "-"}
	tried := map[string]bool{}
	var hits []pageHit

	// fase-1: kandidat journal_policy 200-OK (urut copyright→other→about→fee).
	cands, err := policyCands(db, j.id)
	if err != nil {
		r.evidence = "query policy: " + err.Error()
	}
	for _, c := range cands {
		hits = append(hits, tryPage(f, c.url, "policy:"+c.typ, tried, f.get)...)
	}

	// fase-2: home (canonical, atau derive dari OAI bila canonical kosong) +
	// discovery — kecuali CC sudah ketemu (terminal, hemat GET). Home 403/err
	// → ditandai blocked (bahan fase-3 solver).
	home := j.homeOf()
	blocked, homeNote := false, ""
	if !hasCC(hits) && home != "" {
		hb, st, err := f.get(home)
		switch {
		case err != nil:
			blocked = true
			homeNote = trim(err.Error(), 80)
		case st != 200:
			blocked = true
			homeNote = fmt.Sprintf("HTTP %d", st)
		}
		if err == nil && st == 200 {
			hits = append(hits, pageHitsOf(corpus.PolicyHits(corpus.PolicyPageText(hb)), "home", home)...)
			for _, u := range discoverLinks(home, hb) {
				hits = append(hits, tryPage(f, u, "discovery", tried, f.get)...)
			}
		}
	}

	r.status, r.source, r.url, r.licURL, r.evidence = decide(hits)

	// PAGU jujur: pagu habis & tak ada bukti → PAGU (bukan unresolved-murni).
	if r.status == "unresolved" && r.evidence == "" && f.exhausted {
		r.status = "PAGU"
		r.evidence = "pagu GET habis sebelum bukti ditemukan"
	}

	// Opsi B — DOAJ: cross-check bila A resolvable · fallback bila belum ·
	// OVERRIDE utk DIFF A=OA-generik vs B=CC-eksplisit (spesifik > generik —
	// fakta j896 "CC BY-SA + URL" vs about "open access tanpa biaya").
	issn := normISSN(j.issn, j.doajURL)
	if issn != "" {
		st, lu, detail, doajOK, doajErr := doajLicense(f, issn)
		toc := "https://doaj.org/toc/" + issn
		switch {
		case doajErr != nil && !errors.Is(doajErr, errPagu):
			r.doajChk = "err:" + trim(doajErr.Error(), 40)
		case doajErr != nil:
			r.doajChk = "pagu"
		case !doajOK:
			r.doajChk = "miss"
		case r.status == "unresolved" || r.status == "PAGU":
			r.status, r.url, r.source, r.licURL = st, toc, "doaj", lu
			r.evidence = "DOAJ bibjson.license: " + detail
			r.doajChk = "FALLBACK"
		case st == corpus.LicenseOpen && r.status == corpus.LicenseFreeNotOA:
			r.evidence = "OVERRIDE-DOAJ (" + detail + ") || A: " + trim(r.evidence, 80)
			r.status, r.url, r.source, r.licURL = st, toc, "doaj-diff", lu
			r.doajChk = "OVERRIDE"
		case st == r.status:
			r.doajChk = "MATCH"
		default:
			r.doajChk = "DIFF:" + st // beda arah selain pola di atas → tetap A, tandai review
		}
	}

	// fase-3 (bila -solver): home 403-WAF → retry via garuda.Client.Get
	// (std → hybrid TLS → solver chromedp — rantai C3b, j692 terbukti 27 Okt).
	// Hanya bila SEMUA jalur lain (termasuk DOAJ) sudah miss → efisien.
	if (r.status == "unresolved" || r.status == "PAGU") && blocked && f.solver && home != "" {
		if hb, st, err := f.getHard(home); err == nil && st == 200 {
			hits = append(hits, pageHitsOf(corpus.PolicyHits(corpus.PolicyPageText(hb)), "home", home)...)
			for _, u := range discoverLinks(home, hb) {
				hits = append(hits, tryPage(f, u, "discovery", tried, f.getHard)...)
			}
			r.status, r.source, r.url, r.licURL, r.evidence = decide(hits)
			if r.doajChk == "miss" {
				r.doajChk = "miss+solver" // jejak: DOAJ kosong, solver ikut dicoba
			}
		} else if homeNote != "" && r.evidence == "" {
			r.evidence = "solver home: " + homeNote
		}
	}
	return r
}

// normISSN = ISSN utk API DOAJ: utamakan segmen /toc/ (sudah XXXX-XXXX),
// fallback 8 digit polos → disisipkan strip. Fakta bug C7-a: query `issn:25799363`
// (tanpa strip) → 0 hasil → SEMUA "miss" selama ini palsu (fix: 2549-3892
// j88 ternyata = CC BY-SA di DOAJ).
func normISSN(raw, doajURL string) string {
	if doajURL != "" {
		if i := strings.LastIndex(doajURL, "/"); i >= 0 {
			if s := doajURL[i+1:]; strings.Contains(s, "-") {
				return s
			}
		}
	}
	d := strings.TrimSpace(raw)
	if len(d) == 8 && !strings.Contains(d, "-") {
		return d[:4] + "-" + d[4:]
	}
	return d
}

// decide = keputusan 3-tier §6.x.2-REV: CC terminal → halaman kontradiktif
// dibuang → kuat sejajar (unik / AMBIGU) → lemah / kosong. Return
// (status, source, url, licURL, evidence): licURL = URL CC bila keputusan dari
// sinyal CC (→ articles.license_url saat C7-b).
func decide(hits []pageHit) (status, source, url, licURL, evidence string) {
	// 1. CC = sinyal terkuat & terminal (utamakan urutan kandidat).
	for _, h := range hits {
		if h.Strong && h.Status == corpus.LicenseOpen {
			return h.Status, h.src, h.page, h.URL, h.Evidence
		}
	}
	// 2. halaman yg BERKONTRADIKSI dlm diri (OA + paywall dalam 1 halaman)
	//    DIBUANG — fakta smoke C7-a: halaman generic index OJS milik jurnal
	//    lain (shared hosting) memuat dua pernyataan berlawanan; jangan biarkan
	//    meracuni halaman lain yg sejajar.
	seen := map[string]map[string]bool{}
	for _, h := range hits {
		if !h.Strong {
			continue
		}
		m := seen[h.page]
		if m == nil {
			m = map[string]bool{}
			seen[h.page] = m
		}
		m[h.Status] = true
	}
	struck := map[string]bool{}
	for p, m := range seen {
		if m[corpus.LicenseFreeNotOA] && m[corpus.LicensePaid] {
			struck[p] = true
		}
	}
	var strong []pageHit
	for _, h := range hits {
		if h.Strong && !struck[h.page] {
			strong = append(strong, h)
		}
	}
	if len(strong) > 0 {
		uniq := map[string]bool{}
		for _, h := range strong {
			uniq[h.Status] = true
		}
		if len(uniq) == 1 {
			return strong[0].Status, strong[0].src, strong[0].page, strong[0].URL, strong[0].Evidence
		}
		// konflik LINTAS-halaman (halaman konsisten masing-masing tapi beda
		// arah) → JANGAN nebak (validitas #1).
		var ev strings.Builder
		for _, h := range strong {
			fmt.Fprintf(&ev, "[%s %s: %s] ", h.src, h.page, trim(h.Evidence, 60))
		}
		return "unresolved", "AMBIGU", "", "", "AMBIGU lintas-halaman: " + ev.String()
	}
	// 3. tanpa sinyal kuat sah → jejak jujur utk iterasi (gugur / lemah / nol).
	if len(hits) > 0 {
		var ev strings.Builder
		for _, h := range hits {
			label := "LEMAH"
			if h.Strong {
				label = "DITOLAK-kontradiktif"
			}
			fmt.Fprintf(&ev, "[%s %s: %s] ", label, h.src, trim(h.Evidence, 60))
		}
		return "unresolved", "weak-only", "", "", ev.String()
	}
	return "unresolved", "", "", "", ""
}

func hasCC(hits []pageHit) bool {
	for _, h := range hits {
		if h.Strong && h.Status == corpus.LicenseOpen {
			return true
		}
	}
	return false
}

// getHard = retry via garuda.Client.Get (std → 403 → hybrid TLS → solver
// chromedp — rantai C3b) utk host WAF. Body 200 di-cache (rerun gratis);
// pagu tetap berlaku. Panggilan hanya dari fase-3 (semua jalur lain miss).
func (f *fetcher) getHard(rawURL string) ([]byte, int, error) {
	sum := sha1.Sum([]byte(rawURL))
	p := filepath.Join(f.dir, hex.EncodeToString(sum[:])+".body")
	if b, err := os.ReadFile(p); err == nil {
		return b, 200, nil
	}
	if f.gets >= f.pagu {
		f.exhausted = true
		return nil, 0, errPagu
	}
	b, st, err := f.c.Get(rawURL, "")
	f.gets++
	if err != nil || st != 200 {
		return b, st, err
	}
	_ = os.MkdirAll(f.dir, 0o755)
	_ = os.WriteFile(p, b, 0o644)
	return b, st, nil
}

// pageCand = kandidat URL+tipenya dari journal_policy.
type pageCand struct{ url, typ string }

// policyCands = URL policy 200-OK, urut prioritas §6.x.2 (copyright→other→
// about→fee) — tie: URL asc (deterministik).
func policyCands(db *sql.DB, jid int64) ([]pageCand, error) {
	rows, err := db.Query(`
		SELECT url, policy_type FROM journal_policy
		WHERE journal_id = ? AND http_status = 200 AND url <> ''
		ORDER BY CASE policy_type
			WHEN 'copyright' THEN 0 WHEN 'other' THEN 1
			WHEN 'about' THEN 2 WHEN 'fee' THEN 3 ELSE 4 END, url`, jid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pageCand
	for rows.Next() {
		var c pageCand
		if err := rows.Scan(&c.url, &c.typ); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// tryPage = GET (cache) → PolicyHits (semua sinyal halaman) → pageHit ber-URL/
// src. g = getter (f.get utk jalur biasa · f.getHard utk fase-3 solver). URL
// selalu ditandai tried (jangan di-GET dua kali lewat jalur lain); gagal GET/
// pagu → tanpa hit (proses lanjut ke kandidat berikutnya).
func tryPage(f *fetcher, u, src string, tried map[string]bool, g getter) []pageHit {
	if tried[u] {
		return nil
	}
	tried[u] = true
	body, st, err := g(u)
	if err != nil || st != 200 {
		return nil
	}
	return pageHitsOf(corpus.PolicyHits(corpus.PolicyPageText(body)), src, u)
}

// getter = kontrak GET utk tryPage (f.get / f.getHard).
type getter func(string) ([]byte, int, error)

func pageHitsOf(ph []corpus.PolicyHit, src, page string) []pageHit {
	out := make([]pageHit, len(ph))
	for i, h := range ph {
		out[i] = pageHit{PolicyHit: h, src: src, page: page}
	}
	return out
}

// discoverLinks = filter ojs.FindPolicyLinks utk path lisensi/about → urut
// prioritas (copyright/licence/open-access > about/policy) lalu URL asc,
// max 5 kandidat/jurnal (pagu discovery).
func discoverLinks(homeURL string, homeBody []byte) []string {
	type cand struct {
		u string
		s int
	}
	var out []cand
	for _, l := range ojs.FindPolicyLinks(homeURL, homeBody, 0) {
		p := strings.ToLower(l.URL)
		switch {
		case reLicTop.MatchString(p):
			out = append(out, cand{l.URL, 2})
		case reLicPath.MatchString(p):
			out = append(out, cand{l.URL, 1})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].s != out[j].s {
			return out[i].s > out[j].s
		}
		return out[i].u < out[j].u
	})
	if len(out) > 5 {
		out = out[:5]
	}
	usr := make([]string, len(out))
	for i, o := range out {
		usr[i] = o.u
	}
	return usr
}

// doajLicense = GET API DOAJ utk ISSN → (status, URL CC, rincian, ok, err).
func doajLicense(f *fetcher, issn string) (string, string, string, bool, error) {
	body, st, err := f.get("https://doaj.org/api/search/journals/issn:" + issn)
	if err != nil {
		return "", "", "", false, err
	}
	if st != 200 {
		return "", "", "", false, fmt.Errorf("HTTP %d", st)
	}
	s, u, d, ok := corpus.DoajLicense(body)
	return s, u, d, ok, nil
}

// report = tabel per-jurnal + totalisasi (status × jurnal/baris) + daftar
// sisa (bahan review user sebelum C7-b).
func report(results []result, f *fetcher) {
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "JID\tUNK\tHASIL\tSUMBER\tDOAJ\tLIC-URL\tBUKTI")
	tot := map[string]int{}   // status → jurnal
	baris := map[string]int{} // status → baris
	var sisa []result
	for _, r := range results {
		tot[r.status]++
		baris[r.status] += r.unk
		if r.status != corpus.LicenseOpen && r.status != corpus.LicenseFreeNotOA && r.status != corpus.LicensePaid {
			sisa = append(sisa, r)
		}
		ev := r.evidence
		if ev == "" {
			ev = "-"
		}
		lic := r.licURL
		if lic == "" {
			lic = "-"
		}
		fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s\t%s\t%s\n",
			r.jid, r.unk, r.status, srcShort(r.source, r.url), r.doajChk, trim(lic, 40), trim(ev, 110))
	}
	_ = w.Flush()

	fmt.Println("\n=== TOTALISASI (jurnal / baris) ===")
	for _, st := range []string{corpus.LicenseOpen, corpus.LicenseFreeNotOA, corpus.LicensePaid, "unresolved", "PAGU"} {
		if tot[st] > 0 {
			fmt.Printf("  %-14s %2d jurnal  %4d baris\n", st, tot[st], baris[st])
		}
	}
	resolved := 0
	for _, st := range []string{corpus.LicenseOpen, corpus.LicenseFreeNotOA, corpus.LicensePaid} {
		resolved += baris[st]
	}
	fmt.Printf("\nRESOLVED: %d baris\n", resolved)
	if len(sisa) > 0 {
		fmt.Println("\n=== SISA (unresolved/PAGU — bahan iterasi berikutnya) ===")
		for _, r := range sisa {
			fmt.Printf("  j%-6d %-40s via=%s doaj=%s\n    ev=%s\n",
				r.jid, trim(r.name, 40), r.source, r.doajChk, trim(r.evidence, 200))
		}
	}
	fmt.Printf("\nGET network=%d / pagu %d (sisanya cache) · body: data/licpages/*.body\n", f.gets, f.pagu)
}

// srcShort = sumber ringkas utk kolom tabel (tipe + URL pendek).
func srcShort(src, u string) string {
	if src == "" {
		return "-"
	}
	if u == "" {
		return src
	}
	return src + " " + trim(u, 55)
}

func trim(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
