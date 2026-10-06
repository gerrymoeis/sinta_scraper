package garuda

// E7d — hyphen-sweep 16 DOAJ-miss E6 + DOAJ utk miss-8 (doc 30 §14.6 butir
// 4, pagu +24 GET APPROVED user 6 Okt 2026): temuan doc 37 §4 — DOAJ
// menerima varian ISSN BER-STRIP (record-dependent) padahal E6 hanya menguji
// bentuk kanonik → 16 miss = understatement. Desain 1 GET/ISSN (doc 37 §2):
//   - H1 sweep 16 miss E6 (11 probe §1 + 5 lampiran §2): query bentuk
//     hyphen E-ISSN → found? → angka DOAJ E6 final (8 → 8+found);
//     nol → STOP (bentuk kanonik SUDAH terbukti 0 di E6, tak diulang).
//   - H2 DOAJ miss-8 (klausul TRIGGERED, doc 36 §4): hyphen-first E-ISSN;
//     kanonik miss-8 = BELUM (margin E7f per teks approve).
// Cross-check independen: hasil H1 utk 11 probe §1 dibandingkan dgn klaim
// cek manual user (doc 37 §1 — 3 FOUND / 8 nol) → MISMATCH dilaporkan jujur.
// TANPA tulis db (angka + fixture saja, E7f yang menulis subject). Offline:
// daftar target statis + parser = murni; GET = driver.

import (
	"fmt"
	"strings"
)

// E7dBaris = satu target query DOAJ (identitas dari db — driver mengisi;
// Key = E-ISSN kanonik utk fixture & dedup; Bagian menunjuk sumber daftar).
type E7dBaris struct {
	Kelompok string // sweep16 (miss E6) | miss8 (miss-8 E4b)
	Bagian   string // 1 (probe 11) | 2 (lampiran 5) | e7c (miss-8)
	ID       int64
	Nama     string
	PISSN    string
	EISSN    string
	Key      string
}

// HyphenISSN = bentuk ber-strip utk query DOAJ ("25799320" → "2579-9320").
// Hanya ISSN kanonik 8 karakter (format ISSN internasional 4+4); selain itu
// "" (jujur — tak menebak format utk input aneh).
func HyphenISSN(kanonik string) string {
	k := NormISSN(kanonik)
	if len(k) != 8 {
		return ""
	}
	return k[:4] + "-" + k[4:]
}

// Klaim manual cek user 6 Okt (doc 37 §1) — key → hasil klaim.
// true = FOUND via hyphen · false = nol (kanonik+hyphen+nama) ·
// baris §2 (5 lampiran) TIDAK ada di sini (BELUM dicek — klaim kosong).
var e7dKlaimManual = map[string]bool{
	"25799320": true,  // #1 JKS — FOUND (user, JSON lengkap)
	"20878575": true,  // #2 Microbiology Indonesia — FOUND
	"23381353": true,  // #6 JAS — FOUND
	"24778516": false, // #3 AGRIVITA — nol (hyphen + nama)
	"25024760": false, // #4 IJEEICS — nol
	"25030310": false, // #5 Molekul — nol
	"25025457": false, // #7 Formatif — nol
	"25494600": false, // #8 Legality — nol
	"27222594": false, // #9 IJAAS — nol
	"25027913": false, // #10 Sosio Informa — nol
	"2355813X": false, // #11 Ar-Raniry — nol
}

// E7dKlaim = label klaim utk output ("found" | "nol" | "" = tanpa klaim §2).
func E7dKlaim(key string) string {
	if v, ok := e7dKlaimManual[key]; ok {
		if v {
			return "found"
		}
		return "nol"
	}
	return ""
}

// E7dHasil = hasil 1 baris target (1 query bentuk hyphen).
type E7dHasil struct {
	Kelompok string `json:"kelompok"`
	Bagian   string `json:"bagian"`
	ID       int64  `json:"journal_id"`
	Nama     string `json:"nama"`
	Key      string `json:"issn_kanonik"`
	Query    string `json:"query"`  // bentuk hyphen yg dikirim
	Doaj     E6DOAJ `json:"doaj"`   // reuse parser E6 (ISSN-verify otomatis)
	Sumber   string `json:"sumber"` // GET | fixture | resume
	Klaim    string `json:"klaim_manual,omitempty"`
	Cross    string `json:"cross_check,omitempty"` // sesuai | SELISIH:<hasil>
}

// E7dRingkasan = agregat utk laporan & guard.
type E7dRingkasan struct {
	SweepTotal     int `json:"sweep_total"`
	SweepFound     int `json:"sweep_found"`
	SweepNol       int `json:"sweep_nol"`
	KlaimDibanding int `json:"klaim_dibanding"` // baris §1 dgn klaim manual
	KlaimSelisih   int `json:"klaim_selisih"`   // hasil tool ≠ klaim user
	Miss8Total     int `json:"miss8_total"`
	Miss8Found     int `json:"miss8_found"`
	Miss8Nol       int `json:"miss8_nol"`
	DoajE6Final    int `json:"doaj_e6_final"` // 8 (E6) + sweep_found, utk 24 sampel E6
}

// HitungE7d = agregat dari daftar hasil (order bebas).
func HitungE7d(items []E7dHasil) E7dRingkasan {
	r := E7dRingkasan{SweepTotal: 16, Miss8Total: 8}
	for _, h := range items {
		switch h.Kelompok {
		case "sweep16":
			if h.Doaj.Tersedia {
				r.SweepFound++
			} else if h.Doaj.HTTP == 200 {
				r.SweepNol++
			}
			if h.Klaim != "" {
				r.KlaimDibanding++
				hasilFound := h.Doaj.Tersedia
				if (h.Klaim == "found") != hasilFound {
					r.KlaimSelisih++
				}
			}
		case "miss8":
			if h.Doaj.Tersedia {
				r.Miss8Found++
			} else if h.Doaj.HTTP == 200 {
				r.Miss8Nol++
			}
		}
	}
	// DOAJ E6 final: 16 sweep = 24 sampel E6 − 8 found E6 (doc 37 §0)
	r.DoajE6Final = 8 + r.SweepFound
	return r
}

// E7dCrossLabel = label cross-check utk 1 baris ("" bila tanpa klaim).
func E7dCrossLabel(klaim string, tersedia bool) string {
	if klaim == "" {
		return ""
	}
	hasil := "nol"
	if tersedia {
		hasil = "found"
	}
	if klaim == hasil {
		return "sesuai"
	}
	return fmt.Sprintf("SELISIH:klaim=%s,hasil=%s", klaim, hasil)
}

// E7dKalimatKendala = kalimat kendala jujur utk log (error parser / mismatch).
func E7dKalimatKendala(h E7dHasil) string {
	var pesan []string
	if h.Doaj.Error != "" {
		pesan = append(pesan, "error="+h.Doaj.Error)
	}
	if strings.HasPrefix(h.Cross, "SELISIH") {
		pesan = append(pesan, h.Cross+" (cek doc 37 §1)")
	}
	return strings.Join(pesan, " · ")
}
