package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sinta-scraper/internal/garuda"
)

// runE7D = Q4 E7d (doc 30 14.6 butir 4 - pagu +24 GET APPROVED user 6 Okt
// 2026): hyphen-sweep 16 DOAJ-miss E6 + DOAJ utk miss-8 (klausul TRIGGERED).
//
// Temuan doc 37 (bagian 4): DOAJ search menerima varian ISSN BER-STRIP utk
// banyak record (record-dependent) - E6 hanya menguji bentuk kanonik maka
// 16 miss = understatement. Desain 1 GET/ISSN (teks approve):
//   - H1: sweep 16 miss E6 (11 probe doc 37 bagian 1 + 5 lampiran bagian 2)
//   - query bentuk hyphen E-ISSN; found stop; nol stop (bentuk kanonik
//     SUDAH terbukti 0 di E6 - tidak diulang, 0 GET sia-sia).
//   - H2: DOAJ utk miss-8 (8 ISSN, hyphen-first); bentuk kanonik miss-8 =
//     BELUM (margin E7f per teks approve).
//
// Cross-check independen: 11 probe bagian 1 vs klaim cek manual user
// (3 FOUND / 8 nol) - SELISIH dilaporkan (bukan GET tambahan; pagu = K6).
// TANPA tulis db (angka + fixture; E7f yang menulis). Resume + fixture-first
// maka rerun 0 GET. Pagu kumulatif lintas run (paritas E6).
const paguE7d = 24

var errPaguE7d = errors.New("PAGU E7d HABIS")

// Daftar id (doc 37 bagian 1/2 + doc 36 bagian 4) - identitas (nama/E-ISSN/
// P-ISSN) dibaca dari db (sumber kebenaran); 4 id silang-rank (2, 2969,
// 7526, 10663) hanya ada di db2 (pool E6 S2/S3).
var (
	e7dSweepID1 = []int64{131, 666, 682, 697, 828, 2115, 3560, 4607, 8823, 2969, 2} // probe 11
	e7dSweepID2 = []int64{1095, 5932, 6910, 7526, 10663}                            // lampiran 5
	e7dMiss8ID  = []int64{60, 673, 687, 688, 689, 948, 3203, 3974}                  // doc 36 bagian 4
)

type e7dHasilJSON struct {
	Dibuat    string              `json:"dibuat"`
	Pagu      int                 `json:"pagu_get"`
	GetTotal  int                 `json:"get_run_ini"`
	Jumlah    int                 `json:"jumlah"`
	Ringkasan garuda.E7dRingkasan `json:"ringkasan"`
	Items     []garuda.E7dHasil   `json:"items"`
}

// e7dMuatBaris = baca identitas dari dua db (read-only), urut doc37/doc36,
// dgn guard: tiap id WAJIB ketemu persis satu (jujur, bukan silently skip).
func e7dMuatBaris(dbPath, db2Path string) ([]garuda.E7dBaris, error) {
	pool1, err := bacaPoolE6(dbPath, "s1", "j.id IN (131,666,682,697,828,1095,2115,3560,4607,5932,6910,8823,60,673,687,688,689,948,3203,3974)")
	if err != nil {
		return nil, err
	}
	pool2, err := bacaPoolE6(db2Path, "s23", "j.id IN (2,2969,7526,10663)")
	if err != nil {
		return nil, err
	}
	byID := map[int64]garuda.E6Baris{}
	for _, b := range append(pool1, pool2...) {
		byID[b.ID] = b
	}
	pilih := func(ids []int64, kelompok, bagian string) ([]garuda.E7dBaris, error) {
		out := make([]garuda.E7dBaris, 0, len(ids))
		for _, id := range ids {
			b, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("id %d (kelompok %s) tak ditemukan di db enrich/db2", id, kelompok)
			}
			if b.Key == "" {
				return nil, fmt.Errorf("id %d (%s) tanpa E-ISSN/P-ISSN valid", id, b.Nama)
			}
			out = append(out, garuda.E7dBaris{
				Kelompok: kelompok, Bagian: bagian,
				ID: b.ID, Nama: b.Nama, PISSN: b.PISSN, EISSN: b.EISSN, Key: b.Key,
			})
		}
		return out, nil
	}
	s1, err := pilih(e7dSweepID1, "sweep16", "1")
	if err != nil {
		return nil, err
	}
	s2, err := pilih(e7dSweepID2, "sweep16", "2")
	if err != nil {
		return nil, err
	}
	m8, err := pilih(e7dMiss8ID, "miss8", "e7c")
	if err != nil {
		return nil, err
	}
	return append(append(s1, s2...), m8...), nil
}

func runE7D(dbPath, db2Path, fixturesDir string, refresh bool, delayMin, delayMax time.Duration) error {
	baris, err := e7dMuatBaris(dbPath, db2Path)
	if err != nil {
		return err
	}

	jsonPath := filepath.Join(filepath.Dir(fixturesDir), "e7d-results.json")
	hasilLama := map[string]garuda.E7dHasil{}
	var getLama int
	if !refresh {
		if b, err := os.ReadFile(jsonPath); err == nil {
			var old e7dHasilJSON
			if json.Unmarshal(b, &old) == nil {
				for _, h := range old.Items {
					hasilLama[h.Key] = h
				}
				getLama = old.GetTotal
				fmt.Printf("resume: %d hasil lama (%d GET) dari %s\n",
					len(hasilLama), getLama, filepath.Base(jsonPath))
			}
		}
	}
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		return fmt.Errorf("buat dir fixture: %w", err)
	}

	fmt.Printf("== E7d hyphen-sweep 16 miss E6 + DOAJ miss-8 (pagu %d GET kumulatif, delay %s-%s) ==\n",
		paguE7d, delayMin, delayMax)
	fmt.Println("temuan doc 37: DOAJ simpan ISSN ber-strip (record-dependent); query bentuk hyphen 4-4")

	c := garuda.NewClient("api-e7d", uaDefault, delayMin, delayMax)
	var getTotal int
	get := func(rawURL string) ([]byte, int, error) {
		if getLama+getTotal >= paguE7d {
			return nil, 0, fmt.Errorf("pagu E7d (%d): %w", paguE7d, errPaguE7d)
		}
		getTotal++
		return c.Get(rawURL, "")
	}

	rep := e7dHasilJSON{
		Dibuat: time.Now().UTC().Format(time.RFC3339),
		Pagu:   paguE7d,
	}
	var gotFix, gotResume int
	for i, b := range baris {
		query := garuda.HyphenISSN(b.Key)
		if query == "" {
			return fmt.Errorf("id %d: HyphenISSN(%q) kosong", b.ID, b.Key)
		}
		h := garuda.E7dHasil{
			Kelompok: b.Kelompok, Bagian: b.Bagian, ID: b.ID, Nama: b.Nama,
			Key: b.Key, Query: query,
		}
		if b.Kelompok == "sweep16" && b.Bagian == "1" {
			h.Klaim = garuda.E7dKlaim(b.Key)
		}

		fixPath := filepath.Join(fixturesDir, fmt.Sprintf("e7d-doaj-%s.json", b.Key))
		var body []byte
		status := 0
		old, adaLama := hasilLama[b.Key]
		switch {
		case !refresh && adaLama && old.Doaj.HTTP == 200:
			h.Doaj = old.Doaj
			h.Sumber = "resume"
			gotResume++
			if fb, err := os.ReadFile(fixPath); err == nil {
				if p, perr := garuda.ParseDOAJSearch(fb, b.Key); perr == nil {
					p.HTTP = 200
					h.Doaj = p
				}
			}
		default:
			if fb, err := os.ReadFile(fixPath); err == nil {
				body, status, h.Sumber = fb, 200, "fixture"
				gotFix++
			} else {
				b2, st, err2 := get("https://doaj.org/api/search/journals/" + query)
				if errors.Is(err2, errPaguE7d) {
					return err2
				}
				h.Sumber = "GET"
				if err2 != nil {
					h.Doaj.Error = "unreachable: " + err2.Error()
				}
				body, status = b2, st
			}
			h.Doaj.HTTP = status
			switch {
			case status == 200:
				if p, perr := garuda.ParseDOAJSearch(body, b.Key); perr != nil {
					h.Doaj.Error = perr.Error()
				} else {
					p.HTTP = 200
					h.Doaj = p
				}
				if h.Sumber == "GET" {
					if werr := os.WriteFile(fixPath, body, 0o644); werr != nil {
						return fmt.Errorf("tulis fixture e7d %s: %w", b.Key, werr)
					}
				}
			case status == 404:
				// tak terdaftar - coverage jujur, bukan error
			case status == 0:
				// unreachable sudah tercatat di h.Doaj.Error
			default:
				h.Doaj.Error = fmt.Sprintf("http %d", status)
			}
		}
		h.Cross = garuda.E7dCrossLabel(h.Klaim, h.Doaj.Tersedia)

		tandai := "nol"
		if h.Doaj.Tersedia {
			tandai = "FOUND"
		}
		extra := ""
		if x := garuda.E7dKalimatKendala(h); x != "" {
			extra = "  [!] " + x
		}
		fmt.Printf("[%2d/%d] %-7s %-3s j%-6d %-10s -> %-5s http=%-3d %-7s %s%s\n",
			i+1, len(baris), h.Kelompok, h.Bagian, h.ID, query, tandai,
			h.Doaj.HTTP, h.Sumber, h.Nama, extra)
		rep.Items = append(rep.Items, h)
	}

	rep.Jumlah = len(rep.Items)
	rep.GetTotal = getTotal + getLama
	rep.Ringkasan = garuda.HitungE7d(rep.Items)

	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	if err := os.WriteFile(jsonPath, out, 0o644); err != nil {
		return fmt.Errorf("tulis json: %w", err)
	}

	r := rep.Ringkasan
	fmt.Printf("\n== E7d SELESAI ==\n")
	fmt.Printf("GET: run ini %d/%d - kumulatif %d - resume=%d fixture=%d\n",
		getTotal, paguE7d, rep.GetTotal, gotResume, gotFix)
	fmt.Printf("H1 sweep 16: found=%d nol=%d -> DOAJ E6 final = %d/24 (%.0f%%) [8 E6 + %d hyphen]\n",
		r.SweepFound, r.SweepNol, r.DoajE6Final, pct(r.DoajE6Final, 24), r.SweepFound)
	fmt.Printf("cross-check 11 probe vs klaim user: dibanding=%d selisih=%d\n",
		r.KlaimDibanding, r.KlaimSelisih)
	fmt.Printf("H2 miss-8   : found=%d nol=%d (kanonik = margin E7f)\n",
		r.Miss8Found, r.Miss8Nol)
	if r.KlaimSelisih > 0 {
		fmt.Println("[!] SELISIH vs cek manual user -> review baris cross_check=SELISIH di json")
	}
	fmt.Printf("JSON: %s\n", jsonPath)
	return nil
}
