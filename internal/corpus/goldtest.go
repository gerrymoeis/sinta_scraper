package corpus

// ==== goldtest.go — tool gold-test keyword (doc 43 §5.6 — spec approve
// 14 Okt; fase C5 + O-A 23 Okt) ====
//
// 0 GET, baca-saja. Sampel artikel dgn keyword AUTHOR (gold) → simulasi
// TF-IDF dgn teks & snapshot IDF YANG SAMA dgn pass 2 → cocokkan top-k vs
// gold (fuzzy: case-fold + light stemming) → metrik P@8/recall/F1 per
// bahasa (id/en + mixed/unknown jujur) → user review lolos/tidak;
// threshold ditentukan dari hasil, BUKAN di-hardcode.
//
// O-A (23 Okt, §6.0 analisa opsi): METRIK STRICT berdampingan fuzzy —
// exact token-set TANPA stemming (Δ dgn fuzzy = match krn stemming) +
// DAFTAR MISS per-item dgn kategori diagnosis: bentuk | stopword | vocab
// | peringkat. Data diagnosis ini = bahan tuning BERBASIS FAKTA (O-B/C/D
// ditolak dulu — spekulasi tanpa diagnosis).

import (
	"encoding/json"
	"fmt"
	"strings"

	"sinta-scraper/internal/storage"
)

// GoldMinSample = jumlah minimum artikel gold (author) utk sample sendiri;
// di bawah ini → perluas dgn Crossref subject (§5.6 — juga 0 GET).
const GoldMinSample = 50

// missCapPerKategori = batas baris miss/categori utk String() default (G13:
// korpus penuh = 11.039 baris → review macet; penuh lewat StringAll/-miss-all).
const missCapPerKategori = 10

// KeywordEntry = 1 isi keywords_json (shape §5.2).
type KeywordEntry struct {
	Kw   string `json:"kw"`
	Src  string `json:"src"`
	Lang string `json:"lang"`
}

// GoldGroup = agregat metrik 1 bahasa (micro-average — μ matched / μ total).
type GoldGroup struct {
	Lang        string
	Articles    int
	GoldKW      int // total keyword gold (dari author/crossref)
	PredKW      int // total keyword prediksi TF-IDF (≤8/artikel)
	MatchedGold int
	MatchedPred int
	Precision   float64 // P@8
	Recall      float64
	F1          float64
	// O-A strict: token-set PERSIS tanpa lightStem. Invarian: strict ≤ fuzzy
	// (selisih = match yg hanya lolos krn stemming).
	MatchedGoldStrict int
	MatchedPredStrict int
	PrecisionStrict   float64
	RecallStrict      float64
	F1Strict          float64
}

// GoldMiss = 1 gold-kw TIDAK match strict → kategori penyebab (O-A).
type GoldMiss struct {
	Journal  int64  // jurnal pemilik
	Record   string // record_id/OAI id (dipotong saat cetak)
	Lang     string
	Gold     string // keyword gold asli (bentuk mentah)
	Kategori string // bentuk|stopword|vocab|tak-diteks|peringkat (whitelist diagnoseGoldMiss)
	Pred     string // top-8 prediksi artikel — bahan banding per-item
}

// GoldReport = hasil gold-test utk review user.
type GoldReport struct {
	SnapshotVersion int
	SnapshotDocs    int
	GoldSource      string // author | author+crossref
	Extended        bool   // diperluas Crossref (author < GoldMinSample)
	SampleTipis     bool   // total gold masih < GoldMinSample (angka apa adanya)
	Groups          []GoldGroup
	Overall         GoldGroup
	Misses          []GoldMiss // O-A: gold strict-miss, urut artikel — diagnosis per-item
}

// RunGoldTest = orkestrator gold-test. Error hanya kondisi yg bikin angka
// tak valid (tanpa snapshot · snapshot basi · tanpa sampel gold).
func RunGoldTest(store *storage.Store, dir string) (*GoldReport, error) {
	all, err := store.AllArticles()
	if err != nil {
		return nil, fmt.Errorf("baca korpus: %w", err)
	}
	snap, err := readNewestSnapshot(dir)
	if err != nil {
		return nil, err
	}
	if snap == nil {
		return nil, fmt.Errorf("belum ada snapshot IDF di %s — jalankan pass 2 dulu (-stages=corpus)", dir)
	}
	// Snapshot harus mewakili korpus yg dites (validitas #1) — fingerprint
	// nangkap isi teks beda walau jumlah dokumen sama.
	if fp := corpusFingerprint(all); snap.Docs != len(all) || snap.Fingerprint != fp {
		return nil, fmt.Errorf("snapshot IDF v%d basi (docs %d→%d / fingerprint beda) — jalankan ulang -stages=corpus utk refresh pass 2",
			snap.Version, snap.Docs, len(all))
	}

	gold, source, extended := pickGold(all)
	if len(gold) == 0 {
		return nil, fmt.Errorf("tanpa sampel gold (0 artikel dgn author/crossref subject) — gold-test mustahil")
	}

	rep := &GoldReport{
		SnapshotVersion: snap.Version,
		SnapshotDocs:    snap.Docs,
		GoldSource:      source,
		Extended:        extended,
		SampleTipis:     len(gold) < GoldMinSample,
	}
	byLang := map[string]*GoldGroup{}
	order := []string{LanguageID, LanguageEN, LanguageMixed, LanguageUnknown}

	for _, a := range gold {
		text := strings.TrimSpace(strings.Join([]string{a.Title, a.AbstractID, a.AbstractEN}, " "))
		counts := ngrams(text, a.Language) // n-gram artikel INI — utk pred & diagnosis "tak-diteks"
		pred := topTFIDF(counts, snap.IDF, TFIDFTopK)

		// Pred: kandidat top-k (string asli utk laporan + token utk match).
		predKws := make([]string, 0, len(pred))
		predSets := make([][]string, 0, len(pred))
		for _, p := range pred {
			predKws = append(predKws, p.Kw)
			predSets = append(predSets, tokenize(p.Kw))
		}
		// Gold: kata asli utk baris diagnosis + token utk match (index-sejajar).
		items := goldItems(a.KeywordsJSON)
		goldSets := make([][]string, len(items))
		for i, it := range items {
			goldSets[i] = it.toks
		}

		g := byLang[a.Language]
		if g == nil {
			g = &GoldGroup{Lang: a.Language}
			byLang[a.Language] = g
		}
		g.Articles++
		g.GoldKW += len(goldSets)
		g.PredKW += len(predSets)
		for _, p := range predSets {
			if anyPhraseMatch(p, goldSets) {
				g.MatchedPred++
			}
			if anyPhraseMatchStrict(p, goldSets) {
				g.MatchedPredStrict++
			}
		}
		stop := stopwordSetByLang(a.Language)
		for i, gs := range goldSets {
			fuzzy := anyPhraseMatch(gs, predSets)
			strict := anyPhraseMatchStrict(gs, predSets)
			if fuzzy {
				g.MatchedGold++
			}
			if strict {
				g.MatchedGoldStrict++
				continue
			}
			// O-A: gold miss → diagnosis kategori penyebab (bahan review).
			rep.Misses = append(rep.Misses, GoldMiss{
				Journal:  a.JournalID,
				Record:   a.RecordID,
				Lang:     a.Language,
				Gold:     items[i].raw,
				Kategori: diagnoseGoldMiss(gs, fuzzy, predSets, counts, stop, snap.IDF),
				Pred:     strings.Join(predKws, " · "),
			})
		}
	}

	rep.Overall = GoldGroup{Lang: "ALL"}
	for _, lang := range order {
		if g, ok := byLang[lang]; ok {
			finishGroup(g)
			rep.Groups = append(rep.Groups, *g)
			addGroup(&rep.Overall, g)
		}
	}
	finishGroup(&rep.Overall)
	return rep, nil
}

// pickGold = sampel author utk dulu; < GoldMinSample → perluas Crossref.
func pickGold(all []storage.ArticleRow) (gold []storage.ArticleRow, source string, extended bool) {
	var author, cross []storage.ArticleRow
	for _, a := range all {
		hasAuthor, hasCross := false, false
		for _, e := range parseKeywordEntries(a.KeywordsJSON) {
			switch e.Src {
			case "author":
				if strings.TrimSpace(e.Kw) != "" {
					hasAuthor = true
				}
			case "crossref":
				if strings.TrimSpace(e.Kw) != "" {
					hasCross = true
				}
			}
		}
		switch {
		case hasAuthor:
			author = append(author, a)
		case hasCross:
			cross = append(cross, a)
		}
	}
	gold, source = author, "author"
	if len(author) < GoldMinSample && len(cross) > 0 {
		gold = append(append([]storage.ArticleRow{}, author...), cross...)
		source = "author+crossref"
		extended = true
	}
	return gold, source, extended
}

// parseKeywordEntries = parse keywords_json (rusak/kosong → nil — jujur).
func parseKeywordEntries(kwJSON string) []KeywordEntry {
	var entries []KeywordEntry
	if err := json.Unmarshal([]byte(kwJSON), &entries); err != nil {
		return nil
	}
	return entries
}

// goldKw = 1 keyword gold: kata asli utk baris diagnosis + token utk match.
type goldKw struct {
	raw  string
	toks []string
}

// goldItems = parse keywords_json → goldKw (yang token-nya kosong dilewati —
// jujur; raw = bentuk mentah utk laporan miss O-A).
func goldItems(kwJSON string) []goldKw {
	var out []goldKw
	for _, e := range parseKeywordEntries(kwJSON) {
		if tok := tokenize(e.Kw); len(tok) > 0 {
			out = append(out, goldKw{raw: strings.TrimSpace(e.Kw), toks: tok})
		}
	}
	return out
}

// anyPhraseMatch = ∃ pasangan dgn phraseMatch.
func anyPhraseMatch(a []string, others [][]string) bool {
	for _, b := range others {
		if phraseMatch(a, b) {
			return true
		}
	}
	return false
}

// anyPhraseMatchStrict = ∃ pasangan dgn phraseMatchStrict (O-A).
func anyPhraseMatchStrict(a []string, others [][]string) bool {
	for _, b := range others {
		if phraseMatchStrict(a, b) {
			return true
		}
	}
	return false
}

// phraseMatchStrict = metrik STRICT O-A: kontainment token-set PERSIS —
// tanpa lightStem (urutan tetap bebas — himpunan topik). Invarian: strict
// ⟹ fuzzy (token persis = stem persis pasti cocok) — selisih kedua metrik
// = match yg hanya lolos krn stemming ("studies" vs "study": fuzzy ✓ strict ✗).
func phraseMatchStrict(a, b []string) bool {
	short, long := a, b
	if len(b) < len(a) {
		short, long = b, a
	}
	if len(short) == 0 || len(long) == 0 {
		return false
	}
	set := make(map[string]bool, len(long))
	for _, t := range long {
		set[t] = true
	}
	for _, t := range short {
		if !set[t] {
			return false
		}
	}
	return true
}

// diagnoseGoldMiss = kategori penyebab gold-kw strict-miss (O-A — diagnosis
// berbasis fakta korpus, urutan pemeriksaan):
//  1. bentuk     — lolos fuzzy (match thx lightStem; beda bentuk wajar)
//  2. stopword   — versi TANPA stopword-nya match → stopword itulah SATU-SATUNYA
//     blokan (ngrams membuangnya by-design)
//  3. vocab      — token non-stopword (mentah/stem) ∉ snapshot IDF → tak pernah
//     ada di korpus manapun → mustahil di-predict
//  4. tak-diteks — ∉ idf korpus TAPI... token ∉ counts artikel INI (judul+abstrak)
//     → kata absen dari teksnya sendiri → naikkan k TIDAK menolong utk token ini
//  5. peringkat  — ada di teks & korpus → kalah skor di luar top-8 → k menolong
//
// Urutan 2 sebelum 3: token stopword memang tak pernah ada di IDF (dibuang
// ngrams) — dicek lewat cabang "bersihkan dulu". Urutan 3 sebelum 4: absensi
// global (se-korpus) = fakta lebih kuat dari absensi lokal 1 artikel.
//
// counts = n-gram KUNCI artikel sendiri (size-1 ada utk tiap token kept) →
// counts[t] > 0 ⟺ token non-stopword t ada di teks artikel tsb.
func diagnoseGoldMiss(gs []string, fuzzyOK bool, predSets [][]string, counts map[string]int, stop map[string]bool, idf map[string]float64) string {
	if fuzzyOK {
		return "bentuk"
	}
	var clean []string
	adaStop := false
	for _, t := range gs {
		if stop[t] {
			adaStop = true
			continue
		}
		clean = append(clean, t)
	}
	// Semua-token-berhenti (gold degenerat) = match mustahil (pred tak pernah
	// memuat stopword) → juga kelas stopword.
	if adaStop && (len(clean) == 0 || anyPhraseMatch(clean, predSets)) {
		return "stopword"
	}
	for _, t := range clean {
		if _, ok := idf[t]; ok {
			continue
		}
		if _, ok := idf[lightStem(t)]; ok {
			continue
		}
		return "vocab"
	}
	for _, t := range clean {
		if counts[t] == 0 {
			return "tak-diteks"
		}
	}
	return "peringkat"
}

// phraseMatch = fuzzy utk kata kunci (deterministik): token-set PENDUKUNG
// sepenuhnya ada dlm panjang (per-token lightStem; urutan tak wajib — kata
// kunci = himpunan topik, bukan kalimat). "stunting" ⊆ "stunting balita" ✓
// · "gizi" vs "kesehatan" ✗.
func phraseMatch(a, b []string) bool {
	short, long := a, b
	if len(b) < len(a) {
		short, long = b, a
	}
	if len(short) == 0 || len(long) == 0 {
		return false
	}
	set := make(map[string]bool, len(long))
	for _, t := range long {
		set[lightStem(t)] = true
	}
	for _, t := range short {
		if !set[lightStem(t)] {
			return false
		}
	}
	return true
}

// lightStem = stemming ringan suffix EN+ID (deterministik, in-repo — §5.6
// "stemming ringan"). Urutan tetap; guard panjang anti over-strip.
// studies→study · results→result · tingkatkan→tingkat · gizinya→gizi.
// CATATAN: "penelitian" TIDAK di-strip "an" (sengaja — salah potong).
func lightStem(tok string) string {
	t := tok
	switch {
	case len(t) > 5 && strings.HasSuffix(t, "ies"):
		t = t[:len(t)-3] + "y" // studies → study
	case len(t) > 4 && strings.HasSuffix(t, "ied"):
		t = t[:len(t)-3] + "y" // died → die-ish (konsisten)
	case len(t) > 5 && strings.HasSuffix(t, "ing"):
		t = t[:len(t)-3] // running → runn (idem — kedua sisi sama)
	case len(t) > 4 && strings.HasSuffix(t, "ed"):
		t = t[:len(t)-2] // tested → test
	case len(t) > 6 && strings.HasSuffix(t, "kan"):
		t = t[:len(t)-3] // tingkatkan → tingkat (ID)
	case len(t) > 5 && strings.HasSuffix(t, "nya"):
		t = t[:len(t)-3] // gizinya → gizi (ID)
	case len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss"):
		t = t[:len(t)-1] // results → result · analisis → analisi (dua sisi sama)
	}
	return t
}

func finishGroup(g *GoldGroup) {
	if g.PredKW > 0 {
		g.Precision = float64(g.MatchedPred) / float64(g.PredKW)
		g.PrecisionStrict = float64(g.MatchedPredStrict) / float64(g.PredKW)
	}
	if g.GoldKW > 0 {
		g.Recall = float64(g.MatchedGold) / float64(g.GoldKW)
		g.RecallStrict = float64(g.MatchedGoldStrict) / float64(g.GoldKW)
	}
	if g.Precision+g.Recall > 0 {
		g.F1 = 2 * g.Precision * g.Recall / (g.Precision + g.Recall)
	}
	if g.PrecisionStrict+g.RecallStrict > 0 {
		g.F1Strict = 2 * g.PrecisionStrict * g.RecallStrict / (g.PrecisionStrict + g.RecallStrict)
	}
}

func addGroup(dst, src *GoldGroup) {
	dst.Articles += src.Articles
	dst.GoldKW += src.GoldKW
	dst.PredKW += src.PredKW
	dst.MatchedGold += src.MatchedGold
	dst.MatchedPred += src.MatchedPred
	dst.MatchedGoldStrict += src.MatchedGoldStrict
	dst.MatchedPredStrict += src.MatchedPredStrict
}

// String = tabel hasil utk review user — RINGKAS (G13): cap baris miss per
// kategori + catatan sisa; korpus penuh butuh StringAll (11rb+ baris).
func (r *GoldReport) String() string { return r.buildString(missCapPerKategori) }

// StringAll = idem String TANPA cap — SEMUA baris miss (cocok redirect ke
// file utk analisis menyeluruh; flag -miss-all cmd/goldtest).
func (r *GoldReport) StringAll() string { return r.buildString(-1) }

// buildString = inti cetak; capPerKat < 0 = tanpa cap, ≥0 = batas baris per
// kategori (ringkas + catatan sisa biar jumlah aslinya tetap jujur).
func (r *GoldReport) buildString(capPerKat int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== GOLD-TEST (0 GET, simulasi TF-IDF dgn snapshot v%d) ===\n", r.SnapshotVersion)
	fmt.Fprintf(&b, "gold: %s", r.GoldSource)
	if r.Extended {
		fmt.Fprintf(&b, " (author <%d → diperluas crossref)", GoldMinSample)
	}
	if r.SampleTipis {
		fmt.Fprintf(&b, "\nCATATAN: sampel < %d artikel — angka apa adanya (review manual)", GoldMinSample)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "%-8s %8s %8s %8s %11s %7s %7s %7s\n",
		"group", "articles", "gold_kw", "pred_kw", "matched", "P@8", "R", "F1")
	rows := make([]GoldGroup, 0, len(r.Groups)+1)
	rows = append(rows, r.Groups...)
	rows = append(rows, r.Overall)
	for _, g := range rows {
		fmt.Fprintf(&b, "%-8s %8d %8d %8d %5d/%-5d %7.3f %7.3f %7.3f\n",
			g.Lang, g.Articles, g.GoldKW, g.PredKW,
			g.MatchedPred, g.MatchedGold, g.Precision, g.Recall, g.F1)
	}

	// ── O-A: metrik STRICT berdampingan fuzzy (Δ = match krn stemming) ──
	fmt.Fprintf(&b, "\nSTRICT (token-set persis, tanpa stemming) — selisih dgn fuzzy di atas = match KARENA stemming:\n")
	fmt.Fprintf(&b, "%-8s %20s %9s %8s %8s\n", "group", "matched_strict(P/G)", "P_strict", "R_strict", "F1_strict")
	for _, g := range rows {
		fmt.Fprintf(&b, "%-8s %20s %9.3f %8.3f %8.3f\n", g.Lang,
			fmt.Sprintf("%d/%d", g.MatchedPredStrict, g.MatchedGoldStrict),
			g.PrecisionStrict, g.RecallStrict, g.F1Strict)
	}

	// ── O-A: diagnosis miss per-item (urut prioritas: actionable dulu) ──
	if len(r.Misses) == 0 {
		b.WriteString("\nMISS DIAGNOSIS: 0 gold-kw strict-miss — semua gold persis cocok ✓\n")
		return b.String()
	}
	katCount := map[string]int{}
	for _, m := range r.Misses {
		katCount[m.Kategori]++
	}
	fmt.Fprintf(&b, "\n=== MISS DIAGNOSIS (%d gold-kw TIDAK match strict dari %d gold) ===\n",
		len(r.Misses), r.Overall.GoldKW)
	fmt.Fprintf(&b, "ringkas: peringkat=%d · tak-diteks=%d · vocab=%d · stopword=%d · bentuk=%d\n",
		katCount["peringkat"], katCount["tak-diteks"], katCount["vocab"], katCount["stopword"], katCount["bentuk"])
	b.WriteString("kategori: bentuk=beda bentuk (lolos fuzzy) · stopword=token dibuang ngrams (by design) · vocab=tak pernah ada di korpus manapun · tak-diteks=kata absen dari judul+abstrak artikel ini (naikkan k tak menolong utk token itu) · peringkat=ada di teks & korpus, kalah skor di luar top-8 (naikkan k menolong)\n")
	for _, kat := range []string{"peringkat", "tak-diteks", "vocab", "stopword", "bentuk"} {
		n := 0
		for _, m := range r.Misses {
			if m.Kategori != kat {
				continue
			}
			if capPerKat >= 0 && n >= capPerKat {
				break
			}
			fmt.Fprintf(&b, "  [%-11s] j%d %.40s (%s): %q — pred: %s\n",
				kat, m.Journal, m.Record, m.Lang, m.Gold, m.Pred)
			n++
		}
		if sisa := katCount[kat] - n; capPerKat >= 0 && sisa > 0 {
			fmt.Fprintf(&b, "  … +%d baris [%s] tak dicetak (cap %d/kategori — utk SEMUA: StringAll / flag -miss-all)\n",
				sisa, kat, capPerKat)
		}
	}
	return b.String()
}
