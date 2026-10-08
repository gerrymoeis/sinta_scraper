package garuda

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// E7g (doc 30 §14.6 butir 9 — pagu APPROVED user 7 Okt 2026): verifikasi
// hidup/matot URL OJS 261 jurnal + rantai repair kandidat (doc 17 §4.2:
// verify → Garuda Home Page → OpenAlex homepage_url → DOAJ ref.journal →
// manual) + evaluasi robots.txt per host (doc 11 Bagian 7). Semua fungsi di
// sini murni (tanpa jaringan/tanpa db) supaya teruji offline.

// ---- klasifikasi status GET ------------------------------------------------

// Klasifikasi E7g — dipakai keputusan "mati → rantai kandidat".
const (
	ClsOK      = "ok"          // 200
	Cls404     = "404"         // hilang
	Cls410     = "410"         // gone
	ClsWAF     = "waf"         // 403/429 — BUKAN mati (approve 7 Okt: WAF ≠ mati)
	ClsServer  = "server"      // 5xx — transien, layak retry
	ClsDNS     = "dns"         // resolusi gagal
	ClsTimeout = "timeout"     // putus waktu
	ClsUnreach = "unreachable" // tanpa respons (refused/dst./loop redirect)
	ClsLain    = "lain"        // status lain (400/451/3xx sisa) — tak mati, tak ok
)

// E7gMati = klasifikasi yang MEMICU rantai kandidat (dan overwrite bila
// kandidat 200). 403/429 WAF, 5xx, dan "lain" sengaja TIDAK mati (approve
// user 7 Okt 2026).
func E7gMati(cls string) bool {
	switch cls {
	case Cls404, Cls410, ClsDNS, ClsTimeout, ClsUnreach:
		return true
	}
	return false
}

// E7gRetry = klasifikasi yang di-retry 1× (transien; DNS deterministik —
// tak dibuang pagu utk retry yang pasti gagal).
func E7gRetry(cls string) bool {
	switch cls {
	case ClsServer, ClsTimeout, ClsUnreach:
		return true
	}
	return false
}

// KlasifikasiE7g mengelompokkan hasil GET. status > 0 = ada respons HTTP
// (status menang, walau baca body gagal — server terbukti menjawab);
// status 0 = tanpa respons → bedah error jaringan.
func KlasifikasiE7g(status int, err error) string {
	switch {
	case status == 200:
		return ClsOK
	case status == 404:
		return Cls404
	case status == 410:
		return Cls410
	case status == 403 || status == 429:
		return ClsWAF
	case status >= 500:
		return ClsServer
	case status > 0:
		return ClsLain
	case err == nil:
		return ClsLain // tak mungkin (GET selalu err dgn status 0) — jujur
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ClsDNS
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ClsTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ClsTimeout
	}
	return ClsUnreach
}

// ---- robots.txt ------------------------------------------------------------

// robotsRule = satu baris Allow/Disallow dgn pola sudah dikompilasi.
type robotsRule struct {
	allow bool
	pat   string // pola asli (utk pemilihan pola terpanjang)
	re    *regexp.Regexp
}

// robotsGroup = blok "User-agent:" + aturan-aturannya.
type robotsGroup struct {
	agents []string // huruf kecil
	rules  []robotsRule
}

// Robots = isi robots.txt terparsing (bila file tak ada / gagal → nil = tanpa
// pembatasan; keputusan konservatif utk GAGAL ada di driver, bukan di sini).
type Robots struct {
	groups []robotsGroup
}

var robotsComment = regexp.MustCompile(`#.*$`)

// ParseRobots mem-parses isi robots.txt. Parser lokal (murni, teruji):
// blok User-agent → aturan Allow/Disallow berikutnya; direktif lain
// (Crawl-delay/Sitemap/Host) diabaikan; `*` wildcard & `$` akhir pola
// (spesifikasi Google/RFC 9309).
func ParseRobots(body []byte) *Robots {
	r := &Robots{}
	var cur *robotsGroup
	haveRules := false
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(robotsComment.ReplaceAllString(raw, ""))
		if line == "" {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		field := strings.ToLower(strings.TrimSpace(line[:idx]))
		value := strings.TrimSpace(line[idx+1:])
		switch field {
		case "user-agent":
			if haveRules || cur == nil {
				// blok baru (aturan sudah terkumpul = ganti kelompok)
				r.groups = append(r.groups, robotsGroup{})
				cur = &r.groups[len(r.groups)-1]
				haveRules = false
			}
			if value != "" {
				cur.agents = append(cur.agents, strings.ToLower(value))
			}
		case "allow", "disallow":
			if cur == nil {
				continue // aturan tanpa blok agent — abaikan (bukan parser
				// sempurna; cukup utk robots nyata — lihat test)
			}
			if value == "" {
				// "Disallow:" kosong = izinkan semua → tanpa aturan
				haveRules = true
				continue
			}
			re, ok := compileRobotsPola(value)
			if !ok {
				continue
			}
			cur.rules = append(cur.rules, robotsRule{
				allow: field == "allow", pat: value, re: re,
			})
			haveRules = true
		}
	}
	if len(r.groups) == 0 {
		return nil
	}
	return r
}

// compileRobotsPola = pola robots → regex terjaminkan: `*` → `.*`, `$` akhir
// = harus berakhir persis, selain itu cocok sebagai prefiks.
func compileRobotsPola(pola string) (*regexp.Regexp, bool) {
	akhir := strings.HasSuffix(pola, "$")
	if akhir {
		pola = pola[:len(pola)-1]
	}
	re := strings.ReplaceAll(regexp.QuoteMeta(pola), `\*`, `.*`)
	if akhir {
		re = "^" + re + "$"
	} else {
		re = "^" + re
	}
	cre, err := regexp.Compile(re)
	if err != nil {
		return nil, false
	}
	return cre, true
}

// RobotsBoleh = evaluasi uri (path?query) thd robots.txt utk UA kita.
// Ketentuan: pilih kelompok dgn agent cocok (substring, huruf kecil) —
// lebih spesifik dulu, `*` hanya fallback; bila tak ada kelompok cocok →
// tanpa pembatasan. Dari kelompok terpilih: pola terpanjang menang, seri →
// Allow menang; pola kosong tak dihitung.
func RobotsBoleh(r *Robots, uri, ua string) bool {
	if r == nil || len(r.groups) == 0 {
		return true
	}
	uaL := strings.ToLower(ua)
	var picked *robotsGroup
	for i := range r.groups {
		g := &r.groups[i]
		for _, a := range g.agents {
			if a == "*" {
				continue
			}
			if strings.Contains(uaL, a) {
				picked = g
			}
		}
		if picked != nil {
			break
		}
	}
	if picked == nil {
		for i := range r.groups {
			g := &r.groups[i]
			for _, a := range g.agents {
				if a == "*" {
					picked = g
				}
			}
			if picked != nil {
				break
			}
		}
	}
	if picked == nil {
		return true // hanya kelompok agent spesifik lain → bukan kita
	}
	terbaik := ""
	boleh := true
	for _, rule := range picked.rules {
		if !rule.re.MatchString(uri) {
			continue
		}
		if rule.pat > terbaik { // pola terpanjang = paling spesifik
			terbaik = rule.pat
			boleh = rule.allow
		} else if rule.pat == terbaik && rule.allow {
			boleh = true // seri → Allow menang
		}
	}
	return boleh
}

// RobotsOrigin = origin (scheme://host[:port]) dari URL — kunci host robots.
// "" bila URL tak valid/tanpa scheme.
func RobotsOrigin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// RobotsURI = path?query utk evaluasi robots ("/" utk URL tanpa path).
func RobotsURI(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.RequestURI()
}

// ---- OpenAlex (rantai kandidat 2) -----------------------------------------

// OpenAlexHomepage = homepage_url dari respons
// GET https://api.openalex.org/sources/issn:{hyphen} (objek sumber tunggal —
// doc 17 §4.2). Selalu diverifikasi GET sebelum dipakai (OpenAlex terbukti
// basi — doc 17 Bagian 5).
func OpenAlexHomepage(body []byte) (string, error) {
	var src struct {
		HomepageURL string `json:"homepage_url"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &src); err != nil {
		return "", fmt.Errorf("parse OpenAlex: %w", err)
	}
	if src.Error != "" {
		return "", fmt.Errorf("OpenAlex: %s", src.Error)
	}
	return strings.TrimSpace(src.HomepageURL), nil
}

// ---- DOAJ ref.journal (rantai kandidat 4) ---------------------------------

// DoajRefJournal = bibjson.ref.journal (URL situs jurnal) dari respons
// GET https://doaj.org/api/search/journals/{key} — baca dari fixture offline
// (e6/e7d/e7f-doaj-*.json), nol GET.
func DoajRefJournal(body []byte) (string, error) {
	var r struct {
		Results []struct {
			Bibjson struct {
				Ref struct {
					Journal string `json:"journal"`
				} `json:"ref"`
			} `json:"bibjson"`
		} `json:"results"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("parse DOAJ ref: %w", err)
	}
	if r.Error != "" {
		return "", fmt.Errorf("DOAJ: %s", r.Error)
	}
	for _, res := range r.Results {
		if j := strings.TrimSpace(res.Bibjson.Ref.Journal); j != "" {
			return j, nil
		}
	}
	return "", nil
}
