package main

// ==== fase "attrs" — D3 probe 2 atribut (doc 43 §6.x.8, approve 27 Okt) ====
//
// ALAT REVIEW (0 tulis DB): per jurnal → tentukan oa_status + license_type
// → tulis artefak JSON (-out) + tabel → REVIEW USER → baru D4 UPDATE.
//
// Rantai sinyal (persis plan §6.x.8 D3):
//
//	oa_status    ① DOAJ results>0 (cache) → free
//	             ② pernyataan halaman KUAT unik (cache) → free|paid
//	             ③ tes PDF 1 artikel TERBARU: 200+%PDF → free · halaman bayar →
//	                paid · halaman login → restricted · 403/404/tanpa-PDF →
//	                unknown (jujur — jangan ditebak)
//	license_type keputusan CC halaman A (PRIORITAS — konsisten dgn license_url
//	             yg ditulis D4 dari keputusan sama) → DOAJ CC → footer ARR
//	             eksplisit → unknown
//
// Keputusan = per-JURNAL (bukti journal-level) → berlaku utk semua baris
// jurnal itu di D4. Sweep = jalur BARU terpisah dari phase "lic" (C7-a
// tuntas — jangan disentuh; pola B-additive).

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	"sinta-scraper/internal/corpus"
	"sinta-scraper/internal/storage"
)

// attrRes = 1 baris artefak keputusan per jurnal (field plan + rincian review
// utk 2 dimensi yang buktinya terpisah).
type attrRes struct {
	JournalID   int64  `json:"journal_id"`
	Name        string `json:"name"`
	Rows        int    `json:"rows"`         // jumlah baris artikel jurnal ini
	OAStatus    string `json:"oa_status"`    // free|paid|restricted|unknown
	OASrc       string `json:"oa_src"`       // doaj|statement|pdf|unknown
	OAEvidence  string `json:"oa_evidence"`  // bukti utk review
	OAPage      string `json:"oa_page"`      // halaman sumber bukti akses
	LicenseType string `json:"license_type"` // CC-kanonik|all_rights_reserved|unknown
	LicenseURL  string `json:"license_url"`  // URL CC (bila ada)
	LicenseSrc  string `json:"license_src"`  // policy|home|discovery|doaj|unknown
	Evidence    string `json:"evidence"`     // bukti lisensi
	Page        string `json:"page"`         // halaman sumber bukti lisensi
	Doaj        string `json:"doaj"`         // jejak ① (found:…|miss|pagu|err)
	PDF         string `json:"pdf"`          // jejak ③ (bila tes dijalankan)
}

// pageText = teks halaman yg berhasil di-GET (utk deteksi ARR + bukti review).
type pageText struct{ src, page, text string }

// runAttrs = driver fase D3: probe semua jurnal → artefak JSON + tabel.
func runAttrs(db *sql.DB, journals []jrow, f *fetcher, outPath string) {
	rowsOf := rowsPerJournal(db)
	fmt.Printf("licfix fase=attrs (D3): %d jurnal · pagu %d GET · 0 tulis DB\n\n",
		len(journals), f.pagu)
	out := make([]attrRes, 0, len(journals))
	for _, j := range journals {
		out = append(out, probeJournal(db, f, j, rowsOf[j.id]))
	}

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fatal("marshal artefak: %v", err)
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		fatal("tulis artefak %s: %v", outPath, err)
	}
	reportAttrs(out, f, outPath)
}

// probeJournal = probe 1 jurnal (semua keputusan per-jurnal, 0 tulis).
func probeJournal(db *sql.DB, f *fetcher, j jrow, rows int) attrRes {
	r := attrRes{
		JournalID: j.id, Name: j.name, Rows: rows,
		OAStatus: corpus.OAUnknown, OASrc: "unknown",
		LicenseType: storage.LicenseTypeUnknown, LicenseSrc: "unknown",
		Doaj: "-", PDF: "-",
	}

	// ── ② sweep halaman (kandidat C7-a — mayoritas cache, 0 GET) ──
	var texts []pageText
	hits, blocked, homeFetched := sweepAttrs(f, db, j, &texts)
	decStatus, decSrc, decPage, decLicURL, decEv := decide(hits)

	// ── ① DOAJ (cache): results>0 → free; bibjson.license → tipe CC ──
	found, dtyp, dlu, ddet := false, "", "", ""
	toc := ""
	if issn := normISSN(j.issn, j.doajURL); issn == "" {
		r.Doaj = "tanpa-ISSN"
	} else {
		toc = "https://doaj.org/toc/" + issn
		body, st, err := f.get("https://doaj.org/api/search/journals/issn:" + issn)
		switch {
		case errors.Is(err, errPagu):
			r.Doaj = "pagu"
		case err != nil:
			r.Doaj = "err:" + trim(err.Error(), 48)
		case st != 200:
			r.Doaj = fmt.Sprintf("HTTP %d", st)
		default:
			found, dtyp, dlu, ddet = corpus.DoajEntry(body)
			switch {
			case !found:
				r.Doaj = "miss"
			case dtyp == "":
				r.Doaj = "found-tanpa-CC"
			default:
				r.Doaj = "found:" + dtyp
			}
		}
	}

	// ── sisa kebutuhan: home belum di-GET (sweep berhenti krn CC sudah ketemu)
	//    dan/atau ① miss → 1 GET home (pernyataan OA + footer ARR);
	//    403 → solver rantai C3b (hanya bila ①② belum cukup — efisien #3) ──
	aCC := canonHit(decLicURL, decEv)
	dCC := canonFromDOAJ(dtyp, dlu)
	need := (decStatus != corpus.LicenseOpen || aCC == "" || !found)
	home := j.homeOf()
	if need && !homeFetched && home != "" {
		if hb, st, err := f.get(home); err == nil && st == 200 {
			txt := corpus.PolicyPageText(hb)
			hits = append(hits, pageHitsOf(corpus.PolicyHits(txt), "home", home)...)
			texts = append(texts, pageText{"home", home, txt})
			decStatus, decSrc, decPage, decLicURL, decEv = decide(hits)
			aCC = canonHit(decLicURL, decEv)
		} else {
			blocked = true
		}
	}
	if need && blocked && f.solver && home != "" {
		if hb, st, err := f.getHard(home); err == nil && st == 200 {
			txt := corpus.PolicyPageText(hb)
			hits = append(hits, pageHitsOf(corpus.PolicyHits(txt), "home", home)...)
			texts = append(texts, pageText{"home", home, txt})
			for _, u := range discoverLinks(home, hb) {
				hits = append(hits, tryPage(f, u, "discovery", map[string]bool{}, f.getHard)...)
			}
			decStatus, decSrc, decPage, decLicURL, decEv = decide(hits)
			aCC = canonHit(decLicURL, decEv)
		} else if st != 0 {
			r.PDF = fmt.Sprintf("solver home HTTP %d", st)
		}
	}

	// ── RESOLUSI license_type: A-CC (konsisten dgn license_url D4) → DOAJ CC
	//    → 2-pass teks halaman (CC dulu — lisensi boleh dibahas di paragraf,
	//    baru ARR eksplisit) → unknown ── D3b: cabang akhir dulu cuma cek ARR
	//    → sebutan CC di paragraf bisa tertimpa ARR/unknown (inkonsistensi
	//    dgn LicenseTypeOf D1) — fixed.
	switch {
	case decStatus == corpus.LicenseOpen && aCC != "":
		r.LicenseType = aCC
		r.LicenseSrc, r.Page, r.Evidence, r.LicenseURL = shortSrc(decSrc), decPage, decEv, decLicURL
	case dCC != "":
		r.LicenseType = dCC
		r.LicenseSrc, r.LicenseURL = "doaj", dlu
		r.Page, r.Evidence = toc, "DOAJ bibjson.license: "+ddet
	default:
		if t, u, src, page, ev := licenseFromTexts(texts); t != "" {
			r.LicenseType, r.LicenseURL, r.LicenseSrc, r.Page, r.Evidence = t, u, src, page, ev
		}
	}

	// ── RESOLUSI oa_status: ① DOAJ → ② pernyataan halaman → ③ tes PDF ──
	if found {
		r.OAStatus = corpus.OAFree // results>0 = jurnal di direktori OA kurasi
		r.OASrc, r.OAPage, r.OAEvidence = "doaj", toc,
			"DOAJ results>0 (jurnal terdaftar di direktori OA kurasi)"
	} else if s, h := strongOA(hits); s != "" {
		r.OAStatus, r.OASrc, r.OAPage, r.OAEvidence = s, "statement", h.page, h.Evidence
	} else {
		st, note, page := pdfProbe(f, db, j.id)
		r.PDF = note
		if st != "" {
			r.OAStatus, r.OASrc, r.OAPage, r.OAEvidence = st, "pdf", page, note
		}
	}
	if blocked && r.OAEvidence == "" {
		r.OAEvidence = "home terblokir/err (bahan review)"
	}
	return r
}

// sweepAttrs = fase-1 policy (urut copyright→…; kandidat C7-a, cache) →
// fase-2 home+discovery (home hanya bila BELUM ada CC — terminal hemat GET,
// pola C7-a). Texts diisi utk deteksi ARR. Return: hits, blocked (home
// di-GET tapi gagal), homeFetched (home sukses di-GET).
func sweepAttrs(f *fetcher, db *sql.DB, j jrow, texts *[]pageText) (hits []pageHit, blocked, homeFetched bool) {
	tried := map[string]bool{}
	cands, err := policyCands(db, j.id)
	if err != nil {
		return nil, false, false // query gagal → tanpa kandidat (jujur, lanjut)
	}
	for _, c := range cands {
		if b, st, gerr := getTried(f, c.url, tried); gerr == nil && st == 200 {
			txt := corpus.PolicyPageText(b)
			hits = append(hits, pageHitsOf(corpus.PolicyHits(txt), "policy:"+c.typ, c.url)...)
			*texts = append(*texts, pageText{"policy:" + c.typ, c.url, txt})
		}
	}

	home := j.homeOf()
	if home == "" || hasCC(hits) {
		return hits, false, false
	}
	b, st, gerr := getTried(f, home, tried)
	if gerr != nil || st != 200 {
		return hits, true, false
	}
	txt := corpus.PolicyPageText(b)
	hits = append(hits, pageHitsOf(corpus.PolicyHits(txt), "home", home)...)
	*texts = append(*texts, pageText{"home", home, txt})
	for _, u := range discoverLinks(home, b) {
		if pb, pst, perr := getTried(f, u, tried); perr == nil && pst == 200 {
			pt := corpus.PolicyPageText(pb)
			hits = append(hits, pageHitsOf(corpus.PolicyHits(pt), "discovery", u)...)
			*texts = append(*texts, pageText{"discovery", u, pt})
		}
	}
	return hits, false, true
}

// getTried = GET sekali per URL (dedup lintas jalur) — pola tryPage tapi
// mengembalikan body (fase attrs butuh teks utk ARR). URL sudah di-GET →
// error → caller lanjut ke kandidat berikutnya.
func getTried(f *fetcher, u string, tried map[string]bool) ([]byte, int, error) {
	if tried[u] {
		return nil, 0, errors.New("sudah di-GET")
	}
	tried[u] = true
	return f.get(u)
}

// strongOA = ② pernyataan halaman KUAT unik (free/paid) → (status, hit).
// CC diabaikan (lisensi ≠ bukti akses — aturan hybrid D1); nol / konflik →
// ("", zero) — jangan nebak (validitas #1).
func strongOA(hits []pageHit) (string, pageHit) {
	uniq := map[string]pageHit{}
	var order []string
	for _, h := range hits {
		if !h.Strong {
			continue
		}
		var s string
		switch h.Status {
		case corpus.LicenseFreeNotOA:
			s = corpus.OAFree
		case corpus.LicensePaid:
			s = corpus.OAPaid
		default:
			continue
		}
		if _, ok := uniq[s]; !ok {
			uniq[s] = h
			order = append(order, s)
		}
	}
	if len(order) == 1 {
		return order[0], uniq[order[0]]
	}
	return "", pageHit{}
}

// rowsPerJournal = jumlah baris artikel per jurnal (info review artefak).
func rowsPerJournal(db *sql.DB) map[int64]int {
	out := map[int64]int{}
	rows, err := db.Query(`SELECT journal_id, COUNT(*) FROM articles GROUP BY journal_id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, n int
		if err := rows.Scan(&id, &n); err != nil {
			return out
		}
		out[int64(id)] = n
	}
	return out
}

// shortSrc = "policy:copyright" → "policy" (jejak license_src ringkas).
func shortSrc(src string) string {
	if i := strings.IndexByte(src, ':'); i >= 0 {
		return src[:i]
	}
	return src
}

// canonHit = tipe CC kanonik dari bukti keputusan halaman (URL → teks).
func canonHit(licURL, evidence string) string {
	if t := corpus.LicenseTypeFromURL(licURL); t != "" {
		return t
	}
	return corpus.LicenseTypeFromText(evidence)
}

// licenseFromTexts = 2-pass PERSIS pola LicenseTypeOf (D1): SEMUA teks
// halaman dicek CC dulu (URL → kata "CC BY…"), baru ARR eksplisit — satu
// halaman boleh memuat footer template ARR + lisensi CC di paragraf (fakta
// D2). D3b: cabang akhir fase attrs dulu cuma cek ARR → CC paragraf bisa
// hilang. Kembalikan (tipe, URL CC, src, page, evidence); "" bila tanpa
// bukti (jujur → caller tetap unknown).
func licenseFromTexts(texts []pageText) (licType, licURL, src, page, evidence string) {
	for _, pt := range texts {
		if t := corpus.LicenseTypeFromText(pt.text); t != "" {
			u := ccURLOf(pt.text, t)
			ev := "teks halaman memuat lisensi " + t
			if u != "" {
				ev += " — " + u
			}
			return t, u, "text-" + pt.src, pt.page, ev
		}
	}
	for _, pt := range texts {
		if ev, ok := corpus.ARREvidence(pt.text); ok {
			return storage.LicenseTypeARR, "", "footer-" + pt.src, pt.page, ev
		}
	}
	return "", "", "", "", ""
}

// reCCURLText = URL Creative Commons apa adanya di teks halaman (dgn/sans
// skema — teks hasil PolicyPageText bisa membuang "https://").
var reCCURLText = regexp.MustCompile(`(?i)(?:https?://)?creativecommons\.org/(?:licenses|l|publicdomain)/[^\s"'<>()]+`)

// ccURLOf = URL CC dalam teks yg TIPE-NYA COCOK dgn tipe hasil parse (maks 3
// kandidat). Tak cocok/ketemu = "" — jangan pasangkan URL beda lisensi dgn
// tipe (pasangan license_url+license_type D4 wajib konsisten, #1).
func ccURLOf(text, typ string) string {
	for _, m := range reCCURLText.FindAllString(text, 3) {
		u := m
		if !strings.HasPrefix(strings.ToLower(u), "http") {
			u = "https://" + u
		}
		if corpus.LicenseTypeFromURL(u) == typ {
			return u
		}
	}
	return ""
}

// canonFromDOAJ = URL (memuat versi, konsisten dgn turunan D2 dari URL)
// → label ("CC BY-SA" tanpa versi — jangan dikarang versinya) → "" (bukan CC).
func canonFromDOAJ(typ, u string) string {
	if t := corpus.LicenseTypeFromURL(u); t != "" {
		return t
	}
	return corpus.LicenseTypeFromLabel(typ)
}

// ==== ③ tes PDF — probe akses faktual (1 artikel TERBARU per jurnal) ====

var (
	reAnchor     = regexp.MustCompile(`(?is)<a\b[^>]*href=["']([^"'#]+)["'][^>]*>(.*?)</a>`)
	reTitle      = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reTagStrip   = regexp.MustCompile(`(?s)<[^>]+>`)
	reGalleyPath = regexp.MustCompile(`(?i)article/view/\d+/\d+`)
	rePaywTitle  = regexp.MustCompile(`(?i)\b(subscribe|subscription|langganan|purchase|checkout|premium|order\s+now|beli)\b`)
	reLoginTitle = regexp.MustCompile(`(?i)\b(log[\s-]?in|sign[\s-]?in|authentication|masuk)\b`)
)

// pdfProbe = ③ (approve user: "artikel terakhir/terbaru"): pilih 1 artikel
// dgn URL (year DESC) · GET landing · temukan pointer PDF (anchor/embed/
// param viewer — D3b) · ikuti. Hasil: 200 + magic %PDF → free · HTML dgn
// judul bayar → paid · judul login → restricted · HTML viewer PDF (D3b:
// embed/param menunjuk .pdf → GET → %PDF) → free · error/tanpa pointer →
// ("", note) → caller tetap unknown (jujur).
func pdfProbe(f *fetcher, db *sql.DB, jid int64) (status, note, page string) {
	var landing string
	if err := db.QueryRow(`
		SELECT article_url FROM articles
		WHERE journal_id = ? AND article_url <> ''
		ORDER BY COALESCE(year, 0) DESC, id DESC LIMIT 1`, jid).Scan(&landing); err != nil {
		return "", "tanpa artikel dgn URL", ""
	}
	body, st, err := f.get(landing)
	if st == 403 && f.solver {
		if b2, st2, err2 := f.getHard(landing); err2 == nil && st2 == 200 {
			body, st, err = b2, st2, nil
		}
	}
	if err != nil {
		return "", "landing: " + trim(err.Error(), 56), landing
	}
	if st != 200 {
		return "", fmt.Sprintf("landing HTTP %d", st), landing
	}
	if isPDF(body) {
		return corpus.OAFree, "landing langsung balik PDF", landing
	}
	ptr, ok := findPDFAny(landing, body)
	if !ok {
		return "", "landing 200 tanpa pointer PDF (anchor/embed/viewer)", landing
	}
	return followPDF(f, ptr, 0)
}

// followPDF = ikuti pointer PDF (maks 2 hop utk rantai
// landing → viewer HTML → embed .pdf — D3b). Tiap hop: GET (+solver bila
// 403) → %PDF = free · HTML → klasifikasi judul (bayar/login) → pointer
// baru? lanjut · habis → unknown (jujur, jangan nebak).
func followPDF(f *fetcher, u string, depth int) (status, note, page string) {
	b, st, err := f.get(u)
	if st == 403 && f.solver {
		if b2, st2, err2 := f.getHard(u); err2 == nil && st2 == 200 {
			b, st, err = b2, st2, nil
		}
	}
	if err != nil {
		return "", "pdf: " + trim(err.Error(), 56), u
	}
	if st != 200 {
		return "", fmt.Sprintf("PDF HTTP %d", st), u
	}
	if isPDF(b) {
		return corpus.OAFree, fmt.Sprintf("PDF 200 + %%PDF magic (%d KB)", len(b)/1024), u
	}
	if s, why := classifyHTML(b); s != "" {
		return s, why, u
	}
	if depth < 2 {
		if p, ok := findPDFAny(u, b); ok && p != u {
			return followPDF(f, p, depth+1)
		}
	}
	return "", "HTML tanpa pointer .pdf lanjutan (viewer tak terbaca)", u
}

// findPDFAny = seluruh bentuk pointer PDF di HTML (D3b — kasus HTML viewer):
//  1. anchor dgn skor (findPDFLink — tombol "PDF/Download" biasa);
//  2. embed: iframe|object|embed dgn src/data *.pdf (viewer menyematkan PDF);
//  3. param viewer: ?file=|?url=|?src=…*.pdf (pdf.js dsb, URL-encoded).
func findPDFAny(base string, body []byte) (string, bool) {
	if u, ok := findPDFLink(base, body); ok {
		return u, true
	}
	if u, ok := findEmbedPDF(base, body); ok {
		return u, true
	}
	return findViewerParam(base, body)
}

var (
	reEmbedPDF = regexp.MustCompile(`(?is)<(?:iframe|object|embed)\b[^>]*?\b(?:src|data)\s*=\s*["']([^"']+\.pdf[^"']*)["']`)
	reViewPrm  = regexp.MustCompile(`(?i)[?&](?:file|url|src)=([^"'&\s]+\.pdf[^"'&\s]*)`)
)

// findEmbedPDF = tag <iframe|object|embed> dgn atribut src/data menuju *.pdf
// → URL absolut (resolve thd base).
func findEmbedPDF(base string, body []byte) (string, bool) {
	return resolveUnder(base, reEmbedPDF.FindSubmatch(body))
}

// findViewerParam = param ?file=…pdf (URL-encoded, mis. viewer.html?file=
// %2Fart%2F1.pdf) → URL absolut setelah unescape.
func findViewerParam(base string, body []byte) (string, bool) {
	m := reViewPrm.FindSubmatch(body)
	if m == nil {
		return "", false
	}
	v, err := url.QueryUnescape(string(m[1]))
	if err != nil {
		return "", false
	}
	return resolveRef(base, v)
}

// resolveUnder = ambil grup ke-1 regex → resolve thd base.
func resolveUnder(base string, m [][]byte) (string, bool) {
	if m == nil {
		return "", false
	}
	return resolveRef(base, string(m[1]))
}

// resolveRef = ref (absolut/relatif/protocol-relative) → URL absolut.
func resolveRef(base, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	bu, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	ru, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	return bu.ResolveReference(ru).String(), true
}

// isPDF = magic byte standar PDF (offset 0).
func isPDF(b []byte) bool { return bytes.HasPrefix(b, []byte("%PDF")) }

// findPDFLink = anchor dgn skor tertinggi → URL absolut (resolve base).
// Skor: href .pdf (+6) / /download/ (+5) / galley view (+4) / article/view
// (+1) · teks "PDF" (+4) / "download|unduh" (+2) · ambang ≥4 (jauh dari
// link nav acak).
func findPDFLink(base string, body []byte) (string, bool) {
	bu, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	best, bestScore := "", 0
	for _, m := range reAnchor.FindAllSubmatch(body, -1) {
		href := string(m[1])
		h := strings.ToLower(href)
		if strings.HasPrefix(h, "javascript:") || strings.HasPrefix(h, "mailto:") {
			continue
		}
		txt := strings.ToLower(string(reTagStrip.ReplaceAll(m[2], []byte(" "))))
		score := 0
		switch {
		case strings.Contains(h, ".pdf"):
			score += 6
		case strings.Contains(h, "/download/"):
			score += 5
		case reGalleyPath.MatchString(h):
			score += 4
		case strings.Contains(h, "/article/view/"):
			score += 1
		}
		if strings.Contains(txt, "pdf") {
			score += 4
		}
		if strings.Contains(txt, "download") || strings.Contains(txt, "unduh") {
			score += 2
		}
		if score > bestScore {
			best, bestScore = href, score
		}
	}
	if bestScore < 4 {
		return "", false
	}
	ref, err := url.Parse(best)
	if err != nil {
		return "", false
	}
	return bu.ResolveReference(ref).String(), true
}

// classifyHTML = HTML balasan utk URL PDF → (restricted|paid|"", catatan).
// Klasifikasi dari <title> saja (kata "login" di navbar rawan positif palsu).
// Tanpa indikator → ("", catatan) → unknown jujur.
func classifyHTML(b []byte) (string, string) {
	title := ""
	if m := reTitle.FindSubmatch(b); m != nil {
		title = strings.Join(strings.Fields(reTagStrip.ReplaceAllString(string(m[1]), " ")), " ")
	}
	switch {
	case rePaywTitle.MatchString(title):
		return corpus.OAPaid, "halaman bayar: " + trim(title, 60)
	case reLoginTitle.MatchString(title):
		return corpus.OARestricted, "halaman login: " + trim(title, 60)
	}
	return "", "judul halaman: " + trim(title, 50)
}

// reportAttrs = tabel review + totalisasi + jejak GET (bahan REVIEW USER D3).
func reportAttrs(out []attrRes, f *fetcher, outPath string) {
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "JID\tROWS\tOA\tOA-SRC\tLIC-TYPE\tLIC-SRC\tDOAJ\tBUKTI-AKSES | PDF")
	for _, r := range out {
		ev := r.OAEvidence
		if ev == "" {
			ev = r.Evidence
		}
		if ev == "" {
			ev = "-"
		}
		fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s | %s\n",
			r.JournalID, r.Rows, r.OAStatus, r.OASrc, r.LicenseType, r.LicenseSrc,
			r.Doaj, trim(ev, 70), trim(r.PDF, 44))
	}
	_ = w.Flush()

	oaTot, licTot := map[string]int{}, map[string]int{}
	rowsTot := 0
	for _, r := range out {
		oaTot[r.OAStatus]++
		licTot[r.LicenseType]++
		rowsTot += r.Rows
	}
	fmt.Println("\n=== TOTALISASI (jurnal) ===")
	for _, k := range sortedKeys(oaTot) {
		fmt.Printf("  oa_status    %-12s %2d jurnal\n", k, oaTot[k])
	}
	for _, k := range sortedKeys(licTot) {
		fmt.Printf("  license_type %-18s %2d jurnal\n", k, licTot[k])
	}
	fmt.Printf("total: %d jurnal · %d baris artikel · GET network=%d / pagu %d (sisanya cache)\n",
		len(out), rowsTot, f.gets, f.pagu)
	fmt.Printf("artefak: %s → REVIEW USER sebelum D4\n", outPath)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
