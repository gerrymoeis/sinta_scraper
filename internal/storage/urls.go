package storage

import "fmt"

// Penulis tabel journal_urls (skema doc 30 §3.2 — semua kandidat URL dari
// semua sumber, TIDAK ditimpa; pemilihan canonical = Fase 3 / keputusan Q5
// rev.3). Ditambah E7g (approve user 7 Okt 2026).

// UpsertJournalURL menulis SATU baris kandidat URL untuk E7g.
//
// Sifat:
//   - idempoten: baris identik → RowsAffected 0 (loop ke-2 wajib 0);
//   - sumber PERTAMA untuk sebuah URL dipertahankan (konflik tak menimpa
//     source — asal-usul URL lebih berguna dari sumber verify ulang);
//   - konflik hanya memperbarui hasil verifikasi (http_status/final_url/
//     checked_at) bila nilai BARU terisi DAN berubah — kandidat belum dicek
//     (NULL/kosong) TIDAK pernah menimpa data lama (bukti test).
//   - is_canonical SELALU 0 (kolom default; pemilihan = Fase 3);
//   - httpStatus nil = belum/tak ada respons HTTP (dicek tapi jaringan
//     gagal → checked_at tetap terisi — dibedakan dari benar-benar belum
//     dicek).
func (s *Store) UpsertJournalURL(journalID int64, url, kind, source string,
	httpStatus *int, finalURL, checkedAt string) (int64, error) {
	if url == "" || kind == "" || source == "" {
		return 0, fmt.Errorf("journal_urls: url/kind/source wajib (j%d %q %q)", journalID, kind, source)
	}
	res, err := s.db.Exec(`
		INSERT INTO journal_urls (journal_id, url, kind, source,
		                          http_status, final_url, checked_at, is_canonical)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(journal_id, kind, url) DO UPDATE SET
			http_status = excluded.http_status,
			final_url   = excluded.final_url,
			checked_at  = excluded.checked_at
		WHERE (excluded.http_status IS NOT NULL
		       AND COALESCE(journal_urls.http_status, -1) <> excluded.http_status)
		   OR (excluded.final_url IS NOT NULL
		       AND COALESCE(journal_urls.final_url, '') <> excluded.final_url)
		   OR (excluded.checked_at IS NOT NULL
		       AND COALESCE(journal_urls.checked_at, '') <> excluded.checked_at)`,
		journalID, url, kind, source, httpStatus, nullKosong(finalURL), nullKosong(checkedAt),
	)
	if err != nil {
		return 0, fmt.Errorf("journal_urls upsert j%d (%s): %w", journalID, url, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("journal_urls rows affected j%d: %w", journalID, err)
	}
	return n, nil
}

// nullKosong = string kosong → NULL (pola COALESCE db: "" & NULL dianggap
// sama saat dibaca, tapi tulis konsisten: kosong = NULL).
func nullKosong(s string) any {
	if s == "" {
		return nil
	}
	return s
}
