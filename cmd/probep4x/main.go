// Program probep4x: silang per-(vol,issue) Crossref ↔ TOC (Q2 approve user
// 13 Okt — bukti utk jalur hybrid Q1a). Hemat GET: (a) Crossref works utk
// 10 jurnal P4 yg 200 (count artikel per vol/issue dari items), (b) GET ulang
// 10 TOC P4 yg volno-teks gagal parse (j16/j783/j3539/j7933/j900 — repair
// parser), (c) 7 jurnal dgn volno valid pakai TOC counts dari log probep4b
// (0 GET). Output per pasang: TOC count vs Crossref count + delta. Probe
// lanjutan; BUKAN pipeline.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// crossrefTargets = 10 jurnal P4 dgn Crossref 200 (679/4748 404 — dilewati).
var crossrefTargets = []struct {
	jid  int64
	issn string
}{
	{16, "08520682"}, {94, "2407814X"}, {107, "24771880"}, {185, "25029363"},
	{783, "08537291"}, {900, "2302299X"}, {3539, "22528822"}, {4644, "25496905"},
	{6126, "24606146"}, {6672, "26148757"}, {7933, "25409573"}, {9792, "27215032"},
}

// tocRepair = TOC P4 dgn volno-teks gagal → GET ulang utk parse lebih baik
// (+ j900 toc1=0 artikel — dicek ulang).
var tocRepair = []struct {
	jid int64
	u   string
}{
	{16, "https://journals.ums.ac.id/index.php/fg/issue/view/1400"},
	{16, "https://journals.ums.ac.id/index.php/fg/issue/view/1338"},
	{783, "https://ejournal.undip.ac.id:443/index.php/ijms/issue/view/4123"},
	{783, "https://ejournal.undip.ac.id:443/index.php/ijms/issue/view/4068"},
	{3539, "https://ijere.iaescore.com/index.php/IJERE/issue/view/594"},
	{3539, "https://ijere.iaescore.com/index.php/IJERE/issue/view/593"},
	{7933, "https://journal.ugm.ac.id/jtbb/issue/view/5518"},
	{7933, "https://journal.ugm.ac.id/jtbb/issue/view/5456"},
	{900, "https://journal.unhas.ac.id/index.php/wallacea/issue/view/1867"},
	{900, "https://journal.unhas.ac.id/index.php/wallacea/issue/view/1773"},
}

// tocLog = (vol,issue)+count TOC valid dari log probep4b (0 GET).
var tocLog = []struct {
	jid     int64
	vol, no string
	n       int
}{
	{94, "12", "1", 10}, {94, "11", "2", 11},
	{107, "11", "2", 16}, {107, "11", "1", 15},
	{185, "11", "1", 8}, {185, "10", "2", 9},
	{6126, "12", "3", 12}, {6126, "12", "2", 13},
	{6672, "9", "1", 6}, {6672, "8", "2", 5},
	{9792, "7", "2", 10}, {9792, "7", "1", 10},
	{4644, "10", "2", 6}, {4644, "10", "1", 7},
}

var (
	reVolNo = regexp.MustCompile(`(?i)Vol\.?\s*(\d+)[^\d]{0,12}No\.?\s*(\d+)`)
	reArt   = regexp.MustCompile(`(?i)article/view/(\d+)`)
	reTag   = regexp.MustCompile(`<[^>]+>`)
	reSpace = regexp.MustCompile(`\s+`)
)

func main() {
	flag.Parse()
	fmt.Println("P4x silang per-(vol,issue) Crossref ↔ TOC — budget 20 GET")

	// 1) Crossref per jurnal → map count (vol/issue) → GET.
	type viCount map[string]int
	byJID := map[int64]viCount{}
	gets := 0
	for _, t := range crossrefTargets {
		m, err := crossrefVI(t.issn)
		gets++
		if err != nil {
			fmt.Printf("JID=%d|crossref_err=%v\n", t.jid, err)
			continue
		}
		byJID[t.jid] = m
		fmt.Printf("JID=%d|crossref_vi_pairs=%d\n", t.jid, len(m))
		time.Sleep(time.Second) // polite pool
	}

	// 2) TOC repair (GET) → volno + count.
	type pasang struct {
		vol, no string
		tocN    int
	}
	byJIDTOC := map[int64][]pasang{}
	for _, tr := range tocRepair {
		vol, no, n, err := tocParse(tr.u)
		gets++
		if err != nil {
			fmt.Printf("JID=%d|toc_err=%v|url=%s\n", tr.jid, err, tr.u)
			continue
		}
		byJIDTOC[tr.jid] = append(byJIDTOC[tr.jid], pasang{vol, no, n})
		fmt.Printf("JID=%d|toc_repair|vol=%s|no=%s|articles=%d|url=%s\n", tr.jid, vol, no, n, tr.u)
		time.Sleep(time.Second)
	}
	// 3) TOC log (0 GET) → gabung.
	for _, tl := range tocLog {
		byJIDTOC[tl.jid] = append(byJIDTOC[tl.jid], pasang{tl.vol, tl.no, tl.n})
	}

	// 4) Silang: TOC count vs Crossref count per (vol,issue).
	fmt.Println("=== SILANG (toc vs crossref) ===")
	ok, beda := 0, 0
	for jid, tocs := range byJIDTOC {
		ci := byJID[jid]
		for _, p := range tocs {
			key := p.vol + "/" + p.no
			cv, ada := ci[key]
			if !ada {
				// coba key polos "vol/no" vs kadang issue="1" vs "" — fallback
				// cari prefix match vol.
				cv, ada = cariVI(ci, p.vol, p.no)
			}
			status := "ADA"
			if !ada {
				status = "CROSSREF_TIDAK_ADA"
				beda++
			} else if cv == p.tocN {
				status = "COCOK"
				ok++
			} else {
				status = "DELTA"
				beda++
			}
			fmt.Printf("JID=%d|vol=%s|no=%s|toc=%d|crossref=%d|ada=%v|%s\n",
				jid, p.vol, p.no, p.tocN, cv, ada, status)
		}
	}
	fmt.Printf("SELESAI: GET=%d | cocok=%d | beda=%d\n", gets, ok, beda)
}

// cariVI = cari key dgn vol "N" + issue cocok (atau prefix "N/" bila issue
// format beda — OJS "1" vs Crossref "1").
func cariVI(ci map[string]int, vol, no string) (int, bool) {
	if v, ok := ci[vol+"/"+no]; ok {
		return v, true
	}
	for k, v := range ci {
		if strings.HasPrefix(k, vol+"/") && (strings.TrimPrefix(k, vol+"/") == no || strings.Contains(k, no)) {
			return v, true
		}
	}
	return 0, false
}

// crossrefVI = GET works rows=1000 → count per (volume,issue).
func crossrefVI(issn string) (map[string]int, error) {
	u := fmt.Sprintf("https://api.crossref.org/journals/%s/works?rows=1000&select=volume,issue&mailto=gerry.23164%%40mhs.unesa.ac.id", issn)
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Message struct {
			Items []struct {
				Volume any `json:"volume"`
				Issue  any `json:"issue"`
			} `json:"items"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, it := range payload.Message.Items {
		k := strAny(it.Volume) + "/" + strAny(it.Issue)
		out[k]++
	}
	return out, nil
}

// tocParse = GET TOC → vol/no (regex longgar: title/h1) + jumlah artikel unik.
func tocParse(u string) (vol, no string, n int, err error) {
	resp, err := http.Get(u)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		err = fmt.Errorf("status %d", resp.StatusCode)
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	s := string(body)
	// vol/no: cari pola Vol..No.. pada judul/h1/h2 dulu, fallback seluruh body.
	for _, pat := range []string{
		`(?is)<title>(.*?)</title>`,
		`(?is)<h1[^>]*>(.*?)</h1>`,
		`(?is)<h2[^>]*>(.*?)</h2>`,
	} {
		if m := reVolNo.FindStringSubmatch(stripHTML(extract(pat, s))); m != nil {
			vol, no = m[1], m[2]
			break
		}
	}
	if vol == "" {
		if m := reVolNo.FindStringSubmatch(stripHTML(s)); m != nil {
			vol, no = m[1], m[2]
		}
	}
	seen := map[string]bool{}
	for _, m := range reArt.FindAllStringSubmatch(s, -1) {
		seen[m[1]] = true
	}
	n = len(seen)
	if vol == "" {
		err = fmt.Errorf("volno tidak ter-parse (articles=%d)", n)
	}
	return
}

func extract(pat, s string) string {
	re := regexp.MustCompile(pat)
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func strAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, e := range x {
			parts = append(parts, strAny(e))
		}
		return strings.Join(parts, ".")
	}
	return ""
}

func stripHTML(s string) string {
	s = reTag.ReplaceAllString(s, " ")
	return strings.TrimSpace(reSpace.ReplaceAllString(s, " "))
}
