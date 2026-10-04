package garuda

import "testing"

func TestPilihPemenangTahunTahunBeda(t *testing.T) {
	// kasus AGRARIS (j94): 8110 = 2015–2022 vs 35522 = 2015–2026 → 35522 menang.
	ks := []KandidatTahun{
		{GarudaID: 8110, YearTo: 2022, PISSN: "25279239"},
		{GarudaID: 35522, YearTo: 2026, PISSN: "25279239"},
	}
	h := PilihPemenangTahun(ks)
	if h.Idx != 1 {
		t.Errorf("pemenang idx = %d (diharapkan 1 = id35522)", h.Idx)
	}
	if h.Tie {
		t.Error("tahun beda bukan tie")
	}
	if h.Alasan == "" {
		t.Error("alasan kosong — wajib auditable")
	}
}

func TestPilihPemenangTahunTiePissn(t *testing.T) {
	// tahun sama → kandidat dgn P-ISSN terisi menang (kelengkapan data).
	ks := []KandidatTahun{
		{GarudaID: 5000, YearTo: 2025, PISSN: ""},
		{GarudaID: 9000, YearTo: 2025, PISSN: "2442-1101"},
	}
	h := PilihPemenangTahun(ks)
	if h.Idx != 1 {
		t.Errorf("pemenang idx = %d (diharapkan 1 = P-ISSN terisi)", h.Idx)
	}
	if !h.Tie {
		t.Error("tahun sama = wajib Tie")
	}
}

func TestPilihPemenangTahunTieIDKecil(t *testing.T) {
	// tahun sama, P-ISSN sama-sama kosong → id kecil (deterministik).
	ks := []KandidatTahun{
		{GarudaID: 9000, YearTo: 2025},
		{GarudaID: 5000, YearTo: 2025},
	}
	h := PilihPemenangTahun(ks)
	if h.Idx != 1 {
		t.Errorf("pemenang idx = %d (diharapkan 1 = id 5000)", h.Idx)
	}
}

func TestPilihPemenangTahunMatiDanTanpaTahun(t *testing.T) {
	// Record Not Found & tanpa blok tahun = kalah; yang hidup menang.
	ks := []KandidatTahun{
		{GarudaID: 100, YearTo: 0, NotFound: true},
		{GarudaID: 200, YearTo: 0},
		{GarudaID: 300, YearTo: 2024},
	}
	h := PilihPemenangTahun(ks)
	if h.Idx != 2 {
		t.Errorf("pemenang idx = %d (diharapkan 2 = tahun 2024)", h.Idx)
	}
	// semua mati/tanpa tahun → pemenang paksa via tiebreak (tidak -1).
	ks2 := []KandidatTahun{{GarudaID: 7}, {GarudaID: 5}}
	h2 := PilihPemenangTahun(ks2)
	if h2.Idx != 1 || !h2.Tie {
		t.Errorf("semua tanpa tahun = idx %d tie %v (diharapkan 1/true)", h2.Idx, h2.Tie)
	}
	if h := PilihPemenangTahun(nil); h.Idx != -1 {
		t.Errorf("input kosong = idx %d (diharapkan -1)", h.Idx)
	}
}

func TestResolveView(t *testing.T) {
	in := Input{Name: "Jurnal Contoh Ilmu", PISSN: "2442-1101", EISSN: "2580-9912",
		Publisher: "Penerbit Contoh"}

	// (a) E-ISSN view cocok → matched 100 eissn.
	r := ResolveView(in, Candidate{GarudaID: 1, Title: "Jurnal Contoh Ilmu",
		EISSN: "2580-9912", Publisher: "Penerbit Contoh"})
	if r.Status != StatusMatched || r.MatchedBy != "eissn" || r.Confidence != 100 {
		t.Errorf("(a) = %s/%s/%.0f (diharapkan matched/eissn/100)", r.Status, r.MatchedBy, r.Confidence)
	}

	// (b) ISSN sama tapi judul beda total → cross-check gagal → ambiguous.
	r = ResolveView(in, Candidate{GarudaID: 2, Title: "Totally Different Name",
		EISSN: "2580-9912"})
	if r.Status != StatusAmbiguous {
		t.Errorf("(b) = %s (diharapkan ambiguous — cross-check <60)", r.Status)
	}

	// (c) view tanpa ISSN tapi judul identik + publisher cocok → 85 title+publisher.
	r = ResolveView(in, Candidate{GarudaID: 3, Title: "Jurnal Contoh Ilmu",
		Publisher: "Penerbit Contoh"})
	if r.Status != StatusMatched || r.Confidence != 85 || !r.AutoAccept {
		t.Errorf("(c) = %s/%.0f auto=%v (diharapkan matched/85/auto)", r.Status, r.Confidence, r.AutoAccept)
	}

	// (d) view identitas kosong → ambiguous.
	r = ResolveView(in, Candidate{GarudaID: 4})
	if r.Status != StatusAmbiguous {
		t.Errorf("(d) = %s (diharapkan ambiguous)", r.Status)
	}

	// (e) judul cocok tapi publisher beda/bedah → title-only 60 (bukan auto).
	r = ResolveView(in, Candidate{GarudaID: 5, Title: "Jurnal Contoh Ilmu",
		Publisher: "Penerbit Lain"})
	if r.Status != StatusMatched || r.Confidence != 60 || r.AutoAccept {
		t.Errorf("(e) = %s/%.0f auto=%v (diharapkan matched/60/tidak-auto)", r.Status, r.Confidence, r.AutoAccept)
	}
}
