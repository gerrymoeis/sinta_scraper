package storage

import (
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"testing"
)

// TestUpdateJournalsE7 = whitelist kolom + idempoten + tak menyentuh kolom
// lain (K1: raw utuh kecuali kolom whitelist).
func TestUpdateJournalsE7(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	j1 := sinta.Journal{ID: 501, Name: "Kosong", SintaRank: 1, SourcePage: 1,
		GarudaURL: "https://garuda.kemdiktisaintek.go.id/journal/view/1"}
	j2 := sinta.Journal{ID: 502, Name: "Terisi", SintaRank: 1, SourcePage: 1,
		SubjectArea: "ASLI JANGAN DIUBAH",
		GarudaURL:   "https://garuda.kemdiktisaintek.go.id/journal/view/2"}
	if _, err := st.UpsertJournals([]sinta.Journal{j1, j2}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}

	// 1) fill subject_area utk baris kosong → 1 baris.
	n, err := st.UpdateJournalsE7(501, "subject_area", "Engineering")
	if err != nil || n != 1 {
		t.Fatalf("fill kosong: n=%d err=%v, want 1/<nil>", n, err)
	}
	// 2) idempoten: ulang → 0 baris.
	if n, err = st.UpdateJournalsE7(501, "subject_area", "Engineering"); err != nil || n != 0 {
		t.Fatalf("ulang: n=%d err=%v, want 0/<nil>", n, err)
	}
	// 3) baris terisi TIDAK lewat method fill (driver hanya target kosong) —
	//    cek pertahanan: paksa write langsung di luar driver = nilai berubah,
	//    tapi method dgn nilai beda utk garuda_url diperbolehkan (overwrite 9).
	var subj string
	if err = st.db.QueryRow(`SELECT subject_area FROM journals WHERE id=502`).Scan(&subj); err != nil || subj != "ASLI JANGAN DIUBAH" {
		t.Fatalf("j502 subject terjaga: subj=%q err=%v", subj, err)
	}

	// 4) garuda_url overwrite idempoten.
	n, err = st.UpdateJournalsE7(501, "garuda_url", "https://garuda.kemdiktisaintek.go.id/journal/view/99")
	if err != nil || n != 1 {
		t.Fatalf("garuda_url: n=%d err=%v, want 1/<nil>", n, err)
	}
	if n, err = st.UpdateJournalsE7(501, "garuda_url", "https://garuda.kemdiktisaintek.go.id/journal/view/99"); err != nil || n != 0 {
		t.Fatalf("garuda_url ulang: n=%d err=%v, want 0/<nil>", n, err)
	}

	// 5) field di luar whitelist → tolak.
	if _, err = st.UpdateJournalsE7(501, "name", "HACK"); err == nil {
		t.Fatal("field name harus ditolak whitelist")
	}
	if _, err = st.UpdateJournalsE7(501, "content_hash", "x"); err == nil {
		t.Fatal("field content_hash harus ditolak whitelist")
	}

	// 6) nama asli j501 tak tersentuh.
	var name string
	if err = st.db.QueryRow(`SELECT name FROM journals WHERE id=501`).Scan(&name); err != nil || name != "Kosong" {
		t.Fatalf("name j501 utuh: %q err=%v", name, err)
	}
}
