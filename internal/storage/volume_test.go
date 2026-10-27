package storage

import (
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"testing"
)

// TestUpsertVolumeStats — idempotent + guard JSON/source + resume refine-C
// (skip bila is_complete=1; retry bila is_complete=0).
func TestUpsertVolumeStats(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "vol.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: 1, Name: "Jurnal A", SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}

	row := VolumeStatsRow{
		JournalID: 1, AnchorYear: 2025, WindowStart: 2023, WindowEnd: 2025,
		VolPerYearAvg:        2.5,
		ArticlesPerVolumeAvg: 10.2, ReleaseMonths: "1,12",
		IssuesJSON:     `[{"vol":11,"no":1,"year":2025}]`,
		Source:         "ojs",
		IsComplete:     true,
		ProvenanceJSON: `{"archive":"ok"}`,
	}
	if err := st.UpsertVolumeStats(row); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Resume: is_complete=1 → skip (true).
	if ok, err := st.VolumeStatsComplete(1); err != nil || !ok {
		t.Fatalf("complete = %v, %v — mau true (skip)", ok, err)
	}

	// Rerun parsial (TOC miss) → is_complete=0 → RETRY (false).
	row.IsComplete = false
	row.ReleaseMonths = ""
	if err := st.UpsertVolumeStats(row); err != nil {
		t.Fatalf("upsert parsial: %v", err)
	}
	if ok, err := st.VolumeStatsComplete(1); err != nil || ok {
		t.Fatalf("complete = %v, %v — mau false (retry parsial)", ok, err)
	}

	// Baca balik: nullable release_months jujur + anchor terbaca.
	got, ok, err := st.VolumeStats(1)
	if err != nil || !ok {
		t.Fatalf("VolumeStats: %v, ok=%v", err, ok)
	}
	if got.AnchorYear != 2025 || got.ReleaseMonths != "" || got.IsComplete {
		t.Errorf("got %+v — mau anchor2025, months \"\", complete=false", got)
	}

	// Guard: source di luar whitelist / JSON invalid.
	bad := row
	bad.Source = "garuda"
	if err := st.UpsertVolumeStats(bad); err == nil {
		t.Error("source 'garuda' harus ditolak")
	}
	bad.Source = "ojs"
	bad.IssuesJSON = "bukan-json"
	if err := st.UpsertVolumeStats(bad); err == nil {
		t.Error("issues invalid harus ditolak")
	}

	// Belum ada baris = proses (false).
	if ok, err := st.VolumeStatsComplete(99); err != nil || ok {
		t.Fatalf("journal 99 = %v, %v — mau false (belum ada)", ok, err)
	}
}
