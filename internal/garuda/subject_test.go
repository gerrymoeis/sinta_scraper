package garuda

import (
	"reflect"
	"testing"
)

// ---------- skenario mini (vocab + run) dipakai beberapa test ----------

func vocabSintaMini() []string {
	return []string{
		"Education", "Science", "Religion", "Humanities", "Health",
		"Agriculture", "Art", "Engineering", "Social", "Economy",
	}
}

func vocabGarudaMini() []string {
	return []string{
		"Education",
		"Arts",
		"Religion",
		"Humanities",
		"Engineering",
		"Civil Engineering",
		"Social Sciences",
		"Agriculture, Biological Sciences & Forestry",
		"Economics, Econometrics & Finance",
		"Public Health",
		"Health Professions",
		"Nursing",
	}
}

func runMini() []RunPair {
	return []RunPair{
		{Sinta: []string{"Social"}, Garuda: []string{"Social Sciences"}}, // ×5 → support 5
		{Sinta: []string{"Social"}, Garuda: []string{"Social Sciences"}},
		{Sinta: []string{"Social"}, Garuda: []string{"Social Sciences"}},
		{Sinta: []string{"Social"}, Garuda: []string{"Social Sciences"}},
		{Sinta: []string{"Social"}, Garuda: []string{"Social Sciences"}},
		{Sinta: []string{"Engineering"}, Garuda: []string{"Civil Engineering"}},
		{Sinta: []string{"Health", "Education"}, Garuda: []string{"Nursing"}},
	}
}

func findRow(t *testing.T, rows []MapRow, system, term string) MapRow {
	t.Helper()
	key := FoldSubject(term)
	for _, r := range rows {
		if r.System == system && r.Key == key {
			return r
		}
	}
	t.Fatalf("baris (%s, %q/key=%q) tidak ditemukan", system, term, key)
	return MapRow{}
}

func TestFoldSubject(t *testing.T) {
	kasus := []struct{ in, want string }{
		{"Art", "art"},
		{"Arts", "art"}, // singular-fold → tier exact Art≡Arts
		{"Social Sciences", "social science"},
		{"Economics, Econometrics & Finance", "economic econometric finance"}, // singular-fold konsisten dgn "economy"→tier prefix tetap jalan
		{"Research & Development", "research development"},
		{"Research and Development", "research development"}, // & ↔ and
		{"R&D", "r d"},
		{"R and D", "r d"},
		{"  ScIence  ", "science"},
		{"Business", "business"}, // akhiran "ss" tidak dipotong
		{"Studies", "study"},
		{"Health Professions", "health profession"},
		{"Kesehatan", "kesehatan"},
		{"", ""},
	}
	for _, k := range kasus {
		got := FoldSubject(k.in)
		if got != k.want {
			t.Errorf("FoldSubject(%q) = %q, want %q", k.in, got, k.want)
		}
		if got2 := FoldSubject(got); got2 != got {
			t.Errorf("FoldSubject tidak idempoten utk %q: %q vs %q", k.in, got2, got)
		}
	}
}

func TestBuildSubjectMapTier(t *testing.T) {
	rows := BuildSubjectMap(vocabSintaMini(), vocabGarudaMini(), runMini())

	t.Run("tier1 exact", func(t *testing.T) {
		kasus := []struct{ system, term, canonical string }{
			{SystemSinta, "Art", "Arts"},              // fold identik, rep terpanjang
			{SystemSinta, "Education", "Education"},   // exact
			{SystemSinta, "Humanities", "Humanities"}, // exact menang di atas contain
			{SystemGaruda, "Arts", "Arts"},
			{SystemGaruda, "Education", "Education"},
		}
		for _, k := range kasus {
			r := findRow(t, rows, k.system, k.term)
			if r.Method != MethodExact {
				t.Errorf("%s %q: method = %s, want exact", k.system, k.term, r.Method)
			}
			if r.Canonical != k.canonical {
				t.Errorf("%s %q: canonical = %q, want %q", k.system, k.term, r.Canonical, k.canonical)
			}
			if r.Origin != OriginComputed {
				t.Errorf("%s %q: origin = %q, want computed", k.system, k.term, r.Origin)
			}
			if r.Confidence != 1.0 {
				t.Errorf("%s %q: confidence = %v, want 1.0", k.system, k.term, r.Confidence)
			}
		}
	})

	t.Run("tier2 contain unik", func(t *testing.T) {
		kasus := []struct{ system, term, canonical string }{
			{SystemSinta, "Social", "Social Sciences"},
			{SystemSinta, "Agriculture", "Agriculture, Biological Sciences & Forestry"},
		}
		for _, k := range kasus {
			r := findRow(t, rows, k.system, k.term)
			if r.Method != MethodContain {
				t.Errorf("%s %q: method = %s, want contain", k.system, k.term, r.Method)
			}
			if r.Canonical != k.canonical {
				t.Errorf("%s %q: canonical = %q, want %q", k.system, k.term, r.Canonical, k.canonical)
			}
			if r.Origin != OriginComputed {
				t.Errorf("%s %q: origin = %q, want computed", k.system, k.term, r.Origin)
			}
		}
	})

	t.Run("tier3 prefix unik", func(t *testing.T) {
		r := findRow(t, rows, SystemSinta, "Economy")
		if r.Method != MethodPrefix {
			t.Errorf("Economy: method = %s, want prefix", r.Method)
		}
		want := "Economics, Econometrics & Finance"
		if r.Canonical != want {
			t.Errorf("Economy: canonical = %q, want %q", r.Canonical, want)
		}
		if r.Origin != OriginComputed {
			t.Errorf("Economy: origin = %q, want computed", r.Origin)
		}
	})

	t.Run("tier exact menang di atas contain/prefix", func(t *testing.T) {
		// "Engineering" punya padanan exact DAN terkandung di "Civil Engineering"
		// → tier 1 wajib menang (urutan tier).
		r := findRow(t, rows, SystemSinta, "Engineering")
		if r.Method != MethodExact || r.Canonical != "Engineering" {
			t.Errorf("Engineering: method=%s canonical=%q, want exact/Engineering", r.Method, r.Canonical)
		}
	})

	t.Run(">=2 kandidat → identity (konservatif)", func(t *testing.T) {
		// "Health" terkandung di "Public Health" & "Health Professions" →
		// tak unik → kedua konsep tampil dua (D3.2).
		r := findRow(t, rows, SystemSinta, "Health")
		if r.Method != MethodIdentity || r.Canonical != "Health" {
			t.Errorf("Health: method=%s canonical=%q, want identity/Health", r.Method, r.Canonical)
		}
		if r.Origin != OriginHarvest {
			t.Errorf("Health: origin = %q, want harvest", r.Origin)
		}
		// "Science" terkandung di "Social Sciences" & "Agriculture, ... Forestry"
		r2 := findRow(t, rows, SystemSinta, "Science")
		if r2.Method != MethodIdentity {
			t.Errorf("Science: method = %s, want identity", r2.Method)
		}
		// tanpa kandidat sama sekali → identity
		r3 := findRow(t, rows, SystemGaruda, "Nursing")
		if r3.Method != MethodIdentity || r3.Canonical != "Nursing" {
			t.Errorf("Nursing: method=%s canonical=%q, want identity/Nursing", r3.Method, r3.Canonical)
		}
	})
}

func TestBuildSubjectMapDeterministik(t *testing.T) {
	a := BuildSubjectMap(vocabSintaMini(), vocabGarudaMini(), runMini())
	b := BuildSubjectMap(vocabSintaMini(), vocabGarudaMini(), runMini())
	if !reflect.DeepEqual(a, b) {
		t.Error("BuildSubjectMap tidak deterministik (2x run beda)")
	}
	// urutan keluaran: (system, key) menaik — idempoten byte-identik
	for i := 1; i < len(a); i++ {
		prev, cur := a[i-1], a[i]
		if prev.System > cur.System || (prev.System == cur.System && prev.Key >= cur.Key) {
			t.Fatalf("keluaran tidak terurut di baris %d: (%s,%s) → (%s,%s)",
				i, prev.System, prev.Key, cur.System, cur.Key)
		}
	}
	// kunci unik per sistem (UNIQUE(source_system, source_key) aman)
	seen := map[string]bool{}
	for _, r := range a {
		k := r.System + "\x00" + r.Key
		if seen[k] {
			t.Fatalf("duplikat kunci: %s %s", r.System, r.Key)
		}
		seen[k] = true
	}
}

func TestBuktiTeksWajibUntukMerge(t *testing.T) {
	// Tier 1–3 hanya boleh merge BILA ada bukti teks; tanpa bukti → identity
	// (pasangan co-occurrence di run TIDAK PERNAH memaksa merge — §13.6).
	rows := BuildSubjectMap(vocabSintaMini(), vocabGarudaMini(), runMini())
	for _, r := range rows {
		key, canon := r.Key, FoldSubject(r.Canonical)
		switch r.Method {
		case MethodExact:
			if key != canon {
				t.Errorf("%s %q: exact tapi fold beda (%q vs %q)", r.System, r.Term, key, canon)
			}
		case MethodContain:
			if !subset(tokenSet(key), tokenSet(canon)) {
				t.Errorf("%s %q: contain tapi token tidak subset dari canonical %q", r.System, r.Term, r.Canonical)
			}
		case MethodPrefix:
			if !sharesStem(tokenSet(key), tokenSet(canon)) {
				t.Errorf("%s %q: prefix tapi tanpa stem ≥5 dari canonical %q", r.System, r.Term, r.Canonical)
			}
		case MethodIdentity:
		default:
			t.Errorf("%s %q: method tak dikenal %q", r.System, r.Term, r.Method)
		}
	}
	// pasangan sebab-akib tak berhubungan tetap identity dua-duanya
	h := findRow(t, rows, SystemSinta, "Health")
	n := findRow(t, rows, SystemGaruda, "Nursing")
	if h.Method != MethodIdentity || n.Method != MethodIdentity {
		t.Error("Health/Nursing harus identity dua-duanya (tak boleh di-merge)")
	}
}

func TestSupportDanConfidence(t *testing.T) {
	rows := BuildSubjectMap(vocabSintaMini(), vocabGarudaMini(), runMini())
	kasus := []struct {
		system, term string
		support      int
		confidence   float64
		lowEvidence  bool
	}{
		{SystemSinta, "Social", 5, 1.0, false},      // contain support 5 → 0.8+0.2
		{SystemSinta, "Engineering", 1, 1.0, false}, // exact → 1.0
		{SystemSinta, "Health", 1, 1.0, false},      // identity → 1.0 (freq 1)
		{SystemSinta, "Agriculture", 0, 0.8, true},  // merge tanpa bukti run
		{SystemSinta, "Economy", 0, 0.8, true},      // prefix support 0
		{SystemSinta, "Education", 1, 1.0, false},   // exact, ck==key → frekuensi
	}
	for _, k := range kasus {
		r := findRow(t, rows, k.system, k.term)
		if r.Support != k.support {
			t.Errorf("%s %q: support = %d, want %d", k.system, k.term, r.Support, k.support)
		}
		if diff := r.Confidence - k.confidence; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s %q: confidence = %v, want %v", k.system, k.term, r.Confidence, k.confidence)
		}
		if got := LowEvidence(r); got != k.lowEvidence {
			t.Errorf("%s %q: LowEvidence = %v, want %v", k.system, k.term, got, k.lowEvidence)
		}
	}
}

func TestLowEvidenceHanyaUntukMerge(t *testing.T) {
	kasus := []struct {
		row  MapRow
		want bool
	}{
		{MapRow{Method: MethodContain, Support: 0}, true},
		{MapRow{Method: MethodPrefix, Support: 0}, true},
		{MapRow{Method: MethodExact, Support: 0}, true}, // merge tanpa bukti run → tetap ditandai
		{MapRow{Method: MethodContain, Support: 1}, false},
		{MapRow{Method: MethodIdentity, Support: 0}, false}, // bukan merge
	}
	for _, k := range kasus {
		if got := LowEvidence(k.row); got != k.want {
			t.Errorf("LowEvidence(%s,%d) = %v, want %v", k.row.Method, k.row.Support, got, k.want)
		}
	}
}

func TestHarmonizeSubject(t *testing.T) {
	idx := NewSubjectIndex(BuildSubjectMap(vocabSintaMini(), vocabGarudaMini(), runMini()))

	kasus := []struct {
		name, sintaRaw, garudaRaw, want string
	}{
		{"beda konsep tampil dua (contoh tier5)", "Health", "Nursing", "Health, Nursing"},
		{"dedup exact", "Education", "Education", "Education"},
		{"dedup Art+Arts → sekali", "Art", "Arts", "Arts"},
		{"dedup subset kanonik", "Social", "Social Sciences", "Social Sciences"},
		{"satu sumber saja", "Religion, Humanities, Education", "", "Education, Humanities, Religion"},
		{"kedua sumber kosong", "", "", ""},
		{"hanya garuda", "", "Civil Engineering", "Civil Engineering"},
		{"term di luar kamus tetap tampil", "Finance", "", "Finance"},
		{"nilai A dipertahankan (subset murni §9.3.b.3)", "Religion, Education", "Education", "Education, Religion"},
		{"pelengkap beda sumber terurut", "Economy", "Nursing", "Economics, Econometrics & Finance, Nursing"},
		{"token spasi/kotor diabaikan", " , Education , ", "", "Education"},
	}
	for _, k := range kasus {
		got := idx.HarmonizeSubject(k.sintaRaw, k.garudaRaw)
		if got != k.want {
			t.Errorf("%s: HarmonizeSubject(%q, %q) = %q, want %q",
				k.name, k.sintaRaw, k.garudaRaw, got, k.want)
		}
		// idempoten byte-identik
		if got2 := idx.HarmonizeSubject(k.sintaRaw, k.garudaRaw); got2 != got {
			t.Errorf("%s: tidak idempoten: %q vs %q", k.name, got2, got)
		}
	}
}

func TestHarmonizeDedupGaransi(t *testing.T) {
	// Jaminan §13.2 L3: output tak pernah memuat 2 istilah fold-sama —
	// diverifikasi lewat kasus dedup konkret + kanonik hasil merge identik.
	idx := NewSubjectIndex(BuildSubjectMap(vocabSintaMini(), vocabGarudaMini(), runMini()))
	kasus := []struct{ sintaRaw, garudaRaw string }{
		{"Art", "Arts"},
		{"Education, Art", "Arts | Education"}, // raw garuda = label join " | " (konvensi harvest)
		{"Social", "Social Sciences"},
		{"Science, Social", "Social Sciences"},
	}
	for _, k := range kasus {
		out := idx.HarmonizeSubject(k.sintaRaw, k.garudaRaw)
		if out == "" {
			t.Errorf("(%q, %q): output kosong", k.sintaRaw, k.garudaRaw)
		}
		if out != idx.HarmonizeSubject(k.sintaRaw, k.garudaRaw) {
			t.Errorf("(%q, %q): tidak idempoten", k.sintaRaw, k.garudaRaw)
		}
	}
	if got := idx.HarmonizeSubject("Education, Art", "Arts | Education"); got != "Arts, Education" {
		t.Errorf("dedup lintas sumber = %q, want %q", got, "Arts, Education")
	}
}

func TestSplitSubjectRoundTrip(t *testing.T) {
	// Label Garuda MEMUAT koma → " | " wajib aman sbg pemisah.
	labels := []string{"Economics, Econometrics & Finance", "Education", "Public Health"}
	joined := ""
	for i, l := range labels {
		if i > 0 {
			joined += GarudaSubjectSep
		}
		joined += l
	}
	got := SplitGarudaSubject(joined)
	if !reflect.DeepEqual(got, labels) {
		t.Errorf("round-trip garuda = %q, want %q", got, labels)
	}

	sinta := "Religion, Humanities, Education"
	if got := SplitSintaSubject(sinta); !reflect.DeepEqual(got, []string{"Religion", "Humanities", "Education"}) {
		t.Errorf("split sinta = %q", got)
	}
}

// ---------- MergeSubjectArea (E7f Opsi A — doc 38 §13 butir 4) ----------

func TestMergeSubjectArea(t *testing.T) {
	kasus := []struct {
		existing string
		incoming []string
		want     string
	}{
		// fill (existing kosong) → istilah baru terurut ASC.
		{"", []string{"Language and Literature", "Philology. Linguistics"},
			"Language and Literature, Philology. Linguistics"},
		// append → existing K1 dipertahankan apa adanya (tak diurutkan ulang).
		{"Social, Economic", []string{"Society"}, "Social, Economic, Society"},
		// dedup per FoldSubject: case + singular-fold.
		{"Education", []string{"education", "Arts"}, "Education, Arts"},
		{"Art", []string{"Arts"}, "Art"},
		// suffix-s: politics≡politic (opsi B/C ditolak — fold saja, tanpa fuzzy).
		{"Politics", []string{"Politic"}, "Politics"},
		// duplikat dlm incoming sendiri juga didedup.
		{"", []string{"Politics", "Politic"}, "Politics"},
		// term kosong / fold kosong dilewati.
		{"Education", []string{"", "  "}, "Education"},
	}
	for _, k := range kasus {
		if got := MergeSubjectArea(k.existing, k.incoming); got != k.want {
			t.Errorf("MergeSubjectArea(%q, %q) = %q, want %q",
				k.existing, k.incoming, got, k.want)
		}
	}
}

func TestMergeSubjectAreaIdempoten(t *testing.T) {
	sekali := MergeSubjectArea("Education", []string{"Society", "arts"})
	kali2 := MergeSubjectArea(sekali, []string{"Society", "arts"})
	if kali2 != sekali {
		t.Errorf("ulang = %q, want byte identik %q", kali2, sekali)
	}
	if got := MergeSubjectArea("", nil); got != "" {
		t.Errorf("kosong = %q, want \"\"", got)
	}
}
