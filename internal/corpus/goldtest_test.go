package corpus

import (
	"fmt"
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"sinta-scraper/internal/storage"
	"strings"
	"testing"
)

// TestLightStem — stemming ringan deterministik suffix EN+ID.
func TestLightStem(t *testing.T) {
	cases := map[string]string{
		"studies":    "study",
		"results":    "result",
		"analisis":   "analisi", // dua sisi sama → tetap cocok
		"tingkatkan": "tingkat",
		"gizinya":    "gizi",
		"stunting":   "stunt",      // ing-strip — kedua sisi di-stem sama → tetap match
		"penelitian": "penelitian", // sengaja TIDAK strip "an" (anti salah potong)
		"tested":     "test",
	}
	for in, want := range cases {
		if got := lightStem(in); got != want {
			t.Errorf("lightStem(%q) = %q — mau %q", in, got, want)
		}
	}
}

// TestPhraseMatch — stem-set containment (fuzzy kata kunci).
func TestPhraseMatch(t *testing.T) {
	if !phraseMatch([]string{"stunting"}, []string{"stunting", "balita"}) {
		t.Error("unigram ⊆ frasa harus match")
	}
	if !phraseMatch([]string{"quality", "life"}, []string{"quality", "of", "life"}) {
		t.Error("frasa dgn stopword di gold harus match")
	}
	if !phraseMatch([]string{"balita", "stunting"}, []string{"stunting", "balita"}) {
		t.Error("urutan beda harus match (himpunan topik)")
	}
	if phraseMatch([]string{"gizi"}, []string{"kesehatan", "masyarakat"}) {
		t.Error("token beda tak boleh match")
	}
	if phraseMatch(nil, []string{"stunting"}) {
		t.Error("kosong tak boleh match")
	}
}

// TestPhraseMatchStrict — O-A: token-set PERSIS tanpa stemming (metrik kedua).
func TestPhraseMatchStrict(t *testing.T) {
	if !phraseMatchStrict([]string{"stunting"}, []string{"stunting", "balita"}) {
		t.Error("unigram persis ⊆ frasa harus match strict")
	}
	if !phraseMatchStrict([]string{"balita", "stunting"}, []string{"stunting", "balita"}) {
		t.Error("urutan beda tetap match strict (himpunan topik)")
	}
	if phraseMatchStrict([]string{"studies"}, []string{"study"}) {
		t.Error("beda bentuk TIDAK boleh match strict — itu wilayah fuzzy")
	}
	if !phraseMatch([]string{"studies"}, []string{"study"}) {
		t.Error("invarian: contoh tsb harus match fuzzy (fuzzy ⊇ strict)")
	}
	if phraseMatchStrict([]string{"gizi"}, []string{"kesehatan"}) {
		t.Error("token beda tak boleh match")
	}
	if phraseMatchStrict(nil, []string{"stunting"}) {
		t.Error("kosong tak boleh match")
	}
}

// TestDiagnoseGoldMiss — O-A: kategori diagnosis deterministik (decision
// tree: bentuk → stopword-yg-blok → vocab → tak-diteks → peringkat).
func TestDiagnoseGoldMiss(t *testing.T) {
	stop := map[string]bool{"of": true}
	idf := map[string]float64{"stunting": 1.2, "balita": 0.9, "prevalensi": 0.5,
		"consistency": 1.1, "interest": 0.7, "technology": 0.4}
	cases := []struct {
		name    string
		gs      []string
		fuzzyOK bool
		pred    [][]string
		counts  map[string]int
		want    string
	}{
		{"lolos fuzzy", []string{"stunting"}, true, nil, nil, "bentuk"},
		{"tanpa-stopword match → stopword",
			[]string{"quality", "of", "life"}, false, [][]string{{"quality", "life"}}, nil, "stopword"},
		{"stopword ada tp bersihnya juga miss → penyebab lain",
			[]string{"consistency", "of", "interest"}, false, [][]string{{"grit"}},
			map[string]int{"consistency": 1, "interest": 1}, "peringkat"},
		{"tak ada di korpus", []string{"giziblabla"}, false, [][]string{{"grit"}}, nil, "vocab"},
		{"ada di korpus tp absen dari teks artikel",
			[]string{"technology"}, false, [][]string{{"grit"}},
			map[string]int{"learning": 3, "scaffolding": 1}, "tak-diteks"},
		{"ada di teks & korpus, kalah top-8",
			[]string{"stunting", "prevalensi"}, false, [][]string{{"balita"}},
			map[string]int{"stunting": 2, "prevalensi": 1}, "peringkat"},
	}
	for _, c := range cases {
		if got := diagnoseGoldMiss(c.gs, c.fuzzyOK, c.pred, c.counts, stop, idf); got != c.want {
			t.Errorf("%s: diagnose = %q — mau %q", c.name, got, c.want)
		}
	}
}

// TestGoldStringCap — G13: String() ringkas (cap per kategori + catatan
// sisa jujur) vs StringAll() penuh — deterministik.
func TestGoldStringCap(t *testing.T) {
	rep := &GoldReport{SnapshotVersion: 2, GoldSource: "author",
		Overall: GoldGroup{Lang: "ALL", Articles: 5, GoldKW: 15, PredKW: 40}}
	for i := 0; i < 15; i++ {
		rep.Misses = append(rep.Misses, GoldMiss{
			Journal: 1, Record: fmt.Sprintf("oai:x:article/%d", i), Lang: "en",
			Gold: fmt.Sprintf("kw-%d", i), Kategori: "peringkat", Pred: "a · b",
		})
	}
	short := rep.String()
	full := rep.StringAll()
	// Ringkas: tepat cap baris + catatan sisa (jangan tertukar dgn legenda).
	if n := strings.Count(short, "  [peringkat"); n != missCapPerKategori {
		t.Errorf("String() baris miss = %d — mau cap %d:\n%s", n, missCapPerKategori, short)
	}
	if !strings.Contains(short, "+5 baris [peringkat] tak dicetak") {
		t.Errorf("String() tanpa catatan sisa:\n%s", short)
	}
	if strings.Contains(short, `"kw-10"`) {
		t.Errorf("String() bocor baris ke-11+ (kw-10):\n%s", short)
	}
	// Penuh: semua baris, tanpa catatan cap.
	if n := strings.Count(full, "  [peringkat"); n != 15 {
		t.Errorf("StringAll() baris = %d — mau 15 (tanpa cap)", n)
	}
	if strings.Contains(full, "tak dicetak") {
		t.Errorf("StringAll() tak boleh memuat catatan cap")
	}
	if !strings.Contains(full, `"kw-14"`) {
		t.Errorf("StringAll() tanpa baris terakhir kw-14")
	}
	if rep.String() != rep.String() {
		t.Error("String() tidak deterministik")
	}
}

// seedGoldDB = korpus uji: 2 author-id + 1 crossref-en + 1 tanpa keyword.
func seedGoldDB(t *testing.T) *storage.Store {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "gold.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: 1, Name: "Jurnal Uji", SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}
	arts := []storage.ArticleRow{
		{JournalID: 1, RecordID: "g1", Title: "Analisis stunting pada balita di Kabupaten X",
			ArticleURL:    "https://j.test/1",
			AbstractID:    "Penelitian ini menganalisis stunting pada balita dengan data survei dan hasilnya menunjukkan penurunan kasus stunting.",
			Language:      LanguageID,
			KeywordsJSON:  `[{"kw":"stunting","src":"author","lang":"id"},{"kw":"balita","src":"author","lang":"id"},{"kw":"virus zika dengue","src":"author","lang":"id"}]`,
			LicenseStatus: LicenseUnknown, Source: storage.ArticleSourceOAI, IsComplete: true},
		{JournalID: 1, RecordID: "g2", Title: "Prevalensi stunting dan gizi balita",
			ArticleURL:    "https://j.test/2",
			AbstractID:    "Data survei menunjukkan prevalensi stunting pada balita masih tinggi dan gizi balita perlu diperbaiki.",
			Language:      LanguageID,
			KeywordsJSON:  `[{"kw":"prevalensi stunting","src":"author","lang":"id"}]`,
			LicenseStatus: LicenseUnknown, Source: storage.ArticleSourceOAI, IsComplete: true},
		{JournalID: 1, RecordID: "g3", Title: "Quality of life among stroke patients",
			ArticleURL:    "https://j.test/3",
			AbstractEN:    "This study analyzes quality of life among stroke patients using survey data from hospitals and quality of life scores.",
			Language:      LanguageEN,
			KeywordsJSON:  `[{"kw":"quality of life","src":"crossref","lang":"en"}]`,
			LicenseStatus: LicenseUnknown, Source: storage.ArticleSourceOAI, IsComplete: true},
		{JournalID: 1, RecordID: "g4", Title: "Gizi buruk pada bayi",
			ArticleURL: "https://j.test/4",
			AbstractID: "Penelitian gizi buruk pada bayi di Indonesia.",
			Language:   LanguageID, KeywordsJSON: "[]",
			LicenseStatus: LicenseUnknown, Source: storage.ArticleSourceOAI, IsComplete: true},
	}
	for _, a := range arts {
		if err := st.UpsertArticle(a); err != nil {
			t.Fatalf("UpsertArticle %s: %v", a.RecordID, err)
		}
	}
	return st
}

// TestRunGoldTest — sampel gold + perluasan crossref + metrik + determinisme.
func TestRunGoldTest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keywords")
	st := seedGoldDB(t)
	// Snapshot dibuat pass 2 (alur produksi: pass1+pass2 dulu → gold-test).
	if _, err := RunTFIDF(st, dir, nil); err != nil {
		t.Fatalf("RunTFIDF: %v", err)
	}

	rep, err := RunGoldTest(st, dir)
	if err != nil {
		t.Fatalf("RunGoldTest: %v", err)
	}
	// author 2 < 50 → diperluas crossref → gold = 3 artikel (g1,g2,g3).
	if rep.GoldSource != "author+crossref" || !rep.Extended || !rep.SampleTipis {
		t.Errorf("source/extended/tipis = %q/%v/%v — mau author+crossref/true/true",
			rep.GoldSource, rep.Extended, rep.SampleTipis)
	}
	// g1: 3 kw author (incl. sintetis "virus zika dengue" utk diagnosis O-A) ·
	// g2: 1 · g3: 1 crossref → gold_kw = 5.
	if rep.Overall.Articles != 3 || rep.Overall.GoldKW != 5 {
		t.Errorf("overall articles/gold = %d/%d — mau 3/5", rep.Overall.Articles, rep.Overall.GoldKW)
	}
	if rep.SnapshotVersion != 1 {
		t.Errorf("snapshot = v%d — mau v1", rep.SnapshotVersion)
	}
	// Metrik wajar: gold-keyword yg muncul dlm teks sendiri harus ke-capture
	// minimal sebagian (fuzzy + stopword) — P@8/R dlm (0,1] utk sample ini.
	if rep.Overall.MatchedGold == 0 {
		t.Errorf("matched_gold = 0 — teks g1/g2 sengaja memuat kata gold, mau ≥1 (R>0); report:\n%s", rep)
	}
	for _, g := range append(rep.Groups, rep.Overall) {
		if g.Precision < 0 || g.Precision > 1 || g.Recall < 0 || g.Recall > 1 || g.F1 < 0 || g.F1 > 1 {
			t.Errorf("metrik %s di luar [0,1]: P=%.3f R=%.3f F1=%.3f", g.Lang, g.Precision, g.Recall, g.F1)
		}
	}
	// Per bahasa: id (g1,g2) + en (g3).
	langs := map[string]bool{}
	for _, g := range rep.Groups {
		langs[g.Lang] = true
	}
	if !langs[LanguageID] || !langs[LanguageEN] {
		t.Errorf("groups = %v — mau id & en", langs)
	}
	// Determinisme: run ulang → tabel identik.
	rep2, err := RunGoldTest(st, dir)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if rep.String() != rep2.String() {
		t.Errorf("tabel ke-2 beda — tak deterministik:\n%s\n%s", rep.String(), rep2.String())
	}
	// String memuat metrik penting utk review (termasuk blok O-A).
	out := rep.String()
	for _, want := range []string{"GOLD-TEST", "P@8", "ALL", "author+crossref", "CATATAN: sampel",
		"STRICT", "MISS DIAGNOSIS", "kategori:"} {
		if !strings.Contains(out, want) {
			t.Errorf("String() tanpa %q:\n%s", want, out)
		}
	}
	// O-A invarian: strict ≤ fuzzy di semua group (metrik lama tak berubah).
	for _, g := range append(rep.Groups, rep.Overall) {
		if g.MatchedGoldStrict > g.MatchedGold || g.MatchedPredStrict > g.MatchedPred {
			t.Errorf("strict (gold %d/pred %d) > fuzzy (gold %d/pred %d) — invarian rusak utk %s",
				g.MatchedGoldStrict, g.MatchedPredStrict, g.MatchedGold, g.MatchedPred, g.Lang)
		}
	}
	// O-A diagnosis: gold sintetis "virus zika dengue" — 3 token tak pernah
	// ada di korpus (fuzzy & strict miss) → kategori "vocab" dijamin.
	if len(rep.Misses) == 0 {
		t.Fatalf("Misses kosong — gold 'virus zika dengue' harus miss [vocab]; report:\n%s", rep)
	}
	whitelist := map[string]bool{"bentuk": true, "stopword": true, "vocab": true,
		"tak-diteks": true, "peringkat": true}
	adaVocab := false
	for _, m := range rep.Misses {
		if !whitelist[m.Kategori] {
			t.Errorf("kategori miss %q di luar whitelist", m.Kategori)
		}
		if m.Gold == "" || m.Record == "" || m.Pred == "" {
			t.Errorf("baris miss tak lengkap: %+v", m)
		}
		if m.Gold == "virus zika dengue" && m.Kategori == "vocab" {
			adaVocab = true
		}
	}
	if !adaVocab {
		t.Errorf("gold 'virus zika dengue' harus tercatat sbg miss [vocab]; misses=%+v", rep.Misses)
	}
}

// TestGoldTanpaSnapshot — error jelas (paksa urutan: pass2 dulu).
func TestGoldTanpaSnapshot(t *testing.T) {
	st := seedGoldDB(t)
	if _, err := RunGoldTest(st, filepath.Join(t.TempDir(), "kosong")); err == nil ||
		!strings.Contains(err.Error(), "pass 2") {
		t.Errorf("tanpa snapshot = %v — mau error suruh pass 2", err)
	}
}

// TestGoldSnapshotBasi — korpus tumbuh tanpa pass2 ulang → error (angka
// tak boleh berdasar snapshot basi — validitas #1).
func TestGoldSnapshotBasi(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keywords")
	st := seedGoldDB(t)
	if _, err := RunTFIDF(st, dir, nil); err != nil {
		t.Fatalf("RunTFIDF: %v", err)
	}
	// Tambah artikel TANPA pass2 ulang.
	if err := st.UpsertArticle(storage.ArticleRow{
		JournalID: 1, RecordID: "g9", Title: "Baru", ArticleURL: "https://j.test/9",
		Language: LanguageID, KeywordsJSON: "[]", LicenseStatus: LicenseUnknown,
		Source: storage.ArticleSourceOAI, IsComplete: true,
	}); err != nil {
		t.Fatalf("tambah: %v", err)
	}
	if _, err := RunGoldTest(st, dir); err == nil || !strings.Contains(err.Error(), "basi") {
		t.Errorf("snapshot basi = %v — mau error 'basi'", err)
	}
}
