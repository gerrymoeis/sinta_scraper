package storage

import (
	"database/sql"
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"testing"
)

func TestOpenAndSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

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
	for _, want := range []string{"journals", "scrape_progress"} {
		if !found[want] {
			t.Errorf("tabel %q tidak dibuat", want)
		}
	}

	var mode string
	if err := st.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("baca journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, harusnya 'wal'", mode)
	}

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("Open kedua: %v", err)
	}
	defer st2.Close()
}

func TestUpsertAndCheckpoint(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	j1 := sinta.Journal{ID: 101, Name: "Jurnal A", SintaRank: 1, SourcePage: 1,
		IsScopus: true, Impact: 1.25, OJSURL: "https://ojs.a.test"}
	j2 := sinta.Journal{ID: 102, Name: "Jurnal B", SintaRank: 2, SourcePage: 2}
	if _, err := st.UpsertJournals([]sinta.Journal{j1, j2}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}

	var name, status string
	var scrapedAt sql.NullString
	var srcPage int
	var hash string
	err = st.db.QueryRow(`SELECT name, ojs_status, ojs_scraped_at, source_page, content_hash
		FROM journals WHERE id = 101`).
		Scan(&name, &status, &scrapedAt, &srcPage, &hash)
	if err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if name != "Jurnal A" || status != "pending" || srcPage != 1 || hash == "" {
		t.Errorf("nama/status/halaman/hash salah: %q %q %d %q", name, status, srcPage, hash)
	}
	if scrapedAt.Valid {
		t.Error("ojs_scraped_at harus NULL sebelum stage OJS berjalan")
	}

	if _, err := st.db.Exec(`UPDATE journals
		SET ojs_status = 'done', ojs_scraped_at = ? WHERE id = 101`,
		"2026-09-27T00:00:00Z"); err != nil {
		t.Fatalf("update ojs: %v", err)
	}

	j1.Name = "Jurnal A (rev 2)"
	rep, err := st.UpsertJournals([]sinta.Journal{j1})
	if err != nil {
		t.Fatalf("upsert kedua: %v", err)
	}
	if rep.Updated != 1 || len(rep.Changes) != 1 || rep.Changes[0].Fields[0].Field != "name" {
		t.Errorf("laporan diff salah: %+v", rep)
	}
	var at2 sql.NullString
	var name2, status2 string
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM journals`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("idempoten gagal: count=%d, harusnya 2 (duplikat!)", n)
	}
	if err := st.db.QueryRow(`SELECT name, ojs_status, ojs_scraped_at FROM journals WHERE id = 101`).
		Scan(&name2, &status2, &at2); err != nil {
		t.Fatal(err)
	}
	if name2 != "Jurnal A (rev 2)" {
		t.Errorf("nama tidak ter-update: %q", name2)
	}
	if status2 != "done" || !at2.Valid {
		t.Errorf("state OJS BOCOR ter-reset oleh stage sinta: status=%q scraped_at_valid=%v", status2, at2.Valid)
	}

	for _, p := range []int{1, 2} {
		if err := st.MarkPageCompleted("rank-1", p); err != nil {
			t.Fatalf("MarkPageCompleted(%d): %v", p, err)
		}
	}
	if done, err := st.CompletedPages("rank-1"); err != nil || !done[1] || !done[2] || len(done) != 2 {
		t.Errorf("CompletedPages(rank-1) = %v, err=%v; want {1,2}", done, err)
	}
	if other, err := st.CompletedPages("rank-2"); err != nil || len(other) != 0 {
		t.Errorf("run-key terpisah harus kosong: %v, err=%v", other, err)
	}

	if err := st.ClearCheckpoint("rank-1"); err != nil {
		t.Fatalf("ClearCheckpoint: %v", err)
	}
	if after, err := st.CompletedPages("rank-1"); err != nil || len(after) != 0 {
		t.Errorf("setelah ClearCheckpoint harus kosong: %v, err=%v", after, err)
	}
}

func TestUpsertDiff(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "diff.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	base := sinta.Journal{ID: 301, Name: "Jurnal D", SintaRank: 1, CitationsTotal: 10, SourcePage: 1}

	rep, err := st.UpsertJournals([]sinta.Journal{base})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if rep.New != 1 || rep.Updated != 0 || rep.Unchanged != 0 {
		t.Fatalf("laporan insert salah: %+v", rep)
	}

	rep, err = st.UpsertJournals([]sinta.Journal{base})
	if err != nil {
		t.Fatalf("upsert identik: %v", err)
	}
	if rep.Unchanged != 1 || rep.New != 0 || rep.Updated != 0 || len(rep.Changes) != 0 {
		t.Errorf("laporan identik salah: %+v", rep)
	}

	chg := base
	chg.SintaRank = 3
	chg.CitationsTotal = 25
	rep, err = st.UpsertJournals([]sinta.Journal{chg})
	if err != nil {
		t.Fatalf("upsert berubah: %v", err)
	}
	if rep.Updated != 1 || len(rep.Changes) != 1 || len(rep.Changes[0].Fields) != 2 {
		t.Fatalf("laporan ubah salah: %+v", rep)
	}
	got := map[string]string{}
	for _, f := range rep.Changes[0].Fields {
		got[f.Field] = f.Old + "→" + f.New
	}
	if got["sinta_rank"] != "1→3" || got["citations_total"] != "10→25" {
		t.Errorf("field berubah salah: %v", got)
	}

	var rank, cit int
	if err := st.db.QueryRow(`SELECT sinta_rank, citations_total FROM journals WHERE id=301`).
		Scan(&rank, &cit); err != nil {
		t.Fatal(err)
	}
	if rank != 3 || cit != 25 {
		t.Errorf("db tidak ter-update: rank=%d cit=%d", rank, cit)
	}

	rep, err = st.UpsertJournals([]sinta.Journal{{ID: 302, Name: "Jurnal E", SourcePage: 1}})
	if err != nil {
		t.Fatalf("upsert id baru: %v", err)
	}
	if rep.New != 1 {
		t.Errorf("laporan id baru salah: %+v", rep)
	}
}
