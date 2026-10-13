package ojs

import (
	"net/url"
	"sort"
	"strings"
)

// ==== resolve.go — pemilihan canonical URL (doc 42 §7.1 LOCKED) ====
//
// 8 aturan P1: (1) kandidat = semua source (journal_urls + garuda_home_url),
// GET tiap kandidat — (2) layak = 200 + body ≥1KB + bukan host pihak-ketiga —
// (3) http→https same-host = normal bukan stale — (4) host-change related =
// canonical boleh + flag REDIRECT_HOST_CHANGE — (5) semua gagal = flag jujur
// (WAF_BLOCKED/UNREACHABLE/SERVER_DOWN), jangan blokir state — (6) STALE_URL =
// riwayat kandidat kalah (final≠asal atau non-200) — (7) retry/solve = driver,
// bukan parser ini — (8) simetri: jalan utk not_found (tanpa Garuda).
// Bukti: j681 stub 24B · j94 sinta menang vs garuda 522 · j1626 → lww.com.

// MinBodyCanonical = ambang body 1 KB (§7.1 butir 2 — tolak stub; bukti
// j681 "journal.portalgaruda.org" = 24B polos).
const MinBodyCanonical = 1024

// Candidate = satu kandidat URL + hasil GET-nya (input Resolve — diisi driver
// dari journal_urls + garuda_home_url, hasil GetPlain/retry).
type Candidate struct {
	URL      string // URL asal (request)
	Source   string // sinta|garuda|doaj|openalex (label log/audit)
	Status   int    // 0 = tak ada respons (network/TLS/DNS)
	FinalURL string // "" = tanpa redirect
	BodyLen  int    // panjang body final (utk ambang 1 KB)
}

// ResolveOutcome = keputusan resolusi utk SATU jurnal (§7.1).
type ResolveOutcome struct {
	CanonicalURL string   // "" = tak ada layak (driver: flag jujur, tanpa blokir)
	Flags        []string // flag fase utk phase2_progress (sorted, deterministik)
	Losers       []string // URL kandidat kalah (riwayat is_canonical=0)
}

// Resolve memilih canonical dari kandidat hasil GET (aturan §7.1).
// Pemenang = layak dgn body terbesar (proxy kualitas — stub sudah tersaring
// ambang); tie-break URL asc (deterministik). Tanpa IO; retry 1× transien =
// urusan driver (butir 7).
func Resolve(cands []Candidate) ResolveOutcome {
	var (
		out        ResolveOutcome
		flagSet    = map[string]bool{}
		layak      []Candidate
		hostChange bool
	)

	for _, c := range cands {
		uAsal, err := url.Parse(c.URL)
		if err != nil || uAsal.Host == "" {
			continue // kandidat rusak → lewati (defensif; db berisi URL valid)
		}
		hostAsal := strings.ToLower(uAsal.Hostname())

		final := c.FinalURL
		if final == "" {
			final = c.URL
		}
		hostFinal := hostAsal // default: tanpa redirect = host sama
		if uFinal, err := url.Parse(final); err == nil && uFinal.Host != "" {
			hostFinal = strings.ToLower(uFinal.Hostname())
		}

		// (4) Host-change related → flag jurnal penuh; (2) unrelated =
		// pihak ketiga (j1626→lww.com) → flag + tak layak canonical.
		gantiHost := hostFinal != hostAsal
		terkait := !gantiHost || relatedHost(hostAsal, hostFinal)
		if gantiHost {
			if terkait {
				hostChange = true
			} else {
				flagSet[FlagRedirectOffsite] = true
			}
		}

		// (2) Layak = 200 + body ≥1KB + bukan offsite.
		if c.Status == 200 && c.BodyLen >= MinBodyCanonical && terkait {
			layak = append(layak, c)
			continue
		}

		// Bukan pemenang → tetap riwayat Losers (butir 6).
		out.Losers = append(out.Losers, c.URL)
		// (6) Stale = final≠asal ATAU non-200 (pemenang tak pernah dinilai
		// di sini — butir 3: upgrade skema pada pemenang bukan stale).
		if c.Status != 200 || c.FinalURL != c.URL {
			flagSet[FlagStaleURL] = true
		}
	}

	if hostChange {
		flagSet[FlagRedirectHostChange] = true
	}

	// (5) Semua kandidat gagal → flag jujur (bisa coexist; jangan blokir).
	if len(layak) == 0 {
		for _, c := range cands {
			switch {
			case c.Status == 403:
				flagSet[FlagWAFBlocked] = true
			case c.Status == 0:
				flagSet[FlagUnreachable] = true
			case c.Status >= 500:
				flagSet[FlagServerDown] = true
			}
		}
	}

	// Pemenang: body terbesar, tie URL asc (deterministik).
	if len(layak) > 0 {
		sort.SliceStable(layak, func(i, j int) bool {
			if layak[i].BodyLen != layak[j].BodyLen {
				return layak[i].BodyLen > layak[j].BodyLen
			}
			return layak[i].URL < layak[j].URL
		})
		out.CanonicalURL = layak[0].URL
	}

	for f := range flagSet {
		out.Flags = append(out.Flags, f)
	}
	sort.Strings(out.Flags)
	return out
}

// relatedHost = host "berhubungan" (bukan pihak ketiga): share label ≥5 huruf
// (subdomain struktural "journal" / brand) ATAU share registrable domain
// (2 label terakhir). Bukti P1: j817 iainkudus→uinsuku (share "journal") &
// j687 org→com (share brand) = related; j1626 bjoaonline→lww = offsite.
func relatedHost(a, b string) bool {
	la, lb := strings.Split(a, "."), strings.Split(b, ".")
	label := map[string]bool{}
	for _, l := range la {
		if len(l) >= 5 {
			label[l] = true
		}
	}
	for _, l := range lb {
		if len(l) >= 5 && label[l] {
			return true
		}
	}
	if len(la) >= 2 && len(lb) >= 2 {
		ra := strings.Join(la[len(la)-2:], ".")
		rb := strings.Join(lb[len(lb)-2:], ".")
		if ra == rb {
			return true
		}
	}
	return false
}
