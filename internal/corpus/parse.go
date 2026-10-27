package corpus

// ==== parse.go — parser OAI-PMH murni (doc 43 §5.3 pass-1 — spec approve
// 14 Okt; fase C2) ====
//
// Fungsi murni tanpa HTTP: ListRecords (multi-element: subject/description/
// identifier/language boleh berulang) + resumptionToken utk loop C3 ·
// citation OJS → vol/no (filter volume terakhir) · pemilihan DOI vs URL ·
// error OAI (`<error code="...">` — noRecordsMatch = jurnal kosong, jujur).

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// OAIRecord = 1 <record> OAI-PMH (oai_dc) — field nullable jujur.
type OAIRecord struct {
	Identifier   string   // header identifier (kunci unik per jurnal)
	Deleted      bool     // header status="deleted" (tanpa metadata)
	Title        string   // dc:title pertama
	Creators     []string // dc:creator (multi)
	Subjects     []string // dc:subject — keyword AUTHOR (multi)
	Descriptions []string // dc:description — abstrak (multi; id+en campur)
	Dates        []string // dc:date (multi — yyyy / yyyy-mm-dd)
	Identifiers  []string // dc:identifier — URL landing + DOI (multi)
	Source       string   // dc:source — citation "…, Vol(12), Iss(1), Pg(…)"
	Languages    []string // dc:language (multi)
	Rights       []string // dc:rights — lisensi/hak cipta (multi)
	Publishers   []string // dc:publisher
	Types        []string // dc:type
}

type oaiEnvelope struct {
	ListRecords struct {
		Records []struct {
			Header struct {
				Identifier string `xml:"identifier"`
				Status     string `xml:"status,attr"`
			} `xml:"header"`
			Metadata struct {
				DC struct {
					Titles       []string `xml:"title"`
					Creators     []string `xml:"creator"`
					Subjects     []string `xml:"subject"`
					Descriptions []string `xml:"description"`
					Dates        []string `xml:"date"`
					Identifiers  []string `xml:"identifier"`
					Sources      []string `xml:"source"`
					Languages    []string `xml:"language"`
					Rights       []string `xml:"rights"`
					Publishers   []string `xml:"publisher"`
					Types        []string `xml:"type"`
				} `xml:"dc"`
			} `xml:"metadata"`
		} `xml:"record"`
		Resumption string `xml:"resumptionToken"`
	} `xml:"ListRecords"`
	Error struct {
		Code  string `xml:"code,attr"`
		Value string `xml:",chardata"`
	} `xml:"error"`
}

// ParseListRecords = record OAI + token lanjut ("" = halaman terakhir).
// body dgn <error code="..."> → (nil, "", err) — caller pilih kebijakan
// (noRecordsMatch = jurnal kosong utk scope, bukan kegagalan network).
func ParseListRecords(body string) ([]OAIRecord, string, error) {
	var env oaiEnvelope
	if err := xml.Unmarshal([]byte(body), &env); err != nil {
		return nil, "", fmt.Errorf("oai: body bukan XML valid: %w", err)
	}
	if env.Error.Code != "" {
		return nil, "", fmt.Errorf("oai error %q: %s", env.Error.Code, strings.TrimSpace(env.Error.Value))
	}
	out := make([]OAIRecord, 0, len(env.ListRecords.Records))
	for _, r := range env.ListRecords.Records {
		dc := r.Metadata.DC
		rec := OAIRecord{
			Identifier:   r.Header.Identifier,
			Deleted:      r.Header.Status == "deleted",
			Creators:     dc.Creators,
			Subjects:     dc.Subjects,
			Descriptions: dc.Descriptions,
			Dates:        dc.Dates,
			Identifiers:  dc.Identifiers,
			Languages:    dc.Languages,
			Rights:       dc.Rights,
			Publishers:   dc.Publishers,
			Types:        dc.Types,
		}
		if len(dc.Titles) > 0 {
			rec.Title = dc.Titles[0]
		}
		if len(dc.Sources) > 0 {
			rec.Source = dc.Sources[0]
		}
		if rec.Deleted {
			out = append(out, rec) // header deleted — tanpa metadata; driver skip
			continue
		}
		out = append(out, rec)
	}
	return out, env.ListRecords.Resumption, nil
}

// OAIErrorIs = true bila body memuat error dgn kode tsb (mis noRecordsMatch).
func OAIErrorIs(body, code string) bool {
	var env oaiEnvelope
	if err := xml.Unmarshal([]byte(body), &env); err != nil {
		return false
	}
	return env.Error.Code == code
}

// ---- citation → vol/no (filter volume terakhir) ----

var (
	reCiteVolIss = regexp.MustCompile(`(?i)vol(?:ume)?\s*[\(.]?\s*(\d{1,4})\s*[\),.].*?(?:iss(?:ue)?|nom(?:or)?|num(?:ber)?|no\.?)\s*[\(.]?\s*(\d{1,3})`)
	reCiteVol    = regexp.MustCompile(`(?i)\bvol(?:ume)?\.?\s*[\(]?\s*(\d{1,4})`)
	reCiteIss    = regexp.MustCompile(`(?i)\b(?:iss(?:ue)?|nom(?:or)?|num(?:ber)?|no)\.?\s*[\(]?\s*(\d{1,3})`)
	reYear4      = regexp.MustCompile(`\b((?:19|20)\d{2})\b`)
)

// ParseCitationVol = (vol, no) dari citation OJS — format umum oai_dc dc:source:
// "…, Vol(12), Iss(1), Pg(1-10)" (OJS 3) · "Vol. 11 No. 2 (2024)" · fallback
// parsial (vol saja/no saja = 0 utk yg tak ada — jujur, pola G2).
func ParseCitationVol(cite string) (vol, no int64) {
	if m := reCiteVolIss.FindStringSubmatch(cite); m != nil {
		vol, _ = strconv.ParseInt(m[1], 10, 64)
		no, _ = strconv.ParseInt(m[2], 10, 64)
		return vol, no
	}
	if m := reCiteVol.FindStringSubmatch(cite); m != nil {
		vol, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if m := reCiteIss.FindStringSubmatch(cite); m != nil {
		no, _ = strconv.ParseInt(m[1], 10, 64)
	}
	return vol, no
}

// RecordYear = tahun pertama dari dc:date ("" / tanpa tahun = 0).
func (r OAIRecord) RecordYear() int64 {
	for _, d := range r.Dates {
		if m := reYear4.FindStringSubmatch(d); m != nil {
			y, _ := strconv.ParseInt(m[1], 10, 64)
			return y
		}
	}
	return 0
}

// PickDOI = identifier DOI ("doi:10.xxx…" / "https://doi.org/10…" / "10.…")
// — "" bila tanpa DOI.
func PickDOI(ids []string) string {
	for _, id := range ids {
		s := strings.TrimSpace(id)
		switch {
		case strings.HasPrefix(strings.ToLower(s), "doi:"):
			return strings.TrimSpace(strings.TrimPrefix(strings.ToLower(s), "doi:"))
		case strings.HasPrefix(strings.ToLower(s), "https://doi.org/"):
			return strings.TrimSpace(s[len("https://doi.org/"):])
		case strings.HasPrefix(strings.ToLower(s), "http://doi.org/"):
			return strings.TrimSpace(s[len("http://doi.org/"):])
		case strings.HasPrefix(s, "10."):
			return s
		}
	}
	return ""
}

// PickURL = identifier pertama yg http(s) (landing) — "" bila tak ada.
func PickURL(ids []string) string {
	for _, id := range ids {
		s := strings.TrimSpace(id)
		if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
			if strings.HasPrefix(strings.ToLower(s), "https://doi.org/") ||
				strings.HasPrefix(strings.ToLower(s), "http://doi.org/") {
				continue // DOI bukan landing
			}
			return s
		}
	}
	return ""
}

// ---- FilterLatestVolume (doc 43 §5.3 butir 3) ----

// VolFilter = hasil filter scope volume ke-N (dari terbesar).
type VolFilter struct {
	Picked []OAIRecord // record dlm scope volume terakhir
	Vol    int64       // vol terpilih (0 = fallback tahun)
	Year   int64       // tahun vol terpilih (utk fallback)
	Flag   string      // "" | "vol_missing" (sebagian record tanpa vol) | "vol_fallback" (semua record tanpa vol)
}

// FilterOwnRecords = guard identitas (G16 — temuan putaran-2 25 Okt): buang
// record lintas-jurnal dari endpoint AGGREGATE (contoh: scholarhub.ui.ac.id/do/
// oai dipakai 5 jurnal → record jurnal UI lain ikut terpilih sbg milik ke-5nya).
// Kecocokan dua arah SETELAH normalisasi tanda baca (": " vs ", " — bukti
// j916: source "Wacana, Journal…" vs nama SINTA "Wacana: Journal…" = nol-match
// tanpa normalisasi): source mengandung nama target ATAU nama target mengandung
// source (utk nama SINTA dgn embel-embel — "Kesmas: … (National Public
// Health Journal)" lebih panjang dr source OAI-nya).
// Filter HANYA aktif dgn bukti positif: ≥1 record milik DAN ≥1 record asing.
// Source "" = TIDAK DIKETAHUI → netral (ikut bila filter aktif; bukan asing —
// jurnal normal dgn record tanpa dc:source tak kehilangan apa pun).
// Tanpa bukti (nama source beda dr nama SINTA) → SEMUA record dipertahankan
// (perilaku lama — jurnal normal tak pernah lebih buruk dari sebelumnya).
func FilterOwnRecords(recs []OAIRecord, name string) (out []OAIRecord, dropped int) {
	lower := normName(name)
	if lower == "" {
		return recs, 0
	}
	milik, asing := 0, 0
	own := make([]bool, len(recs))
	for i, r := range recs {
		s := normName(r.Source)
		if s == "" {
			own[i] = true // netral — bukan bukti asing
			continue
		}
		match := strings.Contains(s, lower) || strings.Contains(lower, s)
		own[i] = match
		if match {
			milik++
		} else {
			asing++
		}
	}
	if milik == 0 || asing == 0 {
		return recs, 0 // tanpa bukti campuran → tak disentuh (aman)
	}
	out = recs[:0:0]
	for i, r := range recs {
		if !own[i] {
			dropped++
			continue
		}
		out = append(out, r)
	}
	return out, dropped
}

// normName = normalisasi nama utk pencocokan identitas (G16): lower + buang
// tanda baca pemisah (":" "," "(" ")") → spasi tunggal. Cukup utk jembatani
// "Wacana: Journal…" vs "Wacana, Journal…" tanpa melonggarkan makna.
func normName(s string) string {
	s = strings.ToLower(s)
	for _, c := range []string{":", ",", "(", ")"} {
		s = strings.ReplaceAll(s, c, " ")
	}
	return strings.Join(strings.Fields(s), " ")
}

// FilterLatestVolume = pilih record volume ke-N (N=1 → terakhir; user:
// default 1 volume terakhir, custom 2 dst). Rules jujur:
//  1. vol ter-parse ≥1 → vol urut desc, pilih indeks N-1; record ikut bila
//     vol==pilih ATAU (vol=0 & tahun==tahun(vol pilih)) → flag vol_missing.
//  2. semua vol=0 → tahun terbesar (record year==tahun) → flag vol_fallback.
func FilterLatestVolume(recs []OAIRecord, volN int) VolFilter {
	if volN < 1 {
		volN = 1
	}
	// vol unik terurut desc + tahunnya (max tahun per vol).
	byVol := map[int64]int64{}
	var vols []int64
	withVol, withYear := 0, int64(0)
	for _, r := range recs {
		if r.Deleted {
			continue
		}
		vol, _ := ParseCitationVol(r.Source)
		y := r.RecordYear()
		if vol > 0 {
			withVol++
			if cur, ok := byVol[vol]; !ok || y > cur {
				byVol[vol] = y
			}
		}
		if y > withYear {
			withYear = y // tahun max global
		}
	}
	// sort vol desc (tanpa dep — insertion sort slice kecil).
	if withVol > 0 {
		for _, v := range makeVolKeys(byVol) {
			vols = append(vols, v)
		}
	}

	if withVol > 0 {
		if volN > len(vols) {
			volN = len(vols) // permintaan melebihi vol tersedia → vol terlama ikut
		}
		pick := vols[volN-1]
		f := VolFilter{Vol: pick, Year: byVol[pick]}
		missing := 0
		for _, r := range recs {
			if r.Deleted {
				continue
			}
			v, _ := ParseCitationVol(r.Source)
			y := r.RecordYear()
			switch {
			case v == pick:
				f.Picked = append(f.Picked, r)
			case v == 0 && y != 0 && y == f.Year:
				f.Picked = append(f.Picked, r)
				missing++
			}
		}
		if missing > 0 {
			f.Flag = "vol_missing"
		}
		return f
	}

	// Fallback: semua record tanpa vol ter-parse → tahun terbesar.
	f := VolFilter{Year: withYear, Flag: "vol_fallback"}
	if withYear == 0 {
		f.Flag = "vol_unparsed" // tanpa vol & tanpa tahun — record gagal total
		return f
	}
	for _, r := range recs {
		if r.Deleted {
			continue
		}
		if r.RecordYear() == withYear {
			f.Picked = append(f.Picked, r)
		}
	}
	return f
}

// makeVolKeys = kunci vol unik terurut DESC (insertion sort — jumlah kecil).
func makeVolKeys(m map[int64]int64) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] > out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
