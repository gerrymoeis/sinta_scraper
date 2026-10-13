package ojs

import (
	"net/url"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// ==== policy.go — filter & skor kandidat halaman policy (doc 42 §7.3) ====
//
// Aturan P3 butir 1 (LOCKED): link dari home wajib (a) host sama dgn home
// (tolak eksternal — bukti noise: CSS bootstrapcdn lolos keyword substring),
// (b) path bukan gateway/plugin (feed — bukti WebFeedGatewayPlugin), (c) skor
// keyword word-boundary (bukan substring — bukti "apc" di "bootstrapcdn",
// "fee" di "webfeed" = TIDAK match dgn \b). Pagu P3 terbuang ~12 GET ke
// feed/CDN → filter ini wajib sebelum GET full-run (~40% GET dihemat).

// rePolicyKeyword = keyword kandidat policy, word-boundary (P3 butir 1c).
var rePolicyKeyword = regexp.MustCompile(`(?i)\b(fee|fees|apc|charge|charges)\b`)

// reAsset = ekstensi aset statis — bukan halaman policy (keamanan ekstra;
// PDF guideline TIDAK diblokir — path .pdf sah sbg kebijakan).
var reAsset = regexp.MustCompile(`(?i)\.(css|js|mjs|png|jpe?g|gif|svg|webp|ico|woff2?|ttf|ept?|map)(\?|#|$)`)

// reGateway = path feed/plugin OJS (WebFeedGatewayPlugin dsb).
var reGateway = regexp.MustCompile(`(?i)gateway/plugin`)

// PolicyLink = kandidat lolos filter (siap di-GET driver F3.3).
type PolicyLink struct {
	URL   string
	Score int // jumlah keyword match (desc) — tie: URL asc (deterministik)
}

// FindPolicyLinks memfilter href halaman home → kandidat policy terurut
// (aturan §7.3 butir 1). max ≤0 = tanpa batas (driver membatasi pagu GET).
// Ekstraksi href via html.Parse (std lib x/net — pola parser existing).
func FindPolicyLinks(homeURL string, body []byte, max int) []PolicyLink {
	base, err := url.Parse(homeURL)
	if err != nil || base.Host == "" {
		return nil
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil
	}

	seen := map[string]bool{}
	var out []PolicyLink
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Attr != nil {
			for _, a := range n.Attr {
				if a.Key != "href" {
					continue
				}
				link := filterPolicyHref(base, a.Val)
				if link == "" || seen[link] {
					continue
				}
				seen[link] = true
				out = append(out, PolicyLink{
					URL:   link,
					Score: len(rePolicyKeyword.FindAllString(policyPath(link), -1)),
				})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].URL < out[j].URL
	})
	// Score 0 = path tanpa keyword — tetap sah sbg kandidat "about" umum,
	// tapi diurutkan paling bawah; pembatasan GET = urusan pagu driver.
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// filterPolicyHref = resolvi href → absolut + lolos tolak host-eksternal /
// gateway / aset. "" = tolak. Upgrade skema (Opsi 1 approve 13 Okt 2026 —
// preseden P1 butir 3/E7gw/opsic: skema http→https SAME-HOST = transformasi
// deterministik, bukan tebakan): bila base (canonical final) https & href http
// same-host → tulis https. Bukti j663: home https memuat href http (konfigurasi
// site_url basi; port80 tutup by physics) padahal path https-nya hidup 200.
func filterPolicyHref(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") ||
		strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	abs := base.ResolveReference(u)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	if !strings.EqualFold(abs.Hostname(), base.Hostname()) {
		return "" // (a) host harus sama dgn home
	}
	if base.Scheme == "https" && abs.Scheme == "http" {
		abs.Scheme = "https" // upgrade skema same-host (Opsi 1)
	}
	path := abs.Path
	if reGateway.MatchString(path) || reAsset.MatchString(path) {
		return "" // (b) feed/gateway & aset statis
	}
	abs.Fragment = ""
	return abs.String()
}

func policyPath(rawurl string) string {
	if u, err := url.Parse(rawurl); err == nil {
		return u.Path + u.RawQuery
	}
	return rawurl
}

// ClassifyPolicyType = label tipe baris journal_policy dari URL (§11.2 —
// about|author_guideline|copyright|privacy|fee|other). Urutan: fee dulu
// ("author-fees" = fee, bukan guideline), lalu spesifik, lalu about.
func ClassifyPolicyType(rawurl string) string {
	p := strings.ToLower(policyPath(rawurl))
	switch {
	case rePolicyKeyword.MatchString(p):
		return "fee"
	case strings.Contains(p, "copyright"):
		return "copyright"
	case strings.Contains(p, "privacy"):
		return "privacy"
	case strings.Contains(p, "guideline") || strings.Contains(p, "instruction") ||
		strings.Contains(p, "author"):
		return "author_guideline"
	case strings.Contains(p, "about"):
		return "about"
	default:
		return "other"
	}
}
