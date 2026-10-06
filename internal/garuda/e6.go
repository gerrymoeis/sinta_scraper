package garuda

// E6 — coverage Crossref & DOAJ (doc 30 §14.5, opsi A APPROVED user 4 Okt
// 2026): sampel 24 ISSN = 18 S1 (db enrich) + 3 S2 + 3 S3 (db stage1
// vdac-l1-r23-kumulatif — silang-rank utk cek bias Q6), 2 GET/ISSN → pagu
// keras 50 (48 + 2 margin). Output = coverage % per sumber + strata +
// fill-rate field (bahan keputusan auxiliary weight Bagian 6). TANPA tulis
// db (E7 yang menulis). Semua fungsi murni/parse = offline (0 GET).

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---------- identitas baris & ISSN ----------

// E6Baris = satu baris kandidat sampel (dari db manapun — driver mengisi
// Sumber utk audit; Key = ISSN kanonik utk dedup, URL, & resume).
type E6Baris struct {
	Sumber string // s1 (enrich) | s23 (stage1 silang-rank)
	ID     int64
	Rank   int
	Nama   string
	PISSN  string
	EISSN  string
	Key    string
}

var reISSN = regexp.MustCompile(`^[0-9A-Z]{7,8}$`)

// NormISSN = kanonikasi ISSN utk key/URL: uppercase, buang spasi & strip
// (2549-9904 → 25499904). "" bila tak valid (7–8 alnum).
func NormISSN(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer("-", "", " ", "").Replace(s)
	if !reISSN.MatchString(s) {
		return ""
	}
	return s
}

// ---------- sampling deterministik (approve opsi A: 18 S1 + 3 S2 + 3 S3) ----------

// stridePilih menyebar n indeks dari pool (deterministik — tanpa random,
// tanpa seed: hasil identik antar run → bisa direview user).
func stridePilih(pool []E6Baris, n int) []E6Baris {
	if n <= 0 || len(pool) == 0 {
		return nil
	}
	if len(pool) <= n {
		out := make([]E6Baris, len(pool))
		copy(out, pool)
		return out
	}
	step := len(pool) / n
	out := make([]E6Baris, 0, n)
	for i := 0; i < n; i++ {
		idx := i * step
		if idx >= len(pool) {
			idx = len(pool) - 1
		}
		out = append(out, pool[idx])
	}
	return out
}

// PilihSampelE6 = stratified deterministik: nS1 dari rank 1 (pool utama),
// nS2/nS3 dari rank 2/3 (pool silang-rank). Dedup global per Key dgn
// prioritas urutan pool (S1 → S2 → S3) — ISSN sama antar strata = pilih
// yang pertama. GAGAL bila salah satu pool kurang (jujur, bukan silently).
func PilihSampelE6(baris []E6Baris, nS1, nS2, nS3 int) ([]E6Baris, error) {
	var pool1, pool2, pool3 []E6Baris
	seen := map[string]bool{}
	for _, b := range baris {
		if b.Key == "" || seen[b.Key] {
			continue
		}
		seen[b.Key] = true
		switch b.Rank {
		case 1:
			pool1 = append(pool1, b)
		case 2:
			pool2 = append(pool2, b)
		case 3:
			pool3 = append(pool3, b)
		}
	}
	if len(pool1) < nS1 || len(pool2) < nS2 || len(pool3) < nS3 {
		return nil, fmt.Errorf("pool tak cukup: S1 %d/%d · S2 %d/%d · S3 %d/%d (sehabis dedup ISSN)",
			len(pool1), nS1, len(pool2), nS2, len(pool3), nS3)
	}
	sampel := append(stridePilih(pool1, nS1), stridePilih(pool2, nS2)...)
	sampel = append(sampel, stridePilih(pool3, nS3)...)
	// urut (rank, id) utk laporan stabil
	sort.Slice(sampel, func(i, j int) bool {
		if sampel[i].Rank != sampel[j].Rank {
			return sampel[i].Rank < sampel[j].Rank
		}
		return sampel[i].ID < sampel[j].ID
	})
	if len(sampel) != nS1+nS2+nS3 {
		return nil, fmt.Errorf("sampel %d ≠ %d", len(sampel), nS1+nS2+nS3)
	}
	return sampel, nil
}

// ---------- parser Crossref (api.crossref.org/journals/{ISSN}) ----------

// E6Crossref = hasil 1 GET Crossref. FAKTA bentuk asli (fixture 4 Okt):
// `message-type:"journal"` + `message` = OBJEK (bukan array!) berisi title,
// publisher, ISSN[] (kanonik 8-huruf tanpa strip — lihat e6ISSNs; cek manual
// user 6 Okt), counts.total-dois, dan fractions `coverage` per-field
// (abstracts/licenses/orcids/ror-ids-current) — fractions INILAH jawaban
// "field berguna (abstract, license, ORCID/ROR)" di level journal.
// Tersedia=false utk 404/empty = BUKAN error (coverage jujur).
type E6Crossref struct {
	Tersedia      bool     `json:"tersedia"`
	Judul         string   `json:"judul,omitempty"`
	Penerbit      string   `json:"penerbit,omitempty"`
	ISSNs         []string `json:"issn_terdaftar,omitempty"`
	DoiTotal      int      `json:"doi_total,omitempty"`
	AbstractsFrac *float64 `json:"abstracts_frac,omitempty"` // 0..1 — ada kunci = field tersedia
	LicensesFrac  *float64 `json:"licenses_frac,omitempty"`
	OrcidsFrac    *float64 `json:"orcids_frac,omitempty"`
	RorFrac       *float64 `json:"ror_frac,omitempty"`
	HTTP          int      `json:"http"`
	Error         string   `json:"error,omitempty"`
}

// ParseCrossrefJournals = parse body 200 utk /journals/{ISSN}. Bentuk asli
// = objek journal (message-type "journal"); array (collection) ditoleransi
// sbg cadangan. Bentuk lain (string failure) = error parse (jujur — 404
// ditangani driver sbg "tidak terdaftar", bukan parser).
func ParseCrossrefJournals(body []byte) (E6Crossref, error) {
	var env struct {
		Status      string          `json:"status"`
		MessageType string          `json:"message-type"`
		Message     json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return E6Crossref{}, fmt.Errorf("envelope crossref: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(env.Message, &m); err != nil {
		// toleransi cadangan: array daftar → ambil elemen pertama (kosong = tak-ada)
		var arr []map[string]any
		if err2 := json.Unmarshal(env.Message, &arr); err2 != nil {
			return E6Crossref{}, fmt.Errorf("message crossref bukan objek jurnal: %w", err)
		}
		if len(arr) > 0 {
			m = arr[0]
		}
	}
	if len(m) == 0 {
		return E6Crossref{Tersedia: false}, nil
	}
	out := E6Crossref{Tersedia: true}
	out.Judul = e6Str(m["title"])
	out.Penerbit = e6Str(m["publisher"])
	out.ISSNs = e6ISSNs(m["ISSN"])
	if c, ok := m["counts"].(map[string]any); ok {
		out.DoiTotal = int(e6Float(c["total-dois"]))
	}
	if cov, ok := m["coverage"].(map[string]any); ok {
		if v, ok := cov["abstracts-current"].(float64); ok {
			out.AbstractsFrac = &v
		}
		if v, ok := cov["licenses-current"].(float64); ok {
			out.LicensesFrac = &v
		}
		if v, ok := cov["orcids-current"].(float64); ok {
			out.OrcidsFrac = &v
		}
		if v, ok := cov["ror-ids-current"].(float64); ok {
			out.RorFrac = &v
		}
	}
	return out, nil
}

// ---------- parser DOAJ (doaj.org/api/search/journals/{ISSN}) ----------

// E6DOAJ = hasil 1 GET DOAJ search. bibjson DIDOKUMENTASIKAN apa adanya
// (presence field = fill-rate); abstract/ORCID/ROR level artikel TIDAK ada
// di record journal — dicatat terus terang dlm laporan (bukan diarang ada).
type E6DOAJ struct {
	Tersedia     bool     `json:"tersedia"`
	Judul        string   `json:"judul,omitempty"`
	Penerbit     string   `json:"penerbit,omitempty"`
	PISSN        string   `json:"pissn_sumber,omitempty"`
	EISSN        string   `json:"eissn_sumber,omitempty"`
	Subjek       []string `json:"subjek,omitempty"`
	AdaLisensi   bool     `json:"ada_lisensi"`
	AdaEditorial bool     `json:"ada_editorial"`
	AdaBoard     bool     `json:"ada_board"`
	HTTP         int      `json:"http"`
	Error        string   `json:"error,omitempty"`
}

// ParseDOAJSearch = parse body 200 utk search: {total, results[]}.
// FAKTA (fixture): query ISSN polos BEKERJA — tapi results bisa memuat noise
// (total=2 utk JIPK: results[1] = jurnal lain) → WAJIB verifikasi ISSN
// dicocokkan dgn `key` (eissn/pissn tiap hasil) — validitas #1: tanpa
// kecocokan ISSN dianggap TIDAK terdaftar (bukan covered asal).
func ParseDOAJSearch(body []byte, key string) (E6DOAJ, error) {
	var env struct {
		Total   float64 `json:"total"`
		Results []struct {
			Bibjson map[string]any `json:"bibjson"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return E6DOAJ{}, fmt.Errorf("envelope doaj: %w", err)
	}
	if env.Total == 0 {
		return E6DOAJ{Tersedia: false}, nil
	}
	if len(env.Results) == 0 {
		return E6DOAJ{}, fmt.Errorf("total=%.0f tetapi results kosong", env.Total)
	}
	keyN := NormISSN(key)
	var pick map[string]any
	for _, r := range env.Results {
		b := r.Bibjson
		if NormISSN(e6Str(b["eissn"])) == keyN || NormISSN(e6Str(b["pissn"])) == keyN {
			pick = b
			break
		}
	}
	if pick == nil {
		// ada hasil tapi tak satu pun = ISSN dicari → BUKAN terdaftar
		return E6DOAJ{Tersedia: false,
			Error: fmt.Sprintf("total=%.0f tetapi tak ada hasil dgn ISSN %s", env.Total, keyN)}, nil
	}
	out := E6DOAJ{Tersedia: true}
	out.Judul = e6Str(pick["title"])
	out.Penerbit = e6AnyName(pick["publisher"])
	out.PISSN = e6Str(pick["pissn"])
	out.EISSN = e6Str(pick["eissn"])
	if subj, ok := pick["subject"].([]any); ok {
		for _, s := range subj {
			if m, ok := s.(map[string]any); ok {
				if t := e6Str(m["term"]); t != "" && len(out.Subjek) < 5 {
					out.Subjek = append(out.Subjek, t)
				}
			}
		}
	}
	out.AdaLisensi = e6Ada(pick["license"])
	out.AdaEditorial = e6Ada(pick["editorial"])
	out.AdaBoard = e6Ada(pick["board"])
	return out, nil
}

// ---------- ekstraksi generik toleran (API bisa beda bentuk antar versi) ----------

func e6Str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

// e6Float = ekstraksi angka JSON (counts dsb.).
func e6Float(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

// e6ISSNs = ekstraksi `message.ISSN` utk dinormalisasi (keputusan cek manual
// user 6 Okt: output = kanonik 8-huruf TANPA strip, mis "2355-7885" →
// "23557885"). Dukung DUA bentuk array: string lurus (fixture /journals/{ISSN})
// dan objek {type:"print"|"electronic", value:"..."} (bentuk endpoint Crossref
// lain) — objek yg tanpa value di-skip, hasil didedup, urut apa adanya.
func e6ISSNs(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, el := range arr {
		var raw string
		switch t := el.(type) {
		case string:
			raw = t
		case map[string]any:
			raw = e6Str(t["value"])
		}
		if s := NormISSN(raw); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// e6AnyName = publisher DOAJ bisa berupa string lama / object {name,...} baru.
func e6AnyName(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		return e6Str(t["name"])
	}
	return ""
}

// e6Ada = presence check (array non-kosong / map non-kosong / string non-kosong).
func e6Ada(v any) bool {
	switch t := v.(type) {
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	case string:
		return t != ""
	}
	return false
}

// ---------- cross-check judul (corroboration offline — 0 GET) ----------

var reBukanAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// NormJudul = lower + buang non-alfa-num + spasi tunggal (untuk banding
// judul SINTA vs sumber — K2 dlm skala kecil, murni lokal).
func NormJudul(s string) string {
	s = strings.ToLower(s)
	s = reBukanAlnum.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// TitleSama = true bila norm identik ATAU salah satu mengandung penuh
// pihak lain (≥16 char bersih — cegah "journal" kembar kecil menimpa).
// false = beda (informatif utk auxiliary weight — bukan kesalahan).
func TitleSama(a, b string) bool {
	na, nb := NormJudul(a), NormJudul(b)
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	if len(na) >= 16 && strings.Contains(na, nb) {
		return true
	}
	if len(nb) >= 16 && strings.Contains(nb, na) {
		return true
	}
	return false
}

// ---------- hasil & ringkasan ----------

type E6Hasil struct {
	SumberDB          string     `json:"sumber_db"`
	ID                int64      `json:"journal_id"`
	Rank              int        `json:"rank"`
	Nama              string     `json:"nama"`
	PISSN             string     `json:"print_issn"`
	EISSN             string     `json:"electronic_issn"`
	Key               string     `json:"issn_kanonik"`
	Crossref          E6Crossref `json:"crossref"`
	Doaj              E6DOAJ     `json:"doaj"`
	DoajProbe         []string   `json:"doaj_probe,omitempty"` // Opsi B: ISSN kedua utk query DOAJ
	TitleSamaCrossref bool       `json:"title_sama_crossref"`
	TitleSamaDoaj     bool       `json:"title_sama_doaj"`
}

type E6StrataHit struct {
	Sampel     int `json:"sampel"`
	CrossrefOK int `json:"crossref_ok"`
	DoajOK     int `json:"doaj_ok"`
}

type E6Ringkasan struct {
	Sampel            int                    `json:"sampel"`
	CrossrefOK        int                    `json:"crossref_ok"`
	DoajOK            int                    `json:"doaj_ok"`
	Keduanya          int                    `json:"keduanya"`
	SalahSatu         int                    `json:"salah_satu"`
	TidakAda          int                    `json:"tidak_sama_sekali"`
	PerStrata         map[string]E6StrataHit `json:"per_strata"`
	Field             map[string]int         `json:"field_ada"`
	TitleSamaCrossref int                    `json:"title_sama_crossref"`
	TitleSamaDoaj     int                    `json:"title_sama_doaj"`
}

// HitungE6Ringkasan = agregat coverage (dasar keputusan auxiliary weight
// Bagian 6) + fill-rate field per sumber.
func HitungE6Ringkasan(items []E6Hasil) E6Ringkasan {
	r := E6Ringkasan{
		Sampel:    len(items),
		PerStrata: map[string]E6StrataHit{},
		Field:     map[string]int{},
	}
	for _, h := range items {
		strata := "S" + strconv.Itoa(h.Rank)
		s := r.PerStrata[strata]
		s.Sampel++
		switch {
		case h.Crossref.Tersedia && h.Doaj.Tersedia:
			r.Keduanya++
		case h.Crossref.Tersedia:
			r.SalahSatu++
		case h.Doaj.Tersedia:
			r.SalahSatu++
		default:
			r.TidakAda++
		}
		if h.Crossref.Tersedia {
			r.CrossrefOK++
			s.CrossrefOK++
			if h.Crossref.Judul != "" {
				r.Field["crossref.judul"]++
			}
			if h.Crossref.Penerbit != "" {
				r.Field["crossref.penerbit"]++
			}
			if h.Crossref.DoiTotal > 0 {
				r.Field["crossref.doi"]++
			}
			if h.Crossref.AbstractsFrac != nil {
				r.Field["crossref.abstract"]++
			}
			if h.Crossref.LicensesFrac != nil {
				r.Field["crossref.license"]++
			}
			if h.Crossref.OrcidsFrac != nil {
				r.Field["crossref.orcid"]++
			}
			if h.Crossref.RorFrac != nil {
				r.Field["crossref.ror"]++
			}
			if h.TitleSamaCrossref {
				r.TitleSamaCrossref++
			}
		}
		if h.Doaj.Tersedia {
			r.DoajOK++
			s.DoajOK++
			if h.Doaj.Judul != "" {
				r.Field["doaj.judul"]++
			}
			if h.Doaj.Penerbit != "" {
				r.Field["doaj.penerbit"]++
			}
			if len(h.Doaj.Subjek) > 0 {
				r.Field["doaj.subjek"]++
			}
			if h.Doaj.AdaLisensi {
				r.Field["doaj.lisensi"]++
			}
			if h.Doaj.AdaEditorial {
				r.Field["doaj.editorial"]++
			}
			if h.Doaj.AdaBoard {
				r.Field["doaj.board"]++
			}
			if h.TitleSamaDoaj {
				r.TitleSamaDoaj++
			}
		}
		r.PerStrata[strata] = s
	}
	return r
}
