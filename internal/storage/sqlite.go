package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sinta-scraper/internal/sinta"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// affProfileRe mencocokkan affidavit dari URL profil affiliations
// (https://…/affiliations/profile/8244358 → 8244358) — resolver offline
// ID→publisher untuk L3 (doc 27; R9 doc 23: dari katalog ekspektasi, bukan
// parser halaman detail).
var affProfileRe = regexp.MustCompile(`affiliations/profile/(\d+)`)

const schema = `
CREATE TABLE IF NOT EXISTS journals (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	sinta_profile_url TEXT NOT NULL,
	google_scholar_url TEXT NOT NULL,
	ojs_url TEXT NOT NULL,
	editor_url TEXT NOT NULL,
	affiliation_name TEXT NOT NULL,
	affiliation_url TEXT NOT NULL,
	print_issn TEXT NOT NULL,
	electronic_issn TEXT NOT NULL,
	subject_area TEXT,
	sinta_rank INTEGER NOT NULL,
	is_scopus INTEGER NOT NULL DEFAULT 0,
	is_garuda INTEGER NOT NULL DEFAULT 0,
	scopus_url TEXT,
	garuda_url TEXT,
	doaj_url TEXT,
	impact REAL,
	h5_index INTEGER,
	citations_last_5_years INTEGER,
	citations_total INTEGER,
	source_page INTEGER NOT NULL,
	ojs_status TEXT NOT NULL DEFAULT 'pending', -- pending|done|error|skip
	ojs_error TEXT,
	ojs_scraped_at TEXT,
	content_hash TEXT NOT NULL,
	first_seen_at TEXT NOT NULL,
	last_scraped_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS scrape_progress (
	run_key TEXT NOT NULL,
	page INTEGER NOT NULL,
	completed_at TEXT NOT NULL,
	PRIMARY KEY (run_key, page)
);

CREATE INDEX IF NOT EXISTS idx_journals_ojs_status ON journals (ojs_status);

-- ============================================================
-- Tahap 2 — Garuda Enrichment & Source Resolver (doc 30 rev.3).
-- Semua IF NOT EXISTS → idempoten pada db lama & baru (Q1 Langkah 1).
-- Enrichment tidak menulis journals (sinkronisasi Tahap 2 → journals
-- = E7; AMENDMEN K1/K5, doc 30 Bagian 0) — tabel-tabel baru hanya
-- merujuk journals.id.
-- ============================================================

-- 3.1 hasil matching + enrichment Garuda; provenance = JSON per-field
-- {value, source, retrieved_at, confidence} (keputusan Q1, Opsi A) —
-- hanya boleh ditulis lewat MergeProvenance (provenance.go).
CREATE TABLE IF NOT EXISTS journal_enrichment (
	journal_id                 INTEGER PRIMARY KEY REFERENCES journals(id),
	garuda_id                  INTEGER,
	garuda_url                 TEXT,
	garuda_title               TEXT,
	garuda_publisher           TEXT,
	garuda_pissn               TEXT,
	garuda_eissn               TEXT,
	garuda_subject             TEXT,
	garuda_original_source_url TEXT,
	garuda_source_url          TEXT,
	matched_by                 TEXT,
	match_confidence           REAL,
	match_status               TEXT,
	garuda_retrieved_at        TEXT,
	provenance                 TEXT NOT NULL DEFAULT '{}',
	subject_area_canonical     TEXT,
	garuda_home_url            TEXT, -- link "Home Page" halaman view/N (E3, Opsi A — doc 31 §4)
	garuda_oai_url             TEXT, -- link "OAI Link" halaman view/N (E3, Opsi A — doc 31 §4)
	UNIQUE(garuda_id)
);

-- 3.2 semua kandidat URL dari semua sumber (TIDAK ditimpa; pemilihan
-- canonical = tugas resolver Fase 3 — keputusan Q5, rev.3).
CREATE TABLE IF NOT EXISTS journal_urls (
	id           INTEGER PRIMARY KEY,
	journal_id   INTEGER NOT NULL REFERENCES journals(id),
	url          TEXT    NOT NULL,
	kind         TEXT    NOT NULL,
	source       TEXT    NOT NULL,
	confidence   REAL,
	http_status  INTEGER,
	final_url    TEXT,
	checked_at   TEXT,
	is_canonical INTEGER DEFAULT 0,
	UNIQUE(journal_id, kind, url)
);

-- 3.3 profil sumber resmi: platform terdeteksi berbasis KONTEN
-- (detection_evidence wajib diisi) — diisi Fase 3.
CREATE TABLE IF NOT EXISTS journal_source_profile (
	journal_id          INTEGER PRIMARY KEY REFERENCES journals(id),
	platform            TEXT NOT NULL,
	canonical_url       TEXT,
	oai_url             TEXT,
	article_index       TEXT,
	article_url_pattern TEXT,
	policy_urls         TEXT,
	pdf_strategy        TEXT,
	detection_evidence  TEXT,
	detected_at         TEXT
);

-- 3.4 resume/state per-jurnal Tahap 2 (pola checkpoint terbukti).
CREATE TABLE IF NOT EXISTS phase2_progress (
	journal_id INTEGER PRIMARY KEY REFERENCES journals(id),
	phase      TEXT NOT NULL,
	flags      TEXT DEFAULT '{}',
	updated_at TEXT NOT NULL,
	last_error TEXT
);

-- 3.6 kamus subject (Q3, doc 30 §13.3): term sumber → kanonik. DIBUAT
-- DARI SUMBER (harvest vocab SINTA dari DB + Garuda dari /area), BUKAN
-- const Go — dibangun ulang idempoten tiap run bila taxonomy berubah.
-- method: exact|contain|prefix|identity (merge co-occur DICABUT rev.2);
-- support = # jurnal run sbg bukti; confidence 0..1 (K2).
CREATE TABLE IF NOT EXISTS subject_map (
	id             INTEGER PRIMARY KEY,
	source_system  TEXT NOT NULL,              -- 'sinta' | 'garuda'
	source_term    TEXT NOT NULL,              -- label RAW asli (K1)
	source_key     TEXT NOT NULL,              -- kunci fold utk join deterministik
	canonical_term TEXT NOT NULL,              -- representatif (terpanjang, seri → alfabet)
	method         TEXT NOT NULL,              -- exact|contain|prefix|identity
	support        INTEGER NOT NULL DEFAULT 0, -- bukti co-occurrence di run
	confidence     REAL NOT NULL DEFAULT 0,    -- 0..1 (K2)
	origin         TEXT NOT NULL,              -- 'harvest' (vocab) | 'computed' (alignment)
	built_at       TEXT NOT NULL,
	UNIQUE(source_system, source_key)
);
`

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("gagal membuat direktori db: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("gagal membuka db: %w", err)
	}
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("gagal set %s: %w", pragma, err)
		}
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("gagal migrasi schema: %w", err)
	}

	// Tahap 1b Langkah 23 (doc 18 Bagian 3.2): field university dihapus
	// total — db lama masih punya kolomnya. Tanpa DROP, INSERT pada db
	// lama gagal (university NOT NULL tanpa default). Db baru: kolom
	// tidak ada → dilewati.
	if err := dropLegacyUniversityColumn(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("gagal migrasi hapus kolom university: %w", err)
	}

	// Q4 E3 (doc 31 §4 Opsi A): 2 kolom link halaman view — db kerja Tahap 2
	// dibuat sebelum E3 → kolom ditambahkan lewat ALTER (idempoten; db baru
	// sudah punya kolomnya lewat CREATE TABLE).
	if err := addQ4ViewColumns(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("gagal migrasi kolom view E3: %w", err)
	}

	return &Store{db: db}, nil
}

// addQ4ViewColumns menambah garuda_home_url & garuda_oai_url ke
// journal_enrichment bila belum ada (migrasi ALTER idempoten utk db kerja
// yang dibuat sebelum E3; doc 31 §4 Opsi A).
func addQ4ViewColumns(db *sql.DB) error {
	for _, col := range []string{"garuda_home_url TEXT", "garuda_oai_url TEXT"} {
		name := strings.Fields(col)[0]
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('journal_enrichment') WHERE name = ?`,
			name,
		).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(`ALTER TABLE journal_enrichment ADD COLUMN ` + col); err != nil {
				return fmt.Errorf("ALTER %s: %w", name, err)
			}
		}
	}
	return nil
}

// dropLegacyUniversityColumn membuang kolom `university` dari tabel
// journals bila masih ada (idempoten — db baru tidak punya kolom ini).
func dropLegacyUniversityColumn(db *sql.DB) error {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('journals') WHERE name = 'university'`,
	).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE journals DROP COLUMN university`)
	return err
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) CompletedPages(runKey string) (map[int]bool, error) {
	rows, err := s.db.Query(`SELECT page FROM scrape_progress WHERE run_key = ?`, runKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	done := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		done[p] = true
	}
	return done, rows.Err()
}

func (s *Store) MarkPageCompleted(runKey string, page int) error {
	_, err := s.db.Exec(`
		INSERT INTO scrape_progress (run_key, page, completed_at)
		VALUES (?, ?, ?)
		ON CONFLICT(run_key, page) DO UPDATE SET completed_at = excluded.completed_at
		`, runKey, page, time.Now().UTC().Format(time.RFC3339))
	return err
}

// ClearCheckpoint menghapus semua progres halaman milik run-key (dipakai
// -refresh, doc 16 Bagian 3.2): setelah wipe, baca checkpoint pasti kosong.
func (s *Store) ClearCheckpoint(runKey string) error {
	_, err := s.db.Exec(`DELETE FROM scrape_progress WHERE run_key = ?`, runKey)
	return err
}

// RankCounts menghitung distribusi sinta_rank di seluruh DB — dipakai sanity
// GAGAL-FILTER (doc 20 Bagian 2.4): memastikan data = -rank yang diminta.
func (s *Store) RankCounts() (map[int]int, error) {
	rows, err := s.db.Query(`SELECT sinta_rank, COUNT(*) FROM journals GROUP BY sinta_rank`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var rank, n int
		if err := rows.Scan(&rank, &n); err != nil {
			return nil, err
		}
		out[rank] = n
	}
	return out, rows.Err()
}

// CatalogAffIDs memuat peta id jurnal → affiliation ID dari sebuah db KATALOG
// untuk publisher recovery (L3, doc 27): missing ID = katalog \ run-ini lalu
// di-resolve ke /journals/index/{affid} tanpa request halaman detail.
// ranks kosong = semua rank; selain itu hanya baris rank yang diminta (satu
// katalog boleh berisi banyak rank lintas run). Baris tanpa
// affiliation_url valid dilewati (missing-nya tetap tak teratasi — dicatat
// pipeline sebagai unresolved, bukan hilang diam-diam).
func (s *Store) CatalogAffIDs(ranks []int) (map[int]int, error) {
	return queryCatalogAffIDs(s.db, ranks)
}

// CatalogAffIDsFile = CatalogAffIDs tanpa lewat Open(): baca db katalog
// read-only (mode=ro) supaya TIDAK dimutasi skemanya oleh CREATE/migrasi
// Open() — file katalog milik run/arsip lain harus dibiarkan persis apa
// adanya (L3 doc 27).
func CatalogAffIDsFile(path string, ranks []int) (map[int]int, error) {
	uri := "file:" + filepath.ToSlash(path) + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, fmt.Errorf("buka katalog (read-only): %w", err)
	}
	defer db.Close()
	return queryCatalogAffIDs(db, ranks)
}

func queryCatalogAffIDs(db *sql.DB, ranks []int) (map[int]int, error) {
	q := `SELECT id, affiliation_url FROM journals`
	var args []any
	if len(ranks) > 0 {
		ph := make([]string, len(ranks))
		for i, r := range ranks {
			ph[i] = "?"
			args = append(args, r)
		}
		q += ` WHERE sinta_rank IN (` + strings.Join(ph, ",") + `)`
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int]int{}
	for rows.Next() {
		var id int
		var affURL string
		if err := rows.Scan(&id, &affURL); err != nil {
			return nil, err
		}
		if m := affProfileRe.FindStringSubmatch(affURL); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				out[id] = n
			}
		}
	}
	return out, rows.Err()
}

func (s *Store) UpsertJournals(journals []sinta.Journal) (sinta.UpsertReport, error) {
	rep := sinta.UpsertReport{}
	if len(journals) == 0 {
		return rep, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return rep, err
	}
	defer tx.Rollback()

	// 1. Baca baris lama (20 kolom konten + hash) — dasar deteksi perubahan.
	ph := make([]string, len(journals))
	ids := make([]any, len(journals))
	for i, j := range journals {
		ph[i] = "?"
		ids[i] = j.ID
	}
	rows, err := tx.Query(`
		SELECT id, name, sinta_profile_url, google_scholar_url, ojs_url, editor_url,
			affiliation_name, affiliation_url, print_issn, electronic_issn,
			COALESCE(subject_area, ''), sinta_rank, is_scopus, is_garuda,
			COALESCE(scopus_url, ''), COALESCE(garuda_url, ''), COALESCE(doaj_url, ''),
			COALESCE(impact, 0), COALESCE(h5_index, 0), COALESCE(citations_last_5_years, 0),
			COALESCE(citations_total, 0), content_hash
		FROM journals WHERE id IN (`+strings.Join(ph, ",")+`)`, ids...)
	if err != nil {
		return rep, err
	}
	type oldRow struct {
		j    sinta.Journal
		hash string
	}
	oldByID := map[int]oldRow{}
	for rows.Next() {
		var o oldRow
		if err := rows.Scan(&o.j.ID, &o.j.Name, &o.j.SINTAProfileURL, &o.j.GoogleScholarURL,
			&o.j.OJSURL, &o.j.EditorURL, &o.j.AffiliationName,
			&o.j.AffiliationURL, &o.j.PrintISSN, &o.j.ElectronicISSN, &o.j.SubjectArea,
			&o.j.SintaRank, &o.j.IsScopus, &o.j.IsGaruda, &o.j.ScopusURL, &o.j.GarudaURL,
			&o.j.DOAJURL, &o.j.Impact, &o.j.H5Index, &o.j.CitationsLast5Years,
			&o.j.CitationsTotal, &o.hash); err != nil {
			rows.Close()
			return rep, err
		}
		oldByID[o.j.ID] = o
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}
	if err := rows.Close(); err != nil {
		return rep, err
	}

	// 2. Empat statement: insert penuh (INSERT ... ON CONFLICT — tanpa kolom
	//    ojs_* supaya state OJS stage berikutnya tidak bocor ter-reset),
	//    update penuh, sentuh last_scraped_at, dan rehash (migrasi content_hash).
	stmtInsert, err := tx.Prepare(`
		INSERT INTO journals
		(id, name, sinta_profile_url, google_scholar_url, ojs_url, editor_url,
		affiliation_name, affiliation_url, print_issn, electronic_issn,
		subject_area, sinta_rank, is_scopus, is_garuda, scopus_url, garuda_url, doaj_url,
		impact, h5_index, citations_last_5_years, citations_total,
		source_page, content_hash, first_seen_at, last_scraped_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		sinta_profile_url = excluded.sinta_profile_url,
		google_scholar_url = excluded.google_scholar_url,
		ojs_url = excluded.ojs_url,
		editor_url = excluded.editor_url,
		affiliation_name = excluded.affiliation_name,
		affiliation_url = excluded.affiliation_url,
		print_issn = excluded.print_issn,
		electronic_issn = excluded.electronic_issn,
		subject_area = excluded.subject_area,
		sinta_rank = excluded.sinta_rank,
		is_scopus = excluded.is_scopus,
		is_garuda = excluded.is_garuda,
		scopus_url = excluded.scopus_url,
		garuda_url = excluded.garuda_url,
		doaj_url = excluded.doaj_url,
		impact = excluded.impact,
		h5_index = excluded.h5_index,
		citations_last_5_years = excluded.citations_last_5_years,
		citations_total = excluded.citations_total,
		source_page = excluded.source_page,
		content_hash = excluded.content_hash,
		last_scraped_at = excluded.last_scraped_at
		`)
	if err != nil {
		return rep, err
	}
	defer stmtInsert.Close()
	stmtUpdate, err := tx.Prepare(`
		UPDATE journals SET
			name = ?, sinta_profile_url = ?, google_scholar_url = ?, ojs_url = ?, editor_url = ?,
			affiliation_name = ?, affiliation_url = ?, print_issn = ?, electronic_issn = ?,
			subject_area = ?, sinta_rank = ?, is_scopus = ?, is_garuda = ?, scopus_url = ?,
			garuda_url = ?, doaj_url = ?, impact = ?, h5_index = ?, citations_last_5_years = ?,
			citations_total = ?, source_page = ?, content_hash = ?, last_scraped_at = ?
		WHERE id = ?`)
	if err != nil {
		return rep, err
	}
	defer stmtUpdate.Close()
	stmtTouch, err := tx.Prepare(`UPDATE journals SET last_scraped_at = ? WHERE id = ?`)
	if err != nil {
		return rep, err
	}
	defer stmtTouch.Close()
	stmtRehash, err := tx.Prepare(`UPDATE journals SET content_hash = ?, last_scraped_at = ? WHERE id = ?`)
	if err != nil {
		return rep, err
	}
	defer stmtRehash.Close()

	// 3. Klasifikasi per kartu: baru / sama / diperbarui (doc 16 Bagian 3.1).
	now := time.Now().UTC().Format(time.RFC3339)
	for _, j := range journals {
		h := contentHash(j)
		old, exists := oldByID[j.ID]
		var err error
		switch {
		case !exists: // baru → INSERT penuh (sama seperti argumen lama baris 174-181)
			_, err = stmtInsert.Exec(
				j.ID, j.Name, j.SINTAProfileURL, j.GoogleScholarURL, j.OJSURL, j.EditorURL,
				j.AffiliationName, j.AffiliationURL, j.PrintISSN, j.ElectronicISSN,
				j.SubjectArea, j.SintaRank, boolToInt(j.IsScopus), boolToInt(j.IsGaruda),
				j.ScopusURL, j.GarudaURL, j.DOAJURL,
				j.Impact, j.H5Index, j.CitationsLast5Years, j.CitationsTotal,
				j.SourcePage, h, now, now,
			)
			if err == nil {
				rep.New++
			}
		case old.hash == h: // identik → tanpa tulis ulang konten
			_, err = stmtTouch.Exec(now, j.ID)
			if err == nil {
				rep.Unchanged++
			}
		default: // hash beda → kebenaran = diff field
			changes := diffJournals(old.j, j)
			if len(changes) == 0 { // hash lama ≠ hash baru, konten sama = migrasi hash
				_, err = stmtRehash.Exec(h, now, j.ID)
				if err == nil {
					rep.Unchanged++
				}
			} else {
				_, err = stmtUpdate.Exec(
					j.Name, j.SINTAProfileURL, j.GoogleScholarURL, j.OJSURL, j.EditorURL,
					j.AffiliationName, j.AffiliationURL, j.PrintISSN, j.ElectronicISSN,
					j.SubjectArea, j.SintaRank, boolToInt(j.IsScopus), boolToInt(j.IsGaruda),
					j.ScopusURL, j.GarudaURL, j.DOAJURL,
					j.Impact, j.H5Index, j.CitationsLast5Years, j.CitationsTotal,
					j.SourcePage, h, now, j.ID,
				)
				if err == nil {
					rep.Updated++
					rep.Changes = append(rep.Changes, sinta.JournalChange{ID: j.ID, Name: j.Name, Fields: changes})
				}
			}
		}
		if err != nil {
			return rep, fmt.Errorf("gagal upsert journal id=%d (%q): %w", j.ID, j.Name, err)
		}
	}
	return rep, tx.Commit()
}

// diffJournals membandingkan 20 field konten; nama field = kolom db (snake_case).
func diffJournals(old, cur sinta.Journal) []sinta.FieldChange {
	var out []sinta.FieldChange
	add := func(field string, o, n any) {
		os, ns := fmt.Sprint(o), fmt.Sprint(n)
		if os != ns {
			out = append(out, sinta.FieldChange{Field: field, Old: os, New: ns})
		}
	}
	add("name", old.Name, cur.Name)
	add("sinta_profile_url", old.SINTAProfileURL, cur.SINTAProfileURL)
	add("google_scholar_url", old.GoogleScholarURL, cur.GoogleScholarURL)
	add("ojs_url", old.OJSURL, cur.OJSURL)
	add("editor_url", old.EditorURL, cur.EditorURL)
	add("affiliation_name", old.AffiliationName, cur.AffiliationName)
	add("affiliation_url", old.AffiliationURL, cur.AffiliationURL)
	add("print_issn", old.PrintISSN, cur.PrintISSN)
	add("electronic_issn", old.ElectronicISSN, cur.ElectronicISSN)
	add("subject_area", old.SubjectArea, cur.SubjectArea)
	add("sinta_rank", old.SintaRank, cur.SintaRank)
	add("is_scopus", old.IsScopus, cur.IsScopus)
	add("is_garuda", old.IsGaruda, cur.IsGaruda)
	add("scopus_url", old.ScopusURL, cur.ScopusURL)
	add("garuda_url", old.GarudaURL, cur.GarudaURL)
	add("doaj_url", old.DOAJURL, cur.DOAJURL)
	add("impact", old.Impact, cur.Impact)
	add("h5_index", old.H5Index, cur.H5Index)
	add("citations_last_5_years", old.CitationsLast5Years, cur.CitationsLast5Years)
	add("citations_total", old.CitationsTotal, cur.CitationsTotal)
	return out
}

func contentHash(j sinta.Journal) string {
	raw := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d|%s|%v|%v|%v|%v|%v|%v|%s|%s|%s|%s|%s|%s",
		j.Name, j.OJSURL, j.AffiliationName, j.PrintISSN, j.ElectronicISSN,
		j.SubjectArea, j.SintaRank, j.DOAJURL,
		j.IsScopus, j.IsGaruda, j.Impact, j.H5Index, j.CitationsLast5Years, j.CitationsTotal,
		j.SINTAProfileURL, j.GoogleScholarURL, j.EditorURL, j.AffiliationURL, j.ScopusURL, j.GarudaURL)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
