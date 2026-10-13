// Program probep3: probe P3 Fase 3 (doc 42 §4/§7.3) — policy + author fees.
// Tiap jurnal: GET home → parse link kandidat policy/about/author → GET halaman
// kandidat → deteksi blok fee (fee|charge|APC case-insensitive, pola doc 11 §2)
// → ekstrak snippet + nominal (IDR|USD|Rp) + "free of charge". Alat diagnosis;
// BUKAN pipeline.
package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"
)

type target struct {
	jid  int
	name string
	home string
}

// Sampel P3: 11 jurnal hidup (200 di P1/P2) campuran theme + 1 WordPress
// (j687 — policy di luar OJS). Pagu 30 GET: home + 1–2 halaman policy per
// jurnal.
var targets = []target{
	{16, "Forum Geografi", "https://journals.ums.ac.id/index.php/fg/issue/archive"},
	{19, "Al-Albab", "https://jurnaliainpontianak.or.id/index.php/alalbab/index"},
	{26, "Millah", "https://journal.uii.ac.id/Millah"},
	{94, "AGRARIS", "https://agraris.umy.ac.id/index.php/agraris"},
	{681, "BEEI", "https://beei.org/index.php/EEI"},
	{817, "ADDIN", "https://journal.uinsuku.ac.id/index.php/Addin"},
	{673, "TEFLIN", "http://www.journal.teflin.org/index.php/journal"},
	{687, "Critical Care (WP)", "https://criticalcareshock.com/"},
	{948, "Inabj", "https://inabj.org/index.php/ibj"},
	{3974, "IJOSH", "https://e-journal.unair.ac.id/IJOSH/issue/archive"},
	{20, "Otoritas", "https://journal.unismuh.ac.id/index.php/Otoritas/"},
}

var (
	reHref  = regexp.MustCompile(`(?i)href="([^"]+)"`)
	reFeeKw = regexp.MustCompile(`(?i)\b(author\s+fee|author\s+charge|article\s+processing\s+charge|\bapc\b|free\s+of\s+charge|\bfees?\b|\bcharges?\b)`)
	reNom   = regexp.MustCompile(`(?i)\b(idr|usd|rp\.?|us\$)\s*[\d][\d.,]*|[\d][\d.,]*\s*(idr|usd|rupiah|dollar)`)
	reFree  = regexp.MustCompile(`(?i)free\s+of\s+charge|tidak\s+dipungut\s+biaya|gratis`)
	reTag   = regexp.MustCompile(`<[^>]+>`)
	reSpace = regexp.MustCompile(`\s+`)
)

// policyKeywords: skor utk pilih link policy dari home (lebih tinggi = lebih
// relevan ke fee/author). Diurutkan; ambil max 2 kandidat per jurnal.
var policyKeywords = []struct {
	kw   string
	skor int
}{
	{"author", 5}, {"instruction", 5}, {"guideline", 5}, {"author-guideline", 6},
	{"for-authors", 6}, {"submission", 4}, {"about", 3}, {"policy", 3},
	{"fee", 6}, {"charge", 6}, {"apc", 6}, {"copyright", 2}, {"privacy", 2},
}

func absolutize(base, href string) string {
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "javascript:") {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return b.ResolveReference(u).String()
}

func scoreLink(href string) int {
	low := strings.ToLower(href)
	best := 0
	for _, k := range policyKeywords {
		if strings.Contains(low, k.kw) && k.skor > best {
			best = k.skor
		}
	}
	return best
}

func stripHTML(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}

func main() {
	pagu := flag.Int("pagu", 30, "pagu GET")
	flag.Parse()

	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)"
	c := garuda.NewClient("probep3", ua, time.Second, 3*time.Second)

	fmt.Printf("P3 probe: %d jurnal, pagu %d GET\n", len(targets), *pagu)
	gets := 0

	for _, t := range targets {
		if gets >= *pagu {
			fmt.Fprintf(os.Stderr, "PAGU %d tercapai — berhenti\n", *pagu)
			break
		}
		// 1) GET home
		homeBody, homeStatus, homeFinal, err := c.GetPlain(t.home, "")
		gets++
		if err != nil || homeStatus != 200 || len(homeBody) == 0 {
			fmt.Printf("JID=%d|%s|home_status=%d|err=%v|SKIP\n", t.jid, t.name, homeStatus, err)
			continue
		}
		// 2) parse link kandidat policy
		seen := map[string]bool{}
		type cand struct {
			href string
			skor int
		}
		var cands []cand
		for _, m := range reHref.FindAllStringSubmatch(string(homeBody), -1) {
			abs := absolutize(homeFinal, m[1])
			if abs == "" || seen[abs] || abs == homeFinal {
				continue
			}
			seen[abs] = true
			if s := scoreLink(abs); s > 0 {
				cands = append(cands, cand{abs, s})
			}
		}
		// sort desc skor, ambil max 2
		for i := 0; i < len(cands); i++ {
			for j := i + 1; j < len(cands); j++ {
				if cands[j].skor > cands[i].skor {
					cands[i], cands[j] = cands[j], cands[i]
				}
			}
		}
		nCand := len(cands)
		if nCand > 2 {
			nCand = 2
		}
		fmt.Printf("JID=%d|%s|home=200|kandidat=%d\n", t.jid, t.name, len(cands))

		// 3) GET max 2 halaman policy → deteksi blok fee
		for i := 0; i < nCand; i++ {
			if gets >= *pagu {
				break
			}
			pBody, pStatus, pFinal, perr := c.GetPlain(cands[i].href, homeFinal)
			gets++
			if perr != nil || pStatus != 200 {
				fmt.Printf("  POLICY[%d]|status=%d|err=%v|url=%s\n", i, pStatus, perr, cands[i].href)
				continue
			}
			text := stripHTML(string(pBody))
			// deteksi blok fee: cari semua match keyword, ambil ±200 char snippet
			matches := reFeeKw.FindAllStringIndex(text, -1)
			feeFound := len(matches) > 0
			var snippets []string
			for _, loc := range matches {
				if len(snippets) >= 3 {
					break // batasi snippet utk probe
				}
				start := loc[0] - 200
				if start < 0 {
					start = 0
				}
				end := loc[1] + 200
				if end > len(text) {
					end = len(text)
				}
				snippets = append(snippets, text[start:end])
			}
			// ekstraksi nominal + free dalam keseluruhan teks
			noms := reNom.FindAllString(text, -1)
			free := reFree.MatchString(text)
			fmt.Printf("  POLICY[%d]|status=200|url=%s|fee_block=%v|kw_matches=%d|free=%v|nominals=%v\n",
				i, pFinal, feeFound, len(matches), free, noms)
			for si, sn := range snippets {
				if len(sn) > 300 {
					sn = sn[:300]
				}
				fmt.Printf("    SNIPPET[%d]: %s\n", si, sn)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "SELESAI: GET=%d solves=%d\n", gets, c.Solves())
}
