// Program probep2: probe P2 Fase 3 (doc 42 §4/§7.2) — deteksi platform
// berbasis KONTEN: GET halaman via garuda.Client.GetPlain, ekstrak marker
// (citation_*/PKP/OJS · wp-json/WordPress), klasifikasi ojs|wordpress|custom|
// static|unknown + detection_evidence. Alat diagnosis; BUKAN pipeline.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"
)

type target struct {
	jid  int
	role string // p1_200 | waf | down | extra
	url  string
}

// Sampel P2 (~20): final_url 200 hasil P1 (9) + WAF (5) + down (3) + extra (3)
// = 20 target. WAF/down = evidence negatif (harus terklasifikasi unknown/tanpa
// marker — memvalidasi marker tak false-positive di halaman error).
var targets = []target{
	{16, "p1_200", "https://journals.ums.ac.id/index.php/fg/issue/archive"},
	{19, "p1_200", "https://jurnaliainpontianak.or.id/index.php/alalbab/index"},
	{26, "p1_200", "https://journal.uii.ac.id/Millah"},
	{94, "p1_200", "https://agraris.umy.ac.id/index.php/agraris"},
	{681, "p1_200", "https://beei.org/index.php/EEI"},
	{817, "p1_200", "https://journal.uinsuku.ac.id/index.php/Addin"},
	{673, "p1_200", "http://www.journal.teflin.org/index.php/journal"},
	{687, "p1_200", "https://criticalcareshock.com/"},
	{948, "p1_200", "https://inabj.org/index.php/ibj"},

	{88, "waf", "https://industria.ub.ac.id/"},
	{4214, "waf", "https://joiv.org/index.php/joiv"},
	{911, "waf", "https://jtrolis.ub.ac.id/index.php/jtrolis/index"},
	{668, "waf", "http://ijims.iainsalatiga.ac.id"},
	{1626, "waf", "https://bjoa.balijournals.org/index.php/bjoa"},

	{79, "down", "http://shirkah.or.id/new-ojs/index.php/home"},
	{931, "down", "https://www.jurnalet.com/"},
	{1623, "down", "http://ajstd.org/index.php/ajstd/index"},

	{3974, "extra", "https://e-journal.unair.ac.id/IJOSH/issue/archive"},
	{696, "extra", "http://ijaseit.insightsociety.org/"},
	{20, "extra", "https://journal.unismuh.ac.id/index.php/Otoritas/"},
}

// detectPlatform klasifikasi konten + evidence (jangan ditebak: unknown utk
// yang tak punya marker cukup — doc 42 §4 P2).
func detectPlatform(body []byte, status int) (platform string, evidence string) {
	if status != 200 || len(body) == 0 {
		return "unknown", fmt.Sprintf("status=%d body=%dB (tanpa konten)", status, len(body))
	}
	// case-insensitive search utk marker (HTML campur besar-kecil)
	low := strings.ToLower(string(body))
	has := func(m string) bool { return strings.Contains(low, m) }

	var ev []string
	ojs, wp := false, false

	// marker OJS/PKP
	if has("citation_") {
		ojs = true
		ev = append(ev, "meta citation_*")
	}
	if has("open journal systems") {
		ojs = true
		ev = append(ev, "teks Open Journal Systems")
	}
	if has("pkp") {
		ojs = true
		ev = append(ev, "pkp")
	}
	if has("/index.php/") {
		// path OJS umum, tapi BUKAN bukti cukup sendiri (custom bisa punya) —
		// catat sebagai petunjuk lemah, tak menentukan sendiri
		ev = append(ev, "path /index.php/ (lemah)")
	}

	// marker WordPress
	if has("wp-json") || has("wp-content") {
		wp = true
		ev = append(ev, "wp-json/wp-content")
	}
	if has(`generator" content="wordpress`) || has(`generator' content='wordpress`) {
		wp = true
		ev = append(ev, "generator WordPress")
	}

	switch {
	case ojs && !wp:
		return "ojs", strings.Join(ev, "; ")
	case wp && !ojs:
		return "wordpress", strings.Join(ev, "; ")
	case ojs && wp:
		// langka: WordPress + citation meta — jangan ditebak, unknown dgn catatan
		return "unknown", "campuran marker OJS+WP: " + strings.Join(ev, "; ")
	case len(ev) > 0 && has("<html"):
		return "custom", "HTML ada, tanpa marker OJS/WP; petunjuk: " + strings.Join(ev, "; ")
	case has("<html"):
		return "custom", "HTML ada, tanpa marker OJS/WP"
	case len(body) < 512:
		return "static", fmt.Sprintf("body kecil %dB, tanpa HTML marker", len(body))
	default:
		return "unknown", fmt.Sprintf("body %dB, tanpa marker", len(body))
	}
}

func main() {
	pagu := flag.Int("pagu", 25, "pagu GET")
	flag.Parse()

	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)"
	c := garuda.NewClient("probep2", ua, time.Second, 3*time.Second)

	fmt.Printf("P2 probe: %d target, pagu %d GET\n", len(targets), *pagu)
	fmt.Println("jid|role|status|bytes|platform|evidence|final")

	gets := 0
	count := map[string]int{}
	for _, t := range targets {
		if gets >= *pagu {
			fmt.Fprintf(os.Stderr, "PAGU %d tercapai — sisa dilewati\n", *pagu)
			break
		}
		body, status, final, err := c.GetPlain(t.url, "")
		gets++
		errStr := ""
		if err != nil {
			errStr = err.Error()
			if len(errStr) > 60 {
				errStr = errStr[:60]
			}
		}
		plat, ev := detectPlatform(body, status)
		if errStr != "" && plat == "unknown" {
			ev = ev + "; err: " + errStr
		}
		count[plat]++
		fmt.Printf("%d|%s|%d|%d|%s|%s|%s\n", t.jid, t.role, status, len(body), plat, ev, final)
	}
	fmt.Fprintf(os.Stderr, "SELESAI: GET=%d distribusi=%v solves=%d\n", gets, count, c.Solves())
}
