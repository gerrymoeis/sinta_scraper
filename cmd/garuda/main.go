// Program cmd/garuda: driver eksperimen Tahap 2 Garuda (doc 30 Bagian 13 §13.4).
//
// Mode:
//
//	-probe : probe 10 E-ISSN campuran (pembuka E1) — read-only, tanpa tulis DB,
//	         HTML disimpan sbg fixture (§13.5). Q3 langkah 3.
//	-subjects : harvest subject 261 → build subject_map → harmonisasi (Q3 langkah 4–5).
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"
	"sinta-scraper/internal/storage"

	_ "modernc.org/sqlite"
)

const uaDefault = "sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)"

func main() {
	dbPath := flag.String("db", "data/stage2/sinta-r2-enrich.db", "path db kerja Tahap 2 (db sumber baca = sinta-r1-source.db, di luar proses)")
	fixtures := flag.String("fixtures", "data/stage2/fixtures", "dir simpan HTML mentah")
	probe := flag.Bool("probe", false, "probe 10 E-ISSN campuran (E1-subset), read-only")
	subjects := flag.Bool("subjects", false, "harvest subject + build subject_map + harmonisasi")
	refresh := flag.Bool("refresh", false, "abaikan resume — ulang semua request")
	delayMin := flag.Duration("delay-min", time.Second, "jeda acak minimum antar request")
	delayMax := flag.Duration("delay-max", 2*time.Second, "jeda acak maksimum antar request")
	flag.Parse()

	if *probe {
		if err := runProbe(*dbPath, *fixtures, *delayMin, *delayMax); err != nil {
			fmt.Fprintf(os.Stderr, "probe GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *subjects {
		if err := runSubjects(*dbPath, *fixtures, *refresh, *delayMin, *delayMax); err != nil {
			fmt.Fprintf(os.Stderr, "subjects GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	flag.Usage()
}

// runSubjects = Q3 langkah 4–5: harvest 261 → build subject_map → harmonisasi.
func runSubjects(dbPath, fixturesDir string, refresh bool, delayMin, delayMax time.Duration) error {
	store, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	targets, err := store.HarvestTargets()
	if err != nil {
		return err
	}
	c := garuda.NewClient("garuda", uaDefault, delayMin, delayMax)

	fmt.Printf("== Harvest search (target: %d jurnal, resume=%v) ==\n", len(targets), !refresh)
	hrep, err := garuda.HarvestSubjects(store, c, targets, garuda.HarvestOptions{
		FixturesDir: fixturesDir,
		Refresh:     refresh,
		OnProgress: func(done, total int, t storage.HarvestTarget, status string) {
			if status == "skip" {
				return
			}
			fmt.Printf("[%d/%d] %-12s EISSN=%s %s\n", done, total, status, t.EISSN, t.Name)
		},
	})
	if err != nil {
		return err
	}
	fmt.Printf("harvest: matched=%d not_found=%d ambiguous=%d skip(resume)=%d error=%d\n",
		hrep.Matched, hrep.NotFound, hrep.Ambiguous, hrep.Skipped, hrep.Errors)

	fmt.Println("\n== Build subject_map + harmonisasi ==")
	mrep, err := garuda.BuildDanHarmonisasi(store, c, fixturesDir)
	if err != nil {
		return err
	}
	fmt.Printf("vocab: sinta=%d token, garuda=%d label (GET /area=%v)\n",
		mrep.SintaVocab, mrep.GarudaVocab, mrep.FetchedArea)
	if len(mrep.UnknownLabels) > 0 {
		fmt.Printf("label capture di luar /area (dipakai + dicatat): %v\n", mrep.UnknownLabels)
	}
	fmt.Printf("subject_map: %d baris\n", mrep.MapRows)
	fmt.Printf("canonical: terisi=%d NO_SUBJECT=%d LOW_EVIDENCE=%d\n",
		mrep.Filled, mrep.NoSubject, mrep.LowEvidence)

	// distribusi method + merge tanpa bukti (audit K2)
	rows, err := store.SubjectMapSnapshot()
	if err != nil {
		return err
	}
	dist := map[string]int{}
	var support0 int
	for _, r := range rows {
		dist[r.Method]++
		if r.Method != "identity" && r.Support == 0 {
			support0++
		}
	}
	fmt.Printf("method: exact=%d contain=%d prefix=%d identity=%d | merge support=0: %d\n",
		dist["exact"], dist["contain"], dist["prefix"], dist["identity"], support0)
	return nil
}

// ---------- probe (Q3 langkah 3 / pembuka E1) ----------

type kasus struct {
	label string
	q     string
}

func runProbe(dbPath, fixturesDir string, delayMin, delayMax time.Duration) error {
	eissn, pissn, err := seedISSN(dbPath)
	if err != nil {
		return fmt.Errorf("baca seed ISSN dari db: %w", err)
	}
	if len(eissn) == 0 || pissn == "" {
		return fmt.Errorf("seed ISSN tak lengkap (eissn=%d, pissn=%d)", len(eissn), len(pissn))
	}

	kasus := []kasus{
		{"eissn-1", eissn[0]},
		{"eissn-2", eissn[1]},
		{"eissn-3", eissn[2]},
		{"eissn-4", eissn[3]},
		{"eissn-hyphen", eissn[0][:4] + "-" + eissn[0][4:]}, // varian hyphen
		{"pissn", pissn},
		{"nol", "00000000"},
		{"huruf", "xyz"},
		{"pendek", "ab"},    // di bawah minlength=3 (form) — uji server
		{"multi", "jurnal"}, // multi-hit + paginasi (lanjut page=2)
	}
	if len(eissn) < 4 {
		return fmt.Errorf("eissn valid di db hanya %d (butuh 4)", len(eissn))
	}

	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		return fmt.Errorf("buat dir fixture: %w", err)
	}
	c := garuda.NewClient("garuda-probe", uaDefault, delayMin, delayMax)

	fmt.Printf("%-13s %-10s %-4s %5s %7s %6s %5s %6s %6s %s\n",
		"kasus", "q", "http", "ms", "found", "total", "rows", "label", "exact", "catatan")
	for i, k := range kasus {
		t0 := time.Now()
		body, status, err := c.Get(garuda.SearchURL(k.q, 1), garuda.DefaultReferer)
		ms := time.Since(t0).Milliseconds()
		if err != nil && body == nil {
			fmt.Printf("%-13s %-10s %-4d %5d %s\n", k.label, k.q, status, ms, "ERR "+err.Error())
			continue
		}
		fixture := filepath.Join(fixturesDir, fmt.Sprintf("probe-%02d-%s.html", i+1, slug(k.label)))
		if werr := os.WriteFile(fixture, body, 0o644); werr != nil {
			return fmt.Errorf("tulis fixture: %w", werr)
		}
		page, perr := garuda.ParseSearchPage(strings.NewReader(string(body)))
		if perr != nil {
			fmt.Printf("%-13s %-10s %-4d parse ERR %v\n", k.label, k.q, status, perr)
			continue
		}
		var label int
		exact := "-"
		if key := garuda.CanonicalISSN(k.q); key != "" {
			n := 0
			for _, r := range page.Rows {
				if garuda.CanonicalISSN(r.EISSN) == key {
					n++
				}
			}
			exact = fmt.Sprintf("%d/%d", n, len(page.Rows))
		}
		for _, r := range page.Rows {
			label += len(r.Areas)
		}
		note := ""
		if k.label == "multi" {
			// lanjut halaman 2 — uji paginasi utk E1
			body2, st2, err2 := c.Get(garuda.SearchURL(k.q, 2), garuda.DefaultReferer)
			if err2 != nil && body2 == nil {
				note = fmt.Sprintf("p2 ERR status=%d %v", st2, err2)
			} else {
				f2 := filepath.Join(fixturesDir, "probe-10-multi-p2.html")
				_ = os.WriteFile(f2, body2, 0o644)
				if page2, e2 := garuda.ParseSearchPage(strings.NewReader(string(body2))); e2 == nil {
					note = fmt.Sprintf("p2 rows=%d page=%d/%d", len(page2.Rows), page2.Page, page2.OfPages)
				} else {
					note = "p2 parse ERR"
				}
			}
		}
		if page.Found == -1 && k.q != "ab" {
			note = strings.TrimSpace(note + " tanpa blok found")
		}
		fmt.Printf("%-13s %-10s %-4d %5d %7d %6d %5d %6d %6s %s\n",
			k.label, k.q, status, ms, page.Found, page.Total, len(page.Rows), label, exact, note)
	}

	fmt.Printf("\nfixture tersimpan di: %s\n", fixturesDir)
	fmt.Println("temuan probe dicatat di doc 30 Bagian 13 (§13.4 langkah 3) saat keep-track.")
	return nil
}

// seedISSN membaca seed campuran utk probe: 4 E-ISSN + 1 P-ISSN valid dari
// rank S1 (read-only — db sumber TIDAK dimutasi schema oleh probe).
func seedISSN(path string) ([]string, string, error) {
	uri := "file:" + filepath.ToSlash(path) + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, "", err
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT electronic_issn FROM journals
		WHERE sinta_rank = 1 AND electronic_issn <> ''
		ORDER BY id LIMIT 4`)
	if err != nil {
		return nil, "", err
	}
	var e []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return nil, "", err
		}
		e = append(e, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, "", err
	}
	rows.Close()

	var p string
	if err := db.QueryRow(`
		SELECT print_issn FROM journals
		WHERE sinta_rank = 1 AND print_issn <> '' ORDER BY id LIMIT 1`).Scan(&p); err != nil {
		return nil, "", err
	}
	return e, p, nil
}

var reNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	out := reNonAlnum.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(out, "-")
}
