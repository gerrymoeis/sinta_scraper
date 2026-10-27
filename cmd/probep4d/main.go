// Program probep4d: date-probe utk K6 volume (approve user 14 Okt — menutup
// gap fakta "date-parse rate belum diukur" utk anchor & D3; +6 GET = pagu P4
// 120 pas). 6 TOC campuran theme → cetak kandidat tanggal: meta citation/DC,
// teks "(Month Year)", <time datetime>. Alat diagnosis; BUKAN pipeline.
package main

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var urls = []struct {
	jid int64
	u   string
}{
	{16, "https://journals.ums.ac.id/index.php/fg/issue/view/1400"},
	{94, "https://agraris.umy.ac.id/index.php/agraris/issue/view/23"},
	{9792, "https://e-journal.iainptk.ac.id/index.php/jil/issue/view/262"},
	{4644, "https://tasfiyah.journal.unida.gontor.ac.id/index.php/tasfiyah/issue/view/4"},
	{7933, "https://journal.ugm.ac.id/jtbb/issue/view/5518"},
	{3539, "https://ijere.iaescore.com/index.php/IJERE/issue/view/594"},
}

var (
	reMetaCitation = regexp.MustCompile(`(?i)<meta[^>]+name="citation_(?:date|publication_date)"[^>]+content="([^"]+)"`)
	reMetaDC       = regexp.MustCompile(`(?i)<meta[^>]+name="DC\.Date"[^>]+content="([^"]+)"`)
	reMetaCTM      = regexp.MustCompile(`(?i)<meta[^>]+name="cstr_[^"]*date[^"]*"[^>]+content="([^"]+)"`)
	reTimeTag      = regexp.MustCompile(`(?i)<time[^>]+datetime="([^"]+)"`)
	reParenMonth   = regexp.MustCompile(`(?i)\((January|February|March|April|May|June|July|August|September|October|November|December)[^)]{0,20}(20\d\d)\)`)
	reTag          = regexp.MustCompile(`<[^>]+>`)
	reSpace        = regexp.MustCompile(`\s+`)
)

func main() {
	fmt.Println("P4d date-probe: 6 TOC — kandidat tanggal (meta/teks/time)")
	metak, teksk, timek, n := 0, 0, 0, 0
	for _, t := range urls {
		body, status, err := get(t.u)
		if err != nil || status != 200 {
			fmt.Printf("JID=%d|status=%d|err=%v|url=%s\n", t.jid, status, err, t.u)
			continue
		}
		n++
		mc := first(reMetaCitation, body)
		if mc == "" {
			mc = first(reMetaDC, body)
		}
		if mc == "" {
			mc = first(reMetaCTM, body)
		}
		tt := first(reTimeTag, body)
		text := stripHTML(body)
		pm := ""
		if m := reParenMonth.FindStringSubmatch(text); m != nil {
			pm = m[0]
		}
		fmt.Printf("JID=%d|meta=%q|teks_paren=%q|time=%q\n", t.jid, mc, pm, tt)
		if mc != "" {
			metak++
		}
		if pm != "" {
			teksk++
		}
		if tt != "" {
			timek++
		}
		time.Sleep(time.Second)
	}
	fmt.Printf("SELESAI: GET=%d | meta_ada=%d/%d | teks_paren_ada=%d/%d | time_ada=%d/%d\n",
		len(urls), metak, n, teksk, n, timek, n)
}

func get(u string) (string, int, error) {
	resp, err := http.Get(u)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), resp.StatusCode, err
}

func first(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func stripHTML(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}
