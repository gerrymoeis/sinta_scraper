package storage

import (
	"fmt"
	"regexp"
	"strconv"
)

// Hasil E3 (doc 31 §4 Opsi A, disetujui 4 Okt 2026): 2 kolom
// garuda_home_url & garuda_oai_url di journal_enrichment diisi dari fixture
// halaman view terverifikasi. Kolom khusus ini tidak lewat provenance Q1 —
// sifatnya buffer corroboration URL (nilai mentah tak pernah ditimpa oleh
// pencocokan; doc 31 §4).

var garudaViewPathRe = regexp.MustCompile(`/journal/view/(\d+)`)

// ViewLinkRef = pasangan garuda_id → journal_id dari URL view tercatat.
type ViewLinkRef struct {
	GarudaID  int
	JournalID int64
}

// ViewLinkRefs mengumpulkan resolve garuda_id → journal_id dari 2 sumber,
// urut prioritas: (1) garuda_url di journal_enrichment (matched — termasuk
// 2 kasus garuda_url beda, id Q3 yang benar), (2) garuda_url di journals
// untuk baris match_status='not_found'. id yang sama dipertahankan dari
// sumber prioritas pertama (halaman lama tidak menimpa halaman Q3).
func (s *Store) ViewLinkRefs() ([]ViewLinkRef, error) {
	rows, err := s.db.Query(`
		SELECT garuda_url, journal_id FROM journal_enrichment
		WHERE garuda_url LIKE '%/journal/view/%'
		UNION ALL
		SELECT j.garuda_url, j.id FROM journals j
		JOIN journal_enrichment e ON e.journal_id = j.id
		WHERE e.match_status = 'not_found' AND j.garuda_url LIKE '%/journal/view/%'`)
	if err != nil {
		return nil, fmt.Errorf("query ViewLinkRefs: %w", err)
	}
	defer rows.Close()
	seen := map[int]bool{}
	var out []ViewLinkRef
	for rows.Next() {
		var u string
		var jid int64
		if err := rows.Scan(&u, &jid); err != nil {
			return nil, err
		}
		m := garudaViewPathRe.FindStringSubmatch(u)
		if m == nil {
			continue
		}
		id, err := strconv.Atoi(m[1])
		if err != nil || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, ViewLinkRef{GarudaID: id, JournalID: jid})
	}
	return out, rows.Err()
}

// AmbiguousJournalIDs = journal_id baris match_status='ambiguous' (garuda_id
// NULL — kandidatnya diambil cmd dari fixture search, offline).
func (s *Store) AmbiguousJournalIDs() ([]int64, error) {
	rows, err := s.db.Query(`SELECT journal_id FROM journal_enrichment
		WHERE match_status = 'ambiguous' ORDER BY journal_id`)
	if err != nil {
		return nil, fmt.Errorf("query AmbiguousJournalIDs: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var jid int64
		if err := rows.Scan(&jid); err != nil {
			return nil, err
		}
		out = append(out, jid)
	}
	return out, rows.Err()
}

// UpdateViewLinks menulis 2 kolom hasil E3 (Opsi A) utk satu jurnal.
// Idempoten (nilai identik = update ulang byte sama); gagal bila baris
// enrichment belum ada.
func (s *Store) UpdateViewLinks(journalID int64, home, oai string) error {
	res, err := s.db.Exec(`UPDATE journal_enrichment
		SET garuda_home_url = ?, garuda_oai_url = ?
		WHERE journal_id = ?`, home, oai, journalID)
	if err != nil {
		return fmt.Errorf("update view links j%d: %w", journalID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("baris enrichment journal %d tak ada", journalID)
	}
	return nil
}

// CountViewLinks = jumlah baris enrichment yang punya minimal 1 link view
// (verifikasi akhir view-fill).
func (s *Store) CountViewLinks() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_enrichment
		WHERE COALESCE(garuda_home_url,'') <> '' OR COALESCE(garuda_oai_url,'') <> ''`).Scan(&n)
	return n, err
}
