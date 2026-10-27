package corpus

// ==== tfidf.go — pass 2 keyword TF-IDF (doc 43 §5.3 pass-2 + §5.2 snapshot
// — spec approve 14 Okt; fase C4) ====
//
// 0 GET, murni CPU. Kumpul teks (title + abstrak id/en) utk artikel dgn
// keyword KOSONG saja (author/crossref sudah ada TIDAK ditimpa) → n-gram
// 1–3 (stopword id/en by language §4.1) → DF/IDF atas SELURUH korpus →
// snapshot data/keywords/idf-v{N}.json (sumber determinisme §5.2) → skor
// tf×idf → top-k → UpdateArticleKeywords(src=tfidf, provenance versi).

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"sinta-scraper/internal/storage"
)

const (
	// TFIDFTopK = k keyword TF-IDF per artikel (tetap dlm kode §5.3 pass-2).
	TFIDFTopK = 8
	// DefaultKeywordsDir = folder snapshot IDF global (relatif cwd).
	DefaultKeywordsDir = "data/keywords"
	// MinTokenLen = panjang minimal token (1 huruf = noise; digit murni dibuang).
	MinTokenLen = 2
	// MaxNGram = maksimum panjang n-gram.
	MaxNGram = 3
)

// IDFSnapshot = file idf-v{N}.json (§5.2: {token: idf} + meta versi/dokumen).
// Fingerprint = checksum isi teks korpus (urut deterministik) — "docs sama"
// saja TIDAK cukup utk reuse (validitas #1: korpus beda isi = IDF beda).
type IDFSnapshot struct {
	Version     int                `json:"version"`
	Docs        int                `json:"docs"`
	Fingerprint string             `json:"fingerprint"`
	BuiltAt     string             `json:"built_at"`
	IDF         map[string]float64 `json:"idf"`
}

// TFIDFReport = ringkasan jujur pass 2.
type TFIDFReport struct {
	Docs            int // korpus (artikel) ikut DF/IDF
	Candidates      int // artikel tanpa keyword (kandidat tag)
	Tagged          int // artikel ditulis src=tfidf
	NoSignal        int // kandidat tanpa n-gram skorable (jujur — tetap "[]")
	SnapshotVersion int
	SnapshotReused  bool
	Verified        bool
	VerifyMsg       string
}

// RunTFIDF = orkestrator pass 2. Error hanya fatal (db/snapshot rusak).
func RunTFIDF(store *storage.Store, dir string, logf func(string, ...any)) (*TFIDFReport, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	rep := &TFIDFReport{}
	all, err := store.AllArticles()
	if err != nil {
		return nil, fmt.Errorf("baca korpus: %w", err)
	}
	rep.Docs = len(all)
	if rep.Docs == 0 {
		return nil, fmt.Errorf("korpus kosong — jalankan pass 1 (-stages=corpus) dulu")
	}
	fp := corpusFingerprint(all)

	snap, reused, err := loadOrBuildSnapshot(dir, all, fp, logf)
	if err != nil {
		return nil, err
	}
	rep.SnapshotVersion = snap.Version
	rep.SnapshotReused = reused

	for i := range all {
		a := &all[i]
		if !keywordsEmpty(a.KeywordsJSON) {
			continue // author/crossref sudah ada — TIDAK ditimpa (§5.3)
		}
		rep.Candidates++
		text := strings.TrimSpace(strings.Join([]string{a.Title, a.AbstractID, a.AbstractEN}, " "))
		scores := topTFIDF(ngrams(text, a.Language), snap.IDF, TFIDFTopK)
		if len(scores) == 0 {
			rep.NoSignal++
			continue // teks terlalu pendek / semua stopword — jujur tetap "[]"
		}
		kw := tfidfKeywordsJSON(scores, a.Language)
		prov := mergeProvenance(a.Provenance, map[string]any{"tfidf_version": snap.Version})
		if err := store.UpdateArticleKeywords(a.JournalID, a.RecordID, kw, prov); err != nil {
			return nil, err
		}
		rep.Tagged++
	}

	total := rep.Tagged + rep.NoSignal
	rep.Verified = total == rep.Candidates
	rep.VerifyMsg = fmt.Sprintf("docs %d | kandidat %d | tagged %d | no_signal %d | snapshot v%d (reused=%v)",
		rep.Docs, rep.Candidates, rep.Tagged, rep.NoSignal, snap.Version, reused)
	logf("[corpus-tfidf] %s → VERIFIED=%v", rep.VerifyMsg, rep.Verified)
	return rep, nil
}

// ---- tokenizer + n-gram (deterministik) ----

// tokenize = lower → strip non-alnum → token ≥2 huruf, non-digit murni
// (tahun "2025" bukan keyword riset — keputusan C4 #2).
func tokenize(text string) []string {
	raw := strings.Fields(strings.ToLower(stripNonAlnum(text)))
	out := make([]string, 0, len(raw))
	for _, tok := range raw {
		if len(tok) < MinTokenLen || allDigits(tok) {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// ngrams = token non-stopword (by bahasa artikel §4.1) → n-gram 1..MaxNGram
// (spasi-pemisah). "quality of life" → [quality life] (bigram) — stopword
// dibuang SEBELUM penggabungan (sinyal frasa tetap ada).
func ngrams(text, lang string) map[string]int {
	stop := stopwordSetByLang(lang)
	toks := tokenize(text)
	kept := make([]string, 0, len(toks))
	for _, t := range toks {
		if !stop[t] {
			kept = append(kept, t)
		}
	}
	counts := map[string]int{}
	for size := 1; size <= MaxNGram; size++ {
		for i := 0; i+size <= len(kept); i++ {
			counts[strings.Join(kept[i:i+size], " ")]++
		}
	}
	return counts
}

// TokenScore = 1 kandidat keyword TF-IDF (kw = n-gram; Score utk sort).
type TokenScore struct {
	Kw    string
	Score float64
}

// topTFIDF = skor tf×idf → urut skor desc, tie token asc (deterministik) →
// top-k. Token di luar snapshot IDF dilewati (idf tak dikenal = 0 sinyal).
func topTFIDF(counts map[string]int, idf map[string]float64, k int) []TokenScore {
	var all []TokenScore
	for tok, tf := range counts {
		id, ok := idf[tok]
		if !ok {
			continue
		}
		all = append(all, TokenScore{Kw: tok, Score: float64(tf) * id})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		return all[i].Kw < all[j].Kw
	})
	if len(all) > k {
		all = all[:k]
	}
	return all
}

// tfidfKeywordsJSON = bentuk keywords_json (sama dgn KeywordJSON — shape
// seragam §5.2): [{"kw","src":"tfidf","lang"}].
func tfidfKeywordsJSON(scores []TokenScore, lang string) string {
	kws := make([]map[string]string, 0, len(scores))
	for _, s := range scores {
		kws = append(kws, map[string]string{"kw": s.Kw, "src": "tfidf", "lang": lang})
	}
	b, _ := json.Marshal(kws)
	return string(b)
}

// ---- snapshot IDF ----

// loadOrBuildSnapshot = reuse bila docs+fingerprint cocok; kalau tidak →
// build versi berikutnya + tulis file (§5.2). Snapshot rusak = ERROR (jangan
// diam-diam rebuild — IDF beda = keyword beda, validitas #1).
func loadOrBuildSnapshot(dir string, articles []storage.ArticleRow, fp string,
	logf func(string, ...any)) (IDFSnapshot, bool, error) {

	docs := len(articles)
	prev, prevErr := readNewestSnapshot(dir)
	if prevErr != nil {
		return IDFSnapshot{}, false, prevErr
	}
	if prev != nil && prev.Docs == docs && prev.Fingerprint == fp {
		return *prev, true, nil // determinisme: korpus sama → IDF sama
	}

	idf, err := computeIDF(articles)
	if err != nil {
		return IDFSnapshot{}, false, err
	}
	ver := 1
	if prev != nil {
		ver = prev.Version + 1 // korpus berubah → versi+1 (jujur)
	}
	snap := IDFSnapshot{
		Version: ver, Docs: docs, Fingerprint: fp,
		BuiltAt: nowUTC(), IDF: idf,
	}
	if err := writeSnapshot(dir, snap); err != nil {
		return IDFSnapshot{}, false, err
	}
	logf("[corpus-tfidf] snapshot BARU idf-v%d.json (docs %d, %d token)",
		ver, docs, len(idf))
	return snap, false, nil
}

// computeIDF = DF per n-gram (dokumen mengandung) → idf = ln(1 + N/(1+df))
// (smooth — selalu >0, deterministik float64).
func computeIDF(articles []storage.ArticleRow) (map[string]float64, error) {
	n := float64(len(articles))
	if n == 0 {
		return nil, fmt.Errorf("0 dokumen")
	}
	df := map[string]int{}
	for _, a := range articles {
		text := strings.TrimSpace(strings.Join([]string{a.Title, a.AbstractID, a.AbstractEN}, " "))
		for tok := range ngrams(text, a.Language) {
			df[tok]++
		}
	}
	idf := make(map[string]float64, len(df))
	for tok, d := range df {
		idf[tok] = math.Log(1 + n/(1+float64(d)))
	}
	return idf, nil
}

// readNewestSnapshot = file idf-v{N}.json dgn N terbesar ("" = belum ada).
func readNewestSnapshot(dir string) (*IDFSnapshot, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("baca folder snapshot: %w", err)
	}
	best := -1
	var bestPath string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "idf-v") || !strings.HasSuffix(name, ".json") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "idf-v"), ".json"))
		if err == nil && n > best {
			best, bestPath = n, filepath.Join(dir, name)
		}
	}
	if bestPath == "" {
		return nil, nil
	}
	b, err := os.ReadFile(bestPath)
	if err != nil {
		return nil, fmt.Errorf("baca snapshot %s: %w", bestPath, err)
	}
	var snap IDFSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("snapshot %s bukan JSON valid (jangan rebuild diam-diam — periksa manual): %w", bestPath, err)
	}
	if snap.Version != best || len(snap.IDF) == 0 {
		return nil, fmt.Errorf("snapshot %s tidak konsisten (version=%d, %d token) — periksa manual", bestPath, snap.Version, len(snap.IDF))
	}
	return &snap, nil
}

// writeSnapshot = tulis idf-v{N}.json (MarshalIndent — stabil & auditable).
func writeSnapshot(dir string, snap IDFSnapshot) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("buat folder snapshot: %w", err)
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	p := filepath.Join(dir, fmt.Sprintf("idf-v%d.json", snap.Version))
	if err := os.WriteFile(p, b, 0644); err != nil {
		return fmt.Errorf("tulis snapshot: %w", err)
	}
	return nil
}

// corpusFingerprint = checksum FNV-64 teks korpus (urut AllArticles —
// deterministik). Idx+judul+abstrak → perubahan isi teks terdeteksi walau
// jumlah dokumen sama.
func corpusFingerprint(articles []storage.ArticleRow) string {
	h := fnv.New64a()
	for _, a := range articles {
		fmt.Fprintf(h, "%d|%s|%s|%s|%s\n", a.JournalID, a.RecordID, a.Title, a.AbstractID, a.AbstractEN)
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

// keywordsEmpty = keywords_json "[]" (tanpa keyword — kandidat pass 2).
func keywordsEmpty(kwJSON string) bool {
	var arr []any
	if err := json.Unmarshal([]byte(kwJSON), &arr); err != nil {
		return false // bukan JSON rapi — jangan sentuh (jujur)
	}
	return len(arr) == 0
}

// mergeProvenance = gabung provenance pass 1 + kunci pass 2 (versi snapshot)
// — UpdateArticleKeywords menimpa kolom provenance, jadi gabung dulu.
func mergeProvenance(prev string, add map[string]any) string {
	m := map[string]any{}
	if prev != "" {
		_ = json.Unmarshal([]byte(prev), &m) // rusak → mulai kosong (dgn catatan via add)
	}
	for k, v := range add {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
