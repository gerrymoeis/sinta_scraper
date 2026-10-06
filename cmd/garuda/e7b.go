package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"sinta-scraper/internal/storage"
)

// runE7B = Q4 E7b (doc 30 §14.6 sub-fase 2 — approve user 6 Okt 2026 atas
// 3 aturan E7a, doc 38 §5): sinkronisasi Tahap 2 → tabel `journals`
// (TULIS db, 0 GET):
//
//	(i)   isi journals.subject_area utk baris kosong DARI subject_area_canonical
//	      (fill-if-absent: yang sudah terisi TIDAK disentuh; baris canonical
//	      kosong / dobel-kosong DIBIARKAN) + provenance "journals_subject_area";
//	(ii)  overwrite journals.garuda_url utk daftar id tempat nilai Q3/E4b
//	      beda dgn Tahap 1 (approved: 9 — E3-verified 2, E4b dup-winner 6,
//	      E4b resolve 1) + provenance "journals_garuda_url";
//	(iii) name TIDAK disentuh (REVIEW → kandidat cuma pengetahuan, Fase 3).
//
// Mekanisme (teliti): snapshot PRE → guard jumlah target (129 / 9, drift =
// berhenti sebelum tulis) → backup .preE7.bak.db → tulis idempoten (loop
// kedua wajib 0) + provenance K5 → snapshot POST → diff per baris (hanya
// subject_area & garuda_url target yang boleh berubah) → e7b-results.json.
const (
	e7bJumlahFill = 129 // jumlah baris fill subject (fakta E7a/db 6 Okt)
	e7bJumlahURL  = 9   // jumlah garuda_url beda (fakta E7a/db 6 Okt)
)

type e7bJSON struct {
	Dibuat          string            `json:"dibuat"`
	Mode            string            `json:"mode"`
	Backup          string            `json:"backup"`
	FillSubjectFill int               `json:"fill_subject_terisi"`
	FillUlang       int               `json:"fill_subject_ulang_idempoten"`
	URLSync         int               `json:"garuda_url_terisi"`
	URLUlang        int               `json:"garuda_url_ulang_idempoten"`
	ProvTulis       int               `json:"provenance_tulis"`
	TargetFill      int               `json:"target_fill"`
	TargetURL       int               `json:"target_url"`
	Verifikasi      map[string]string `json:"verifikasi"`
	DiffLarang      []string          `json:"diff_larangan"` // harus kosong
	UrlBaris        []e7bURLBaris     `json:"url_baris"`
}

// e7bURLBaris = satu baris sinkronisasi garuda_url (asal nilai utk laporan).
type e7bURLBaris struct {
	ID       int64   `json:"id"`
	Sebelum  string  `json:"sebelum"`
	Sesudah  string  `json:"sesudah"`
	Retrievd string  `json:"retrieved_at"`
	Conf     float64 `json:"confidence"`
	MatchBy  string  `json:"matched_by"`
}

// e7bSnap = potret satu baris journals utk verifikasi K1 (raw utuh).
type e7bSnap struct {
	Name        string
	SubjectArea string
	GarudaURL   string
	ContentHash string
	LastScr     string
	PrintISSN   string
	EISSN       string
	Rank        int
	OJSStatus   string
}

func runE7B(dbPath, outPath string) error {
	start := time.Now().UTC().Format(time.RFC3339)

	// ---- 1. snapshot PRE + target (semua read-only) ----------------------
	pre, err := e7bSnapshot(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot PRE: %w", err)
	}
	fillRows, err := e7bTargetFill(dbPath)
	if err != nil {
		return fmt.Errorf("target fill: %w", err)
	}
	urlRows, err := e7bTargetURL(dbPath)
	if err != nil {
		return fmt.Errorf("target garuda_url: %w", err)
	}
	if len(fillRows) != e7bJumlahFill || len(urlRows) != e7bJumlahURL {
		return fmt.Errorf(
			"DRIFT target: fill=%d (want %d) · garuda_url=%d (want %d) — berhenti sebelum tulis; review dulu (db mungkin sudah disinkronkan atau berubah)",
			len(fillRows), e7bJumlahFill, len(urlRows), e7bJumlahURL)
	}
	fmt.Printf("target: fill subject=%d · overwrite garuda_url=%d\n", len(fillRows), len(urlRows))

	// ---- 2. backup SEBELUM tulis (sekali; pakai yang sudah ada bila ada) --
	bak := strings.TrimSuffix(dbPath, ".db") + ".preE7.bak.db"
	if _, err := os.Stat(bak); err != nil {
		if err := salinFile(dbPath, bak); err != nil {
			return fmt.Errorf("backup db: %w", err)
		}
		fmt.Printf("backup db → %s\n", bak)
	} else {
		fmt.Printf("backup sudah ada (dipakai apa adanya): %s\n", bak)
	}

	// ---- 3. tulis + provenance (idempoten: loop ke-2 wajib 0) ------------
	store, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("buka store: %w", err)
	}
	fillN, urlN, provN, fillN2, urlN2 := 0, 0, 0, 0, 0
	proses := func() (int, int, error) {
		f, u := 0, 0
		for _, r := range fillRows {
			n, err := store.UpdateJournalsE7(r.id, "subject_area", r.canon)
			if err != nil {
				return 0, 0, fmt.Errorf("fill subject j%d: %w", r.id, err)
			}
			f += int(n)
			if n > 0 {
				if err := store.MergeProvenance(r.id, "journals_subject_area", storage.ProvEntry{
					Value: r.canon, Source: "garuda", RetrievedAt: r.retr, Confidence: 1,
				}); err != nil {
					return 0, 0, fmt.Errorf("prov subject j%d: %w", r.id, err)
				}
			}
		}
		for _, r := range urlRows {
			n, err := store.UpdateJournalsE7(r.id, "garuda_url", r.target)
			if err != nil {
				return 0, 0, fmt.Errorf("sync garuda_url j%d: %w", r.id, err)
			}
			u += int(n)
			if n > 0 {
				if err := store.MergeProvenance(r.id, "journals_garuda_url", storage.ProvEntry{
					Value: r.target, Source: "garuda", RetrievedAt: r.retr, Confidence: r.conf,
				}); err != nil {
					return 0, 0, fmt.Errorf("prov garuda_url j%d: %w", r.id, err)
				}
			}
		}
		return f, u, nil
	}
	fillN, urlN, err = proses()
	if err != nil {
		store.Close()
		return fmt.Errorf("tulis: %w", err)
	}
	provN = fillN + urlN
	fillN2, urlN2, err = proses() // idempoten
	store.Close()
	if err != nil {
		return fmt.Errorf("ulang (idempoten): %w", err)
	}
	if fillN2 != 0 || urlN2 != 0 {
		return fmt.Errorf("IDEMPOTEN GAGAL: ulang mengubah fill=%d url=%d (harus 0) — cek backup %s", fillN2, urlN2, bak)
	}
	fmt.Printf("tulis: fill=%d · url=%d · prov=%d · ulang(idempoten)=%d/%d\n",
		fillN, urlN, provN, fillN2, urlN2)

	// ---- 4. snapshot POST + diff K1 (hanya target yang boleh berubah) ----
	post, err := e7bSnapshot(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot POST: %w", err)
	}
	fillSet := map[int64]string{}
	for _, r := range fillRows {
		fillSet[r.id] = r.canon
	}
	urlSet := map[int64]string{}
	for _, r := range urlRows {
		urlSet[r.id] = r.target
	}
	diff := e7bDiff(pre, post, fillSet, urlSet)
	if len(diff) > 0 {
		return fmt.Errorf("VERIFIKASI GAGAL — perubahan di luar target: %v · RESTORE manual dari %s", diff, bak)
	}

	// counts verifikasi akhir
	v, err := e7bCounts(dbPath)
	if err != nil {
		return fmt.Errorf("counts POST: %w", err)
	}
	if v["subj_kosong"] != 41 || v["url_beda"] != 0 || v["hash_unik"] != 261 || v["journals"] != 261 {
		return fmt.Errorf("VERIFIKASI GAGAL counts: subj_kosong=%d (want 41) url_beda=%d (want 0) hash_unik=%d journals=%d — RESTORE manual dari %s",
			v["subj_kosong"], v["url_beda"], v["hash_unik"], v["journals"], bak)
	}

	// ---- 5. hasil JSON ---------------------------------------------------
	out := e7bJSON{
		Dibuat:          start,
		Mode:            "E7b sinkronisasi journals — TULIS (0 GET) · fill-if-absent subject + overwrite garuda_url 9 · name tak disentuh",
		Backup:          bak,
		FillSubjectFill: fillN,
		FillUlang:       fillN2,
		URLSync:         urlN,
		URLUlang:        urlN2,
		ProvTulis:       provN,
		TargetFill:      e7bJumlahFill,
		TargetURL:       e7bJumlahURL,
		Verifikasi:      map[string]string{},
		DiffLarang:      diff,
	}
	for k, n := range v {
		out.Verifikasi[k] = fmt.Sprintf("%d", n)
	}
	for _, r := range urlRows {
		out.UrlBaris = append(out.UrlBaris, e7bURLBaris{
			ID: r.id, Sebelum: r.sebelum, Sesudah: r.target,
			Retrievd: r.retr, Conf: r.conf, MatchBy: r.matchBy,
		})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal hasil: %w", err)
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		return fmt.Errorf("tulis %s: %w", outPath, err)
	}

	fmt.Printf("VERIFIKASI OK · diff larangan=0 · counts: journals=%d subj_kosong=%d url_beda=%d hash_unik=%d\n",
		v["journals"], v["subj_kosong"], v["url_beda"], v["hash_unik"])
	fmt.Printf("Tertulis: %s (%d byte)\n", outPath, len(b))
	return nil
}

// ---- helper baca (read-only) -------------------------------------------

type e7bFillRow struct {
	id    int64
	canon string
	retr  string
}

type e7bURLRow struct {
	id      int64
	target  string
	sebelum string
	retr    string
	conf    float64
	matchBy string
}

func e7bOpenRO(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+strings.ReplaceAll(dbPath, "\\", "/")+"?mode=ro")
}

func e7bTargetFill(dbPath string) ([]e7bFillRow, error) {
	db, err := e7bOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`
		SELECT j.id, e.subject_area_canonical, e.provenance, COALESCE(e.garuda_retrieved_at,'')
		FROM journals j JOIN journal_enrichment e ON e.journal_id = j.id
		WHERE (j.subject_area IS NULL OR j.subject_area = '')
		  AND COALESCE(e.subject_area_canonical, '') <> ''
		ORDER BY j.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []e7bFillRow
	for rows.Next() {
		var r e7bFillRow
		var prov string
		if err := rows.Scan(&r.id, &r.canon, &prov, &r.retr); err != nil {
			return nil, err
		}
		if ret := provRetrieved(prov, "subject_area"); ret != "" {
			r.retr = ret // provenance Q3 menang (lebih presisi)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func e7bTargetURL(dbPath string) ([]e7bURLRow, error) {
	db, err := e7bOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`
		SELECT j.id, e.garuda_url, j.garuda_url,
		       COALESCE(e.garuda_retrieved_at,''), e.match_confidence, COALESCE(e.matched_by,'')
		FROM journals j JOIN journal_enrichment e ON e.journal_id = j.id
		WHERE COALESCE(j.garuda_url,'') <> '' AND COALESCE(e.garuda_url,'') <> ''
		  AND j.garuda_url <> e.garuda_url
		ORDER BY j.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []e7bURLRow
	for rows.Next() {
		var r e7bURLRow
		if err := rows.Scan(&r.id, &r.target, &r.sebelum, &r.retr, &r.conf, &r.matchBy); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func e7bSnapshot(dbPath string) (map[int64]e7bSnap, error) {
	db, err := e7bOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`
		SELECT id, name, COALESCE(subject_area,''), COALESCE(garuda_url,''),
		       content_hash, last_scraped_at, print_issn, electronic_issn,
		       sinta_rank, ojs_status
		FROM journals`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]e7bSnap{}
	for rows.Next() {
		var id int64
		var s e7bSnap
		if err := rows.Scan(&id, &s.Name, &s.SubjectArea, &s.GarudaURL,
			&s.ContentHash, &s.LastScr, &s.PrintISSN, &s.EISSN, &s.Rank, &s.OJSStatus); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}

// e7bDiff = periksa perubahan antar snapshot: hanya subject_area (id ∈ fillSet,
// nilai = canonical) & garuda_url (id ∈ urlSet, nilai = target) yang boleh
// berubah; selain itu = pelanggaran K1.
func e7bDiff(pre, post map[int64]e7bSnap, fillSet, urlSet map[int64]string) []string {
	var bad []string
	if len(pre) != len(post) {
		bad = append(bad, fmt.Sprintf("jumlah baris berubah %d→%d", len(pre), len(post)))
	}
	for id, a := range pre {
		b, ok := post[id]
		if !ok {
			bad = append(bad, fmt.Sprintf("j%d hilang", id))
			continue
		}
		if a.Name != b.Name || a.ContentHash != b.ContentHash || a.LastScr != b.LastScr ||
			a.PrintISSN != b.PrintISSN || a.EISSN != b.EISSN || a.Rank != b.Rank ||
			a.OJSStatus != b.OJSStatus {
			bad = append(bad, fmt.Sprintf("j%d kolom non-target berubah", id))
		}
		if a.SubjectArea != b.SubjectArea {
			if want, ok := fillSet[id]; !ok || b.SubjectArea != want {
				bad = append(bad, fmt.Sprintf("j%d subject_area berubah di luar target fill", id))
			}
		}
		if a.GarudaURL != b.GarudaURL {
			if want, ok := urlSet[id]; !ok || b.GarudaURL != want {
				bad = append(bad, fmt.Sprintf("j%d garuda_url berubah di luar target url", id))
			}
		}
	}
	return bad
}

func e7bCounts(dbPath string) (map[string]int64, error) {
	db, err := e7bOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[string]int64{}
	q := []struct {
		key, sql string
	}{
		{"journals", `SELECT COUNT(*) FROM journals`},
		{"subj_kosong", `SELECT COUNT(*) FROM journals WHERE subject_area IS NULL OR subject_area = ''`},
		{"hash_unik", `SELECT COUNT(DISTINCT content_hash) FROM journals`},
		{"url_beda", `SELECT COUNT(*) FROM journals j JOIN journal_enrichment e ON e.journal_id = j.id
			WHERE COALESCE(j.garuda_url,'') <> '' AND COALESCE(e.garuda_url,'') <> '' AND j.garuda_url <> e.garuda_url`},
		{"canon_terisi", `SELECT COUNT(*) FROM journal_enrichment WHERE COALESCE(subject_area_canonical,'') <> ''`},
	}
	for _, x := range q {
		var n int64
		if err := db.QueryRow(x.sql).Scan(&n); err != nil {
			return nil, fmt.Errorf("%s: %w", x.key, err)
		}
		out[x.key] = n
	}
	return out, nil
}
