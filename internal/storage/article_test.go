package storage

import (
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"testing"
)

func seedArticleJournal(t *testing.T, st *Store, id int) {
	t.Helper()
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: id, Name: "Jurnal A", SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}
}

func fullArticleRow(jid int64) ArticleRow {
	return ArticleRow{
		JournalID: jid, RecordID: "oai:jurnalku:123",
		VolumeNo: 12, IssueNo: 1, Year: 2025,
		Title: "Judul Artikel", DOI: "10.1234/abc",
		ArticleURL:    "https://j.test/index.php/j/article/view/123",
		AbstractID:    "Abstrak dalam Bahasa Indonesia.",
		AbstractEN:    "Abstract in English.",
		Language:      LanguageMixed,
		KeywordsJSON:  `[{"kw":"stunting","src":"author","lang":"id"}]`,
		LicenseStatus: LicenseOpen, LicenseURL: "https://creativecommons.org/licenses/by/4.0/",
		Source: ArticleSourceOAI, IsComplete: true,
		Provenance: `{"oai":"ok"}`,
	}
}

// TestUpsertArticle — insert valid + guard whitelist/JSON + idempotent rerun.
func TestUpsertArticle(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "art.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	seedArticleJournal(t, st, 1)

	row := fullArticleRow(1)
	if err := st.UpsertArticle(row); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Rerun idempotent — 1 baris, nilai terbaru.
	row.AbstractID = "Abstrak revisi."
	if err := st.UpsertArticle(row); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	got, err := st.Articles(1)
	if err != nil || len(got) != 1 {
		t.Fatalf("Articles = %d baris err=%v — mau 1", len(got), err)
	}
	a := got[0]
	if a.AbstractID != "Abstrak revisi." || a.Language != LanguageMixed ||
		a.LicenseStatus != LicenseOpen || !a.IsComplete {
		t.Errorf("isi baris = %+v — mau ter-update penuh", a)
	}
	if a.VolumeNo != 12 || a.Year != 2025 || a.DOI != "10.1234/abc" {
		t.Errorf("vol/year/doi = %d/%d/%q — mau 12/2025/10.1234/abc", a.VolumeNo, a.Year, a.DOI)
	}

	// Guard: 4 kelas lisensi.
	bad := fullArticleRow(1)
	bad.RecordID = "rec2"
	bad.LicenseStatus = "free"
	if err := st.UpsertArticle(bad); err == nil {
		t.Error("license_status 'free' harusnya DITOLAK (di luar 4 kelas)")
	}
	// Guard: language.
	bad.LicenseStatus = LicenseUnknown
	bad.Language = "de"
	if err := st.UpsertArticle(bad); err == nil {
		t.Error("language 'de' harusnya DITOLAK")
	}
	// Guard: keywords JSON.
	bad.Language = LanguageID
	bad.KeywordsJSON = "bukan-json"
	if err := st.UpsertArticle(bad); err == nil {
		t.Error("keywords bukan JSON harusnya DITOLAK")
	}
	// Source whitelist (C3b opsi A — approve 27 Okt): oai|crossref|doaj|
	// landing sah; nilai di luar whitelist tetap ditolak.
	bad.KeywordsJSON = "[]"
	bad.Source = "crossref"
	if err := st.UpsertArticle(bad); err != nil {
		t.Errorf("source 'crossref' harusnya DITERIMA (C3b API-merge sbg primer): %v", err)
	}
	bad.RecordID = "rec2b"
	bad.Source = "doaj"
	if err := st.UpsertArticle(bad); err != nil {
		t.Errorf("source 'doaj' harusnya DITERIMA (C3b fallback kedua): %v", err)
	}
	bad.RecordID = "rec2c"
	bad.Source = "scraping"
	if err := st.UpsertArticle(bad); err == nil {
		t.Error("source 'scraping' harusnya DITOLAK (whitelist oai|crossref|doaj|landing)")
	}
	// Default JSON: "" → "[]" utk author-kosong.
	ok := fullArticleRow(1)
	ok.RecordID = "rec3"
	ok.KeywordsJSON = ""
	if err := st.UpsertArticle(ok); err != nil {
		t.Fatalf("keywords kosong harus default []: %v", err)
	}
	total, complete, err := st.CountArticles(1)
	if err != nil || total != 4 || complete != 4 {
		t.Errorf("CountArticles = %d/%d err=%v — mau 4/4 (rec1 + crossref + doaj + rec3)", total, complete, err)
	}
}

// TestUpdateArticleKeywords — pass 2 tulis keywords + guard & rec-hilang.
func TestUpdateArticleKeywords(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "art2.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	seedArticleJournal(t, st, 5)

	row := fullArticleRow(5)
	row.KeywordsJSON = "[]"
	row.IsComplete = false
	if err := st.UpsertArticle(row); err != nil {
		t.Fatalf("insert: %v", err)
	}
	kw := `[{"kw":"gizi balita","src":"tfidf","lang":"id"}]`
	if err := st.UpdateArticleKeywords(5, "oai:jurnalku:123", kw, `{"idf":"v1"}`); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := st.Articles(5)
	if len(got) != 1 || got[0].KeywordsJSON != kw {
		t.Errorf("keywords = %q — mau hasil pass-2", got[0].KeywordsJSON)
	}
	if err := st.UpdateArticleKeywords(5, "tidak-ada", kw, "{}"); err == nil {
		t.Error("rec tidak-ada harusnya error")
	}
	if err := st.UpdateArticleKeywords(5, "oai:jurnalku:123", "rusak", "{}"); err == nil {
		t.Error("keywords rusak harusnya DITOLAK")
	}
}

// TestAllArticles — urut (journal, record) deterministik utk pass 2/gold-test.
func TestAllArticles(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "art3.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	seedArticleJournal(t, st, 1)
	seedArticleJournal(t, st, 2)

	for _, j := range []int64{1, 2} {
		for _, rid := range []string{"b", "a"} {
			r := fullArticleRow(j)
			r.RecordID = rid
			if err := st.UpsertArticle(r); err != nil {
				t.Fatalf("insert j%d %s: %v", j, rid, err)
			}
		}
	}
	all, err := st.AllArticles()
	if err != nil || len(all) != 4 {
		t.Fatalf("AllArticles = %d err=%v — mau 4", len(all), err)
	}
	want := []struct {
		jid int64
		rid string
	}{{1, "a"}, {1, "b"}, {2, "a"}, {2, "b"}}
	for i, w := range want {
		if all[i].JournalID != w.jid || all[i].RecordID != w.rid {
			t.Errorf("urutan[%d] = j%d/%s — mau j%d/%s", i, all[i].JournalID, all[i].RecordID, w.jid, w.rid)
		}
	}
}
