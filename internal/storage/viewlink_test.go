package storage

import (
	"path/filepath"
	"testing"

	"sinta-scraper/internal/sinta"
)

func kolomViewAda(t *testing.T, st *Store) (home, oai bool) {
	t.Helper()
	for _, c := range []struct {
		nama string
		*bool
	}{
		{"garuda_home_url", &home},
		{"garuda_oai_url", &oai},
	} {
		var n int
		if err := st.db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('journal_enrichment') WHERE name = ?`,
			c.nama,
		).Scan(&n); err != nil {
			t.Fatalf("cek kolom %s: %v", c.nama, err)
		}
		*c.bool = n > 0
	}
	return home, oai
}

// TestMigrasiKolomViewE3: db "lama" (sebelum E3, tanpa 2 kolom view) →
// Open kedua menambahkannya lewat ALTER; Open ketiga idempoten (doc 31 §4).
func TestMigrasiKolomViewE3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "viewcol.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if h, o := kolomViewAda(t, st); !h || !o {
		t.Fatalf("db baru harus sudah punya kolom view: home=%v oai=%v", h, o)
	}
	// simulasi db lama: jatuhkan kedua kolom
	for _, col := range []string{"garuda_home_url", "garuda_oai_url"} {
		if _, err := st.db.Exec(`ALTER TABLE journal_enrichment DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	if h, o := kolomViewAda(t, st); h || o {
		t.Fatal("drop kolom gagal")
	}
	st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("Open kedua (migrasi): %v", err)
	}
	defer st2.Close()
	if h, o := kolomViewAda(t, st2); !h || !o {
		t.Errorf("ALTER tidak mengembalikan kolom: home=%v oai=%v", h, o)
	}
	st2.Close()
	st3, err := Open(path)
	if err != nil {
		t.Fatalf("Open ketiga (idempoten): %v", err)
	}
	defer st3.Close()
	if h, o := kolomViewAda(t, st3); !h || !o {
		t.Errorf("Open ketiga: kolom hilang: home=%v oai=%v", h, o)
	}
}

func TestUpdateViewLinks(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "fill.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: 501, Name: "Jurnal X", SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}
	if err := st.UpsertGarudaMatch(GarudaMatch{
		JournalID: 501, Status: "matched", GarudaID: 7211,
		GarudaURL: "https://garuda.kemdikbud.go.id/journal/view/7211",
	}); err != nil {
		t.Fatalf("UpsertGarudaMatch: %v", err)
	}

	const home, oai = "https://ojs.example.org", "https://ojs.example.org/oai"
	if err := st.UpdateViewLinks(501, home, oai); err != nil {
		t.Fatalf("UpdateViewLinks: %v", err)
	}
	var h, o string
	if err := st.db.QueryRow(`SELECT garuda_home_url, garuda_oai_url
		FROM journal_enrichment WHERE journal_id = 501`).Scan(&h, &o); err != nil {
		t.Fatalf("baca kolom: %v", err)
	}
	if h != home || o != oai {
		t.Errorf("tersimpan: home=%q oai=%q", h, o)
	}
	// idempoten: tulis nilai sama lagi → tetap sama, tanpa error
	if err := st.UpdateViewLinks(501, home, oai); err != nil {
		t.Fatalf("UpdateViewLinks idempoten: %v", err)
	}
	// jurnal tanpa baris enrichment → error jelas (bukan diam-diam)
	if err := st.UpdateViewLinks(999, home, oai); err == nil {
		t.Error("UpdateViewLinks utk journal tanpa enrichment harus gagal")
	}
	// capture refresh tidak menimpa kolom view (UpsertGarudaMatch ON CONFLICT
	// tak menyentuh garuda_home_url/garuda_oai_url)
	if err := st.UpsertGarudaMatch(GarudaMatch{
		JournalID: 501, Status: "matched", GarudaID: 7211,
		GarudaURL: "https://garuda.kemdikbud.go.id/journal/view/7211",
	}); err != nil {
		t.Fatalf("UpsertGarudaMatch ulang: %v", err)
	}
	if err := st.db.QueryRow(`SELECT garuda_home_url FROM journal_enrichment
		WHERE journal_id = 501`).Scan(&h); err != nil {
		t.Fatalf("baca ulang: %v", err)
	}
	if h != home {
		t.Errorf("refresh menimpa kolom view: %q", h)
	}
}
