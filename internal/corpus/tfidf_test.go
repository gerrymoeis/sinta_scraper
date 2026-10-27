package corpus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"sinta-scraper/internal/storage"
	"strings"
	"testing"
)

// TestTokenizeNGrams — lower+strip · digit murni dibuang · stopword dibuang
// SEBELUM join ("quality of life" → bigram "quality life").
func TestTokenizeNGrams(t *testing.T) {
	toks := tokenize("Stunting di Balita 2025 — Analisis!")
	want := map[string]bool{"stunting": true, "di": true, "balita": true, "analisis": true}
	if len(toks) != 4 {
		t.Fatalf("tokenize = %v — mau 4 token (2025 digit & em-dash dibuang; 'di' disaring di ngrams)", toks)
	}
	for _, tok := range toks {
		if !want[tok] {
			t.Errorf("token tak terduga: %q", tok)
		}
	}
	// EN: stopword "of/the" dibuang → frasa tetap jadi bigram.
	counts := ngrams("The quality of life of patients", LanguageEN)
	if _, ok := counts["quality life"]; !ok {
		t.Errorf("bigram 'quality life' hilang: %v", counts)
	}
	if _, ok := counts["of"]; ok {
		t.Error("unigram stopword 'of' harusnya dibuang")
	}
	if _, ok := counts["the"]; ok {
		t.Error("unigram stopword 'the' harusnya dibuang")
	}
	// ID: stopword per bahasa artikel.
	counts = ngrams("Analisis stunting pada balita", LanguageID)
	if _, ok := counts["pada"]; ok {
		t.Error("'pada' = stopword id — harusnya dibuang")
	}
	if _, ok := counts["stunting balita"]; !ok && counts["analisis stunting"] == 0 {
		t.Errorf("bigram non-stopword hilang: %v", counts)
	}
}

// TestTopTFIDFDeterministik — sort skor desc + tie abc · top-k cap · token
// asing (tak dikenal IDF) dilewati.
func TestTopTFIDFDeterministik(t *testing.T) {
	idf := map[string]float64{"a": 2, "b": 2, "c": 1}
	counts := map[string]int{"a": 1, "b": 1, "c": 5, "asing": 9}
	// a & b skor sama (1×2) → tie-break abc; c=5; asing tak ada idf → skip.
	got := topTFIDF(counts, idf, 8)
	if len(got) != 3 {
		t.Fatalf("top = %v — mau 3 (asing dilewati)", got)
	}
	if got[0].Kw != "c" || got[1].Kw != "a" || got[2].Kw != "b" {
		t.Errorf("urutan = %v/%v/%v — mau c,a,b (skor desc, tie abc)", got[0].Kw, got[1].Kw, got[2].Kw)
	}
	if got[0].Score != 5 {
		t.Errorf("skor c = %v — mau 5", got[0].Score)
	}
	// top-k cap.
	if top := topTFIDF(counts, idf, 2); len(top) != 2 {
		t.Errorf("cap k=2 = %d baris", len(top))
	}
}

// TestRunTFIDF — tag kandidat · author tak ditimpa · snapshot v1 · rerun
// idempoten (reused) · korpus tumbuh → v2.
func TestRunTFIDF(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keywords")
	st := openTFIDFDB(t)

	rep, err := RunTFIDF(st, dir, nil)
	if err != nil {
		t.Fatalf("RunTFIDF: %v", err)
	}
	if rep.Docs != 3 || rep.Candidates != 2 || rep.Tagged != 2 || rep.NoSignal != 0 {
		t.Errorf("docs/cand/tagged/nosignal = %d/%d/%d/%d — mau 3/2/2/0",
			rep.Docs, rep.Candidates, rep.Tagged, rep.NoSignal)
	}
	if rep.SnapshotVersion != 1 || rep.SnapshotReused {
		t.Errorf("snapshot = v%d reused=%v — mau v1 barubaru", rep.SnapshotVersion, rep.SnapshotReused)
	}
	if !rep.Verified {
		t.Errorf("Verified = false (%s)", rep.VerifyMsg)
	}

	// Snapshot file ada + shape benar.
	b, err := os.ReadFile(filepath.Join(dir, "idf-v1.json"))
	if err != nil {
		t.Fatalf("snapshot v1 hilang: %v", err)
	}
	var snap IDFSnapshot
	if err := json.Unmarshal(b, &snap); err != nil || snap.Version != 1 || snap.Docs != 3 || len(snap.IDF) == 0 {
		t.Fatalf("snapshot shape: v%d docs%d %dtoken err=%v", snap.Version, snap.Docs, len(snap.IDF), err)
	}

	// Kandidat ter-tag src=tfidf; author TIDAK ditimpa.
	rows, _ := st.Articles(1)
	byID := map[string]storage.ArticleRow{}
	for _, r := range rows {
		byID[r.RecordID] = r
	}
	kwA1 := byID["a1"].KeywordsJSON
	if !strings.Contains(kwA1, `"src":"tfidf"`) {
		t.Errorf("a1 keywords = %q — mau tfidf", kwA1)
	}
	if !strings.Contains(byID["a1"].Provenance, `"tfidf_version":1`) {
		t.Errorf("a1 provenance = %q — mau tfidf_version 1", byID["a1"].Provenance)
	}
	if !strings.Contains(byID["ak"].KeywordsJSON, `"src":"author"`) {
		t.Errorf("ak (author) ditimpa: %q", byID["ak"].KeywordsJSON)
	}

	// ── Rerun: idempoten — semua sudah ter-tag, snapshot reused ──
	rep2, err := RunTFIDF(st, dir, nil)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if rep2.Tagged != 0 || !rep2.SnapshotReused || rep2.SnapshotVersion != 1 {
		t.Errorf("rerun tagged/reused = %d/%v v%d — mau 0/true v1", rep2.Tagged, rep2.SnapshotReused, rep2.SnapshotVersion)
	}

	// ── Korpus tumbuh → snapshot v2 ──
	if err := st.UpsertArticle(storage.ArticleRow{
		JournalID: 1, RecordID: "a4", Title: "Gizi buruk pada bayi di Indonesia",
		ArticleURL: "https://j.test/4", AbstractID: "Penelitian gizi buruk pada bayi.",
		Language: LanguageID, KeywordsJSON: "[]", LicenseStatus: LicenseUnknown,
		Source: storage.ArticleSourceOAI, IsComplete: true,
	}); err != nil {
		t.Fatalf("tambah artikel: %v", err)
	}
	rep3, err := RunTFIDF(st, dir, nil)
	if err != nil {
		t.Fatalf("run ke-3: %v", err)
	}
	if rep3.SnapshotVersion != 2 || rep3.SnapshotReused || rep3.Tagged != 1 {
		t.Errorf("tumbuh = v%d reused=%v tagged=%d — mau v2/false/1",
			rep3.SnapshotVersion, rep3.SnapshotReused, rep3.Tagged)
	}
}

// openTFIDFDB = DB dgn 3 artikel jurnal 1: a1 & a2 kandidat, ak author.
func openTFIDFDB(t *testing.T) *storage.Store {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kw.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: 1, Name: "Jurnal Uji", SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}
	arts := []storage.ArticleRow{
		{JournalID: 1, RecordID: "a1", Title: "Analisis stunting pada balita di Kabupaten X",
			ArticleURL: "https://j.test/1",
			AbstractID: "Penelitian ini menganalisis stunting pada balita dengan data survei dan hasilnya menunjukkan penurunan.",
			Language:   LanguageID, KeywordsJSON: "[]", LicenseStatus: LicenseUnknown,
			Source: storage.ArticleSourceOAI, IsComplete: true},
		{JournalID: 1, RecordID: "a2", Title: "Quality of life among stroke patients",
			ArticleURL: "https://j.test/2",
			AbstractEN: "This study analyzes quality of life among stroke patients using survey data from hospitals.",
			Language:   LanguageEN, KeywordsJSON: "[]", LicenseStatus: LicenseUnknown,
			Source: storage.ArticleSourceOAI, IsComplete: true},
		{JournalID: 1, RecordID: "ak", Title: "Ekologi nyamuk",
			ArticleURL:    "https://j.test/3",
			Language:      LanguageID,
			KeywordsJSON:  `[{"kw":"ekologi","src":"author","lang":"id"}]`,
			LicenseStatus: LicenseUnknown, Source: storage.ArticleSourceOAI, IsComplete: true},
	}
	for _, a := range arts {
		if err := st.UpsertArticle(a); err != nil {
			t.Fatalf("UpsertArticle %s: %v", a.RecordID, err)
		}
	}
	return st
}

// TestSnapshotRusak — file corrupt = ERROR (rebuild diam-diam = IDF diam-
// diam berubah — validitas #1).
func TestSnapshotRusak(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keywords")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "idf-v3.json"), []byte("rusak!"), 0644); err != nil {
		t.Fatal(err)
	}
	st := openTFIDFDB(t)
	if _, err := RunTFIDF(st, dir, nil); err == nil || !strings.Contains(err.Error(), "JSON valid") {
		t.Errorf("snapshot rusak = %v — mau error jelas", err)
	}
}

// TestMergeProvenance — pass1 provenance utuh + kunci pass2 nempel.
func TestMergeProvenance(t *testing.T) {
	got := mergeProvenance(`{"oai":"x","license_src":"oai"}`, map[string]any{"tfidf_version": 2})
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("merge rusak: %v", err)
	}
	if m["oai"] != "x" || m["license_src"] != "oai" || m["tfidf_version"] != float64(2) {
		t.Errorf("merge = %v — mau lama utuh + tfidf_version", m)
	}
}
