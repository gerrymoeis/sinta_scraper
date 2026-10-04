package garuda

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sinta-scraper/internal/storage"
)

// Langkah 5 (doc 30 §13.2 L0–L4): bangun subject_map DARI SUMBER (bukan
// const Go) lalu harmonisasi union per jurnal → subject_area_canonical.
//
// Sumber vocab:
//   - SINTA : DISTINCT journals.subject_area → split koma (tanpa jaringan);
//   - Garuda: GET /area (sekali). Refresh ON-DEMAND: bila muncul label di
//     capture yang belum ada di kamus → GET /area sekali lagi (bukan tiap
//     run) — inilah syarat "kamus dari sumber, adaptif bila taxonomy berubah".
type MapReport struct {
	GarudaVocab   int // label area resmi dari /area
	SintaVocab    int // token SINTA unik dari DB
	MapRows       int
	FetchedArea   bool     // GET /area dilakukan run ini (refresh/first build)
	UnknownLabels []string // label capture di luar /area (tetap dipakai + peringatan)
	Filled        int      // canonical terisi
	NoSubject     int      // kedua sumber kosong → NULL + flag NO_SUBJECT
	LowEvidence   int      // jurnal dgn ≥1 merge support=0 → flag LOW_EVIDENCE
}

// BuildDanHarmonisasi menjalankan L0 (vocab) → L1+L2 (build map) →
// L3 (union per jurnal) → L4 (tulis) dalam satu run idempoten.
func BuildDanHarmonisasi(store *storage.Store, c *Client, fixturesDir string) (*MapReport, error) {
	rep := &MapReport{}

	// ---- pasangan run (bahan L1 + input L3) ----
	pairs, err := store.SubjectRunPairs()
	if err != nil {
		return nil, fmt.Errorf("baca run pairs: %w", err)
	}

	// ---- L0a: vocab SINTA dari DB (tanpa jaringan) ----
	sintaVocab := sintaVocabFromPairs(pairs)
	rep.SintaVocab = len(sintaVocab)

	// ---- L0b: vocab Garuda ----
	prev, err := store.SubjectMapSnapshot()
	if err != nil {
		return nil, fmt.Errorf("baca subject_map lama: %w", err)
	}
	garudaVocab, areaFetched, unknown, err := garudaVocabFromSource(store, c, fixturesDir, prev, pairs)
	if err != nil {
		return nil, fmt.Errorf("vocab garuda: %w", err)
	}
	rep.GarudaVocab = len(garudaVocab)
	rep.FetchedArea = areaFetched
	rep.UnknownLabels = unknown

	// ---- L1+L2: build map (murni fungsi deterministik) ----
	runPairs := make([]RunPair, 0, len(pairs))
	for _, p := range pairs {
		runPairs = append(runPairs, RunPair{
			Sinta:  SplitSintaSubject(p.SintaRaw),
			Garuda: SplitGarudaSubject(p.GarudaRaw),
		})
	}
	rows := BuildSubjectMap(sintaVocab, garudaVocab, runPairs)

	// ---- L4a: tulis subject_map (rebuild idempoten) ----
	dbRows := make([]storage.SubjectMapRow, 0, len(rows))
	for _, r := range rows {
		dbRows = append(dbRows, storage.SubjectMapRow{
			System: r.System, Term: r.Term, Key: r.Key, Canonical: r.Canonical,
			Method: r.Method, Support: r.Support, Confidence: r.Confidence,
			Origin: r.Origin,
		})
	}
	if err := store.ReplaceSubjectMap(dbRows); err != nil {
		return nil, err
	}
	rep.MapRows = len(dbRows)

	// ---- L3+L4b: union per jurnal → subject_area_canonical + flags ----
	idx := NewSubjectIndex(rows)
	byRef := map[string]MapRow{}
	for _, r := range rows {
		byRef[r.System+"\x00"+r.Key] = r
	}
	for _, p := range pairs {
		canonical := idx.HarmonizeSubject(p.SintaRaw, p.GarudaRaw)
		if err := store.SetSubjectCanonical(p.JournalID, canonical); err != nil {
			return nil, err
		}

		var add, remove []string
		if canonical == "" {
			rep.NoSubject++
			add = append(add, "NO_SUBJECT")
		} else {
			rep.Filled++
			remove = append(remove, "NO_SUBJECT")
		}
		if journalLowEvidence(p, byRef) {
			rep.LowEvidence++
			add = append(add, "LOW_EVIDENCE")
		} else {
			remove = append(remove, "LOW_EVIDENCE")
		}
		if err := store.SetPhase2(p.JournalID, "", add, remove, ""); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

// journalLowEvidence = ada ≥1 term jurnal yg masuk merge tanpa bukti run.
func journalLowEvidence(p storage.SubjectRunPair, byRef map[string]MapRow) bool {
	check := func(system, raw string) bool {
		for _, t := range splitSubject(system, raw) {
			k := FoldSubject(t)
			if k == "" {
				continue
			}
			if r, ok := byRef[system+"\x00"+k]; ok && LowEvidence(r) {
				return true
			}
		}
		return false
	}
	return check(SystemSinta, p.SintaRaw) || check(SystemGaruda, p.GarudaRaw)
}

// sintaVocabFromPairs = token subject SINTA unik dari raw jurnal (L0).
func sintaVocabFromPairs(pairs []storage.SubjectRunPair) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range pairs {
		for _, t := range SplitSintaSubject(p.SintaRaw) {
			if k := FoldSubject(t); k != "" && !seen[k] {
				seen[k] = true
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out
}

// garudaVocabFromSource mengambil vocab Garuda dgn strategi on-demand:
//   - subject_map lama kosong → GET /area (first build);
//   - ada label capture di luar kamus → GET /area sekali lagi (refresh);
//   - masih di luar → pakai label capture (sumber resmi juga) + catat.
func garudaVocabFromSource(store *storage.Store, c *Client, fixturesDir string, prev []storage.SubjectMapRow, pairs []storage.SubjectRunPair) (vocab []string, fetched bool, unknown []string, err error) {
	prevSet := map[string]bool{}
	for _, r := range prev {
		if r.System == SystemGaruda {
			prevSet[r.Key] = true
		}
	}

	// label capture (raw, unik per fold)
	captureLabels := map[string]string{} // fold → raw
	for _, p := range pairs {
		for _, t := range SplitGarudaSubject(p.GarudaRaw) {
			if k := FoldSubject(t); k != "" {
				if _, ok := captureLabels[k]; !ok {
					captureLabels[k] = t
				}
			}
		}
	}

	needFetch := len(prevSet) == 0 // first build
	if !needFetch {
		for k := range captureLabels {
			if !prevSet[k] {
				needFetch = true
				break
			}
		}
	}

	var labels []AreaLabel
	if needFetch {
		body, _, gerr := c.Get(AreaURL, DefaultReferer)
		if gerr != nil {
			return nil, false, nil, fmt.Errorf("GET /area: %w", gerr)
		}
		if werr := os.WriteFile(filepath.Join(fixturesDir, "area-live.html"), body, 0o644); werr != nil {
			return nil, false, nil, fmt.Errorf("tulis fixture /area: %w", werr)
		}
		labels, err = ParseAreaList(strings.NewReader(string(body)))
		if err != nil {
			return nil, false, nil, err
		}
		fetched = true
	}

	// gabung: /area sbg sumber utama; label capture di luar /area → tambah + catat
	seen := map[string]bool{}
	for _, l := range labels {
		if k := FoldSubject(l.Label); k != "" && !seen[k] {
			seen[k] = true
			vocab = append(vocab, l.Label)
		}
	}
	if fetched {
		for k, raw := range captureLabels {
			if !seen[k] {
				unknown = append(unknown, raw)
				seen[k] = true
				vocab = append(vocab, raw)
			}
		}
	} else {
		// tanpa fetch: kamus = label map lama + label capture baru apa adanya
		for _, r := range prev {
			if r.System == SystemGaruda && !seen[r.Key] {
				seen[r.Key] = true
				vocab = append(vocab, r.Term)
			}
		}
		for k, raw := range captureLabels {
			if !seen[k] {
				unknown = append(unknown, raw)
				seen[k] = true
				vocab = append(vocab, raw)
			}
		}
	}
	sort.Strings(vocab)
	return vocab, fetched, unknown, nil
}
