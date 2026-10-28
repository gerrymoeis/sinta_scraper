package corpus

// ==== Fase 3.5 lisensi (C7 — doc 43 §6.x; Opsi A+B approve user 27 Okt) ====
//
// LicenseFromPolicy → kini PolicyHits (rev. C7-a, smoke 27 Okt): aturan
// §6.x.2 utk teks halaman kebijakan jurnal — perluasan licenseFromOAI
// (derive.go) utk 888 baris `unknown` yg sudah GAGAL di jalur Crossref+OAI
// (stage.go:448-463: kedua jalur dilewati saat full-run, hasil tetap unknown
// → OAI record jurnal-jurnal ini memang tanpa `dc:rights`). Sifat
// DETERMINISTIK + mengembalikan SEMUA sinyal dgn flag Strong (heading/menu
// = lemah, jangan jadi dasar UPDATE) + evidence utk review dry-run C7-a.
//
// DoajLicense = Opsi B (§6.x.2 cross-check & fallback): `bibjson.license`
// dari API pencarian DOAJ. Hanya 9/41 jurnal punya doaj_url → cukup sbg
// PEMBANDING (bila A hit) + fallback (bila A miss), bukan primer.

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
)

// Strip HTML — pola ojs/fees.go ExtractFees: blok paragraf/list → \n (baris
// dipertahankan utk evidence), tag dibuang. script/style/komentar dibuang
// PENUH: jangan sampai keyword di JS/analytics jadi "bukti" lisensi palsu.
var (
	rePolBlock = regexp.MustCompile(`(?i)</(p|li|tr|h[1-6]|div|td|span)>|<br\s*/?>`)
	rePolTag   = regexp.MustCompile(`(?s)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<!--.*?-->|<[^>]+>`)
	// CC = sinyal TERKUAT: URL resmi creativecommons.org (termasuk subdomain
	// badge OJS i.creativecommons.org/l/by/4.0/88x31.png — fakta smoke C7-a
	// j828) …
	reCCURL = regexp.MustCompile(`(?i)https?://(?:[a-z0-9-]+\.)?creativecommons\.org/(?:licenses|publicdomain|zero|l)/[a-z0-9][a-z0-9./-]*`)
	// … atau teks "CC BY[-NC/-ND/-SA…] [x.y]" (pemisah spasi/strip; varian
	// nc|nd|sa bertingkat — "by-nc-sa" matched oleh group berulang).
	reCCWord = regexp.MustCompile(`(?i)\bcc[\s-]?by(?:(?:[-\s]?(?:nc|nd|sa))+)?(?:[-\s]?\d+(?:\.\d+)?)?\b`)
	// … atau ejaan penuh "Creative Commons Attribution/License/…." (fakta
	// smoke C7-a j828: halaman memuat "licensed under a Creative Commons
	// …" tanpa singkatan CC BY & tanpa URL /licenses/).
	reCCText = regexp.MustCompile(`(?i)creative\s+commons\s+(?:attribution|sharealike|noncommercial|no[\s-]?derivatives|zero|licen[cs]e)`)
	// Statement OA TANPA CC → free_not_open (konsisten §5.5 baris OAI).
	reOA = regexp.MustCompile(`(?i)\b(open[-\s]?access|akses\s+terbuka|bebas\s+akses|free\s+access)\b`)
	// Paywall → paid. HATI: APC/fee TIDAK dipakai sbg sinyal (fee ≠ lisensi
	// — opsi ditolak §6.x.3); "subscribe" di footer juga tak match (\b).
	rePaid = regexp.MustCompile(`(?i)\b(paywall|berbayar|berlangganan|subscriptions?|paid\s+content)\b`)
)

// PolicyPageText = HTML → teks rapi utk parser (entity di-unescape; spasi
// berlebih dipadatkan; baris dipertahankan sbg batas cuplikan evidence).
func PolicyPageText(body []byte) string {
	text := rePolBlock.ReplaceAllString(string(body), "\n")
	text = rePolTag.ReplaceAllString(text, " ")
	text = html.UnescapeString(text)
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		lines[i] = strings.Join(strings.Fields(ln), " ")
	}
	return strings.Join(lines, "\n")
}

// PolicyHit = SATU sinyal lisensi dalam teks halaman (§6.x.2). Strong=false
// = match pendek ala heading/menu ("OPEN ACCESS STATEMENT" sbg label sidebar —
// bukti riil dari smoke C7-a) → TIDAK boleh jadi dasar UPDATE, cuma bukti utk
// review. URL hanya utk CC ber-URL (jujur utk sisanya).
type PolicyHit struct {
	Status   string
	URL      string
	Evidence string
	Strong   bool
}

// minStrongWords = ambang "kalimat utuh": evidence (baris match + baris
// berikut) ≥10 kata → kuat; di bawah = heading/menu (fakta smoke C7-a: label
// menu "OPEN ACCESS STATEMENT" + "COPY RIGHT NOTICE" = 6 kata = lemah; jg
// menahan menu+1-baris-pendek yg kebetulan padat).
const minStrongWords = 10

// PolicyHits = SEMUA sinyal lisensi dalam teks halaman, urut kemunculan.
// Kandidat di-GET penuh (bukan stop di match pertama — putusan C7-a rev.):
// satu halaman bisa memuat menu lemah + pernyataan kuat; satu jurnal bisa
// punya CC di halaman LAIN dari kandidat pertamanya. Keputusan akhir =
// caller (CC terminal > kuat sejajar > ambigu > lemah).
func PolicyHits(text string) []PolicyHit {
	var hits []PolicyHit
	// Rentang CC-URL: kata "CC BY" di dalam URL jangan dihitung dobel.
	var claimed [][]int
	for _, loc := range reCCURL.FindAllStringIndex(text, -1) {
		hits = append(hits, PolicyHit{LicenseOpen, text[loc[0]:loc[1]], evidenceAround(text, loc), true})
		claimed = append(claimed, loc)
	}
	for _, loc := range reCCText.FindAllStringIndex(text, -1) {
		if inAny(loc, claimed) {
			continue
		}
		hits = append(hits, PolicyHit{LicenseOpen, "", evidenceAround(text, loc), true})
	}
	for _, loc := range reCCWord.FindAllStringIndex(text, -1) {
		if inAny(loc, claimed) {
			continue
		}
		hits = append(hits, PolicyHit{LicenseOpen, "", evidenceAround(text, loc), true})
	}
	for _, loc := range reOA.FindAllStringIndex(text, -1) {
		if inAny(loc, claimed) {
			continue
		}
		ev := evidenceAround(text, loc)
		hits = append(hits, PolicyHit{LicenseFreeNotOA, "", ev, len(strings.Fields(ev)) >= minStrongWords})
	}
	for _, loc := range rePaid.FindAllStringIndex(text, -1) {
		if inAny(loc, claimed) {
			continue
		}
		ev := evidenceAround(text, loc)
		hits = append(hits, PolicyHit{LicensePaid, "", ev, len(strings.Fields(ev)) >= minStrongWords})
	}
	return hits
}

// inAny = loc di dalam salah satu rentang (utk redup CC-word vs CC-URL).
func inAny(loc []int, ranges [][]int) bool {
	for _, r := range ranges {
		if loc[0] >= r[0] && loc[1] <= r[1] {
			return true
		}
	}
	return false
}

// evidenceAround = cuplikan baris match + baris non-kosong berikutnya (heading
// berdiri sendiri → kalimatnya di baris bawah ikut terbawa, utk review),
// dipadatkan jadi ≤2 baris s.d. ±300 char.
func evidenceAround(text string, loc []int) string {
	start := loc[0]
	for start > 0 && text[start-1] != '\n' && loc[0]-start < 300 {
		start--
	}
	end := loc[1]
	for end < len(text) && text[end] != '\n' && end-loc[1] < 300 {
		end++
	}
	// baris berikut non-kosong (kap total 300 char dari start)
	n := end
	for n < len(text) && (text[n] == '\n' || text[n] == '\r' || text[n] == '\t' || text[n] == ' ') {
		n++
	}
	for n < len(text) && text[n] != '\n' && n-start < 300 {
		n++
	}
	return strings.ToValidUTF8(strings.Join(strings.Fields(text[start:n]), " "), "?")
}

// reCCType = pola tipe lisensi DOAJ ("CC BY", "CC BY-NC-SA 4.0", "CC-BY" …).
var reCCType = regexp.MustCompile(`(?i)^cc\b|creative\s+commons`)

// DoajEntry = parse dasar API DOAJ utk fase-attrs (D3 — doc 43 §6.x.8):
// (found, licType, licURL, detail).
//
//	found    = results>0 → jurnal TERDAFTAR di direktori OA DOAJ = sinyal
//	           ① oa_status=free (kriteria DOAJ = jurnal OA; terpisah dari
//	           soal lisensi — DOAJ bisa find tanpa license entry).
//	licType  = tipe CC pertama yg dikenal di bibjson.license ("" bila tak ada
//	           / bukan CC → DOAJ tak mengklaim → jangan dipaksa).
//
// JSON beda bentuk / gagal parse → (false,"","","") — jujur.
func DoajEntry(body []byte) (found bool, licType, licURL, detail string) {
	var resp struct {
		Results []struct {
			Bibjson struct {
				License []struct {
					Type string `json:"type"`
					URL  string `json:"url"`
				} `json:"license"`
			} `json:"bibjson"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, "", "", ""
	}
	if len(resp.Results) == 0 {
		return false, "", "", ""
	}
	for _, res := range resp.Results {
		for _, l := range res.Bibjson.License {
			t := strings.TrimSpace(l.Type)
			if !reCCType.MatchString(t) {
				continue
			}
			u := strings.TrimSpace(l.URL)
			d := t
			if u != "" {
				d += " => " + u
			}
			return true, t, u, d
		}
	}
	return true, "", "", "" // terdaftar, tapi tanpa klaim lisensi CC
}

// DoajLicense = Opsi B: parse `results[].bibjson.license` dari respons API
// https://doaj.org/api/search/journals/issn:<ISSN>. Ada entri CC* → open
// (URL + rincian tipe utk evidence). Entri non-CC / license kosong / JSON
// beda bentuk → ok=false (jujur — DOAJ sendiri tak mengklaim lisensi →
// jangan dipaksa). Implementasi di atas DoajEntry (parser D3 dipakai ulang).
//
// Return (status, url, detail, ok): detail = "CC BY-SA => <url>" sbg bukti
// review dry-run & bahan OVERRIDE DIFF (DOAJ CC eksplisit > OA generik).
func DoajLicense(body []byte) (status, url, detail string, ok bool) {
	found, _, u, d := DoajEntry(body)
	if !found || d == "" {
		return "", "", "", false
	}
	return LicenseOpen, u, d, true
}
