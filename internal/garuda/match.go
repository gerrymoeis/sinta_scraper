// Package garuda berisi mesin pencocokan & enrichment Garuda (Tahap 2,
// doc 30). Match adalah PURE FUNCTION: tanpa network, tanpa DB — diuji
// offline penuh (Q2).
//
// Prinsip K1: normalisasi di package ini HANYA dipakai untuk mencocokkan;
// data asli (Input/Candidate) tidak pernah diubah — string di Go immutable
// dan fungsi-fungsi di sini hanya menghasilkan nilai kembali.
package garuda

import (
	"fmt"
	"strings"
	"unicode"
)

// Konstanta skor — draft sesuai doc 30 §5.2. ANGKA FINAL dikalibrasi
// oleh eksperimen E4 (hit-rate nyata 261 jurnal S1); dikelompokkan di sini
// agar mudah disetel tanpa menyentuh logika.
const (
	skorISSN        = 100.0 // E-ISSN / P-ISSN kanonik sama
	skorTitlePub    = 85.0  // title cocok + publisher tercorroborasi
	skorTitleOnly   = 60.0  // title cocok saja (tanpa bukti kedua)
	ambangTitlePub  = 0.6   // Jaccard minimal utk jalur title+publisher
	ambangTitleOnly = 0.75  // Jaccard minimal utk title-only (lebih ketat)
	ambangCross     = 60.0  // similarity minimal utk cross-check E-ISSN/P-ISSN
	selisihAmbig    = 5.0   // selisih skor top-2 ≤ ini → ambiguous
	autoAcceptMin   = 85.0  // ambang auto-accept (Q2 §5.2)
)

// Status hasil pencocokan — nilai sama dengan kolom
// journal_enrichment.match_status (doc 30 §3.1).
type Status string

const (
	StatusMatched   Status = "matched"
	StatusAmbiguous Status = "ambiguous"
	StatusNotFound  Status = "not_found"
)

// Input = identitas jurnal dari SINTA (data RAW dari Tahap 1, K1).
type Input struct {
	Name      string // judul jurnal (RAW)
	PISSN     string
	EISSN     string
	Publisher string // opsional: affiliation_name SINTA sbg corroboration
}

// Candidate = satu hasil pencarian Garuda (data RAW dari payload).
type Candidate struct {
	GarudaID  int64
	URL       string
	Title     string
	Publisher string // subtitle-journal (RAW)
	PISSN     string
	EISSN     string
}

// Result = keputusan matcher utk satu jurnal.
type Result struct {
	Status     Status     `json:"status"`
	MatchedBy  string     `json:"matched_by,omitempty"` // eissn|pissn|title+publisher|title
	Confidence float64    `json:"confidence"`           // 0..100
	AutoAccept bool       `json:"auto_accept"`          // confidence ≥ autoAcceptMin
	Candidate  *Candidate `json:"candidate,omitempty"`  // kandidat terpilih (bila ada)
	Notes      string     `json:"notes,omitempty"`      // alasan ambiguous/not_found (auditable)
}

// skorKandidat = penilaian satu kandidat pada seluruh ladder (doc 30 §5.1).
// skor 0 = kandidat gugur (tidak lolos ambang mana pun).
type skorKandidat struct {
	idx       int
	sk        float64
	by        string
	sim       float64 // kemiripan title 0..100 (untuk cross-check & notes)
	crossFail bool    // E-ISSN/P-ISSN cocok tapi title kontradiktif
}

// Match memilih kandidat terbaik dari daftar hasil pencarian Garuda.
//
// Aturan (doc 30 §5.2):
//   - ladder: E-ISSN → P-ISSN → title+publisher → title-only;
//   - cross-check: E-ISSN/P-ISSN cocok tapi kemiripan title < 60 → ambiguous
//     (ISSN bisa berpindah / data miring — jangan buta);
//   - selisih skor top-2 ≤ 5 → ambiguous (jangan pilih senyap);
//   - title-only lolos hanya bila Jaccard ≥ 0.75; title+publisher ≥ 0.6;
//   - AutoAccept = Confidence ≥ 85 (draft — dikalibrasi E4).
func Match(in Input, cands []Candidate) Result {
	if len(cands) == 0 {
		return Result{Status: StatusNotFound, Notes: "tidak ada kandidat"}
	}

	// 1) skor semua kandidat, buang yang gugur (skor 0).
	skors := make([]skorKandidat, 0, len(cands))
	for i := range cands {
		s := skorSatu(in, i, cands)
		if s.sk > 0 {
			skors = append(skors, s)
		}
	}
	if len(skors) == 0 {
		return Result{Status: StatusNotFound, Notes: "tidak ada kandidat lolos ladder"}
	}

	// 2) cari top-1 & top-2.
	t1, t2 := 0, -1
	for i := 1; i < len(skors); i++ {
		if skors[i].sk > skors[t1].sk {
			t2 = t1
			t1 = i
		} else if t2 < 0 || skors[i].sk > skors[t2].sk {
			t2 = i
		}
	}

	// 3) aturan ambiguous.
	selisih := 0.0
	if t2 >= 0 {
		selisih = skors[t1].sk - skors[t2].sk
	}
	if skors[t1].crossFail {
		return Result{
			Status: StatusAmbiguous,
			Notes: fmt.Sprintf("%s cocok tapi kemiripan title %.0f < %.0f — perlu verifikasi",
				strings.ToUpper(skors[t1].by), skors[t1].sim, ambangCross),
		}
	}
	if t2 >= 0 && selisih <= selisihAmbig {
		return Result{
			Status: StatusAmbiguous,
			Notes: fmt.Sprintf("selisih skor top-2 %.1f ≤ %.1f (skor %.0f vs %.0f)",
				selisih, selisihAmbig, skors[t1].sk, skors[t2].sk),
		}
	}

	// 4) matched.
	c := cands[skors[t1].idx]
	return Result{
		Status:     StatusMatched,
		MatchedBy:  skors[t1].by,
		Confidence: skors[t1].sk,
		AutoAccept: skors[t1].sk >= autoAcceptMin,
		Candidate:  &c,
	}
}

// skorSatu = satu iterasi ladder utk kandidat pada indeks i.
func skorSatu(in Input, i int, cands []Candidate) skorKandidat {
	c := cands[i]
	out := skorKandidat{idx: i}
	out.sim = TitleSimilarity(in.Name, c.Title)

	// E-ISSN exact (kanonik).
	if a, b := CanonicalISSN(in.EISSN), CanonicalISSN(c.EISSN); a != "" && a == b {
		out.sk, out.by = skorISSN, "eissn"
		out.crossFail = out.sim < ambangCross
		return out
	}
	// P-ISSN exact (kanonik).
	if a, b := CanonicalISSN(in.PISSN), CanonicalISSN(c.PISSN); a != "" && a == b {
		out.sk, out.by = skorISSN, "pissn"
		out.crossFail = out.sim < ambangCross
		return out
	}
	// Title + publisher (corroboration kedua).
	if out.sim >= ambangTitlePub*100 && publisherCocok(in.Publisher, c.Publisher) {
		out.sk, out.by = skorTitlePub, "title+publisher"
		return out
	}
	// Title-only (tanpa bukti kedua → syarat lebih ketat).
	if out.sim >= ambangTitleOnly*100 {
		out.sk, out.by = skorTitleOnly, "title"
		return out
	}
	return out // gugur
}

// TitleSimilarity = kemiripan dua judul dalam skala 0..100:
// 100 bila sama setelah normalisasi, selain itu Jaccard token × 100.
func TitleSimilarity(a, b string) float64 {
	na, nb := NormalizeTitle(a), NormalizeTitle(b)
	if na == "" || nb == "" {
		return 0
	}
	if na == nb {
		return 100
	}
	return Jaccard(Tokens(na), Tokens(nb)) * 100
}

// publisherCocok = corroboration publisher (opsional): norm-equal atau
// salah satu mengandung penuh yang lain. Input kosong → tidak cocok
// (jangan pernah menyetujui sendirian).
func publisherCocok(a, b string) bool {
	na, nb := NormalizeTitle(a), NormalizeTitle(b)
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	return strings.Contains(na, nb) || strings.Contains(nb, na)
}

// NormalizeTitle = normalisasi TAHAP AWAL (moderat, keputusan Q2):
// lowercase → semua karakter selain huruf/digit menjadi spasi →
// collapse spasi → trim. Tidak ada alias/stopword/stemming — tambahan
// hanya bila E4 membuktikan perlu (anti-overengineering).
// HASIL HANYA UNTUK MATCHING — jangan pernah disimpan menimpa data (K1).
func NormalizeTitle(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Tokens = daftar token hasil NormalizeTitle; token 1 karakter dibuang
// (noise) — pola match-candidates.ps1 (len > 1).
func Tokens(s string) []string {
	fields := strings.Fields(NormalizeTitle(s))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len([]rune(f)) > 1 {
			out = append(out, f)
		}
	}
	return out
}

// Jaccard = |A∩B| / |A∪B| atas set token (0..1).
func Jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	setA := make(map[string]bool, len(a))
	for _, t := range a {
		setA[t] = true
	}
	inter := 0
	setB := make(map[string]bool, len(b))
	for _, t := range b {
		if setA[t] {
			inter++
		}
		setB[t] = true
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// CanonicalISSN menormalkan ISSN ke 8 karakter kanonik (7 digit + digit/X)
// tanpa hyphen/spasi. Bila bukan ISSN valid (termasuk placeholder `0`/`-`
// atau varian terpotong) → "" (kosong = tidak dipakai utk pencocokan).
func CanonicalISSN(s string) string {
	var b strings.Builder
	b.Grow(8)
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if (r >= '0' && r <= '9') || r == 'X' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len([]rune(out)) != 8 {
		return ""
	}
	rs := []rune(out)
	for i, r := range rs {
		if i == 7 {
			if r != 'X' && (r < '0' || r > '9') {
				return "" // checksum bukan digit/X
			}
			continue
		}
		if r < '0' || r > '9' {
			return ""
		}
	}
	if strings.Trim(out, "0") == "" {
		return "" // semua nol = placeholder rusak
	}
	return out
}
