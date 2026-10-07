package storage

import "fmt"

// UpdateJournalsE7 = E7b/E7f (doc 38 §7 — approve user 6 Okt 2026): tulis
// SATU field sinkronisasi E7 pada tabel `journals`.
//
// Whitelist kolom ketat (subject_area | garuda_url | print_issn | doaj_url)
// — dua kolom terakhir ditambah E7f (approve 6 Okt 2026: fill-only utk baris
// kosong, nol DDL); kolom lain hanya boleh disentuh lewat keputusan
// K6/review terpisah, jadi sengaja tak bisa dipanggil sembarang string.
//
// Idempoten: `WHERE ... COALESCE(kolom,”) <> ?` → nilai sudah sama = tidak
// ada baris berubah (RowsAffected 0) — aman diulang tanpa jejak.
func (s *Store) UpdateJournalsE7(journalID int64, field, value string) (int64, error) {
	col := ""
	switch field {
	case "subject_area", "garuda_url", "print_issn", "doaj_url":
		col = field
	default:
		return 0, fmt.Errorf("syncE7: field %q di luar whitelist (subject_area|garuda_url|print_issn|doaj_url)", field)
	}
	// col berasal dari switch whitelist (bukan input bebas) → aman dipakai
	// sebagai nama kolom langsung di SQL.
	res, err := s.db.Exec(
		fmt.Sprintf(`UPDATE journals SET %s = ? WHERE id = ? AND COALESCE(%s, '') <> ?`, col, col),
		value, journalID, value,
	)
	if err != nil {
		return 0, fmt.Errorf("syncE7: update %s journal %d: %w", col, journalID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("syncE7: rows affected journal %d: %w", journalID, err)
	}
	return n, nil
}
