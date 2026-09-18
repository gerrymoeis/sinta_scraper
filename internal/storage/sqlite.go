// Package storage menyimpan hasil scraping ke SQLite lokal (single file,
// tanpa server terpisah). Semua penulisan dilakukan lewat satu koneksi
// (SetMaxOpenConns(1)) supaya aman dipanggil dari satu goroutine "writer"
// tunggal, meskipun proses fetching-nya sendiri konkuren lewat banyak worker.
package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // driver SQLite pure-Go, tanpa CGO

	"sinta-scraper/internal/sinta"
)

const schema = `
CREATE TABLE IF NOT EXISTS journals (
    id                 INTEGER PRIMARY KEY,
    name               TEXT NOT NULL,
    profile_url        TEXT NOT NULL,
    google_scholar_url TEXT,
    website_url        TEXT,
    editor_url         TEXT,
    affiliation        TEXT,
    affiliation_url    TEXT,
    issn_print         TEXT,
    issn_electronic    TEXT,
    subject_area       TEXT,
    sinta_rank_raw     TEXT,
    is_scopus          INTEGER NOT NULL DEFAULT 0,
    is_garuda          INTEGER NOT NULL DEFAULT 0,
    garuda_url         TEXT,
    impact             REAL,
    h5_index           INTEGER,
    citations_5yr      INTEGER,
    citations_total    INTEGER,
    source_page        INTEGER,
    content_hash       TEXT NOT NULL,
    first_seen_at      TEXT NOT NULL,
    last_scraped_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS scrape_progress (
    run_key      TEXT NOT NULL,
    page         INTEGER NOT NULL,
    completed_at TEXT NOT NULL,
    PRIMARY KEY (run_key, page)
);
`

type Store struct {
	db *sql.DB
}

// Open membuka (atau membuat) database SQLite di path, menjalankan migrasi
// schema, dan mengunci koneksi ke maksimal 1 supaya penulisan tetap serial.
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

	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("gagal set journal_mode WAL: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("gagal set busy_timeout: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("gagal migrasi schema: %w", err)
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// CompletedPages mengembalikan set nomor halaman yang sudah pernah berhasil
// discrape & disimpan untuk run_key tertentu -- dipakai untuk resume.
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
		INSERT INTO scrape_progress (run_key, page, completed_at) VALUES (?, ?, ?)
		ON CONFLICT(run_key, page) DO UPDATE SET completed_at = excluded.completed_at`,
		runKey, page, time.Now().UTC().Format(time.RFC3339))
	return err
}

// UpsertJournals menyimpan satu batch Journal (hasil parsing satu halaman)
// dalam satu transaksi. first_seen_at TIDAK ikut di-overwrite saat update,
// jadi kolom itu tetap mencatat kapan record itu pertama kali muncul.
func (s *Store) UpsertJournals(journals []sinta.Journal, page int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op kalau sudah di-Commit

	stmt, err := tx.Prepare(`
		INSERT INTO journals (
			id, name, profile_url, google_scholar_url, website_url, editor_url,
			affiliation, affiliation_url, issn_print, issn_electronic, subject_area,
			sinta_rank_raw, is_scopus, is_garuda, garuda_url,
			impact, h5_index, citations_5yr, citations_total,
			source_page, content_hash, first_seen_at, last_scraped_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name               = excluded.name,
			profile_url        = excluded.profile_url,
			google_scholar_url = excluded.google_scholar_url,
			website_url        = excluded.website_url,
			editor_url         = excluded.editor_url,
			affiliation        = excluded.affiliation,
			affiliation_url    = excluded.affiliation_url,
			issn_print         = excluded.issn_print,
			issn_electronic    = excluded.issn_electronic,
			subject_area       = excluded.subject_area,
			sinta_rank_raw     = excluded.sinta_rank_raw,
			is_scopus          = excluded.is_scopus,
			is_garuda          = excluded.is_garuda,
			garuda_url         = excluded.garuda_url,
			impact             = excluded.impact,
			h5_index           = excluded.h5_index,
			citations_5yr      = excluded.citations_5yr,
			citations_total    = excluded.citations_total,
			source_page        = excluded.source_page,
			content_hash       = excluded.content_hash,
			last_scraped_at    = excluded.last_scraped_at
	`)
	if err != nil {
		return fmt.Errorf("gagal prepare statement upsert: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, j := range journals {
		hash := contentHash(j)
		_, err := stmt.Exec(
			j.ID, j.Name, j.ProfileURL, j.GoogleScholarURL, j.WebsiteURL, j.EditorURL,
			j.Affiliation, j.AffiliationURL, j.ISSNPrint, j.ISSNElectronic, j.SubjectArea,
			j.SintaRank, boolToInt(j.IsScopus), boolToInt(j.IsGaruda), j.GarudaURL,
			j.Impact, j.H5Index, j.Citations5yr, j.CitationsTotal,
			page, hash, now, now,
		)
		if err != nil {
			return fmt.Errorf("gagal upsert journal id=%d (%q): %w", j.ID, j.Name, err)
		}
	}

	return tx.Commit()
}

// contentHash merangkum field-field yang "berarti" jadi satu hash, supaya ke
// depan gampang mendeteksi jurnal mana yang datanya benar-benar berubah antar
// run scraping (dibanding sekadar overwrite buta tiap kali).
func contentHash(j sinta.Journal) string {
	raw := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%v|%v|%v|%v|%v|%v",
		j.Name, j.WebsiteURL, j.Affiliation, j.ISSNPrint, j.ISSNElectronic,
		j.SubjectArea, j.SintaRank, j.GarudaURL,
		j.IsScopus, j.IsGaruda, j.Impact, j.H5Index, j.Citations5yr, j.CitationsTotal)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
