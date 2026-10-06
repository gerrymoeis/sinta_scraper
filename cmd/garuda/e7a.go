package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"
	"sinta-scraper/internal/storage"
)

// runE7A = Q4 E7a (doc 30 §14.6 sub-fase 1 — approve user 6 Okt 2026):
// dry-run aturan merge 4 lapis K5 (§6.2) pada 10 jurnal campuran → user
// review aturan SEBELUM coding merge produksi (E7b).
//
// Mode: READ-ONLY — nol GET, nol tulis db (hanya SELECT + tulis JSON hasil
// ke data/stage2/, di luar db). Sampling deterministik (urut id, tanpa
// random) supaya bisa direview & diulang identik:
//
//	2 dup DUPLICATE_GARUDA · 2 not_found · 2 subject kosong
//	(1 canonical terisi + 1 canonical kosong) · 2 garuda_url beda ·
//	2 matched ∩ sampel E6 (kandidat Crossref/DOAJ tersedia offline)
//
// 3 field per jurnal: subject_area (target journals), garuda_url (target
// journals), name (identitas → jalur REVIEW kebijakan utk kandidat
// non-official). Sumber timestamp:
//
//	journals.last_scraped_at = 2026-09-29 (Tahap 1) — nilai "sekarang"
//	enrichment.garuda_retrieved_at = 2026-10-03 (Q3 harvest)
//	e7aTime = 2026-10-04 (run E6) — kandidat Crossref/DOAJ offline
const e7aTime = "2026-10-04T00:00:00Z"

// e7aBaris = satu jurnal sampel + hasil evaluasi per field.
type e7aBaris struct {
	ID       int64           `json:"id"`
	Nama     string          `json:"nama"`
	Kategori string          `json:"kategori"`
	Flags    string          `json:"flags"`
	Match    string          `json:"match_status"`
	Fields   []garuda.E7Eval `json:"fields"`
}

// e7aJSON = envelope hasil (bahan review aturan user + lampiran doc 38).
type e7aJSON struct {
	Dibuat    string         `json:"dibuat"`
	Mode      string         `json:"mode"`
	Rank      map[string]int `json:"rank_sumber"`
	Sampel    []e7aBaris     `json:"sampel"`
	Ringkasan map[string]int `json:"ringkasan"`
}

// e7aRow = baris db gabungan (journals + enrichment + phase2) utk satu id.
type e7aRow struct {
	id        int64
	nama      string
	subj      string // journals.subject_area
	gjURL     string // journals.garuda_url
	lastScr   string // journals.last_scraped_at
	eGURL     string // enrichment.garuda_url
	eGTitle   string // enrichment.garuda_title
	eGSubject string // enrichment.garuda_subject
	eCanon    string // enrichment.subject_area_canonical
	eRet      string // enrichment.garuda_retrieved_at
	match     string // enrichment.match_status
	prov      string // enrichment.provenance (JSON map field → ProvEntry)
	flags     string // phase2_progress.flags (JSON set)
}

func runE7A(dbPath, e6Path, outPath string) error {
	// ---- 0. buka db READ-ONLY + muat hasil E6 (kandidat offline) --------
	uri := "file:" + strings.ReplaceAll(dbPath, "\\", "/") + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return fmt.Errorf("buka db read-only: %w", err)
	}
	defer db.Close()

	e6Map, err := e7aMuatE6(e6Path)
	if err != nil {
		return err
	}

	// ---- 1. sampling deterministik (urut id, exclude id terpakai) -------
	taken := map[int64]bool{}
	type pilihan struct {
		id  int64
		kat string
	}
	var pilih []pilihan
	must := func(q string, n int, kategori string) error {
		ids, err := e7aPick(db, q, n, taken)
		if err != nil {
			return fmt.Errorf("sampling %s: %w", kategori, err)
		}
		if len(ids) != n {
			return fmt.Errorf("sampling %s: dapat %d, want %d", kategori, len(ids), n)
		}
		for _, id := range ids {
			pilih = append(pilih, pilihan{id, kategori})
		}
		return nil
	}
	steps := []struct {
		q   string
		n   int
		kat string
	}{
		{`SELECT journal_id FROM phase2_progress
	      WHERE flags LIKE '%DUPLICATE_GARUDA%' ORDER BY journal_id`, 2, "dup-DUPLICATE_GARUDA"},
		{`SELECT journal_id FROM journal_enrichment
	      WHERE match_status='not_found' ORDER BY journal_id`, 2, "not_found"},
		{`SELECT j.id FROM journals j JOIN journal_enrichment e ON e.journal_id=j.id
	      WHERE (j.subject_area IS NULL OR j.subject_area='')
	        AND COALESCE(e.subject_area_canonical,'')<>''
	      ORDER BY j.id`, 1, "subject-kosong/canonical-ada"},
		{`SELECT j.id FROM journals j JOIN journal_enrichment e ON e.journal_id=j.id
	      WHERE (j.subject_area IS NULL OR j.subject_area='')
	        AND COALESCE(e.subject_area_canonical,'')=''
	      ORDER BY j.id`, 1, "subject-kosong/canonical-kosong"},
		{`SELECT j.id FROM journals j JOIN journal_enrichment e ON e.journal_id=j.id
	      WHERE COALESCE(j.garuda_url,'')<>'' AND COALESCE(e.garuda_url,'')<>''
	        AND j.garuda_url<>e.garuda_url
	      ORDER BY j.id`, 2, "garuda_url-beda"},
	}
	for _, s := range steps {
		if err := must(s.q, s.n, s.kat); err != nil {
			return err
		}
	}

	// matched ∩ sampel E6 (kandidat Crossref/DOAJ offline) — filter in Go.
	{
		rows, err := db.Query(`SELECT journal_id FROM journal_enrichment
			WHERE match_status='matched' ORDER BY journal_id`)
		if err != nil {
			return fmt.Errorf("sampling matched: %w", err)
		}
		n := 0
		for rows.Next() && n < 2 {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("sampling matched scan: %w", err)
			}
			if taken[id] {
				continue
			}
			if _, ada := e6Map[id]; !ada {
				continue
			}
			taken[id] = true
			pilih = append(pilih, pilihan{id, "matched∩E6"})
			n++
		}
		rows.Close()
		if n != 2 {
			return fmt.Errorf("sampling matched∩E6: dapat %d, want 2", n)
		}
	}

	// ---- 2. evaluasi 4 lapis per jurnal per field ------------------------
	out := e7aJSON{
		Dibuat:    time.Now().UTC().Format(time.RFC3339),
		Mode:      "E7a dry-run — READ-ONLY (nol GET, nol tulis db); bahan review aturan §6.2 sebelum E7b",
		Rank:      map[string]int{},
		Ringkasan: map[string]int{},
	}
	for s, r := range map[string]int{"manual": 5, "official": 4, "garuda": 3, "canonical": 3, "sinta": 2, "doaj": 1, "crossref": 1} {
		out.Rank[s] = r
	}

	fmt.Printf("=== E7a dry-run aturan merge 4 lapis (read-only) — %d jurnal × 3 field ===\n", len(pilih))
	for _, p := range pilih {
		row, err := e7aBaca(db, p.id)
		if err != nil {
			return fmt.Errorf("baca jurnal %d: %w", p.id, err)
		}
		baris := e7aBaris{
			ID: row.id, Nama: row.nama, Kategori: p.kat,
			Flags: row.flags, Match: row.match,
			Fields: e7aEval(&row, e6Map[p.id]),
		}
		out.Sampel = append(out.Sampel, baris)

		fmt.Printf("\n[id=%d] %s\n  kategori=%s · match=%s · flags=%s\n",
			row.id, row.nama, p.kat, row.match, row.flags)
		for _, f := range baris.Fields {
			out.Ringkasan[f.Aksi]++
			fmt.Printf("  %-14s %-9s %s\n", f.Field, f.Aksi, f.Alasan)
		}
	}

	fmt.Printf("\nRingkasan aksi: %v\n", out.Ringkasan)

	// ---- 3. tulis JSON hasil (di luar db, gitignored) --------------------
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal hasil: %w", err)
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		return fmt.Errorf("tulis %s: %w", outPath, err)
	}
	fmt.Printf("Tertulis: %s (%d byte, %d jurnal)\n", outPath, len(b), len(out.Sampel))
	return nil
}

// e7aPick = ambil n id pertama (ORDER BY sudah di query) yang belum terpakai.
func e7aPick(db *sql.DB, q string, n int, taken map[int64]bool) ([]int64, error) {
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if taken[id] {
			continue
		}
		taken[id] = true
		out = append(out, id)
		if len(out) == n {
			break
		}
	}
	return out, rows.Err()
}

// e7aBaca = ambil seluruh kolom yang dibutuhkan simulasi utk satu jurnal.
func e7aBaca(db *sql.DB, id int64) (e7aRow, error) {
	var r e7aRow
	r.id = id
	err := db.QueryRow(`
		SELECT j.name, COALESCE(j.subject_area,''), COALESCE(j.garuda_url,''),
		       j.last_scraped_at,
		       COALESCE(e.garuda_url,''), COALESCE(e.garuda_title,''),
		       COALESCE(e.garuda_subject,''), COALESCE(e.subject_area_canonical,''),
		       COALESCE(e.garuda_retrieved_at,''), COALESCE(e.match_status,''),
		       COALESCE(e.provenance,'{}'), COALESCE(p.flags,'{}')
		FROM journals j
		LEFT JOIN journal_enrichment e ON e.journal_id = j.id
		LEFT JOIN phase2_progress p ON p.journal_id = j.id
		WHERE j.id = ?`, id).
		Scan(&r.nama, &r.subj, &r.gjURL, &r.lastScr,
			&r.eGURL, &r.eGTitle, &r.eGSubject, &r.eCanon,
			&r.eRet, &r.match, &r.prov, &r.flags)
	if err != nil {
		return r, fmt.Errorf("scan: %w", err)
	}
	return r, nil
}

// e7aEval = bangun kandidat per field dari baris db + hasil E6 offline →
// jalankan evaluator 4 lapis.
func e7aEval(r *e7aRow, e6 *garuda.E6Hasil) []garuda.E7Eval {
	var evals []garuda.E7Eval

	// ---- 1) subject_area (target: journals.subject_area) ----------------
	nowS := "sinta"
	if strings.TrimSpace(r.subj) == "" {
		nowS = ""
	}
	var ksSubj []garuda.E7Kandidat
	if strings.TrimSpace(r.eCanon) != "" {
		ret := provRetrieved(r.prov, "subject_area")
		if ret == "" {
			ret = r.eRet
		}
		ksSubj = append(ksSubj, garuda.E7Kandidat{
			Sumber: "canonical", Value: r.eCanon, Retrieved: ret,
			Confidence: 1, Capture: "q3-harmonic-subject",
		})
	}
	if s := strings.TrimSpace(r.eGSubject); s != "" && s != r.eCanon {
		ksSubj = append(ksSubj, garuda.E7Kandidat{
			Sumber: "garuda", Value: s, Retrieved: r.eRet,
			Confidence: 1, Capture: "q3-subject-harvest",
		})
	}
	if e6 != nil && e6.Doaj.Tersedia && len(e6.Doaj.Subjek) > 0 {
		ksSubj = append(ksSubj, garuda.E7Kandidat{
			Sumber: "doaj", Value: strings.Join(e6.Doaj.Subjek, " | "),
			Retrieved: e7aTime, Confidence: 1, Capture: "e6-doaj",
		})
	}
	evals = append(evals, garuda.EvalMerge4Lapis(
		"subject_area", nowS, r.subj, r.lastScr, ksSubj, false))

	// ---- 2) garuda_url (target: journals.garuda_url) ---------------------
	nowS = "sinta"
	if strings.TrimSpace(r.gjURL) == "" {
		nowS = ""
	}
	var ksURL []garuda.E7Kandidat
	if strings.TrimSpace(r.eGURL) != "" {
		ev := ""
		switch r.id {
		case 684: // Studia Islamika — verifikasi E3 (doc 31 §4)
			ev = "E3 view 4 Okt: view/4979 hidup tapi ISSN/EISSN/DOI semua '-' (usang) → view/42863 lengkap & EISSN = DB"
		case 931: // Jurnal Elektronika — verifikasi E3 (doc 31 §4)
			ev = "E3 view 4 Okt: view/12707 Record Not Found (mati) → view/46753 hidup"
		}
		ksURL = append(ksURL, garuda.E7Kandidat{
			Sumber: "garuda", Value: r.eGURL, Retrieved: r.eRet,
			Confidence: 1, Capture: "q3-search", Evidence: ev,
		})
	}
	evals = append(evals, garuda.EvalMerge4Lapis(
		"garuda_url", nowS, r.gjURL, r.lastScr, ksURL, false))

	// ---- 3) name (identitas — kandidat non-official → REVIEW) ------------
	var ksName []garuda.E7Kandidat
	if t := strings.TrimSpace(r.eGTitle); t != "" && t != r.nama {
		ksName = append(ksName, garuda.E7Kandidat{
			Sumber: "garuda", Value: t, Retrieved: r.eRet,
			Confidence: 1, Capture: "q3-search",
		})
	}
	if e6 != nil {
		if e6.Crossref.Tersedia && e6.Crossref.Judul != "" && e6.Crossref.Judul != r.nama {
			ksName = append(ksName, garuda.E7Kandidat{
				Sumber: "crossref", Value: e6.Crossref.Judul,
				Retrieved: e7aTime, Confidence: 1, Capture: "e6-crossref",
			})
		}
		if e6.Doaj.Tersedia && e6.Doaj.Judul != "" && e6.Doaj.Judul != r.nama {
			ksName = append(ksName, garuda.E7Kandidat{
				Sumber: "doaj", Value: e6.Doaj.Judul,
				Retrieved: e7aTime, Confidence: 1, Capture: "e6-doaj",
			})
		}
	}
	evals = append(evals, garuda.EvalMerge4Lapis(
		"name", "sinta", r.nama, r.lastScr, ksName, true))

	return evals
}

// e7aMuatE6 = muat e6-results.json → peta journal_id (kandidat offline).
func e7aMuatE6(path string) (map[int64]*garuda.E6Hasil, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("baca %s: %w", path, err)
	}
	var old e6HasilJSON
	if err := json.Unmarshal(b, &old); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	m := make(map[int64]*garuda.E6Hasil, len(old.Items))
	for i := range old.Items {
		m[old.Items[i].ID] = &old.Items[i]
	}
	return m, nil
}

// provRetrieved = retrieved_at utk sebuah field dari JSON provenance
// (bila tak ada entri → "").
func provRetrieved(provJSON, field string) string {
	var prov map[string]storage.ProvEntry
	if err := json.Unmarshal([]byte(provJSON), &prov); err != nil {
		return ""
	}
	return prov[field].RetrievedAt
}
