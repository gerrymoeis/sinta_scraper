package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"sinta-scraper/internal/garuda"

	_ "modernc.org/sqlite"
)

// runE6 = Q4 E6 (doc 30 §14.5 — opsi A APPROVED user 4 Okt 2026):
// coverage Crossref & DOAJ pada 24 ISSN stratified — 18 S1 (db enrich) +
// 3 S2 + 3 S3 (db stage1 vdac-l1-r23-kumulatif, silang-rank utk cek bias
// Q6) — 2 GET/ISSN.
//
// AMENDMEN Opsi B (approve user 6 Okt 2026): query 1-ISSN DOAJ terbukti
// tidak andal (cek 2 — SINERGI E-ISSN 0 padahal P-ISSN ada → FALSE-MISS),
// maka 22 miss DOAJ di-probe dgn ISSN kedua (P-ISSN pool ≠ key, else ISSN
// Crossref ≠ key; berhenti saat pertama found) → 15 GET baru + 2 fixture
// hasil cek manual user + 5 miss single-ISSN tanpa kandidat (definitif,
// tidak dikejar) → PAGU 63 (48 + 15), Q4 realisasi 259.
//
// Desain (approve user):
//   - Sampling deterministik stratified (PilihSampelE6 — tanpa random,
//     bisa direview: urut rank lalu id, stride menyebar, dedup ISSN).
//   - Per ISSN 2 GET: api.crossref.org/journals/{ISSN} ·
//     doaj.org/api/search/journals/{ISSN} (ketentuan §14.5).
//   - 404 / total=0 = "tidak terdaftar" (coverage jujur, BUKAN error);
//     429/5xx retry internal Client TIDAK dihitung ke pagu (paritas E5).
//   - Resume: hasil lama e6-results.json dipakai + re-parse fixture (0 GET);
//     -refresh = ulang semua.
//   - TANPA tulis db (E7 yang menulis). Output: e6-results.json + ringkasan
//     coverage % per sumber & strata + fill-rate field.
const paguE6 = 63

var errPaguE6 = errors.New("PAGU E6 HABIS")

// e6HasilJSON = envelope penyimpan (mirror e5JSON utk pola resume sama).
type e6HasilJSON struct {
	Dibuat    string             `json:"dibuat"`
	Pagu      int                `json:"pagu_get"`
	GetTotal  int                `json:"get_run_ini"`
	Jumlah    int                `json:"jumlah"`
	Sampel    []garuda.E6Baris   `json:"sampel"`
	Ringkasan garuda.E6Ringkasan `json:"ringkasan"`
	Items     []garuda.E6Hasil   `json:"items"`
}

// bacaPoolE6 = baca 1 db read-only → baris kandidat (kolom identik journals).
func bacaPoolE6(path, sumber string, rankFilter string) ([]garuda.E6Baris, error) {
	uri := "file:" + filepath.ToSlash(path) + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, fmt.Errorf("buka db %s: %w", path, err)
	}
	defer db.Close()

	q := `SELECT j.id, j.name, COALESCE(j.print_issn,''), COALESCE(j.electronic_issn,''), j.sinta_rank
	      FROM journals j`
	if rankFilter != "" {
		q += " WHERE " + rankFilter
	}
	q += " ORDER BY j.id"
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", filepath.Base(path), err)
	}
	defer rows.Close()
	var out []garuda.E6Baris
	for rows.Next() {
		var b garuda.E6Baris
		if err := rows.Scan(&b.ID, &b.Nama, &b.PISSN, &b.EISSN, &b.Rank); err != nil {
			return nil, err
		}
		b.Sumber = sumber
		b.Key = garuda.NormISSN(b.EISSN)
		if b.Key == "" {
			b.Key = garuda.NormISSN(b.PISSN)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// runE6 = E6 utuh. Baca dua db read-only; resume dari e6-results.json;
// body disimpan sbg fixture e6-crossref-{issn}.json / e6-doaj-{issn}.json.
func runE6(dbPath, db2Path, fixturesDir string, refresh bool, delayMin, delayMax time.Duration) error {
	poolS1, err := bacaPoolE6(dbPath, "s1", "j.sinta_rank = 1")
	if err != nil {
		return err
	}
	pool23, err := bacaPoolE6(db2Path, "s23", "j.sinta_rank IN (2,3)")
	if err != nil {
		return err
	}
	sampel, err := garuda.PilihSampelE6(append(poolS1, pool23...), 18, 3, 3)
	if err != nil {
		return err
	}

	// resume: hasil lama dari e6-results.json (key = ISSN kanonik)
	jsonPath := filepath.Join(filepath.Dir(fixturesDir), "e6-results.json")
	hasilLama := map[string]garuda.E6Hasil{}
	var getLama int
	if !refresh {
		if b, err := os.ReadFile(jsonPath); err == nil {
			var old e6HasilJSON
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

	fmt.Printf("== E6 coverage Crossref & DOAJ (24 ISSN = 18 S1 + 3 S2 + 3 S3, pagu %d GET, delay %s–%s) ==\n",
		paguE6, delayMin, delayMax)
	for _, b := range sampel {
		fmt.Printf("sampel: S%d %-4s j%-6d %-44.44s\n", b.Rank, b.Sumber, b.ID, b.Nama)
	}
	fmt.Println()

	c := garuda.NewClient("api-e6", uaDefault, delayMin, delayMax)
	var getTotal int
	get := func(rawURL string) ([]byte, int, error) {
		// pagu KUMULATIF lintas run (resume bawa getLama) — bukan per-run
		if getLama+getTotal >= paguE6 {
			return nil, 0, fmt.Errorf("pagu E6 (%d): %w", paguE6, errPaguE6)
		}
		getTotal++
		return c.Get(rawURL, "")
	}

	rep := e6HasilJSON{
		Dibuat: time.Now().UTC().Format(time.RFC3339),
		Pagu:   paguE6,
		Sampel: sampel,
	}
	n := len(sampel)
	for i, b := range sampel {
		var h garuda.E6Hasil
		if old, ok := hasilLama[b.Key]; ok && !refresh {
			h = old
			// identitas SELALU dari db; parse ulang dari fixture (0 GET)
			h.SumberDB, h.ID, h.Rank, h.Nama, h.PISSN, h.EISSN, h.Key =
				b.Sumber, b.ID, b.Rank, b.Nama, b.PISSN, b.EISSN, b.Key
			if fb, err := os.ReadFile(fixtureE6(fixturesDir, "crossref", b.Key)); err == nil && h.Crossref.HTTP == 200 {
				if p, perr := garuda.ParseCrossrefJournals(fb); perr == nil {
					p.HTTP = 200
					h.Crossref = p
				}
			}
			if fb, err := os.ReadFile(fixtureE6(fixturesDir, "doaj", b.Key)); err == nil && h.Doaj.HTTP == 200 {
				if p, perr := garuda.ParseDOAJSearch(fb, b.Key); perr == nil {
					p.HTTP = 200
					h.Doaj = p
				}
			}
		} else {
			h = garuda.E6Hasil{
				SumberDB: b.Sumber, ID: b.ID, Rank: b.Rank, Nama: b.Nama,
				PISSN: b.PISSN, EISSN: b.EISSN, Key: b.Key,
			}

			// GET 1: Crossref
			body, status, err := get("https://api.crossref.org/journals/" + b.Key)
			if errors.Is(err, errPaguE6) {
				return err
			}
			h.Crossref.HTTP = status
			switch {
			case status == 200:
				if werr := os.WriteFile(fixtureE6(fixturesDir, "crossref", b.Key), body, 0o644); werr != nil {
					return fmt.Errorf("tulis fixture crossref %s: %w", b.Key, werr)
				}
				if p, perr := garuda.ParseCrossrefJournals(body); perr != nil {
					h.Crossref.Error = perr.Error()
				} else {
					p.HTTP = 200
					h.Crossref = p
				}
			case status == 404:
				// tidak terdaftar = coverage jujur, bukan error
			case status == 0:
				h.Crossref.Tersedia = false
				if err != nil {
					h.Crossref.Error = "unreachable: " + err.Error()
				}
			default:
				h.Crossref.Tersedia = false
				h.Crossref.Error = fmt.Sprintf("http %d", status)
			}

			// GET 2: DOAJ
			body2, status2, err2 := get("https://doaj.org/api/search/journals/" + b.Key)
			if errors.Is(err2, errPaguE6) {
				return err2
			}
			h.Doaj.HTTP = status2
			switch {
			case status2 == 200:
				if werr := os.WriteFile(fixtureE6(fixturesDir, "doaj", b.Key), body2, 0o644); werr != nil {
					return fmt.Errorf("tulis fixture doaj %s: %w", b.Key, werr)
				}
				if p, perr := garuda.ParseDOAJSearch(body2, b.Key); perr != nil {
					h.Doaj.Error = perr.Error()
				} else {
					p.HTTP = 200
					h.Doaj = p
				}
			case status2 == 404:
				// tidak terdaftar
			case status2 == 0:
				if err2 != nil {
					h.Doaj.Error = "unreachable: " + err2.Error()
				}
			default:
				h.Doaj.Error = fmt.Sprintf("http %d", status2)
			}
		}

		h.TitleSamaCrossref = h.Crossref.Tersedia && garuda.TitleSama(b.Nama, h.Crossref.Judul)
		h.TitleSamaDoaj = h.Doaj.Tersedia && garuda.TitleSama(b.Nama, h.Doaj.Judul)

		xr, dj := "tidak", "tidak"
		if h.Crossref.Tersedia {
			xr = "OK"
		}
		if h.Doaj.Tersedia {
			dj = "OK"
		}
		fmt.Printf("[%2d/%d] S%d %-10s crossref=%-6s doaj=%-6s title(xr/doaj)=%d/%d  %s\n",
			i+1, n, b.Rank, b.Key, xr, dj,
			b2i(h.TitleSamaCrossref), b2i(h.TitleSamaDoaj), b.Nama)
		rep.Items = append(rep.Items, h)
	}

	// FASE PROBE DOAJ dual-ISSN — Opsi B presisi (approve user 6 Okt 2026):
	// utk tiap miss DOAJ coba kandidat ISSN kedua (P-ISSN bila ≠ key, else
	// ISSN Crossref ≠ key), berhenti saat pertama found. Resume idempoten:
	// fixture e6-doaj-{issn}.json dulu (2 = hasil cek manual user), baru GET;
	// kandidat yg sudah tercatat di doaj_probe dilewati (rerun 0 GET).
	fmt.Println("\n== FASE PROBE DOAJ (ISSN kedua utk miss) ==")
	missAwal, probeGet, probeFix, probeFound := 0, 0, 0, 0
	for i := range rep.Items {
		if !rep.Items[i].Doaj.Tersedia {
			missAwal++
		}
	}
	for i := range rep.Items {
		h := &rep.Items[i]
		if h.Doaj.Tersedia {
			continue
		}
		var cand string
		if p := garuda.NormISSN(h.PISSN); p != "" && p != h.Key {
			cand = p
		} else {
			for _, x := range h.Crossref.ISSNs {
				if x != h.Key {
					cand = x
					break
				}
			}
		}
		if cand == "" || slices.Contains(h.DoajProbe, cand) {
			continue
		}
		h.DoajProbe = append(h.DoajProbe, cand)

		sumber, body, status := "GET", []byte(nil), 0
		if fb, err := os.ReadFile(fixtureE6(fixturesDir, "doaj", cand)); err == nil {
			// fixture hanya disimpan saat HTTP 200 (2 = cek manual user 6 Okt)
			body, status, sumber = fb, 200, "fixture-c/manual"
			probeFix++
		} else {
			b, st, err := get("https://doaj.org/api/search/journals/" + cand)
			if errors.Is(err, errPaguE6) {
				return err
			}
			probeGet++
			if err != nil {
				if h.Doaj.Error == "" {
					h.Doaj.Error = "unreachable: " + err.Error()
				}
				continue
			}
			if body, status = b, st; st == 200 {
				if werr := os.WriteFile(fixtureE6(fixturesDir, "doaj", cand), body, 0o644); werr != nil {
					return fmt.Errorf("tulis fixture doaj %s: %w", cand, werr)
				}
			}
		}

		res := fmt.Sprintf("http %d", status)
		if status == 200 {
			if p, perr := garuda.ParseDOAJSearch(body, cand); perr != nil {
				if h.Doaj.Error == "" {
					h.Doaj.Error = perr.Error()
				}
			} else if p.Tersedia {
				// terbukti via ISSN kedua — timpa hasil miss (JSON mentah
				// tetap di fixture utk audit; K1 bukan isu utk coverage)
				h.Doaj = p
				h.TitleSamaDoaj = garuda.TitleSama(h.Nama, p.Judul)
				probeFound++
				res = "FOUND " + p.Judul
			} else {
				res = "miss"
			}
		}
		fmt.Printf("probe %s via %s (%s) = %s\n", h.Key, cand, sumber, res)
	}

	rep.Jumlah = len(rep.Items)
	rep.GetTotal = getTotal + getLama
	rep.Ringkasan = garuda.HitungE6Ringkasan(rep.Items)

	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	if err := os.WriteFile(jsonPath, out, 0o644); err != nil {
		return fmt.Errorf("tulis json: %w", err)
	}

	r := rep.Ringkasan
	fmt.Printf("\nE6 selesai: get=%d/%d → %s\n", getTotal, paguE6, jsonPath)
	fmt.Printf("coverage: crossref=%d/%d (%.0f%%) doaj=%d/%d (%.0f%%) · keduanya=%d salah-satu=%d tidak-ada=%d\n",
		r.CrossrefOK, r.Sampel, pct(r.CrossrefOK, r.Sampel),
		r.DoajOK, r.Sampel, pct(r.DoajOK, r.Sampel),
		r.Keduanya, r.SalahSatu, r.TidakAda)
	fmt.Printf("probe  : miss-awal=%d → %d GET baru + %d fixture c/manual → found=%d\n",
		missAwal, probeGet, probeFix, probeFound)
	fmt.Printf("strata  :")
	for _, s := range []string{"S1", "S2", "S3"} {
		if v, ok := r.PerStrata[s]; ok {
			fmt.Printf(" %s(xr %d/%d doaj %d/%d)", s, v.CrossrefOK, v.Sampel, v.DoajOK, v.Sampel)
		}
	}
	fmt.Println()
	fmt.Printf("title   : crossref-sama=%d/%d doaj-sama=%d/%d\n",
		r.TitleSamaCrossref, r.CrossrefOK, r.TitleSamaDoaj, r.DoajOK)
	fmt.Printf("field   :")
	for _, k := range []string{"crossref.judul", "crossref.penerbit", "crossref.doi",
		"crossref.abstract", "crossref.license", "crossref.orcid", "crossref.ror",
		"doaj.judul", "doaj.penerbit", "doaj.subjek", "doaj.lisensi", "doaj.editorial", "doaj.board"} {
		fmt.Printf(" %s=%d", k, r.Field[k])
	}
	fmt.Println()

	// daftar kendala utk review user (respons aneh / judul beda / tak terdaftar
	// di KEDUA sumber = kandidat miss baru utk daftar E7)
	fmt.Printf("\n== KENDALA E6 — utk review user ==\n")
	ada := false
	for _, h := range rep.Items {
		switch {
		case h.Crossref.Error != "" || h.Doaj.Error != "":
			ada = true
			fmt.Printf("S%d %-10s ERROR crossref=%q doaj=%q (%s)\n",
				h.Rank, h.Key, h.Crossref.Error, h.Doaj.Error, h.Nama)
		case !h.Crossref.Tersedia && !h.Doaj.Tersedia:
			ada = true
			fmt.Printf("S%d %-10s TIDAK TERDAFTAR di kedua sumber (%s)\n", h.Rank, h.Key, h.Nama)
		case h.Crossref.Tersedia && !h.TitleSamaCrossref, h.Doaj.Tersedia && !h.TitleSamaDoaj:
			ada = true
			fmt.Printf("S%d %-10s judul beda: xr=%q doaj=%q (SINTA=%q)\n",
				h.Rank, h.Key, h.Crossref.Judul, h.Doaj.Judul, h.Nama)
		}
	}
	if !ada {
		fmt.Println("tidak ada — semua respons normal, judul konsisten")
	}
	return nil
}

func fixtureE6(dir, jenis, key string) string {
	return filepath.Join(dir, fmt.Sprintf("e6-%s-%s.json", jenis, key))
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}
