package garuda

import (
	"testing"
)

func TestCanonicalISSN(t *testing.T) {
	kasus := []struct{ in, want string }{
		{"2442-8620", "24428620"},   // varian hyphen (fakta doc 12)
		{" 2442 8620 ", "24428620"}, // varian spasi
		{"1234-567X", "1234567X"},   // checksum X
		{"1234567x", "1234567X"},    // huruf kecil → besar
		{"0", ""},                   // placeholder (K5: tolak)
		{"-", ""},                   // placeholder
		{"", ""},
		{"1234567", ""},   // 7 digit = terpotong
		{"123456789", ""}, // 9 digit
		{"ABCDEFGH", ""},  // bukan ISSN
		{"00000000", ""},  // semua nol = placeholder rusak
		{"1234-567", ""},  // 7 digit setelah norm
	}
	for _, k := range kasus {
		if got := CanonicalISSN(k.in); got != k.want {
			t.Errorf("CanonicalISSN(%q) = %q, want %q", k.in, got, k.want)
		}
	}
}

func TestNormalizeTitle(t *testing.T) {
	kasus := []struct{ in, want string }{
		{"Religion, Humanities, Education", "religion humanities education"}, // vocab SINTA asli
		{"A&B: C-D", "a b c d"},
		{"  Jurnal   Of  Physics  ", "jurnal of physics"},
		{"COVID-19 & Kesehatan!", "covid 19 kesehatan"},
		{"P\u00C9NSIUN", "p\u00E9nsiun"}, // É → é: huruf non-ASCII dipertahankan (bukan dihapus)
		{"", ""},
	}
	for _, k := range kasus {
		got := NormalizeTitle(k.in)
		if got != k.want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", k.in, got, k.want)
		}
		// idempoten: normalisasi 2× = normalisasi 1×
		if got2 := NormalizeTitle(got); got2 != got {
			t.Errorf("NormalizeTitle tidak idempoten utk %q: %q vs %q", k.in, got, got2)
		}
	}
}

func TestTitleSimilarityDanJaccard(t *testing.T) {
	if s := TitleSimilarity("Jurnal Pendidikan", "jurnal   PENDIDIKAN"); s != 100 {
		t.Errorf("beda case/spasi seharusnya 100, dapat %.1f", s)
	}
	if s := TitleSimilarity("Jurnal Pendidikan", "Majalah Kedokteran Nusantara"); s != 0 {
		t.Errorf("judul beda total seharusnya 0, dapat %.1f", s)
	}
	if s := TitleSimilarity("", "apa saja"); s != 0 {
		t.Errorf("input kosong seharusnya 0, dapat %.1f", s)
	}
	// parsial: 3 dari 4 token → 0.75 → 75
	if s := TitleSimilarity("Jurnal Ilmu Komputer", "Jurnal Ilmu Komputer Lanjutan"); s < 74.9 || s > 75.1 {
		t.Errorf("Jaccard parsial seharusnya ~75, dapat %.1f", s)
	}
	if j := Jaccard([]string{"a", "b"}, []string{"a", "b"}); j != 1 {
		t.Errorf("Jaccard identik = %v, want 1", j)
	}
	if j := Jaccard(nil, []string{"a"}); j != 0 {
		t.Errorf("Jaccard kosong = %v, want 0", j)
	}
}

func TestMatch(t *testing.T) {
	const eissn = "24428620"
	kasus := []struct {
		nama        string
		in          Input
		cands       []Candidate
		wantStatus  Status
		wantMatchBy string
		wantConf    float64
		wantAuto    bool
	}{
		{
			nama:       "tanpa kandidat → not_found",
			in:         Input{Name: "Jurnal Apa Saja"},
			cands:      nil,
			wantStatus: StatusNotFound,
		},
		{
			nama: "E-ISSN cocok + title mirip → matched eissn",
			in:   Input{Name: "Jurnal Pendidikan Nusantara", EISSN: "2442-8620"},
			cands: []Candidate{
				{GarudaID: 445, Title: "Jurnal Pendidikan Nusantara", EISSN: eissn},
			},
			wantStatus: StatusMatched, wantMatchBy: "eissn", wantConf: 100, wantAuto: true,
		},
		{
			nama: "E-ISSN cocok tapi title kontradiktif → ambiguous (cross-check)",
			in:   Input{Name: "Jurnal Pendidikan Nusantara", EISSN: eissn},
			cands: []Candidate{
				{GarudaID: 9, Title: "Majalah Kedokteran Nusantara", EISSN: eissn},
			},
			wantStatus: StatusAmbiguous,
		},
		{
			nama: "dua kandidat E-ISSN sama (selisih 0) → ambiguous",
			in:   Input{Name: "Jurnal Pendidikan Nusantara", EISSN: eissn},
			cands: []Candidate{
				{GarudaID: 1, Title: "Jurnal Pendidikan Nusantara", EISSN: eissn},
				{GarudaID: 2, Title: "Jurnal Pendidikan Nusantara Edition", EISSN: eissn},
			},
			wantStatus: StatusAmbiguous,
		},
		{
			nama: "P-ISSN cocok → matched pissn",
			in:   Input{Name: "X Y", PISSN: "1234-5678"},
			cands: []Candidate{
				{GarudaID: 77, Title: "X Y", PISSN: "12345678"},
			},
			wantStatus: StatusMatched, wantMatchBy: "pissn", wantConf: 100, wantAuto: true,
		},
		{
			nama: "title + publisher cocok → matched title+publisher (85)",
			in:   Input{Name: "Buletin Psikologi", Publisher: "Universitas Gadjah Mada"},
			cands: []Candidate{
				{GarudaID: 3, Title: "Buletin Psikologi Terapan", Publisher: "Universitas Gadjah Mada"},
			},
			wantStatus: StatusMatched, wantMatchBy: "title+publisher", wantConf: 85, wantAuto: true,
		},
		{
			nama: "title-only lolos (Jaccard 0.75) → matched title (60), TANPA auto-accept",
			in:   Input{Name: "Jurnal Ilmu Komputer"},
			cands: []Candidate{
				{GarudaID: 4, Title: "Jurnal Ilmu Komputer Lanjutan"},
			},
			wantStatus: StatusMatched, wantMatchBy: "title", wantConf: 60, wantAuto: false,
		},
		{
			nama: "title Jaccard 0.60 tanpa publisher → gugur semua ladder → not_found",
			in:   Input{Name: "Jurnal Ilmu Komputer"},
			cands: []Candidate{
				{GarudaID: 5, Title: "Jurnal Ilmu Komputer Lanjutan XYZ"},
			},
			wantStatus: StatusNotFound,
		},
		{
			nama: "E-ISSN menang telak atas title (selisih 40) → matched eissn",
			in:   Input{Name: "Jurnal Nasional", EISSN: eissn},
			cands: []Candidate{
				{GarudaID: 6, Title: "Jurnal Nasional"},                      // title-only, skor 60
				{GarudaID: 7, Title: "Jurnal Nasional Khusus", EISSN: eissn}, // skor 100, sim 66 ≥ 60
			},
			wantStatus: StatusMatched, wantMatchBy: "eissn", wantConf: 100, wantAuto: true,
		},
	}

	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			r := Match(k.in, k.cands)
			if r.Status != k.wantStatus {
				t.Fatalf("status = %q (%s), want %q", r.Status, r.Notes, k.wantStatus)
			}
			if r.Status == StatusMatched {
				if r.MatchedBy != k.wantMatchBy {
					t.Errorf("matched_by = %q, want %q", r.MatchedBy, k.wantMatchBy)
				}
				if r.Confidence != k.wantConf {
					t.Errorf("confidence = %.0f, want %.0f", r.Confidence, k.wantConf)
				}
				if r.AutoAccept != k.wantAuto {
					t.Errorf("auto_accept = %v, want %v", r.AutoAccept, k.wantAuto)
				}
				if r.Candidate == nil {
					t.Error("Candidate = nil padahal matched")
				}
			}
			if r.Status == StatusAmbiguous && r.Notes == "" {
				t.Error("ambiguous harus punya Notes (auditable)")
			}
		})
	}
}

// TestMatchIssuerKandidatTerpilih memastikan pointer hasil menunjuk
// kandidat yang benar (bukan kandidat pertama secara kebetulan).
func TestMatchKandidatTerpilih(t *testing.T) {
	in := Input{Name: "Jurnal A", EISSN: "24428620"}
	cands := []Candidate{
		{GarudaID: 1, Title: "Jurnal B", EISSN: "11111111"},
		{GarudaID: 2, Title: "Jurnal A", EISSN: "24428620"},
	}
	r := Match(in, cands)
	if r.Status != StatusMatched || r.Candidate == nil {
		t.Fatalf("harus matched: %+v", r)
	}
	if r.Candidate.GarudaID != 2 {
		t.Errorf("kandidat terpilih ID=%d, want 2", r.Candidate.GarudaID)
	}
}
