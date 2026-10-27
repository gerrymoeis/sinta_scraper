package corpus

// ==== crossref.go — pelengkap Crossref pass 1 (doc 43 §5.3 butir 4 + §5.5
// rules lisensi — spec approve 14 Okt; fase C3) ====
//
// 1 GET per jurnal: /journals/{issn}/works?rows=1000&select=DOI,license,
// subject,abstract → index map[DOI] utk merge per artikel ber-DOI (license
// primer utk ber-DOI; subject/abstract = pelengkap). CATATAN C6/G10: field
// `free-to-read` TIDAK dipilih (route /journals/{issn}/works menolaknya —
// 400) — parser tetap tangkap bila dikirim server.
// ISSN kosong / status≠200 = skip jujur (OAI tetap primer — pola Gap-E
// volume: OJS tanpa ISSN bukan kegagalan).

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CrossrefItem = 1 baris works utk merge korpus.
type CrossrefItem struct {
	DOI         string
	LicenseURL  string   // license[].URL pertama (audit — jenis CC dkk)
	LicenseOpen bool     // license ada & start ≤ now → open (§5.5)
	Embargo     bool     // license ada tapi start > now → free_not_open
	FreeToRead  bool     // free-to-read aktif (tanpa license → free_not_open)
	Subject     []string // keyword pelengkap (chain TF-IDF bila author kosong)
	Abstract    string   // pelengkap bila OAI kosong (JATS XML — strip ringan)
}

// CrossrefWorks = hasil parse 1 response works (index per DOI).
type CrossrefWorks struct {
	Items map[string]CrossrefItem
	Rows  int // message.items-total (audit — cap rows=1000)
}

// ParseCrossrefWorks = parse body works?select=... Crossref (fungsi murni).
// Body bukan JSON / message hilang → error (caller kategori jujur).
func ParseCrossrefWorks(body []byte, now time.Time) (CrossrefWorks, error) {
	var env struct {
		Message struct {
			TotalItems int `json:"total-results"`
			Items      []struct {
				DOI      string `json:"DOI"`
				Abstract string `json:"abstract"`
				License  []struct {
					URL   string `json:"URL"`
					Start struct {
						DateTime string `json:"date-time"`
					} `json:"start"`
				} `json:"license"`
				FreeToRead *struct {
					StartDate string `json:"start-date"`
				} `json:"free-to-read"`
				Subject []string `json:"subject"`
			} `json:"items"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return CrossrefWorks{}, fmt.Errorf("crossref: body bukan JSON valid: %w", err)
	}
	out := CrossrefWorks{Items: map[string]CrossrefItem{}, Rows: env.Message.TotalItems}
	for _, it := range env.Message.Items {
		doi := strings.ToLower(strings.TrimSpace(it.DOI))
		if doi == "" {
			continue
		}
		ci := CrossrefItem{DOI: doi, Subject: it.Subject, Abstract: stripJATS(it.Abstract)}
		for _, lic := range it.License {
			if lic.URL == "" {
				continue
			}
			ci.LicenseURL = lic.URL
			if lic.Start.DateTime == "" {
				ci.LicenseOpen = true // tanpa start = aktif sejak awal (asumsi longgar — audit C6)
				break
			}
			t, err := time.Parse(time.RFC3339, lic.Start.DateTime)
			if err != nil {
				ci.LicenseOpen = true // start rusak — pakai license dgn catatan (jujur via LicenseURL)
				break
			}
			if t.After(now) {
				ci.Embargo = true // start > now → free_not_open (§5.5)
			} else {
				ci.LicenseOpen = true
			}
			break // license pertama ber-URL = keputusan (Crossref urut relevan dulu)
		}
		if it.FreeToRead != nil {
			ci.FreeToRead = true
		}
		out.Items[doi] = ci
	}
	return out, nil
}

// licenseFromCrossref = aturan §5.5 sumber Crossref (ber-DOI). Return
// (status, url, ok) — ok=false = "turun ke OAI dc:rights" (keduanya kosong).
func licenseFromCrossref(ci CrossrefItem) (status, url string, ok bool) {
	switch {
	case ci.LicenseOpen:
		return LicenseOpen, ci.LicenseURL, true
	case ci.Embargo:
		return LicenseFreeNotOA, ci.LicenseURL, true // embargo — gratis belum open-license
	case ci.FreeToRead:
		return LicenseFreeNotOA, "", true
	}
	return "", "", false
}

// stripJATS = tag <p>/<italic>/... dari abstract Crossref → teks polos
// (ringan — abstrak JATS Crossref formatnya sederhana).
func stripJATS(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	out := &strings.Builder{}
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			out.WriteRune(' ')
		case !inTag:
			out.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}
