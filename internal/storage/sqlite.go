package storage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS journals (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	sinta_profile_url TEXT NOT NULL,
	google_scholar_url TEXT NOT NULL,
	ojs_url TEXT NOT NULL,
	editor_url TEXT NOT NULL,
	university TEXT NOT NULL,
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

	return &Store{db: db}, nil
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

func (s *Store) UpsertJournals(journals []sinta.Journal) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO journals
		(id, name, sinta_profile_url, google_scholar_url, ojs_url, editor_url,
		university, affiliation_name, affiliation_url, print_issn, electronic_issn,
		subject_area, sinta_rank, is_scopus, is_garuda, scopus_url, garuda_url, doaj_url,
		impact, h5_index, citations_last_5_years, citations_total,
		source_page, content_hash, first_seen_at, last_scraped_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		name = excluded.name,
		sinta_profile_url = excluded.sinta_profile_url,
		google_scholar_url = excluded.google_scholar_url,
		ojs_url = excluded.ojs_url,
		editor_url = excluded.editor_url,
		university = excluded.university,
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
		return err
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, j := range journals {
		_, err := stmt.Exec(
			j.ID, j.Name, j.SINTAProfileURL, j.GoogleScholarURL, j.OJSURL, j.EditorURL,
			j.University, j.AffiliationName, j.AffiliationURL, j.PrintISSN, j.ElectronicISSN,
			j.SubjectArea, j.SintaRank, boolToInt(j.IsScopus), boolToInt(j.IsGaruda),
			j.ScopusURL, j.GarudaURL, j.DOAJURL,
			j.Impact, j.H5Index, j.CitationsLast5Years, j.CitationsTotal,
			j.SourcePage, contentHash(j), now, now,
		)
		if err != nil {
			return fmt.Errorf("gagal upsert journal id=%d (%q): %w", j.ID, j.Name, err)
		}
	}
	return tx.Commit()
}

func contentHash(j sinta.Journal) string {
	raw := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%d|%s|%v|%v|%v|%v|%v|%v",
		j.Name, j.OJSURL, j.University, j.AffiliationName, j.PrintISSN, j.ElectronicISSN,
		j.SubjectArea, j.SintaRank, j.DOAJURL,
		j.IsScopus, j.IsGaruda, j.Impact, j.H5Index, j.CitationsLast5Years, j.CitationsTotal)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
