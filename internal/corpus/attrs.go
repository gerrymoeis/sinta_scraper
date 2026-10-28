package corpus

// ==== attrs.go — 2 atribut C7 B-additive (doc 43 §6.x.7-8 — approve user
// 27 Okt 2026) ====
//
// Terpisah dari 4 kelas legacy (`license_status` = arsip):
//   - oa_status   = AKSES pembaca: free|paid|restricted|unknown.
//   - license_type = LISENSI yang benar2 tertulis: "CC BY-SA 4.0" | CC0 |
//     all_rights_reserved (HANYA eksplisit) | unknown (Not Stated).
//
// Prinsip D1 (plan §6.x.8): SEMUA fungsi di sini DETERMINISTIK 0-GET —
// derive dari bukti yang sudah ada (URL Crossref/OAI, teks rights, halaman
// policy cache). Probe jaringan utk oa_status = tahap D3, bukan di sini.
// Prinsip #1: absennya bukti ≠ bukti ARR; probe gagal = unknown (jangan nebak).

import (
	"regexp"
	"strings"

	"sinta-scraper/internal/storage"
)

// ---- license_type: derive dari bukti ----

// reCCLicURL / reCCZeroURL = URL resmi Creative Commons → tipe. `l` = jalur
// badge (i.creativecommons.org/l/by/4.0/88x31.png — fakta C7-a j828);
// legalcode & query ikut tertangkap versinya via group.
var (
	reCCLicURL  = regexp.MustCompile(`(?i)creativecommons\.org/(?:licenses|l)/([a-z0-9-]+)(?:/(\d+(?:\.\d+)*))?`)
	reCCZeroURL = regexp.MustCompile(`(?i)creativecommons\.org/(?:publicdomain/)?zero(?:/(\d+(?:\.\d+)*))?`)
	// reCCLabel = label kanonik utk mentransformasi match reCCWord jadi
	// tipe kanonik. Pemisah [ \t_-] (multi-spasi aman); urutan alternatif
	// terpanjang-dulu (nc-nd sebelum nc) — RE2 leftmost-first.
	reCCLabel = regexp.MustCompile(`(?i)^(?:cc[\s_-]*(by(?:[\s_-]*(?:nc[\s_-]*nd|nc[\s_-]*sa|nc|nd|sa))?|0|zero))(?:[\s_-]*(\d+(?:\.\d+)*))?$`)
)

// LicenseTypeFromURL → tipe CC kanonik ("CC BY-SA 4.0", "CC0 1.0") bila URL
// memuat jalur lisensi CC dgn tipe dikenal; "" bila bukan (URL hak cipta
// biasa / host lain — jangan dipaksa).
func LicenseTypeFromURL(u string) string {
	if m := reCCZeroURL.FindStringSubmatch(u); m != nil {
		return canonCC("zero", m[1])
	}
	if m := reCCLicURL.FindStringSubmatch(u); m != nil {
		return canonCC(m[1], m[2])
	}
	return ""
}

// LicenseTypeFromLabel = label teks ("CC BY-NC-SA 4.0", "cc by 4.0", "CC0")
// → kanonik; "" bila bukan label lisensi valid.
func LicenseTypeFromLabel(s string) string {
	m := reCCLabel.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ""
	}
	return canonCC(m[1], m[2])
}

// LicenseTypeFromText = cari tipe CC dalam teks kebijakan / OAI dc:rights:
// URL CC dulu (lebih spesifik) → kata "CC BY[-NC…] [ver]" (reCCWord — sudah
// dipakai PolicyHits). Ejaan penuh "Creative Commons Attribution …" TANPA
// URL/singkatan → "" (tak ada tipe eksplisit — jangan nebak).
func LicenseTypeFromText(text string) string {
	if t := LicenseTypeFromURL(text); t != "" {
		return t
	}
	if m := reCCWord.FindString(text); m != "" {
		return LicenseTypeFromLabel(m)
	}
	return ""
}

// LicenseTypeOf = turunkan license_type dari bukti YANG SUDAH ADA (0 GET):
// URL CC (Crossref license.URL / OAI dc:rights / cache policy) → teks CC →
// teks "All Rights Reserved" EKSPLISIT → unknown (Not Stated).
// Urutan 2-pass: SEMUA teks dicek CC dulu, baru ARR — satu halaman boleh
// memuat footer ARR + lisensi CC (kasus footer template).
func LicenseTypeOf(licURL string, texts ...string) string {
	if licURL != "" {
		if t := LicenseTypeFromURL(licURL); t != "" {
			return t
		}
	}
	for _, t := range texts {
		if x := LicenseTypeFromText(t); x != "" {
			return x
		}
	}
	for _, t := range texts {
		if _, ok := ARREvidence(t); ok {
			return LicenseTypeARR
		}
	}
	return LicenseTypeUnknown
}

// reARR = pemberitahuan hak cipta EKSPLISIT (footer/notice) → license_type =
// all_rights_reserved (bukti terbaca — §6.x.7 putaran 3). Blocklist di bawah
// menahan "SOME rights reserved" (makna berlawanan!).
var reARR = regexp.MustCompile(`(?i)(?:([A-Za-z-]+)\s+)?\ball\s+rights\s+reserved\b`)

// arrBlocked = kata sebelum "All Rights Reserved" yang MEMBALIK maknanya.
var arrBlocked = map[string]bool{"some": true, "no": true, "not": true}

// ARREvidence = cuplikan baris frasa ARR eksplisit (evidence utk review
// D3/D4). (evidence, true) bila benar2 tertulis; ("", false) = tak ketemu —
// caller WAJIB hasilkan unknown, BUKAN asumsi ARR.
func ARREvidence(text string) (string, bool) {
	for _, loc := range reARR.FindAllStringSubmatchIndex(text, -1) {
		if g := loc[2]; g >= 0 { // grup 1 (kata sebelum frasa) ikut tertangkap
			if arrBlocked[strings.ToLower(text[g:loc[3]])] {
				continue
			}
		}
		return evidenceAround(text, loc), true
	}
	return "", false
}

// canonCC = (kind URL/label, versi) → tipe kanonik lolos whitelist storage.
// kind "zero" → CC0; sisanya "by-sa" → "CC BY-SA". Hasil di-VALIDASI ulang
// ke whitelist (satu sumber kebenaran) — kind liar ("sampling") → "".
func canonCC(kind, ver string) string {
	k := strings.ToLower(kind)
	// pemisah label ("by nc sa") seragamkan jadi strip ("by-nc-sa")
	k = strings.Join(strings.FieldsFunc(k, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '_'
	}), "-")
	var out string
	switch {
	case k == "zero" || k == "0":
		out = "CC0"
	case strings.HasPrefix(k, "by"):
		out = "CC " + strings.ToUpper(k)
	default:
		return "" // jalur URL aneh (creativecommons.org/<lain>) — jangan nebak
	}
	if ver != "" {
		out += " " + ver
	}
	if !storage.ValidLicenseType(out) {
		return "" // kind tak dikenal whitelist (mis. "by-xyz") → jujur kosong
	}
	return out
}

// ---- oa_status: derive dari bukti ----

// OACrossref = atribut akses dari bukti Crossref per-artikel (0 GET):
// embargo (license start > now) → restricted · free-to-read → free ·
// CC-saja / tanpa sinyal → ("", false): lisensi terbuka BUKAN bukti akses
// gratis (kasus hybrid — asumsi dilarang, §6.x.8 D1 aturan 4).
func OACrossref(ci CrossrefItem) (string, bool) {
	switch {
	case ci.Embargo:
		return OARestricted, true
	case ci.FreeToRead:
		return OAFree, true
	}
	return "", false
}

// OAFromOAI = atribut akses dari pernyataan OAI dc:rights (reuse regex
// PolicyHits: reOA → free · rePaid → paid). CC-saja / tanpa pernyataan →
// ("", false) — turun ke tahap probe D3.
func OAFromOAI(rights []string) (string, bool) {
	for _, r := range rights {
		low := strings.ToLower(r)
		switch {
		case reOA.MatchString(low):
			return OAFree, true
		case rePaid.MatchString(low):
			return OAPaid, true
		}
	}
	return "", false
}

// ---- license_reuse: DERIVE (bukan kolom — "keep it lean", putaran 4) ----

// Nilai derive reuse utk konsumen tampilan (bukan data tersendiri):
//
//	cc-*/CC0  → ReuseWithTerms  (reuse dgn istilah lisensi — wajib sitasi dkk)
//	ARR       → ReuseNo         (tanpa izin reuse)
//	unknown   → ReuseNotAssumed (TIDAK diasumsikan bebas — jangan ditampilkan
//	              seolah libre)
const (
	ReuseWithTerms  = "reuse_with_terms"
	ReuseNo         = "no_reuse"
	ReuseNotAssumed = "not_assumed"
)

// LicenseReuse = turunan license_type (§6.x.7 R1: kolom ke-3 ditolak, derive).
func LicenseReuse(licenseType string) string {
	switch {
	case licenseType == LicenseTypeARR:
		return ReuseNo
	case licenseType != LicenseTypeUnknown && strings.HasPrefix(licenseType, "CC"):
		return ReuseWithTerms
	default:
		return ReuseNotAssumed
	}
}
