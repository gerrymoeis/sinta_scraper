package garuda

import (
	"fmt"
	"strings"
)

// E4b (doc 30 §14.3 — disetujui user 4 Okt 2026): resolusi duplikat via
// year-range check + resolve view. Fungsi di sini PURE (tanpa network/DB)
// agar bisa diuji offline penuh — driver fetch/tulis ada di cmd/garuda.

// KandidatTahun = ringkasan satu kandidat duplikat utk pemilihan pemenang.
type KandidatTahun struct {
	GarudaID int64
	YearTo   int    // tahun akhir blok "Filter by Year" (0 = tak ada/mati)
	NotFound bool   // halaman view = "Record Not Found"
	PISSN    string // P-ISSN baris search (utk tiebreak kelengkapan)
}

// HasilPilihan = pemenang year-range check utk satu jurnal.
type HasilPilihan struct {
	Idx    int    // indeks pemenang pada slice input; -1 = tak ada pemenang
	Alasan string // penjelasan auditable utk laporan/JSON
	Tie    bool   // pemenang lewat tiebreak (tahun sama / tanpa tahun)
}

// PilihPemenangTahun menentukan kandidat entri Garuda yang AKTIF dari blok
// "Filter by Year" halaman view (temuan user 4 Okt 2026 — doc 32 §4):
//
//  1. skor utama = YearTo terbesar (entri masih di-update Garuda);
//  2. kandidat NotFound / tanpa tahun (0) = KALAH otomatis;
//  3. tie YearTo → P-ISSN terisi menang (kelengkapan data);
//  4. masih tie → GarudaID kecil (deterministik);
//  5. semua kalah → tetap jalankan tiebreak 3–4 (pilihan terpaksa, ditandai).
func PilihPemenangTahun(ks []KandidatTahun) HasilPilihan {
	if len(ks) == 0 {
		return HasilPilihan{Idx: -1, Alasan: "tanpa kandidat"}
	}
	// skor: hidup & punya tahun → YearTo; selain itu -1 (kalah).
	skor := make([]int, len(ks))
	for i, k := range ks {
		if !k.NotFound && k.YearTo > 0 {
			skor[i] = k.YearTo
		} else {
			skor[i] = -1
		}
	}
	// cari skor tertinggi.
	best := 0
	for i := 1; i < len(skor); i++ {
		if skor[i] > skor[best] {
			best = i
		}
	}
	// kumpulkan yang setara skor (tiebreak).
	var setara []int
	for i, s := range skor {
		if s == skor[best] {
			setara = append(setara, i)
		}
	}
	if len(setara) == 1 {
		h := HasilPilihan{Idx: setara[0]}
		if skor[best] < 0 {
			h.Tie = true
			h.Alasan = "semua kandidat tanpa tahun/mati → pemenang paksa"
		} else {
			h.Alasan = fmt.Sprintf("tahun akhir %d tertinggi", skor[best])
		}
		return h
	}
	// tie: (1) P-ISSN kanonik terisi; (2) GarudaID kecil.
	pemenang := setara[0]
	for _, i := range setara[1:] {
		piMenang := CanonicalISSN(ks[pemenang].PISSN) != ""
		piI := CanonicalISSN(ks[i].PISSN) != ""
		switch {
		case piI && !piMenang:
			pemenang = i
		case piI == piMenang && ks[i].GarudaID < ks[pemenang].GarudaID:
			pemenang = i
		}
	}
	h := HasilPilihan{Idx: pemenang, Tie: true}
	if skor[best] < 0 {
		h.Alasan = "semua kandidat tanpa tahun/mati → tiebreak (P-ISSN/id kecil)"
		return h
	}
	penjelas := "id kecil menang"
	if CanonicalISSN(ks[pemenang].PISSN) != "" &&
		CanonicalISSN(ks[setara[0]].PISSN) == "" {
		penjelas = "P-ISSN terisi menang"
	}
	h.Alasan = fmt.Sprintf("tie tahun %d → %s", skor[best], penjelas)
	return h
}

// AmbangCrossMin = ambang cross-check title (60) — di-export utk driver E4b
// (hukum tulis: pemenang tahun dgn sim < 60 masuk review, bukan tulis).
func AmbangCrossMin() float64 { return ambangCross }

// ResolveView menilai SATU kandidat hasil halaman view/N terhadap identitas
// SINTA memakai ladder yang sama (§5.2) — dipakai utk resolve jurnal
// not_found/URL-Tahap-1 (E4b fase C). Kandidat = field terparsing dari view
// (GarudaID diisi driver setelahnya).
//
// Kembalikan: matched (skor ladder, confidence jujur 60/85/100) ATAU
// ambiguous (tak lolos / cross-check gagal → driver TIDAK menulis).
func ResolveView(in Input, c Candidate) Result {
	if c.Title == "" && c.EISSN == "" && c.PISSN == "" {
		return Result{Status: StatusAmbiguous, Notes: "halaman view tanpa identitas (judul/ISSN kosong)"}
	}
	s := skorSatu(in, 0, []Candidate{c})
	if s.sk == 0 {
		return Result{Status: StatusAmbiguous, Notes: fmt.Sprintf("view tak lolos ladder (sim title %.0f)", s.sim)}
	}
	if s.crossFail {
		return Result{
			Status: StatusAmbiguous,
			Notes: fmt.Sprintf("%s cocok tapi kemiripan title %.0f < %.0f — perlu verifikasi",
				strings.ToUpper(s.by), s.sim, ambangCross),
		}
	}
	return Result{
		Status:     StatusMatched,
		MatchedBy:  s.by,
		Confidence: s.sk,
		AutoAccept: s.sk >= autoAcceptMin,
		Candidate:  &c,
	}
}
