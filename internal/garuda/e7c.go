package garuda

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// E7cKand = satu kandidat halaman view Garuda utk tie-break year-range
// (metode resmi duplikat approve 4 Okt — doc 30 §14.2).
type E7cKand struct {
	ID       int
	YearFrom int
	YearTo   int
	NotFound bool
}

// E7cPilihKandidat memilih pemenang antar kandidat view: (1) halaman
// "Record Not Found" gugur; (2) `Filter by Year` tahun AKHIR terbesar =
// entri paling aktif; (3) tie → yang datanya lengkap (ada rentang) menang;
// (4) tie lagi → garuda_id kecil. Mengembalikan indeks + alasan jujur utk
// laporan (K2).
func E7cPilihKandidat(ks []E7cKand) (int, string) {
	idup := -1
	for i, k := range ks {
		if !k.NotFound {
			if idup < 0 {
				idup = i
			}
		}
	}
	if idup < 0 {
		return -1, "semua kandidat Record-Not-Found"
	}
	menang := idup
	for i, k := range ks {
		if i == menang || k.NotFound {
			continue
		}
		switch {
		case k.YearTo > ks[menang].YearTo:
			menang = i
		case k.YearTo == ks[menang].YearTo:
			lengkapK := k.YearFrom > 0 && k.YearTo > 0
			lengkapM := ks[menang].YearFrom > 0 && ks[menang].YearTo > 0
			switch {
			case lengkapK && !lengkapM:
				menang = i
			case lengkapK == lengkapM && k.ID < ks[menang].ID:
				menang = i
			}
		}
	}
	return menang, fmt.Sprintf("year_to=%d (id=%d)", ks[menang].YearTo, ks[menang].ID)
}

// E7cISSNHyphen = kanonik 8-huruf → bentuk ber-strip ("0215773X" →
// "0215-773X") utk query DOAJ 2-bentuk (temuan doc 37 §4: DOAJ menerima
// ISSN ber-strip). Bukan 8-huruf → norm apa adanya.
func E7cISSNHyphen(s string) string {
	n := NormISSN(s)
	if len(n) != 8 {
		return n
	}
	return n[:4] + "-" + n[4:]
}

// e7cISSNCocok = true bila ISSN halaman view (kosong = tak diverifikasi)
// menyentuh salah satu ISSN baris SINTA.
func e7cISSNCocok(vp, ve, sp, se string) bool {
	np, ne := NormISSN(vp), NormISSN(ve)
	if np == "" && ne == "" {
		return false
	}
	nsp, nse := NormISSN(sp), NormISSN(se)
	return (np != "" && (np == nsp || np == nse)) ||
		(ne != "" && (ne == nsp || ne == nse))
}

// E7cNilaiCrossref = value + confidence prov `alt_crossref` (Opsi A miss-8,
// doc 30 §14.6 butir 3). tipe = map ISSN → "print"/"electronic" (opsional,
// dari `issn-type` Crossref — bukti label E/P utk deteksi tertukar).
//
// Confidence jujur (K2): ISSN-query = identitas registry kuat, TAPI nilai
// tetap dikunci ke kecocokan judul — 0.9 bila TitleSama, 0.6 bila beda
// (registry tepat tapi identitas ragu — jangan di atas itu).
func E7cNilaiCrossref(nama string, xr E6Crossref, tipe map[string]string) (string, float64) {
	var issn []string
	for _, s := range xr.ISSNs {
		if t := tipe[s]; t != "" {
			issn = append(issn, s+"("+t+")")
		} else {
			issn = append(issn, s)
		}
	}
	v := fmt.Sprintf("judul=%s | penerbit=%s | issn=%s | dois=%d",
		xr.Judul, xr.Penerbit, strings.Join(issn, ","), xr.DoiTotal)
	conf := 0.6
	if TitleSama(nama, xr.Judul) {
		conf = 0.9
	}
	return v, conf
}

// E7cNilaiOAI = value + confidence prov `alt_oai` — konfirmasi identitas
// dari host jurnal SENDIRI (repositoryName + daftar metadataFormat).
// 0.9 bila nama repository konsisten dgn nama SINTA, 0.6 bila beda.
func E7cNilaiOAI(nama, base, repo string, formats []string) (string, float64) {
	v := fmt.Sprintf("oai=%s | repo=%s | formats=%s",
		base, repo, strings.Join(formats, ","))
	conf := 0.6
	if TitleSama(nama, repo) {
		conf = 0.9
	}
	return v, conf
}

// E7cNilaiView = value + confidence prov `alt_view` (j60 — view resolve
// E4b ditahan ambiguous). Confidence konsisten dgn preseden K6 (j744
// by=title conf 0.6): ISSN-cocok 0.8 · title-sama 0.6 · selebihnya 0.5.
// Fakta pendukung (tahun, penerbit) ikut di value — bukan di confidence.
func E7cNilaiView(nama string, id int, v *ViewInfo, sp, se string) (string, float64) {
	if v.NotFound {
		return fmt.Sprintf("view/%d = Record Not Found", id), 0
	}
	year := "-"
	if v.YearFrom > 0 || v.YearTo > 0 {
		year = fmt.Sprintf("%d-%d", v.YearFrom, v.YearTo)
	}
	val := fmt.Sprintf("view/%d | tahun=%s | judul=%s | penerbit=%s",
		id, year, v.Title, v.Publisher)
	switch {
	case e7cISSNCocok(v.PrintISSN, v.EISSN, sp, se):
		return val, 0.8
	case TitleSama(nama, v.Title):
		return val, 0.6
	}
	return val, 0.5
}

// ---------- parser OAI-PMH (utk E7c; parser E5 tinggal di cmd/garuda) -----

var (
	reE7cIdentify = regexp.MustCompile(`(?i)<(oai:)?identify[\s>]`)
	reE7cRepoName = regexp.MustCompile(`(?i)<(oai:)?repositoryName>\s*([^<]*?)\s*</`)
	reE7cLMFPfx   = regexp.MustCompile(`(?i)<(oai:)?metadataPrefix>\s*([^<\s]+)\s*</`)
	reE7cOAIError = regexp.MustCompile(`(?i)<(oai:)?error[^>]*code\s*=\s*"([^"]*)"[^>]*>\s*([^<]*)<`)
)

// E7cParseIdentify = deteksi respons `verb=Identify` + repositoryName.
// ok=false = bukan respons Identify (bukan OAI / halaman lain).
func E7cParseIdentify(body []byte) (repo string, ok bool) {
	if !reE7cIdentify.Match(body) {
		return "", false
	}
	if m := reE7cRepoName.FindSubmatch(body); m != nil {
		repo = strings.TrimSpace(string(m[2]))
	}
	return repo, true
}

// E7cParseLMF = parse respons `verb=ListMetadataFormats` → daftar
// metadataPrefix (urut kemunculan, dedup). errorMsg diisi bila endpoint
// membalas <oai:error> (bukti endpoint HIDUP — lihat doc 34 §5). Bukan
// respons OAI sama sekali (mis. HTML fallback) = error.
func E7cParseLMF(body []byte) (formats []string, errorMsg string, err error) {
	if m := reE7cOAIError.FindSubmatch(body); m != nil {
		return nil, fmt.Sprintf("%s: %s", strings.TrimSpace(string(m[2])),
			strings.TrimSpace(string(m[3]))), nil
	}
	if !bytes.Contains(bytes.ToLower(body), []byte("metadataformat")) {
		return nil, "", fmt.Errorf("bukan respons ListMetadataFormats")
	}
	seen := map[string]bool{}
	for _, m := range reE7cLMFPfx.FindAllSubmatch(body, -1) {
		f := strings.TrimSpace(string(m[2]))
		if f != "" && !seen[f] {
			seen[f] = true
			formats = append(formats, f)
		}
	}
	if len(formats) == 0 {
		return nil, "", fmt.Errorf("ListMetadataFormats tanpa metadataPrefix")
	}
	return formats, "", nil
}
