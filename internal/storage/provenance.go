package storage

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ProvEntry = satu entri provenance per-field per-sumber (keputusan Q1,
// Opsi A — doc 30 §3.5). Disimpan sebagai JSON map field → ProvEntry pada
// kolom journal_enrichment.provenance.
type ProvEntry struct {
	Value       string  `json:"value"`
	Source      string  `json:"source"`
	RetrievedAt string  `json:"retrieved_at"`
	Confidence  float64 `json:"confidence"`
}

// provSources = whitelist sumber yang diizinkan. Menutup typo/drift data
// sejak awal (K2: skor & sumber jelas, bukan string bebas).
var provSources = map[string]bool{
	"sinta":    true,
	"garuda":   true,
	"official": true,
	"doaj":     true,
	"crossref": true,
	"manual":   true,
}

// MergeProvenance menulis/menimpa SATU field pada JSON provenance milik
// journalID. Ini adalah SATU-SATUNYA penulis kolom provenance (Q1: satu
// pintu tulis — mencegah format JSON drift antar kode).
//
// Sifat:
//   - idempoten: merge entri identik 2× menghasilkan byte JSON sama
//     (map di-marshal terurut kunci oleh encoding/json);
//   - read-modify-write aman karena Open() memakai SetMaxOpenConns(1)
//     (sqlite.go) dan pipeline berjalan sekuensial;
//   - RetrievedAt kosong → diisi waktu sekarang (UTC, RFC3339);
//   - tolak: field kosong, sumber di luar whitelist, confidence di luar
//     0..1 — JSON lama TIDAK diubah bila validasi gagal.
func (s *Store) MergeProvenance(journalID int64, field string, e ProvEntry) error {
	if strings.TrimSpace(field) == "" {
		return fmt.Errorf("provenance: field kosong")
	}
	if !provSources[e.Source] {
		return fmt.Errorf("provenance: sumber %q di luar whitelist", e.Source)
	}
	if e.Confidence < 0 || e.Confidence > 1 {
		return fmt.Errorf("provenance: confidence %.4g di luar rentang 0..1", e.Confidence)
	}
	if strings.TrimSpace(e.RetrievedAt) == "" {
		e.RetrievedAt = time.Now().UTC().Format(time.RFC3339)
	}

	// Baris enrichment mungkin belum ada (jurnal belum match) → buat dulu.
	// ON CONFLICT DO NOTHING = aman diulang; kolom lain semuanya nullable
	// atau punya DEFAULT (provenance = '{}').
	if _, err := s.db.Exec(
		`INSERT INTO journal_enrichment (journal_id) VALUES (?) ON CONFLICT DO NOTHING`,
		journalID,
	); err != nil {
		return fmt.Errorf("provenance: siapkan baris journal %d: %w", journalID, err)
	}

	var raw string
	if err := s.db.QueryRow(
		`SELECT provenance FROM journal_enrichment WHERE journal_id = ?`, journalID,
	).Scan(&raw); err != nil {
		return fmt.Errorf("provenance: baca journal %d: %w", journalID, err)
	}

	entries := map[string]ProvEntry{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			return fmt.Errorf("provenance: JSON lama journal %d tidak valid: %w", journalID, err)
		}
	}
	entries[field] = e

	out, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("provenance: marshal journal %d: %w", journalID, err)
	}
	if _, err := s.db.Exec(
		`UPDATE journal_enrichment SET provenance = ? WHERE journal_id = ?`,
		string(out), journalID,
	); err != nil {
		return fmt.Errorf("provenance: tulis journal %d: %w", journalID, err)
	}
	return nil
}

// Provenance mengembalikan seluruh entri provenance milik journalID
// (baca saja — untuk audit/verifikasi, bukan penulis).
func (s *Store) Provenance(journalID int64) (map[string]ProvEntry, error) {
	var raw string
	err := s.db.QueryRow(
		`SELECT provenance FROM journal_enrichment WHERE journal_id = ?`, journalID,
	).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("provenance: baca journal %d: %w", journalID, err)
	}
	entries := map[string]ProvEntry{}
	if strings.TrimSpace(raw) == "" {
		return entries, nil
	}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("provenance: JSON journal %d tidak valid: %w", journalID, err)
	}
	return entries, nil
}
