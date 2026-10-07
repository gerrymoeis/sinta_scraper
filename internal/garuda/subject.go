package garuda

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Pemisah istilah subject per sumber (L0, doc 30 §13.2):
//   - SINTA : subject_area disimpan koma-separated ("Religion, Humanities, Education").
//   - Garuda: label area = teks <a href="/area/index/N"; BEBERAPA label
//     MEMUAT koma ("Economics, Econometrics & Finance") sehingga koma tidak
//     aman sbg pemisah. " | " dipakai — tidak ada label Garuda memuat "|".
const (
	SintaSubjectSep  = ","
	GarudaSubjectSep = " | "
)

// Sistem, metode alignment (tier §13.2), dan asal baris subject_map.
const (
	SystemSinta  = "sinta"
	SystemGaruda = "garuda"

	MethodExact    = "exact"    // tier 1: fold identik
	MethodContain  = "contain"  // tier 2: subset token, unik
	MethodPrefix   = "prefix"   // tier 3: stem ≥5 huruf, unik
	MethodIdentity = "identity" // tier 5: beda konsep → tampil dua (D3)

	// OriginHarvest  = kanonik = term itu sendiri (baris dari L0, tanpa merge).
	// OriginComputed = kanonik dihitung lewat merge tier 1–3 (baris L2).
	OriginHarvest  = "harvest"
	OriginComputed = "computed"
)

// MapRow = satu baris subject_map (tanpa id/built_at — diisi storage).
type MapRow struct {
	System     string // 'sinta' | 'garuda'
	Term       string // label RAW (K1)
	Key        string // FoldSubject(Term) — kunci join deterministik
	Canonical  string // representatif: label terpanjang, seri → alfabet ASC
	Method     string // exact|contain|prefix|identity
	Support    int    // # jurnal run sbg bukti (§13.2 L1)
	Confidence float64
	Origin     string // harvest|computed
}

// RunPair = pasangan subject satu jurnal hasil binding E-ISSN exact (bahan L1).
type RunPair struct {
	Sinta  []string // token subject SINTA (hasil split koma)
	Garuda []string // label area Garuda
}

// FoldSubject mengubah label subject menjadi kunci fold: lowercase → huruf/
// digit non-ASCII dipertahankan, sisanya jadi spasi → token "and" dibuang
// (setara &↔and) → singular-fold per token → join spasi.
// Idempoten: FoldSubject(FoldSubject(x)) == FoldSubject(x).
func FoldSubject(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	fields := strings.Fields(b.String())
	out := make([]string, 0, len(fields))
	for _, w := range fields {
		if w == "and" {
			continue
		}
		out = append(out, singularFold(w))
	}
	return strings.Join(out, " ")
}

// singularFold membuang akhiran jamak sederhana. Aturan KONSISTEN di kedua
// sumber (hanya mempengaruhi kunci, bukan label tampilan) sehingga
// "Arts"≡"Art", "Sciences"≡"Science" jatuh ke tier exact (§13.2 tier 1).
func singularFold(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y" // studies → study
	case len(w) > 3 && strings.HasSuffix(w, "ss"):
		return w // class/business: jangan dipotong
	case len(w) > 3 && strings.HasSuffix(w, "s"):
		return w[:len(w)-1] // arts → art
	}
	return w
}

// LowEvidence menandai merge tier 1–3 tanpa bukti run (support=0): tetap
// merge (bukti teks kuat) tapi ditandai jujur (flag LOW_EVIDENCE, §13.2 tier 4).
func LowEvidence(r MapRow) bool {
	return r.Method != MethodIdentity && r.Support == 0
}

// BuildSubjectMap membangun kamus term → kanonik (L0+L2, §13.2) TANPA kamus
// hardcoded: vocab murni dari input (harvest sumber), keputusan dari tier
// teks 1–3, angka bukti dari run. Deterministik & idempoten (run ulang =
// byte identik): vocab diurutkan & didedup, keluaran diurut (System, Key).
//
// Tier (kandidat = vocab sistem LAIN, cross-system saja):
//  1. exact    : FoldSubject identik → merge.
//  2. contain  : token(term) ⊆ token(kandidat).
//  3. prefix   : ada pasangan token dgn stem bersama ≥5 huruf.
//     Kandidat tier 2+3 di-union; merge HANYA bila tepat 1 kandidat unik —
//     bila ≥2 (mis. "Health" vs "Public Health" & "Health Professions")
//     → identity, KONSERVATIF: jangan menebak granularitas (prinsip D3).
//  4. (bukti)  : support/co-occurrence TIDAK PERNAH memaksa merge — hanya
//     angka; support=0 pada merge → tetap merge + LowEvidence.
//  5. identity : beda konsep → kedua istilah tampil dua-duanya.
func BuildSubjectMap(sintaVocab, garudaVocab []string, run []RunPair) []MapRow {
	sintaTerms := dedupeVocab(sintaVocab)
	garudaTerms := dedupeVocab(garudaVocab)

	rows := make([]MapRow, 0, len(sintaTerms)+len(garudaTerms))
	rows = append(rows, alignSystem(SystemSinta, sintaTerms, garudaTerms)...)
	rows = append(rows, alignSystem(SystemGaruda, garudaTerms, sintaTerms)...)

	// L1 — angka bukti dari run (per-jurnal set fold gabungan kedua sumber).
	sets := make([]map[string]bool, 0, len(run))
	for _, p := range run {
		all := map[string]bool{}
		for _, t := range p.Sinta {
			if k := FoldSubject(t); k != "" {
				all[k] = true
			}
		}
		for _, t := range p.Garuda {
			if k := FoldSubject(t); k != "" {
				all[k] = true
			}
		}
		sets = append(sets, all)
	}
	for i := range rows {
		key, ck := rows[i].Key, FoldSubject(rows[i].Canonical)
		n := 0
		for _, all := range sets {
			if !all[key] {
				continue
			}
			// identity / canonical = diri sendiri → frekuensi kemunculan;
			// merge lintas sistem → wajib co-occur dgn kanoniknya.
			if rows[i].Method == MethodIdentity || ck == key || all[ck] {
				n++
			}
		}
		rows[i].Support = n
		rows[i].Confidence = confidenceOf(rows[i].Method, n)
	}

	sort.Slice(rows, func(a, b int) bool {
		if rows[a].System != rows[b].System {
			return rows[a].System < rows[b].System
		}
		return rows[a].Key < rows[b].Key
	})
	return rows
}

// confidenceOf = skema draft §13.2 (diuji di test): exact/identity 1.0;
// contain/prefix 0.8 + 0.2×min(support,5)/5 (support 0 → 0.8 + LOW_EVIDENCE).
func confidenceOf(method string, support int) float64 {
	switch method {
	case MethodExact, MethodIdentity:
		return 1.0
	default:
		return 0.8 + 0.2*float64(min(support, 5))/5
	}
}

// alignSystem menempatkan tiap term satu sistem ke kanonik dgn kandidat dari
// sistem lain.
func alignSystem(system string, terms, others []string) []MapRow {
	otherByKey := map[string]string{} // fold → term lain (vocab unik per key)
	for _, o := range others {
		otherByKey[FoldSubject(o)] = o
	}

	rows := make([]MapRow, 0, len(terms))
	for _, t := range terms {
		key := FoldSubject(t)
		if key == "" {
			continue // term tak ber-kunci (karakter non-ASCII semua) — lewati
		}
		row := MapRow{System: system, Term: t, Key: key}

		// Tier 1 — exact.
		if o, ok := otherByKey[key]; ok {
			row.Canonical = representative(t, o)
			row.Method = MethodExact
			row.Origin = OriginComputed
			rows = append(rows, row)
			continue
		}

		// Tier 2+3 — kandidat union (contain ∪ prefix); merge bila unik.
		toks := tokenSet(key)
		contain, prefix := "", ""
		nContain, nPrefix := 0, 0
		for _, o := range others {
			oToks := tokenSet(FoldSubject(o))
			if subset(toks, oToks) {
				nContain++
				contain = o
			} else if sharesStem(toks, oToks) {
				nPrefix++
				prefix = o
			}
		}
		switch {
		case nContain+nPrefix == 1 && nContain == 1:
			row.Canonical = representative(t, contain)
			row.Method = MethodContain
		case nContain+nPrefix == 1 && nPrefix == 1:
			row.Canonical = representative(t, prefix)
			row.Method = MethodPrefix
		default:
			// 0 kandidat ATAU ≥2 kandidat (tak unik) → beda konsep / ragu →
			// tampil dua-duanya (D3.2).
			row.Canonical = t
			row.Method = MethodIdentity
			row.Origin = OriginHarvest
			rows = append(rows, row)
			continue
		}
		row.Origin = OriginComputed
		rows = append(rows, row)
	}
	return rows
}

// representative = label terpanjang sbg wajah merge; seri → alfabet ASC (§13.2).
func representative(a, b string) string {
	ra, rb := utf8.RuneCountInString(a), utf8.RuneCountInString(b)
	if ra != rb {
		if ra > rb {
			return a
		}
		return b
	}
	if a < b {
		return a
	}
	return b
}

// dedupeVocab mengurutkan raw ASC lalu membuang duplikat per fold-key (dan
// term ber-key kosong) — menjamin UNIQUE(source_system, source_key) aman.
func dedupeVocab(v []string) []string {
	s := append([]string(nil), v...)
	sort.Strings(s)
	seen := map[string]bool{}
	out := make([]string, 0, len(s))
	for _, t := range s {
		k := FoldSubject(t)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	return out
}

// tokenSet memecah kunci fold menjadi set token unik.
func tokenSet(key string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.Fields(key) {
		set[w] = true
	}
	return set
}

// subset: semua token a ada di b (a wajib tak kosong; b boleh lebih kaya).
func subset(a, b map[string]bool) bool {
	if len(a) == 0 {
		return false
	}
	for w := range a {
		if !b[w] {
			return false
		}
	}
	return true
}

// sharesStem: ada pasangan token (a∈A, b∈B) dgn prefiks bersama ≥5 huruf.
func sharesStem(a, b map[string]bool) bool {
	for x := range a {
		for y := range b {
			if commonPrefixLen(x, y) >= 5 {
				return true
			}
		}
	}
	return false
}

func commonPrefixLen(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n := min(len(ra), len(rb))
	i := 0
	for i < n && ra[i] == rb[i] {
		i++
	}
	return i
}

// SubjectIndex = indeks kanonik (system → fold-key → canonical) utk union
// per jurnal (L3, §13.2).
type SubjectIndex map[string]map[string]string

// NewSubjectIndex menyusun indeks dari hasil BuildSubjectMap.
func NewSubjectIndex(rows []MapRow) SubjectIndex {
	idx := SubjectIndex{}
	for _, r := range rows {
		if idx[r.System] == nil {
			idx[r.System] = map[string]string{}
		}
		idx[r.System][r.Key] = r.Canonical
	}
	return idx
}

// HarmonizeSubject menyatukan subject SATU jurnal (L3, §13.2):
// map tiap term → kanonik → set_uniq per fold-key (dedup guarantee: output
// tak pernah memuat 2 istilah dgn fold sama — "Education"+"Education",
// "Art"+"Arts" tampil sekali) → sort ASC → join ", ".
// Istilah BEDA tampil bersamaan tanpa flag (pelengkap, D3.2); term tak
// dikenal tetap tampil apa adanya (tak ada data dibuang). Idempoten.
func (idx SubjectIndex) HarmonizeSubject(sintaRaw, garudaRaw string) string {
	seen := map[string]string{} // fold(canonical) → canonical (first-menang)
	add := func(system, raw string) {
		for _, t := range splitSubject(system, raw) {
			k := FoldSubject(t)
			if k == "" {
				continue
			}
			canon, ok := idx[system][k]
			if !ok {
				canon = t // di luar kamus (label baru) → tampil apa adanya
			}
			ck := FoldSubject(canon)
			if ck == "" {
				continue
			}
			if _, dup := seen[ck]; !dup {
				seen[ck] = canon
			}
		}
	}
	add(SystemSinta, sintaRaw)
	add(SystemGaruda, garudaRaw)

	out := make([]string, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// splitSubject memecah raw subject sesuai konvensi sumbernya.
func splitSubject(system, raw string) []string {
	sep := SintaSubjectSep
	if system == SystemGaruda {
		sep = GarudaSubjectSep
	}
	parts := strings.Split(raw, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// SplitSintaSubject / SplitGarudaSubject = pemecah resmi utk pemanggil luar
// (harvest & pembangun run-pair) supaya pemisah TIDAK pernah drift dari
// aturan HarmonizeSubject.
func SplitSintaSubject(raw string) []string  { return splitSubject(SystemSinta, raw) }
func SplitGarudaSubject(raw string) []string { return splitSubject(SystemGaruda, raw) }

// MergeSubjectArea = fill/append subject_area E7f (Opsi A — doc 38 §13 butir 4,
// approve user 6 Okt 2026): gabung istilah baru dgn nilai existing DEDUP per
// FoldSubject ("politics"≡"politic" lewat suffix-s, "Arts"≡"Art" lewat
// singular-fold → tak diduplikat; beda konsep tetap tampil dua — TANPA
// contain/fuzzy yg ambigu, doc 38 §13 butir 4 opsi B/C ditolak).
//
// Invariant:
//   - K1: istilah existing dipertahankan APA ADANYA (tidak disaring/diurutkan
//     ulang — nilai lama tak pernah berubah bentuk);
//   - istilah baru diurutkan ASC lalu di-append (deterministik);
//   - idempoten: MergeSubjectArea(x, MergeSubjectArea-y-ish) — panggilan ke-2
//     dgn istilah sama = byte identik (semua sudah di seen);
//   - existing kosong → hasil = istilah baru terurut dedup.
func MergeSubjectArea(existing string, incoming []string) string {
	seen := map[string]bool{}
	out := make([]string, 0, len(SplitSintaSubject(existing))+len(incoming))
	for _, t := range SplitSintaSubject(existing) {
		out = append(out, t)
		if k := FoldSubject(t); k != "" {
			seen[k] = true
		}
	}
	var baru []string
	for _, t := range incoming {
		t = strings.TrimSpace(t)
		k := FoldSubject(t)
		if t == "" || k == "" || seen[k] {
			continue
		}
		seen[k] = true
		baru = append(baru, t)
	}
	sort.Strings(baru)
	out = append(out, baru...)
	return strings.Join(out, ", ")
}
