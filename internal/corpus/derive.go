package corpus

// ==== derive.go — derive per-artikel pass 1 (doc 43 §4.1 dwibahasa + §5.5
// lisensi + §5.3 butir 6 — spec approve 14 Okt; fase C3) ====
//
// Semua fungsi murni (0 GET, deterministik — kunci novelty dwibahasa):
// split abstrak id/en (penanda "Abstrak:"/"Abstract:" deterministik) ·
// language (dc:language first → ratio kata umum id/en) · lisensi 4 kelas
// OAI dc:rights (Crossref urusan crossref.go) · keyword chain author→crossref
// (TF-IDF = pass 2 C4).

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

var (
	reAbsPrefixID = regexp.MustCompile(`(?i)^\s*(?:abstrak|abstrack|ringkasan)\s*[:.\-–]\s*`)
	reAbsPrefixEN = regexp.MustCompile(`(?i)^\s*(?:abstract|summary)\s*[:.\-–]\s*`)
	// Penanda di TENGAH teks (1 description berisi id+en — umum di OAI OJS).
	reMidID = regexp.MustCompile(`(?i)\.\s+(?:abstrak|abstrack)\s*:\s*`)
	reMidEN = regexp.MustCompile(`(?i)\.\s+(?:abstract)\s*:\s*`)
)

// SplitAbstract = pecah []dc:description → (absID, absEN) dgn penanda
// deterministik (§4.1). Multi-description per bahasa digabung "\n\n".
// Hasil bisa: dua-duanya · satu · kosong (jujur — pass 2 TF-IDF tetap jalan).
func SplitAbstract(descs []string) (absID, absEN string) {
	var ids, ens []string
	for _, d := range descs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		id, en := splitOne(d)
		if id != "" {
			ids = append(ids, id)
		}
		if en != "" {
			ens = append(ens, en)
		}
	}
	return strings.Join(ids, "\n\n"), strings.Join(ens, "\n\n")
}

// splitOne = 1 description → (id, en). Rules berurutan (deterministik):
//  1. penanda "… . Abstrak:" / "… . Abstract:" di tengah → split di situ;
//  2. prefix "Abstrak:"/"Abstract:" → taruh ke bahasa tsb;
//  3. tanpa penanda → ratio kata umum → 1 bahasa / kosong.
func splitOne(d string) (id, en string) {
	if m := reMidEN.FindStringIndex(d); m != nil && m[0] > 0 {
		id = strings.TrimSpace(reAbsPrefixID.ReplaceAllLiteralString(strings.TrimSpace(d[:m[0]]), ""))
		en = strings.TrimSpace(reAbsPrefixEN.ReplaceAllLiteralString(strings.TrimSpace(d[m[1]:]), ""))
		if id != "" || en != "" {
			return id, en
		}
	}
	if m := reMidID.FindStringIndex(d); m != nil && m[0] > 0 {
		en = strings.TrimSpace(reAbsPrefixEN.ReplaceAllLiteralString(strings.TrimSpace(d[:m[0]]), ""))
		id = strings.TrimSpace(reAbsPrefixID.ReplaceAllLiteralString(strings.TrimSpace(d[m[1]:]), ""))
		if id != "" || en != "" {
			return id, en
		}
	}
	if reAbsPrefixID.MatchString(d) {
		return strings.TrimSpace(reAbsPrefixID.ReplaceAllLiteralString(d, "")), ""
	}
	if reAbsPrefixEN.MatchString(d) {
		return "", strings.TrimSpace(reAbsPrefixEN.ReplaceAllLiteralString(d, ""))
	}
	// Tanpa penanda — ratio kata umum utk 1 teks.
	switch ratioLang(d) {
	case LanguageID:
		return d, ""
	case LanguageEN:
		return "", d
	}
	return "", ""
}

// DetectLanguage = bahasa artikel (§4.1 + **amandemen O-B1/G12 — fakta isi
// abstrak didahulukan HANYA utk kasus yang tak bisa dibantah**):
//  1. dua abstrak terisi → mixed (tag tunggal tak mewakili bilingual; audit
//     G12: 147 bilingual salah label en/id krn dc:language first).
//  2. tag vs isi SATU abstrak KONTRADIKSI → isi menang (audit G12: 6 absID-
//     saja salah label en — metadata jurnal keliru).
//  3. sisanya: dc:language first (spec §4.1) → satu abstrak = bahasanya →
//     ratio kata umum (title+subject) → unknown (jujur).
func DetectLanguage(dcLangs []string, absID, absEN string, extraText string) string {
	if absID != "" && absEN != "" {
		return LanguageMixed
	}
	tag := normLangTags(dcLangs)
	switch {
	case absID != "" && tag == LanguageEN:
		return LanguageID // isi ID-saja tapi tag en → kontradiksi, isi menang
	case absEN != "" && tag == LanguageID:
		return LanguageEN // isi EN-saja tapi tag id → kontradiksi, isi menang
	case tag != "":
		return tag // dc:language first (spec §4.1)
	case absID != "":
		return LanguageID
	case absEN != "":
		return LanguageEN
	}
	return ratioLang(strings.TrimSpace(extraText))
}

// normLangTags = tag dc:language → id|en|mixed ("" bila tak dikenal — lanjut
// tahap berikut). Multi-tag "id"+"en" → mixed.
func normLangTags(tags []string) string {
	set := map[string]bool{}
	for _, t := range tags {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "id", "ind", "indonesian", "bahasa indonesia", "id-id":
			set[LanguageID] = true
		case "en", "eng", "english", "en-us", "en-gb":
			set[LanguageEN] = true
		}
	}
	switch {
	case set[LanguageID] && set[LanguageEN]:
		return LanguageMixed
	case set[LanguageID]:
		return LanguageID
	case set[LanguageEN]:
		return LanguageEN
	}
	return ""
}

// Kata umum id/en (deterministik, kecil — threshold tetap dlm kode §4.1;
// teks abstrak pendek → ratio cukup akurat utk tag sumber).
var (
	idCommon = []string{"yang", "dan", "di", "dari", "untuk", "dengan", "pada",
		"ini", "itu", "adalah", "dalam", "tidak", "akan", "oleh", "karena",
		"juga", "telah", "bisa", "secara", "hasil", "penelitian", "pada"}
	enCommon = []string{"the", "and", "of", "in", "for", "with", "on", "this",
		"that", "is", "are", "was", "were", "to", "as", "by", "an", "be",
		"been", "from", "or", "at", "which", "can", "has", "have", "study",
		"results"}
)

// ratioLang = hitung kata umum id vs en (token lower, strip tanda).
// id menang bila ≥ en & >0 · en menang bila >id · seri>0 = mixed · 0 = unknown.
func ratioLang(text string) string {
	idN, enN := 0, 0
	for _, tok := range strings.Fields(strings.ToLower(stripNonAlnum(text))) {
		for _, w := range idCommon {
			if tok == w {
				idN++
				break
			}
		}
		for _, w := range enCommon {
			if tok == w {
				enN++
				break
			}
		}
	}
	switch {
	case idN > 0 && idN >= enN:
		return LanguageID
	case enN > idN:
		return LanguageEN
	}
	return LanguageUnknown
}

func stripNonAlnum(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
}

// licenseFromOAI = aturan §5.5 sumber OAI dc:rights (non-DOI / Crossref
// kosong). (status, url, ok) — ok=false = rights kosong/tak dikenal →
// caller tetap pakai LicenseUnknown (jujur, antrean Fase 3.5).
func licenseFromOAI(rights []string) (status, url string, ok bool) {
	for _, r := range rights {
		raw := strings.TrimSpace(r)
		if raw == "" {
			continue
		}
		low := strings.ToLower(raw)
		switch {
		case strings.Contains(low, "creativecommons.org"):
			return LicenseOpen, raw, true
		case strings.Contains(low, "open access"), strings.Contains(low, "gratis"),
			strings.Contains(low, "bebas akses"), strings.Contains(low, "free access"),
			strings.Contains(low, "tidak berbayar"), strings.Contains(low, "no article processing charge"):
			return LicenseFreeNotOA, raw, true
		case strings.Contains(low, "berbayar"), strings.Contains(low, "paywall"),
			strings.Contains(low, "subscription"), strings.Contains(low, "berlangganan"),
			strings.Contains(low, "processing charge"):
			return LicensePaid, raw, true
		}
	}
	return "", "", false
}

// KeywordJSON = 1 keywords_json utk 1 artikel (chain §5.4: author → crossref;
// kosong = antrean TF-IDF pass 2). JSON deterministik (encoding/json sort key).
func KeywordJSON(authorSubjects, crossrefSubjects []string, lang string) string {
	var kws []map[string]string
	for _, s := range cleanList(authorSubjects) {
		kws = append(kws, map[string]string{"kw": s, "src": "author", "lang": kwLang(s, lang)})
	}
	if len(kws) == 0 {
		for _, s := range cleanList(crossrefSubjects) {
			kws = append(kws, map[string]string{"kw": s, "src": "crossref", "lang": kwLang(s, lang)})
		}
	}
	if len(kws) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(kws)
	return string(b)
}

// kwLang = bahasa per keyword: ratio keyword pendek → fallback bahasa record.
func kwLang(kw, fallback string) string {
	if l := ratioLang(kw); l != LanguageUnknown {
		return l
	}
	return fallback
}

// cleanList = trim + buang dup + buang kosong (urut sumber dipertahankan).
func cleanList(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out
}
