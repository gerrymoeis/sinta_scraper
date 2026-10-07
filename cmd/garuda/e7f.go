package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"
	"sinta-scraper/internal/storage"
)

// runE7F = Q4 E7f (doc 30 §14.6 butir 6 — pagu 41+1 GET APPROVED user 6 Okt
// 2026): fill subject_area + print_issn + doaj_url utk 41 baris dobel-kosong
// (subjek & canonical kosong), + append-dedup Opsi A utk 2 found miss-8 yg
// subjek SINTA terisi (j689 dobel-kosong → tercakup query 41), nol DDL.
//
// Struktur (satu run, dua fase):
//  1. QUERY (live): bentuk hyphen E-ISSN dulu — fixture-first cascade
//     (e7f-doaj → e7d-doaj → e6-doaj, 0 GET utk yang sudah ada) → GET.
//     Resume json (definitif 200/404) → rerun 0 GET. Pagu 41 kumulatif.
//  2. SPOT-CHECK: 1 GET halaman TOC doaj_url pertama yg found (pagu terpisah
//  1. — bukti pola URL publik valid.
//  3. FILL (offline, 0 GET — pola E7b): backup .preE7f.bak.db → tulis
//     idempoten (loop ke-2 wajib 0) + provenance → snapshot diff K1 → counts.
//
// Kolom target (whitelist UpdateJournalsE7): subject_area (fill kosong /
// append dedup FoldSubject), print_issn (fill dari garuda_pissn — format
// polos, pola db), doaj_url (https://doaj.org/toc/{hyphen}). Canonical =
// SetSubjectCanonical nilai merge. Name/content_hash/last_scraped_at tak
// disentuh (K1; hash ikut konvensi E7b: tak di-rehash utk kolom sync).
const (
	paguE7f        = 41 // GET search DOAJ bentuk hyphen (kumulatif lintas run)
	paguSpotE7f    = 1  // GET spot-check TOC (terpisah — approve 41+1)
	e7fJumlahBaris = 41 // guard drift query  (fakta db 6 Okt)
	e7fJumlahISSN  = 13 // kandidat dgn garuda_pissn VALID (dari 28 terisi
	// garuda_pissn — 15 placeholder '-' di-skip, koreksi 7 Okt)
	e7fBaselinePrint = 30 // baris print_issn kosong/'0' sebelum E7f (fakta db)
)

var errPaguE7f = errors.New("PAGU E7f HABIS")

// 3 found miss-8 (E7d): j689 = dobel-kosong → tercakup query 41 (group A);
// j673/j3974 subjek SINTA terisi → APPEND dedup Opsi A (fixture E7d, 0 GET).
var e7fAppend = []struct {
	id  int64
	key string
}{{673, "23562641"}, {3974, "25407872"}}

type e7fHasilJSON struct {
	Dibuat   string            `json:"dibuat"`
	Pagu     int               `json:"pagu_get"`
	PaguSpot int               `json:"pagu_spotcheck"`
	GetTotal int               `json:"get_run_ini"`
	Spot     *e7fSpot          `json:"spotcheck_toc,omitempty"`
	Jumlah   int               `json:"jumlah"`
	Ringkas  garuda.E7fRingkas `json:"ringkasan"`
	Fill     *e7fFillJSON      `json:"fill,omitempty"`
	Items    []garuda.E7fHasil `json:"items"`
}

type e7fSpot struct {
	URL  string `json:"url"`
	HTTP int    `json:"http"`
	Dari string `json:"dari"` // GET | resume
}

type e7fFillJSON struct {
	Backup     string            `json:"backup"`
	SubjFill   int               `json:"subjek_fill"`
	SubjAppend int               `json:"subjek_append"`
	SubjUlang  int               `json:"subjek_ulang_idempoten"`
	PrintFill  int               `json:"print_issn_fill"`
	PrintUlang int               `json:"print_issn_ulang_idempoten"`
	URLFill    int               `json:"doaj_url_fill"`
	URLUlang   int               `json:"doaj_url_ulang_idempoten"`
	ProvTulis  int               `json:"provenance_tulis"`
	Verifikasi map[string]string `json:"verifikasi"`
	DiffLarang []string          `json:"diff_larangan"` // harus kosong
	Koreksi    string            `json:"koreksi,omitempty"`
}

// ---- helper baca db (read-only) -----------------------------------------

func e7fOpenRO(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+strings.ReplaceAll(dbPath, "\\", "/")+"?mode=ro")
}

// e7fTarget41 = 41 baris dobel-kosong (subjek SINTA kosong DAN canonical
// kosong). Key = E-ISSN dulu, fallback P-ISSN / kolom Garuda — WAJIB valid
// (fakta db: 41/41).
func e7fTarget41(dbPath string) ([]garuda.E7fBaris, error) {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`
		SELECT j.id, j.name, COALESCE(j.electronic_issn,''), COALESCE(j.print_issn,''),
		       COALESCE(e.garuda_eissn,''), COALESCE(e.garuda_pissn,'')
		FROM journals j JOIN journal_enrichment e ON e.journal_id = j.id
		WHERE (j.subject_area IS NULL OR j.subject_area = '')
		  AND COALESCE(e.subject_area_canonical, '') = ''
		ORDER BY j.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []garuda.E7fBaris
	for rows.Next() {
		var r garuda.E7fBaris
		var eIssn, pIssn, gE, gP string
		if err := rows.Scan(&r.ID, &r.Nama, &eIssn, &pIssn, &gE, &gP); err != nil {
			return nil, err
		}
		key := garuda.NormISSN(eIssn)
		if key == "" {
			key = garuda.NormISSN(pIssn)
		}
		if key == "" {
			key = garuda.NormISSN(gE)
		}
		if key == "" {
			key = garuda.NormISSN(gP)
		}
		if key == "" {
			return nil, fmt.Errorf("j%d (%s) tanpa ISSN valid — berhenti (jujur, bukan skip)", r.ID, r.Nama)
		}
		r.Key = key
		out = append(out, r)
	}
	return out, rows.Err()
}

type e7fIssnRow struct {
	id    int64
	value string // garuda_pissn (format polos — pola kolom print_issn)
}

// e7fTargetISSN = baris print_issn kosong/'0' dgn garuda_pissn VALID
// (koreksi 7 Okt: 13 valid NormISSN; 15 placeholder garuda_pissn '-' tak
// lolos — doc 30 §6.2 butir 2 melarang placeholder masuk kolom). Dilewat
// dikembalikan utk dicatat jujur, bukan disenyapkan.
func e7fTargetISSN(dbPath string) (rows_ []e7fIssnRow, dilewat []int64, err error) {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	rs, err := db.Query(`
		SELECT j.id, e.garuda_pissn
		FROM journals j JOIN journal_enrichment e ON e.journal_id = j.id
		WHERE COALESCE(j.print_issn,'') IN ('','0')
		  AND COALESCE(e.garuda_pissn,'') <> ''
		ORDER BY j.id`)
	if err != nil {
		return nil, nil, err
	}
	defer rs.Close()
	for rs.Next() {
		var r e7fIssnRow
		if err := rs.Scan(&r.id, &r.value); err != nil {
			return nil, nil, err
		}
		// value ditulis dgn format kanonik polos (pola db) — NormISSN
		// menolak placeholder ('-' → "").
		v := garuda.NormISSN(r.value)
		if v == "" {
			dilewat = append(dilewat, r.id)
			continue
		}
		r.value = v
		rows_ = append(rows_, r)
	}
	return rows_, dilewat, rs.Err()
}

type e7fSubjRow struct {
	id      int64
	sebelum string
	sesudah string
	append  bool
}

// e7fAppendRows = rencana append utk 2 found miss-8 (subjek SINTA terisi) —
// subjek dari fixture E7d (0 GET). Guard: baris ketemu, subjek terisi, E-ISSN
// cocok dgn key fixture, DOAJ found.
func e7fAppendRows(dbPath, fixturesDir string) ([]e7fSubjRow, error) {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var out []e7fSubjRow
	for _, a := range e7fAppend {
		var nama, subj, eIssn string
		if err := db.QueryRow(
			`SELECT name, COALESCE(subject_area,''), COALESCE(electronic_issn,'')
			 FROM journals WHERE id = ?`, a.id,
		).Scan(&nama, &subj, &eIssn); err != nil {
			return nil, fmt.Errorf("j%d (%s): %w", a.id, a.key, err)
		}
		if subj == "" || garuda.NormISSN(eIssn) != a.key {
			return nil, fmt.Errorf(
				"j%d: guard append gagal (subjek=%q eissn=%q want key %s) — review dulu",
				a.id, subj, eIssn, a.key)
		}
		fixPath := filepath.Join(fixturesDir, fmt.Sprintf("e7d-doaj-%s.json", a.key))
		body, err := os.ReadFile(fixPath)
		if err != nil {
			return nil, fmt.Errorf("fixture E7d j%d %s: %w", a.id, a.key, err)
		}
		d, err := garuda.ParseDOAJSearch(body, a.key)
		if err != nil || !d.Tersedia {
			return nil, fmt.Errorf("fixture E7d j%d: parse/found gagal (%v / tersedia=%v)",
				a.id, err, d.Tersedia)
		}
		sesudah := garuda.MergeSubjectArea(subj, d.Subjek)
		out = append(out, e7fSubjRow{id: a.id, sebelum: subj, sesudah: sesudah, append: true})
	}
	return out, nil
}

// e7fUrlsSampel = doaj_url utk record DOAJ found pada hasil E6 (8) & E7d (6,
// termasuk 3 miss-8) — baca json, 0 GET. URL = pola TOC.
func e7fUrlsSampel(jsonPath string) (map[int64]string, error) {
	out := map[int64]string{}
	b, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, err
	}
	var j struct {
		Items []struct {
			ID   int64  `json:"journal_id"`
			Key  string `json:"issn_kanonik"`
			Doaj struct {
				Tersedia bool `json:"tersedia"`
			} `json:"doaj"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(jsonPath), err)
	}
	for _, it := range j.Items {
		if !it.Doaj.Tersedia {
			continue
		}
		u := garuda.DoajTocURL(it.Key)
		if u == "" {
			return nil, fmt.Errorf("%s: j%d key %q tak valid utk TOC", filepath.Base(jsonPath), it.ID, it.Key)
		}
		out[it.ID] = u
	}
	return out, nil
}

// ---- snapshot & diff K1 (pola E7b + kolom doaj_url/print_issn) ----------

type e7fSnap struct {
	Name        string
	SubjectArea string
	GarudaURL   string
	ContentHash string
	LastScr     string
	PrintISSN   string
	EISSN       string
	DoajURL     string
	Rank        int
	OJSStatus   string
}

func e7fSnapshot(dbPath string) (map[int64]e7fSnap, error) {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`
		SELECT id, name, COALESCE(subject_area,''), COALESCE(garuda_url,''),
		       content_hash, last_scraped_at, COALESCE(print_issn,''),
		       COALESCE(electronic_issn,''), COALESCE(doaj_url,''),
		       sinta_rank, ojs_status
		FROM journals`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]e7fSnap{}
	for rows.Next() {
		var id int64
		var s e7fSnap
		if err := rows.Scan(&id, &s.Name, &s.SubjectArea, &s.GarudaURL,
			&s.ContentHash, &s.LastScr, &s.PrintISSN, &s.EISSN, &s.DoajURL,
			&s.Rank, &s.OJSStatus); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}

func e7fSnapshotCanon(dbPath string) (map[int64]string, error) {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT journal_id, COALESCE(subject_area_canonical,'') FROM journal_enrichment`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

// e7fDiff = K1: hanya subject_area (id ∈ subjWant, nilai persis), print_issn
// (id ∈ issnWant), doaj_url (id ∈ urlWant), canonical (id ∈ subjWant) yg
// boleh berubah; sisanya = pelanggaran.
func e7fDiff(pre, post map[int64]e7fSnap, preC, postC map[int64]string,
	subjWant, issnWant, urlWant map[int64]string) []string {
	var bad []string
	if len(pre) != len(post) {
		bad = append(bad, fmt.Sprintf("jumlah baris journals %d→%d", len(pre), len(post)))
	}
	for id, a := range pre {
		b, ok := post[id]
		if !ok {
			bad = append(bad, fmt.Sprintf("j%d hilang", id))
			continue
		}
		if a.Name != b.Name || a.ContentHash != b.ContentHash || a.LastScr != b.LastScr ||
			a.GarudaURL != b.GarudaURL || a.EISSN != b.EISSN || a.Rank != b.Rank ||
			a.OJSStatus != b.OJSStatus {
			bad = append(bad, fmt.Sprintf("j%d kolom non-target berubah", id))
		}
		if a.SubjectArea != b.SubjectArea {
			if want, ok := subjWant[id]; !ok || b.SubjectArea != want {
				bad = append(bad, fmt.Sprintf("j%d subject_area berubah di luar target", id))
			}
		}
		if a.PrintISSN != b.PrintISSN {
			if want, ok := issnWant[id]; !ok || b.PrintISSN != want {
				bad = append(bad, fmt.Sprintf("j%d print_issn berubah di luar target", id))
			}
		}
		if a.DoajURL != b.DoajURL {
			if want, ok := urlWant[id]; !ok || b.DoajURL != want {
				bad = append(bad, fmt.Sprintf("j%d doaj_url berubah di luar target", id))
			}
		}
		if ca, cb := preC[id], postC[id]; ca != cb {
			if want, ok := subjWant[id]; !ok || cb != want {
				bad = append(bad, fmt.Sprintf("j%d canonical berubah di luar target", id))
			}
		}
	}
	for id := range postC {
		if _, ok := preC[id]; !ok {
			bad = append(bad, fmt.Sprintf("j%d canonical baris baru", id))
		}
	}
	return bad
}

// e7fExistingIDs = set id baris journals (validasi target tulis — id luar db
// = no-op, tapi wajib disaring & dicatat sejak rencana).
func e7fExistingIDs(dbPath string) (map[int64]bool, error) {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id FROM journals`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func e7fCounts(dbPath string) (map[string]int64, error) {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[string]int64{}
	q := []struct{ key, sql string }{
		{"journals", `SELECT COUNT(*) FROM journals`},
		{"hash_unik", `SELECT COUNT(DISTINCT content_hash) FROM journals`},
		{"subj_kosong", `SELECT COUNT(*) FROM journals WHERE subject_area IS NULL OR subject_area = ''`},
		{"print_kosong", `SELECT COUNT(*) FROM journals WHERE COALESCE(print_issn,'') IN ('','0')`},
		// koreksi 7 Okt: placeholder dilarang & tak boleh ada kandidat valid
		// tersisa (IN ('','0','-') utk garuda_pissn = batas fakta db: '0'/'-'/'';
		// nilai aneh dlm kolom enrich garuda_pissn tak dihitung — utk baris
		// itu print_issn baseline sudah terisi).
		{"print_placeholder", `SELECT COUNT(*) FROM journals WHERE COALESCE(print_issn,'') IN ('-','0')`},
		{"print_sisa_kandidat", `
			SELECT COUNT(*) FROM journals j JOIN journal_enrichment e ON e.journal_id = j.id
			WHERE COALESCE(j.print_issn,'') IN ('','0')
			  AND COALESCE(e.garuda_pissn,'') NOT IN ('','0','-')`},
		{"doaj_terisi", `SELECT COUNT(*) FROM journals WHERE COALESCE(doaj_url,'') <> ''`},
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

// ---- driver utama ---------------------------------------------------------

func runE7F(dbPath, fixturesDir string, refresh bool, delayMin, delayMax time.Duration) error {
	start := time.Now().UTC().Format(time.RFC3339)

	// ---- 1. target (read-only, guard first-run) ---------------------------
	baris, err := e7fTarget41(dbPath)
	if err != nil {
		return err
	}
	issnRows, lewatISSN, err := e7fTargetISSN(dbPath)
	if err != nil {
		return err
	}
	if len(lewatISSN) > 0 {
		fmt.Printf("lewati %d garuda_pissn placeholder (bukan ISSN — doc30 §6.2): %v\n",
			len(lewatISSN), lewatISSN)
	}
	appRows, err := e7fAppendRows(dbPath, fixturesDir)
	if err != nil {
		return err
	}
	if len(appRows) != len(e7fAppend) {
		return fmt.Errorf("guard append: %d (want %d)", len(appRows), len(e7fAppend))
	}

	// ---- 2. resume json ---------------------------------------------------
	jsonPath := filepath.Join(filepath.Dir(fixturesDir), "e7f-results.json")
	hasilLama := map[string]garuda.E7fHasil{}
	var getLama int
	var spotOld *e7fSpot
	var fillLama *e7fFillJSON
	var itemsLamaUrut []garuda.E7fHasil
	if !refresh {
		if b, err := os.ReadFile(jsonPath); err == nil {
			var old e7fHasilJSON
			if json.Unmarshal(b, &old) == nil {
				for _, h := range old.Items {
					hasilLama[h.Key] = h
				}
				itemsLamaUrut = old.Items
				getLama = old.GetTotal
				spotOld = old.Spot
				fillLama = old.Fill
				fmt.Printf("resume: %d hasil lama (%d GET) dari %s\n",
					len(hasilLama), getLama, filepath.Base(jsonPath))
			}
		}
	}
	if len(hasilLama) == 0 {
		// first-run: guard drift target sebelum GET/tulis.
		if len(baris) != e7fJumlahBaris {
			return fmt.Errorf("DRIFT target: dobel-kosong=%d (want %d) — review db dulu",
				len(baris), e7fJumlahBaris)
		}
		if len(issnRows) != e7fJumlahISSN {
			return fmt.Errorf("DRIFT target: print_issn fill=%d (want %d) — review db dulu",
				len(issnRows), e7fJumlahISSN)
		}
	}
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		return fmt.Errorf("buat dir fixture: %w", err)
	}

	fmt.Printf("== E7f fill subject/print_issn/doaj_url (pagu %d GET + %d spot, delay %s-%s) ==\n",
		paguE7f, paguSpotE7f, delayMin, delayMax)
	fmt.Printf("target: dobel-kosong=%d · print_issn=%d · append=%d\n",
		len(baris), len(issnRows), len(appRows))

	c := garuda.NewClient("api-e7f", uaDefault, delayMin, delayMax)
	var getTotal int
	get := func(rawURL string) ([]byte, int, error) {
		if getLama+getTotal >= paguE7f {
			return nil, 0, fmt.Errorf("pagu E7f (%d): %w", paguE7f, errPaguE7f)
		}
		getTotal++
		return c.Get(rawURL, "")
	}

	// ---- 3. fase QUERY (fixture cascade → GET) ----------------------------
	var gotFix, gotResume int
	items := make([]garuda.E7fHasil, 0, len(baris))
	terpakai := map[string]bool{}
	for i, b := range baris {
		terpakai[b.Key] = true
		query := garuda.HyphenISSN(b.Key)
		if query == "" {
			return fmt.Errorf("j%d: HyphenISSN(%q) kosong", b.ID, b.Key)
		}
		h := garuda.E7fHasil{ID: b.ID, Nama: b.Nama, Key: b.Key, Query: query}

		old, adaLama := hasilLama[b.Key]
		switch {
		case !refresh && adaLama && (old.Doaj.HTTP == 200 || old.Doaj.HTTP == 404):
			h.Doaj, h.Sumber = old.Doaj, "resume"
			gotResume++
		default:
			var body []byte
			status := 0
			sumber := ""
			for _, pref := range []string{"e7f-doaj", "e7d-doaj", "e6-doaj"} {
				if fb, err := os.ReadFile(filepath.Join(fixturesDir,
					fmt.Sprintf("%s-%s.json", pref, b.Key))); err == nil {
					body, sumber = fb, "fixture "+pref
					gotFix++
					break
				}
			}
			if body == nil {
				b2, st, err2 := get("https://doaj.org/api/search/journals/" + query)
				if errors.Is(err2, errPaguE7f) {
					return err2
				}
				sumber = "GET"
				if err2 != nil {
					h.Doaj.Error = "unreachable: " + err2.Error()
				}
				body, status = b2, st
			}
			h.Doaj.HTTP = status
			if status == 0 && body == nil {
				// unreachable — sudah tercatat di Error
			} else {
				if status == 0 {
					status = 200 // dari fixture (body mentah, tanpa status http)
					h.Doaj.HTTP = 200
				}
				switch {
				case status == 200:
					if p, perr := garuda.ParseDOAJSearch(body, b.Key); perr != nil {
						h.Doaj.Error = perr.Error()
					} else {
						p.HTTP = 200
						h.Doaj = p
					}
					if sumber == "GET" {
						if werr := os.WriteFile(filepath.Join(fixturesDir,
							fmt.Sprintf("e7f-doaj-%s.json", b.Key)), body, 0o644); werr != nil {
							return fmt.Errorf("tulis fixture e7f %s: %w", b.Key, werr)
						}
					}
				case status == 404:
					// tak terdaftar — coverage jujur
				default:
					h.Doaj.Error = fmt.Sprintf("http %d", status)
				}
			}
			h.Sumber = sumber
		}

		tandai := "nol"
		if h.Doaj.Tersedia {
			tandai = "FOUND"
		}
		fmt.Printf("[%2d/%d] j%-6d %-10s -> %-5s http=%-3d %-14s %s\n",
			i+1, len(baris), b.ID, query, tandai, h.Doaj.HTTP, h.Sumber, b.Nama)
		items = append(items, h)
	}
	// baris yg terisi subjek lewat fill run sebelumnya (tak lagi dobel-kosong)
	// tetap ikut agar ringkasan/plan STABLE antar run.
	for _, h := range itemsLamaUrut {
		if !terpakai[h.Key] {
			items = append(items, h)
		}
	}

	// ---- 4. spot-check TOC (1 GET, pagu terpisah) -------------------------
	var foundPertama *garuda.E7fHasil
	for i := range items {
		if items[i].Doaj.Tersedia {
			foundPertama = &items[i]
			break
		}
	}
	spot := spotOld
	if spot == nil && foundPertama != nil {
		u := garuda.DoajTocURL(foundPertama.Key)
		body, st, err2 := c.Get(u, "")
		_ = body
		if err2 != nil {
			fmt.Printf("spot-check TOC %s: unreachable (%v) — dicatat jujur\n", u, err2)
			st = 0
		}
		spot = &e7fSpot{URL: u, HTTP: st, Dari: "GET"}
		fmt.Printf("spot-check: %s -> http=%d\n", u, st)
	}

	rep := e7fHasilJSON{
		Dibuat: start, Pagu: paguE7f, PaguSpot: paguSpotE7f,
		GetTotal: getLama + getTotal,
		Spot:     spot,
		Jumlah:   len(items),
		Ringkas:  garuda.HitungE7f(items),
		Fill:     fillLama,
		Items:    items,
	}
	if w, err := json.MarshalIndent(rep, "", "  "); err != nil {
		return fmt.Errorf("marshal json: %w", err)
	} else if err := os.WriteFile(jsonPath, w, 0o644); err != nil {
		return fmt.Errorf("tulis json: %w", err)
	}

	// ---- 5. bangun rencana FILL (0 GET) -----------------------------------
	planSubj := make([]e7fSubjRow, 0, len(items))
	urls := map[int64]string{}
	for _, h := range items {
		if h.Doaj.Tersedia {
			u := garuda.DoajTocURL(h.Key)
			if u == "" {
				return fmt.Errorf("j%d: TOC url kosong utk key %q", h.ID, h.Key)
			}
			urls[h.ID] = u
			if len(h.Doaj.Subjek) > 0 {
				planSubj = append(planSubj, e7fSubjRow{
					id: h.ID, sesudah: garuda.MergeSubjectArea("", h.Doaj.Subjek),
				})
			}
		}
	}
	for _, f := range []string{"e6-results.json", "e7d-results.json"} {
		m, err := e7fUrlsSampel(filepath.Join(filepath.Dir(fixturesDir), f))
		if err != nil {
			return fmt.Errorf("urls sampel: %w", err)
		}
		for id, u := range m {
			urls[id] = u
		}
	}
	// Sampel E6 memuat 6 id silang-rank (S2/S3) yg hanya hidup di db stage1 —
	// utk id tak ada di db enrich: skip jujur (bukan doaj_url baris ini, dan
	// UpdateJournalsE7 utk id tak ada = no-op; counts akan menangkap salah
	// tempat andai ada kolisi id).
	idsDB, err := e7fExistingIDs(dbPath)
	if err != nil {
		return fmt.Errorf("id db: %w", err)
	}
	var lewat []int64
	for id := range urls {
		if !idsDB[id] {
			lewat = append(lewat, id)
			delete(urls, id)
		}
	}
	if len(lewat) > 0 {
		fmt.Printf("lewati %d id sampel di luar db enrich (S2/S3 db2): %v\n", len(lewat), lewat)
	}
	planSubj = append(planSubj, appRows...)

	// want-map utk diff (id → nilai final).
	subjWant, issnWant, urlWant := map[int64]string{}, map[int64]string{}, map[int64]string{}
	for _, r := range planSubj {
		subjWant[r.id] = r.sesudah
	}
	for _, r := range issnRows {
		issnWant[r.id] = r.value
	}
	for id, u := range urls {
		urlWant[id] = u
	}
	fmt.Printf("rencana tulis: subject=%d (fill %d + append %d) · print_issn=%d · doaj_url=%d\n",
		len(planSubj), len(planSubj)-len(appRows), len(appRows), len(issnRows), len(urls))

	// ---- 6. backup SEBELUM tulis -----------------------------------------
	bak := strings.TrimSuffix(dbPath, ".db") + ".preE7f.bak.db"
	if _, err := os.Stat(bak); err != nil {
		if err := salinFile(dbPath, bak); err != nil {
			return fmt.Errorf("backup db: %w", err)
		}
		fmt.Printf("backup db → %s\n", bak)
	} else {
		fmt.Printf("backup sudah ada (dipakai apa adanya): %s\n", bak)
	}

	pre, err := e7fSnapshot(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot PRE: %w", err)
	}
	preC, err := e7fSnapshotCanon(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot PRE canonical: %w", err)
	}

	// ---- 7. tulis idempoten + provenance (loop ke-2 wajib 0) --------------
	store, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("buka store: %w", err)
	}
	type tulis struct{ subjFill, subjApp, issn, url, prov int }
	proses := func() (tulis, error) {
		var t tulis
		for _, r := range planSubj {
			n, err := store.UpdateJournalsE7(r.id, "subject_area", r.sesudah)
			if err != nil {
				return t, fmt.Errorf("subject j%d: %w", r.id, err)
			}
			if n > 0 {
				if err := store.SetSubjectCanonical(r.id, r.sesudah); err != nil {
					return t, fmt.Errorf("canonical j%d: %w", r.id, err)
				}
				if err := store.MergeProvenance(r.id, "journals_subject_area", storage.ProvEntry{
					Value: r.sesudah, Source: "doaj", RetrievedAt: start, Confidence: 1,
				}); err != nil {
					return t, fmt.Errorf("prov subject j%d: %w", r.id, err)
				}
				t.prov++
				if r.append {
					t.subjApp++
				} else {
					t.subjFill++
				}
			}
		}
		for _, r := range issnRows {
			n, err := store.UpdateJournalsE7(r.id, "print_issn", r.value)
			if err != nil {
				return t, fmt.Errorf("print_issn j%d: %w", r.id, err)
			}
			if n > 0 {
				if err := store.MergeProvenance(r.id, "journals_print_issn", storage.ProvEntry{
					Value: r.value, Source: "garuda", RetrievedAt: start, Confidence: 1,
				}); err != nil {
					return t, fmt.Errorf("prov print j%d: %w", r.id, err)
				}
				t.issn++
				t.prov++
			}
		}
		for id, u := range urls {
			n, err := store.UpdateJournalsE7(id, "doaj_url", u)
			if err != nil {
				return t, fmt.Errorf("doaj_url j%d: %w", id, err)
			}
			if n > 0 {
				if err := store.MergeProvenance(id, "journals_doaj_url", storage.ProvEntry{
					Value: u, Source: "doaj", RetrievedAt: start, Confidence: 1,
				}); err != nil {
					return t, fmt.Errorf("prov doaj_url j%d: %w", id, err)
				}
				t.url++
				t.prov++
			}
		}
		return t, nil
	}
	t1, err := proses()
	if err != nil {
		store.Close()
		return fmt.Errorf("tulis: %w", err)
	}
	t2, err := proses()
	store.Close()
	if err != nil {
		return fmt.Errorf("ulang (idempoten): %w", err)
	}
	if t2.subjFill+t2.subjApp+t2.issn+t2.url != 0 {
		return fmt.Errorf("IDEMPOTEN GAGAL: ulang mengubah subject=%d print=%d url=%d (harus 0) — cek backup %s",
			t2.subjFill+t2.subjApp, t2.issn, t2.url, bak)
	}
	fmt.Printf("tulis: subject fill=%d append=%d · print=%d · url=%d · prov=%d · ulang=%d/%d/%d/%d\n",
		t1.subjFill, t1.subjApp, t1.issn, t1.url, t1.prov,
		t2.subjFill+t2.subjApp, t2.issn, t2.url, 0)

	// ---- 8. snapshot POST + diff K1 --------------------------------------
	post, err := e7fSnapshot(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot POST: %w", err)
	}
	postC, err := e7fSnapshotCanon(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot POST canonical: %w", err)
	}
	diff := e7fDiff(pre, post, preC, postC, subjWant, issnWant, urlWant)
	if len(diff) > 0 {
		return fmt.Errorf("VERIFIKASI GAGAL — perubahan di luar target: %v · RESTORE manual dari %s", diff, bak)
	}

	v, err := e7fCounts(dbPath)
	if err != nil {
		return fmt.Errorf("counts POST: %w", err)
	}
	nFill41 := 0
	for _, h := range items {
		if h.Doaj.Tersedia && len(h.Doaj.Subjek) > 0 {
			nFill41++
		}
	}
	wantSubj := int64(e7fJumlahBaris - nFill41)
	wantPrint := e7fBaselinePrint - int64(e7fJumlahISSN) // 30 − 13 = 17
	if v["journals"] != 261 || v["hash_unik"] != 261 ||
		v["subj_kosong"] != wantSubj ||
		v["print_kosong"] != wantPrint ||
		v["print_placeholder"] != 0 || v["print_sisa_kandidat"] != 0 ||
		v["doaj_terisi"] != int64(len(urls)) {
		return fmt.Errorf(
			"VERIFIKASI GAGAL counts: journals=%d hash_unik=%d subj_kosong=%d (want %d) print_kosong=%d (want %d) placeholder=%d sisa_kandidat=%d doaj_terisi=%d (want %d) — RESTORE manual dari %s",
			v["journals"], v["hash_unik"], v["subj_kosong"], wantSubj,
			v["print_kosong"], wantPrint, v["print_placeholder"], v["print_sisa_kandidat"],
			v["doaj_terisi"], len(urls), bak)
	}

	// ---- 9. hasil JSON final ---------------------------------------------
	fill := e7fFillJSON{
		Backup: bak, SubjFill: t1.subjFill, SubjAppend: t1.subjApp, SubjUlang: t2.subjFill + t2.subjApp,
		PrintFill: t1.issn, PrintUlang: t2.issn, URLFill: t1.url, URLUlang: t2.url,
		ProvTulis: t1.prov, Verifikasi: map[string]string{}, DiffLarang: diff,
	}
	// Fill = jejak kumulatif: rerun plan-flat (t1 semua 0) tak menimpa jejak
	// fill asli di json. Asumsi jujur: fillLama & run ini tak pernah menulis
	// baris YANG SAMA dua kali — bila db di-restore manual, json ikut
	// dikoreksi (pola koreksi 7 Okt: PrintFill 28→13 disesuaikan bersamaan).
	if fillLama != nil {
		fill.Backup = fillLama.Backup
		fill.SubjFill += fillLama.SubjFill
		fill.SubjAppend += fillLama.SubjAppend
		fill.SubjUlang += fillLama.SubjUlang
		fill.PrintFill += fillLama.PrintFill
		fill.PrintUlang += fillLama.PrintUlang
		fill.URLFill += fillLama.URLFill
		fill.URLUlang += fillLama.URLUlang
		fill.ProvTulis += fillLama.ProvTulis
		fill.Koreksi = fillLama.Koreksi
	}
	for k, n := range v {
		fill.Verifikasi[k] = fmt.Sprintf("%d", n)
	}
	rep.Fill = &fill
	rep.GetTotal = getLama + getTotal
	if w, err := json.MarshalIndent(rep, "", "  "); err != nil {
		return fmt.Errorf("marshal json final: %w", err)
	} else if err := os.WriteFile(jsonPath, w, 0o644); err != nil {
		return fmt.Errorf("tulis json final: %w", err)
	}

	// ---- 10. ringkasan utk laporan ---------------------------------------
	r := rep.Ringkas
	fmt.Printf("\n== E7f SELESAI ==\n")
	fmt.Printf("GET: run ini %d/%d - kumulatif %d - resume=%d fixture=%d\n",
		getTotal, paguE7f, rep.GetTotal, gotResume, gotFix)
	if spot != nil {
		fmt.Printf("spot-check TOC: %s -> http=%d (%s)\n", spot.URL, spot.HTTP, spot.Dari)
	} else {
		fmt.Printf("spot-check TOC: DILEWATI (tidak ada record found)\n")
	}
	fmt.Printf("found=%d nol=%d kendala=%d (dgn subjek=%d)\n",
		r.Found, r.Nol, r.KendalaHTTP, r.DenganSubjek)
	fmt.Printf("fill: subj=%d (+append %d) print_issn=%d doaj_url=%d prov=%d\n",
		t1.subjFill, t1.subjApp, t1.issn, t1.url, t1.prov)
	fmt.Printf("VERIFIKASI OK · diff larangan=0 · subj_kosong→%d · print_kosong→%d · doaj_url→%d\n",
		v["subj_kosong"], v["print_kosong"], v["doaj_terisi"])
	fmt.Printf("JSON: %s\n", jsonPath)
	return nil
}
