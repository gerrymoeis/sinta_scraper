// Program probep1: probe P1 Fase 3 (doc 42 §4/§7) — GET penuh per URL kandidat
// via garuda.Client.GetPlain (redirect otomatis + status + final_url), cetak
// ringkasan per baris (label, status, final, redirect?, host-change?, ukuran).
// Alat diagnosis sekali pakai; BUKAN bagian dari pipeline (-stages).
package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"
)

// target = satu kasus uji P1: journal id, peran URL dalam kandidat, URL asli.
type target struct {
	jid  int
	role string // sinta | garuda | not_found | waf | down
	url  string
}

// Sampel P1 (doc 42 §4): http:// (5) · beda-host sinta/garuda (4 jurnal) ·
// org↔com (1) · not_found simetri (3) · known-WAF (2) · known-down (2) = 22.
var targets = []target{
	{16, "sinta", "http://journals.ums.ac.id/index.php/fg/issue/archive"},
	{19, "sinta", "http://jurnaliainpontianak.or.id/index.php/alalbab/index"},
	{26, "sinta", "http://journal.uii.ac.id/Millah"},
	{79, "sinta", "http://shirkah.or.id/new-ojs/index.php/home"},
	{88, "sinta", "http://industria.ub.ac.id"},

	{94, "sinta", "https://agraris.umy.ac.id/index.php/agraris"},
	{94, "garuda", "http://journal.umy.ac.id/index.php/ag"},
	{668, "sinta", "http://ijims.iainsalatiga.ac.id"},
	{668, "garuda", "http://e-journal.iainsalatiga.ac.id/index.php/ijims"},
	{681, "sinta", "http://journal.portalgaruda.org/index.php/EEI"},
	{681, "garuda", "http://beei.org/index.php/EEI"},
	{817, "sinta", "http://journal.stainkudus.ac.id/index.php/Addin"},
	{817, "garuda", "https://journal.iainkudus.ac.id/index.php/Addin"},

	{1626, "sinta", "http://www.bjoaonline.com/"},
	{1626, "garuda", "https://bjoa.balijournals.org/index.php/bjoa"},

	{673, "not_found", "http://www.journal.teflin.org"},
	{687, "not_found", "http://criticalcareshock.org/"},
	{948, "not_found", "http://inabj.org/"},

	{4214, "waf", "http://joiv.org/index.php/joiv"},
	{911, "waf", "http://jtrolis.ub.ac.id/index.php/jtrolis/index"},

	{1623, "down", "http://ajstd.org/index.php/ajstd/index"},
	{931, "down", "http://www.jurnalet.com"},
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

func main() {
	pagu := flag.Int("pagu", 25, "pagu GET (lampaui = berhenti)")
	flag.Parse()

	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)"
	c := garuda.NewClient("probep1", ua, time.Second, 3*time.Second)

	fmt.Printf("P1 probe: %d target, pagu %d GET\n", len(targets), *pagu)
	fmt.Println("jid|role|status|redirect|host_change|bytes|error|start|final")

	gets := 0
	ok, redir, hostchg, gagal := 0, 0, 0, 0
	for _, t := range targets {
		if gets >= *pagu {
			fmt.Fprintf(os.Stderr, "PAGU %d tercapai — %d target tersisa dilewati\n", *pagu, len(targets)-gets)
			break
		}
		body, status, final, err := c.GetPlain(t.url, "")
		gets++

		redirect := final != "" && final != t.url
		hostChange := final != "" && hostOf(final) != hostOf(t.url)
		nbytes := 0
		if body != nil {
			nbytes = len(body)
		}
		errStr := ""
		if err != nil {
			errStr = err.Error()
			if len(errStr) > 80 {
				errStr = errStr[:80]
			}
			gagal++
		} else {
			ok++
			if redirect {
				redir++
			}
			if hostChange {
				hostchg++
			}
		}
		fmt.Printf("%d|%s|%d|%v|%v|%d|%s|%s|%s\n",
			t.jid, t.role, status, redirect, hostChange, nbytes, errStr, t.url, final)
	}
	fmt.Fprintf(os.Stderr, "SELESAI: GET=%d ok=%d redirect=%d host_change=%d gagal=%d solves=%d\n",
		gets, ok, redir, hostchg, gagal, c.Solves())
}
