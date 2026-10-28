package main

// ==== probe.go — rantai probe AKSES per artikel (full rantai approve 29 Okt) ====
//
// Urutan (BUKTI LANGSUNG > PERNYATAAN — prinsip user "PDF bisa diunduh =
// free" sbg bukti tertinggi):
//
//	① GET view (GetPlain — status jujur, final URL utk resolve)
//	   landing balik %PDF → free · non-200/error → unknown (jujur, dicatat)
//	② pernyataan kuat halaman (PolicyHits strong — CC diabaikan) dicadangkan
//	③ TES PDF tiap pointer (meta→anchor→embed→param; hop ≤2):
//	   200+%PDF → FREE (terminal) · HTML jelas bayar/login → paid/restricted
//	④ bila ③ inconclusive: pernyataan kuat → judul halaman → unknown

import (
	"fmt"
	"strings"

	"sinta-scraper/internal/corpus"
	"sinta-scraper/internal/garuda"
)

// maxHops = batas lanjutan pointer dlm HTML viewer (pola D3b licfix).
const maxHops = 2

// prober = pembungkus client + pagu GET (observability utk run tak berjaga).
type prober struct {
	c       *garuda.Client
	gets    int
	pagu    int
	paguHit bool
}

// get = GET tunggal dgn pagu. Setelah pagu habis → error permanen (caller
// berhenti — artefak berisi yg sudah terprobe, jujur).
func (p *prober) get(u string) ([]byte, int, string, error) {
	if p.paguHit {
		return nil, 0, "", fmt.Errorf("pagu habis")
	}
	if p.pagu > 0 && p.gets >= p.pagu {
		p.paguHit = true
		return nil, 0, "", fmt.Errorf("pagu habis")
	}
	p.gets++
	return p.c.GetPlain(u, "")
}

// probe = 1 artikel: rantai ①→④ (lihat header file).
func (p *prober) probe(t *target) res {
	r := res{
		JournalID: t.journalID, Journal: t.journal,
		ArticleID: t.id, URL: t.url, Decided: corpus.OAUnknown,
	}

	body, st, final, err := p.get(t.url)
	if err != nil {
		r.Note = "view: " + trim(err.Error(), 80)
		return r // unknown (jujur)
	}
	r.ViewStatus = st
	if final != "" {
		r.FinalURL = final
	}
	if st != 200 {
		r.Note = fmt.Sprintf("view HTTP %d", st)
		return r // unknown — 403 WAF/404 dicatat apa adanya
	}
	base := final
	if base == "" {
		base = t.url
	}
	if isPDF(body) {
		r.Decided, r.Evidence = corpus.OAFree, "landing 200 langsung balik %PDF"
		return r
	}

	// ② pernyataan kuat (cadangan bila tes PDF inconclusive — CC abaikan).
	stmt, stmtEv := strongStatement(body)

	// ③ TES PDF — selalu dijalankan bila ada pointer (fallback terakhir).
	decided, ev := "", ""
	for _, pt := range pointersOf(base, body) {
		r.Tried = append(r.Tried, pt.via+":"+pt.url)
		d, e := p.followPDF(pt, 0)
		if d != "" {
			decided, ev = d, e
			break
		}
	}
	if decided == "" && stmt != "" {
		decided, ev = stmt, stmtEv
	}
	if decided == "" {
		if s, why := classifyHTMLTitle(body); s != "" {
			decided, ev = s, why
		}
	}
	if decided == "" {
		r.Note = "tanpa pointer PDF & tanpa pernyataan kuat (jujur — tetap unknown)"
		return r
	}
	r.Decided, r.Evidence = decided, ev
	return r
}

// followPDF = ikuti 1 pointer (maks maxHops lanjutan dlm HTML viewer).
// Return ("", note) bila tak conclusif (caller coba pointer berikutnya).
func (p *prober) followPDF(pt ptr, depth int) (status, note string) {
	b, st, final, err := p.get(pt.url)
	if err != nil {
		return "", "pdf " + pt.via + ": " + trim(err.Error(), 56)
	}
	if st != 200 {
		return "", fmt.Sprintf("pdf %s: HTTP %d", pt.via, st)
	}
	if isPDF(b) {
		return corpus.OAFree, fmt.Sprintf("PDF 200 + %%PDF magic (%d KB) via %s", len(b)/1024, pt.via)
	}
	// Balasan HTML — klasifikasi judul (bayar/login) bila jelas.
	if s, why := classifyHTMLTitle(b); s != "" {
		return s, why
	}
	// HTML viewer → pointer lanjutan (maks maxHops — pola D3b).
	if depth < maxHops {
		base := final
		if base == "" {
			base = pt.url
		}
		for _, nx := range pointersOf(base, b) {
			if nx.url == pt.url {
				continue // jangan putar balik ke diri sendiri
			}
			if s, why := p.followPDF(nx, depth+1); s != "" {
				return s, why
			}
		}
	}
	return "", fmt.Sprintf("pdf %s: HTML tanpa pointer .pdf lanjutan", pt.via)
}

// strongStatement = pernyataan KUAT di halaman (corpus.PolicyHits: ≥10 kata
// — label menu/sidebar lemah ditahan; pola strongOA D3 licfix):
//
//	akses terbuka → free · berbayar/langganan → paid · CC diabaikan
//	(lisensi ≠ bukti akses — aturan hybrid D1) · konflik → "" (jujur).
func strongStatement(body []byte) (status, evidence string) {
	txt := corpus.PolicyPageText(body)
	var freeEv, paidEv string
	for _, h := range corpus.PolicyHits(txt) {
		if !h.Strong {
			continue
		}
		switch h.Status {
		case corpus.LicenseFreeNotOA:
			if freeEv == "" {
				freeEv = h.Evidence
			}
		case corpus.LicensePaid:
			if paidEv == "" {
				paidEv = h.Evidence
			}
		}
	}
	switch {
	case freeEv != "" && paidEv != "":
		return "", "" // konflik — jangan nebak (jujur)
	case freeEv != "":
		return corpus.OAFree, "pernyataan kuat akses-terbuka: " + strings.TrimSpace(freeEv)
	case paidEv != "":
		return corpus.OAPaid, "pernyataan kuat berbayar: " + strings.TrimSpace(paidEv)
	}
	return "", ""
}

// classifyHTMLTitle = <title> halaman → paid/restricted (pola D3 licfix:
// klasifikasi dari <title> saja — kata "login" di navbar rawan positif palsu).
func classifyHTMLTitle(b []byte) (status, note string) {
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
	return "", ""
}
