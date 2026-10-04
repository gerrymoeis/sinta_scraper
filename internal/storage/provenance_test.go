package storage

import (
	"os"
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"strconv"
	"testing"
)

// tahap2Tables = tabel Tahap 2 yang wajib dibuat oleh DDL (doc 30 §3;
// subject_map ditambahkan Q3 — §13.3).
var tahap2Tables = []string{
	"journal_enrichment", "journal_urls", "journal_source_profile", "phase2_progress",
	"subject_map",
}

// daftarTabelQuery mengembalikan set nama tabel di sebuah db.
func daftarTabelQuery(t *testing.T, st *Store) map[string]bool {
	t.Helper()
	rows, err := st.db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		found[n] = true
	}
	return found
}

func TestSchemaPhase2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tahap2.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	found := daftarTabelQuery(t, st)
	for _, want := range tahap2Tables {
		if !found[want] {
			t.Errorf("tabel Tahap 2 %q tidak dibuat", want)
		}
	}

	// Idempoten: Open kedua pada db yang sama tidak boleh error/ganda.
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("Open kedua: %v", err)
	}
	defer st2.Close()
	if found2 := daftarTabelQuery(t, st2); len(found2) < len(found) {
		t.Errorf("tabel hilang setelah Open kedua: %v", found2)
	}
}

func TestMergeProvenance(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "prov.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: 101, Name: "Jurnal A",
		SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}
	const ts = "2026-10-02T10:00:00Z"

	// 1) dua field berbeda tersimpan berdampingan.
	if err := st.MergeProvenance(101, "subject_area", ProvEntry{
		Value: "Education", Source: "sinta", RetrievedAt: ts, Confidence: 1.0,
	}); err != nil {
		t.Fatalf("merge subject_area: %v", err)
	}
	if err := st.MergeProvenance(101, "eissn", ProvEntry{
		Value: "24428620", Source: "garuda", RetrievedAt: ts, Confidence: 0.98,
	}); err != nil {
		t.Fatalf("merge eissn: %v", err)
	}
	entries, err := st.Provenance(101)
	if err != nil {
		t.Fatalf("baca provenance: %v", err)
	}
	if entries["subject_area"].Value != "Education" || entries["subject_area"].Source != "sinta" {
		t.Errorf("subject_area salah: %+v", entries["subject_area"])
	}
	if entries["eissn"].Value != "24428620" || entries["eissn"].Confidence != 0.98 {
		t.Errorf("eissn salah: %+v", entries["eissn"])
	}

	// 2) merge field sama → REPLACE entri itu; field lain TIDAK berubah.
	if err := st.MergeProvenance(101, "subject_area", ProvEntry{
		Value: "Pendidikan", Source: "garuda", RetrievedAt: ts, Confidence: 0.9,
	}); err != nil {
		t.Fatalf("merge ulang subject_area: %v", err)
	}
	entries, err = st.Provenance(101)
	if err != nil {
		t.Fatalf("baca ulang: %v", err)
	}
	if entries["subject_area"].Value != "Pendidikan" {
		t.Errorf("subject_area tidak terganti: %+v", entries["subject_area"])
	}
	if entries["eissn"].Value != "24428620" {
		t.Errorf("eissn ikut rusak oleh merge subject_area: %+v", entries["eissn"])
	}

	// 3) idempoten byte-identik: merge entri identik → JSON persis sama.
	rawSebelum := bacaProvRaw(t, st, 101)
	if err := st.MergeProvenance(101, "subject_area", ProvEntry{
		Value: "Pendidikan", Source: "garuda", RetrievedAt: ts, Confidence: 0.9,
	}); err != nil {
		t.Fatalf("merge idempoten: %v", err)
	}
	if rawSesudah := bacaProvRaw(t, st, 101); rawSesudah != rawSebelum {
		t.Errorf("merge identik tidak idempoten:\nsebelum = %s\nsesudah = %s", rawSebelum, rawSesudah)
	}

	// 4) validasi menolak, JSON tidak berubah.
	rawValid := bacaProvRaw(t, st, 101)
	kasus := []struct {
		nama  string
		field string
		e     ProvEntry
	}{
		{"sumber di luar whitelist", "x", ProvEntry{Value: "v", Source: "typo-source", RetrievedAt: ts, Confidence: 0.5}},
		{"confidence > 1", "x", ProvEntry{Value: "v", Source: "sinta", RetrievedAt: ts, Confidence: 1.5}},
		{"confidence < 0", "x", ProvEntry{Value: "v", Source: "sinta", RetrievedAt: ts, Confidence: -0.1}},
		{"field kosong", " ", ProvEntry{Value: "v", Source: "sinta", RetrievedAt: ts, Confidence: 0.5}},
	}
	for _, k := range kasus {
		if err := st.MergeProvenance(101, k.field, k.e); err == nil {
			t.Errorf("%s: seharusnya DITOLAK", k.nama)
		}
	}
	if rawSesudahGagal := bacaProvRaw(t, st, 101); rawSesudahGagal != rawValid {
		t.Errorf("JSON berubah meski validasi gagal:\n%s\nvs\n%s", rawValid, rawSesudahGagal)
	}

	// 5) RetrievedAt kosong → diisi otomatis (bukan kosong).
	if err := st.MergeProvenance(101, "pissn", ProvEntry{
		Value: "12345678", Source: "sinta", Confidence: 1.0,
	}); err != nil {
		t.Fatalf("merge tanpa RetrievedAt: %v", err)
	}
	if e, _ := st.Provenance(101); e["pissn"].RetrievedAt == "" {
		t.Error("RetrievedAt tidak diisi otomatis")
	}
}

func TestProvenanceTidakSentuhJournals(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "immutable.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: 7, Name: "Jurnal B",
		SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}

	sebelum := bacaJournalsSnapshot(t, st)
	if err := st.MergeProvenance(7, "subject_area", ProvEntry{
		Value: "Health", Source: "garuda", RetrievedAt: "2026-10-02T10:00:00Z", Confidence: 1.0,
	}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	sesudah := bacaJournalsSnapshot(t, st)
	if sebelum != sesudah {
		t.Errorf("tabel journals BERUBAH oleh MergeProvenance!\nsebelum = %s\nsesudah = %s", sebelum, sesudah)
	}
}

// TestMigrasiDBSumberStage2 = verifikasi Q1 Langkah 5 (doc 30 §11.2):
// jalankan Open() terhadap duplikat DB sumber Tahap 2 → 4 tabel muncul,
// journals tetap 261 (IMMUTABLE), provenance semua '{}'. Idempoten;
// di-skip bila file tidak ada (mis. sebelum rapikan folder).
func TestMigrasiDBSumberStage2(t *testing.T) {
	path := filepath.Join("..", "..", "data", "stage2", "sinta-r1-source.db")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("db sumber Tahap 2 tidak ada (%s) — lewati", path)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open db sumber: %v", err)
	}
	defer st.Close()

	found := daftarTabelQuery(t, st)
	for _, want := range tahap2Tables {
		if !found[want] {
			t.Errorf("tabel %q belum ada di db sumber", want)
		}
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM journals`).Scan(&n); err != nil {
		t.Fatalf("hitung journals: %v", err)
	}
	if n != 261 {
		t.Errorf("journals = %d, HARUS 261 (immutable terlanggar!)", n)
	}
	var provKosong int
	if err := st.db.QueryRow(
		`SELECT COUNT(*) FROM journal_enrichment WHERE provenance NOT IN ('{}')`,
	).Scan(&provKosong); err != nil {
		t.Fatalf("hitung provenance: %v", err)
	}
	if provKosong != 0 {
		t.Errorf("%d baris provenance tidak '{}' pada db sumber", provKosong)
	}
	t.Logf("db sumber OK: 4 tabel Tahap 2 ada, journals=%d utuh, provenance bersih", n)
}

// bacaProvRaw mengambil byte mentah kolom provenance utk assert idempoten.
func bacaProvRaw(t *testing.T, st *Store, journalID int64) string {
	t.Helper()
	var raw string
	if err := st.db.QueryRow(
		`SELECT provenance FROM journal_enrichment WHERE journal_id = ?`, journalID,
	).Scan(&raw); err != nil {
		t.Fatalf("baca raw provenance: %v", err)
	}
	return raw
}

// bacaJournalsSnapshot = ringkasan seluruh isi journals (count + hash per
// baris) utk membuktikan MergeProvenance tidak menyentuh tabel itu.
func bacaJournalsSnapshot(t *testing.T, st *Store) string {
	t.Helper()
	rows, err := st.db.Query(`SELECT id, name, content_hash, last_scraped_at FROM journals ORDER BY id`)
	if err != nil {
		t.Fatalf("query journals: %v", err)
	}
	defer rows.Close()
	out := ""
	for rows.Next() {
		var id int
		var name, hash, at string
		if err := rows.Scan(&id, &name, &hash, &at); err != nil {
			t.Fatal(err)
		}
		out += "|" + strconv.Itoa(id) + ":" + name + ":" + hash + ":" + at
	}
	return out
}
