package storage

import (
	"database/sql"
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"testing"
)

// statusPtr = bantuan arg http_status (nil = belum/tak ada respons).
func statusPtr(n int) *int { return &n }

func TestUpsertJournalURL(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "urls.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: 1, Name: "Jurnal A", SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}
	const ts = "2026-10-07T10:00:00Z"

	// 1) insert pertama = 1 baris.
	n, err := st.UpsertJournalURL(1, "https://a.id/", "ojs", "sinta", statusPtr(200), "", ts)
	if err != nil || n != 1 {
		t.Fatalf("insert: n=%d err=%v (want 1, nil)", n, err)
	}
	// 2) identik = idempoten (nol berubah).
	n, err = st.UpsertJournalURL(1, "https://a.id/", "ojs", "sinta", statusPtr(200), "", ts)
	if err != nil || n != 0 {
		t.Fatalf("ulang identik: n=%d err=%v (want 0)", n, err)
	}
	// 3) kandidat dari sumber lain dgn URL SAMA: source asal dipertahankan,
	//    status diperbarui bila berubah.
	n, err = st.UpsertJournalURL(1, "https://a.id/", "ojs", "doaj", statusPtr(404), "", ts)
	if err != nil || n != 1 {
		t.Fatalf("update status: n=%d err=%v (want 1)", n, err)
	}
	// 4) URL beda → baris baru (source doaj dipakai utk URL baru).
	n, err = st.UpsertJournalURL(1, "https://b.id/", "ojs", "doaj", nil, "", "")
	if err != nil || n != 1 {
		t.Fatalf("kandidat baru: n=%d err=%v (want 1)", n, err)
	}
	// 5) kandidat belum dicek (NULL) TIDAK menimpa hasil verifikasi lama.
	n, err = st.UpsertJournalURL(1, "https://a.id/", "ojs", "garuda", nil, "", "")
	if err != nil || n != 0 {
		t.Fatalf("NULL tak boleh menimpa: n=%d err=%v (want 0)", n, err)
	}

	rows, err := st.db.Query(`SELECT url, source, http_status, checked_at, is_canonical
		FROM journal_urls WHERE journal_id = 1 ORDER BY url`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type baris struct {
		url, src, cek string
		status        *int
		canon         int
	}
	var semua []baris
	for rows.Next() {
		var b baris
		var status *int
		var cek sql.NullString
		if err := rows.Scan(&b.url, &b.src, &status, &cek, &b.canon); err != nil {
			t.Fatal(err)
		}
		b.status, b.cek = status, cek.String
		semua = append(semua, b)
	}
	if len(semua) != 2 {
		t.Fatalf("jumlah baris = %d, want 2", len(semua))
	}
	// baris pertama (a.id): source asal "sinta" bertahan, status 404 (update #3).
	if semua[0].src != "sinta" || semua[0].status == nil || *semua[0].status != 404 {
		t.Errorf("a.id: source=%q status=%v (want sinta/404)", semua[0].src, semua[0].status)
	}
	// baris kedua (b.id): belum dicek → NULL/NULL, source doaj.
	if semua[1].url != "https://b.id/" || semua[1].src != "doaj" ||
		semua[1].status != nil || semua[1].cek != "" {
		t.Errorf("b.id: %+v (want doaj/null/null)", semua[1])
	}
	for _, b := range semua {
		if b.canon != 0 {
			t.Errorf("%s: is_canonical=%d (harus 0)", b.url, b.canon)
		}
	}
}

func TestUpsertJournalURLGuard(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "urls2.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertJournalURL(1, "", "ojs", "sinta", nil, "", ""); err == nil {
		t.Error("url kosong harus ditolak")
	}
	if _, err := st.UpsertJournalURL(1, "https://x/", "", "sinta", nil, "", ""); err == nil {
		t.Error("kind kosong harus ditolak")
	}
	if _, err := st.UpsertJournalURL(1, "https://x/", "ojs", "", nil, "", ""); err == nil {
		t.Error("source kosong harus ditolak")
	}
}
