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
		OAStatus: OAFree, LicenseType: "CC BY 4.0", // C7: atribut terpisah
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
	if a.OAStatus != OAFree || a.LicenseType != "CC BY 4.0" {
		t.Errorf("atribut C7 = %q/%q — mau free/CC BY 4.0 (roundtrip)", a.OAStatus, a.LicenseType)
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
	// Guard C7: oa_status & license_type di luar whitelist DITOLAK (D1);
	// atribut kosong "" → default jujur "unknown" (pemanggil lama).
	bad.KeywordsJSON = "[]"
	bad.OAStatus = "gratis"
	if err := st.UpsertArticle(bad); err == nil {
		t.Error("oa_status 'gratis' harusnya DITOLAK (whitelist free|paid|restricted|unknown)")
	}
	bad.OAStatus = OARestricted
	bad.LicenseType = "CC BY-XYZ"
	if err := st.UpsertArticle(bad); err == nil {
		t.Error("license_type 'CC BY-XYZ' harusnya DITOLAK (whitelist CC-kanonik|ARR|unknown)")
	}
	bad.OAStatus, bad.LicenseType = "", ""
	if err := st.UpsertArticle(bad); err != nil {
		t.Errorf("atribut kosong harus default unknown: %v", err)
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

// TestMigrasiKolomAtributC7 — db "lama" (sebelum C7, tanpa 2 kolom atribut)
// → Open menambahkannya lewat ALTER dgn DEFAULT 'unknown' (jujur utk baris
// lama), Open ulang idempoten (doc 43 §6.x.8 tahap D1).
func TestMigrasiKolomAtributC7(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c7attr.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	kolomAda := func(st *Store) (oa, lt bool) {
		t.Helper()
		for _, c := range []struct {
			nama string
			*bool
		}{
			{"oa_status", &oa},
			{"license_type", &lt},
		} {
			var n int
			if err := st.db.QueryRow(
				`SELECT COUNT(*) FROM pragma_table_info('articles') WHERE name = ?`,
				c.nama,
			).Scan(&n); err != nil {
				t.Fatalf("cek kolom %s: %v", c.nama, err)
			}
			*c.bool = n > 0
		}
		return oa, lt
	}
	if oa, lt := kolomAda(st); !oa || !lt {
		t.Fatalf("db baru harus sudah punya kolom C7: oa=%v lt=%v", oa, lt)
	}
	// baris lama → simulasi db pra-C7: jatuhkan kedua kolom
	seedArticleJournal(t, st, 1)
	if err := st.UpsertArticle(fullArticleRow(1)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	for _, col := range []string{"oa_status", "license_type"} {
		if _, err := st.db.Exec(`ALTER TABLE articles DROP COLUMN ` + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	if oa, lt := kolomAda(st); oa || lt {
		t.Fatal("drop kolom gagal")
	}
	st.Close()

	// Open kedua (migrasi): kolom kembali + baris lama default 'unknown'
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("Open kedua (migrasi C7): %v", err)
	}
	defer st2.Close()
	if oa, lt := kolomAda(st2); !oa || !lt {
		t.Fatalf("ALTER C7 tidak menambah kolom: oa=%v lt=%v", oa, lt)
	}
	got, err := st2.Articles(1)
	if err != nil || len(got) != 1 {
		t.Fatalf("Articles = %d err=%v — mau 1", len(got), err)
	}
	if got[0].OAStatus != OAUnknown || got[0].LicenseType != LicenseTypeUnknown {
		t.Errorf("baris pra-C7 = %q/%q — mau default unknown/unknown", got[0].OAStatus, got[0].LicenseType)
	}
	// rerun Open = idempoten (tanpa error, kolom tetap)
	st2.Close()
	if st3, err := Open(path); err != nil {
		t.Fatalf("Open ketiga (idempoten): %v", err)
	} else {
		st3.Close()
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

// TestCorpusTargetsFallbackOJSURL — G17b (28 Okt): Canonical fallback ke
// journals.ojs_url bila source_profile kosong (kasus j688 IJEEI/j696 IJASEIT
// — enrichment garuda tanpa baris profile, tapi stage sinta MENYIMPAN
// ojs_url). Profil menang bila ada (NULLIF kedua lapis → kebal string ”).
func TestCorpusTargetsFallbackOJSURL(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "tgt.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	// j10 TANPA profile + ojs_url terisi → fallback aktif.
	// j11 profile ADA + ojs_url juga ada → profil menang.
	// j12 profile ada tapi canonical='' (string kosong, bukan NULL) → ojs_url menang.
	if _, err := st.UpsertJournals([]sinta.Journal{
		{ID: 10, Name: "Tanpa Profil", SintaRank: 1, SourcePage: 1, OJSURL: "https://ojs.tanpaprofil.test/index.php/tp"},
		{ID: 11, Name: "Profil Menang", SintaRank: 1, SourcePage: 1, OJSURL: "https://ojs.lama.test/x"},
		{ID: 12, Name: "Profil Kosong", SintaRank: 1, SourcePage: 1, OJSURL: "https://ojs.profilkosong.test/index.php/pk"},
	}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}
	for _, p := range []SourceProfile{
		{JournalID: 11, Platform: "ojs", Evidence: "marker", CanonicalURL: "https://baru.test/ny"},
		{JournalID: 12, Platform: "ojs", Evidence: "marker", CanonicalURL: ""},
	} {
		if err := st.UpsertSourceProfile(p); err != nil {
			t.Fatalf("UpsertSourceProfile j%d: %v", p.JournalID, err)
		}
	}
	tgts, err := st.CorpusTargets([]int{1})
	if err != nil {
		t.Fatalf("CorpusTargets: %v", err)
	}
	want := map[int64]string{
		10: "https://ojs.tanpaprofil.test/index.php/tp",
		11: "https://baru.test/ny",
		12: "https://ojs.profilkosong.test/index.php/pk",
	}
	if len(tgts) != 3 {
		t.Fatalf("target = %d — mau 3", len(tgts))
	}
	for _, tg := range tgts {
		if tg.Canonical != want[tg.ID] {
			t.Errorf("j%d Canonical = %q — mau %q", tg.ID, tg.Canonical, want[tg.ID])
		}
	}
}
