package garuda

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sinta-scraper/internal/storage"
)

// Tahap 4 (doc 30 §13.4): harvest subject 261 jurnal dari halaman search
// /journal?q=<E-ISSN kanonik> — SATU capture per jurnal (seluruh field baris,
// §13.5), resume via phase2_progress, HTML mentah disimpan sbg fixture utk
// E2 (parser test offline) — tanpa refetch di fase berikut.

// Status match & fase Tahap 2 (status = konstanta Q2 di match.go).
const (
	PhaseSearchDone = "GARUDA_SEARCHED" // search selesai (termasuk not_found)
	PhaseMatched    = "GARUDA_MATCHED"  // binding exact cocok
	MatchedByEISSN  = "eissn"
	FlagAmbiguous   = "AMBIGUOUS"
	maxSearchPages  = 5 // batas pagination cari exact (E-ISSN normal = hal. 1)
)

type HarvestOptions struct {
	FixturesDir string
	Refresh     bool // abaikan resume — ulang semua (HANYA bila diminta)
	OnProgress  func(done, total int, t storage.HarvestTarget, status string)
}

type HarvestReport struct {
	Matched   int
	NotFound  int
	Ambiguous int
	Skipped   int // sudah pernah search (resume §13.5)
	Errors    int
}

// HarvestSubjects menjalankan pencarian+binding E-ISSN exact utk semua target.
// Binding: exact equality CanonicalISSN(E-ISSN baris) == kunci q (tanpa
// ladder/skoring — itu E4/Q4). Pagination hanya ditarik bila exact belum
// ketemu & masih ada halaman (≤ maxSearchPages) — hemat request.
func HarvestSubjects(store *storage.Store, c *Client, targets []storage.HarvestTarget, opt HarvestOptions) (*HarvestReport, error) {
	state, err := store.Phase2State()
	if err != nil {
		return nil, fmt.Errorf("baca resume state: %w", err)
	}
	if err := os.MkdirAll(opt.FixturesDir, 0o755); err != nil {
		return nil, fmt.Errorf("buat dir fixture: %w", err)
	}

	rep := &HarvestReport{}
	for i, t := range targets {
		if !opt.Refresh {
			switch state[t.ID] {
			case PhaseMatched, PhaseSearchDone:
				rep.Skipped++
				if opt.OnProgress != nil {
					opt.OnProgress(i+1, len(targets), t, "skip")
				}
				continue
			}
		}

		status, herr := harvestSatu(store, c, t, opt.FixturesDir)
		switch herr {
		case nil:
			// status dicatat di bawah
		default:
			rep.Errors++
			// fase belum ditandai → run berikut mencoba lagi (jujur, bukan basi)
			if err := store.SetPhase2(t.ID, "", nil, nil, herr.Error()); err != nil {
				return nil, err
			}
			if opt.OnProgress != nil {
				opt.OnProgress(i+1, len(targets), t, "ERR "+herr.Error())
			}
			continue
		}
		switch status {
		case StatusMatched:
			rep.Matched++
		case StatusAmbiguous:
			rep.Ambiguous++
		default:
			rep.NotFound++
		}
		if opt.OnProgress != nil {
			opt.OnProgress(i+1, len(targets), t, string(status))
		}
	}
	return rep, nil
}

// harvestSatu = 1 GET (+pagination bila perlu) → klasifikasi → tulis DB.
func harvestSatu(store *storage.Store, c *Client, t storage.HarvestTarget, fixturesDir string) (Status, error) {
	q := CanonicalISSN(t.EISSN)
	if q == "" {
		// E-ISSN 261/261 kanonik (fakta §2.1); bila kosong → not_found jujur
		// (ladder P-ISSN = E4/Q4).
		return StatusNotFound, tulisHasil(store, t, StatusNotFound, storage.GarudaMatch{
			JournalID: t.ID, Status: string(StatusNotFound),
		})
	}

	var rows []SearchRow
	var page *SearchPage
	for p := 1; p <= maxSearchPages; p++ {
		body, _, err := c.Get(SearchURL(q, p), DefaultReferer)
		if err != nil {
			return StatusNotFound, fmt.Errorf("GET search q=%s p%d: %w", q, p, err)
		}
		if werr := os.WriteFile(fixturePath(fixturesDir, t, p), body, 0o644); werr != nil {
			return StatusNotFound, fmt.Errorf("tulis fixture: %w", werr)
		}
		page, err = ParseSearchPage(strings.NewReader(string(body)))
		if err != nil {
			return StatusNotFound, fmt.Errorf("parse search q=%s p%d: %w", q, p, err)
		}
		rows = append(rows, page.Rows...)
		if len(exactMatches(rows, q)) > 0 {
			break // ketemu di halaman ini — stop (hemat request §13.5)
		}
		if page.Page >= page.OfPages {
			break
		}
	}

	exact := exactMatches(rows, q)
	switch len(exact) {
	case 1:
		r := exact[0]
		var labels []string
		for _, a := range r.Areas {
			labels = append(labels, a.Label)
		}
		m := storage.GarudaMatch{
			JournalID:   t.ID,
			Status:      string(StatusMatched),
			RetrievedAt: time.Now().UTC().Format(time.RFC3339),
			GarudaID:    r.GarudaID,
			GarudaURL:   fmt.Sprintf("https://garuda.kemdiktisaintek.go.id/journal/view/%d", r.GarudaID),
			Title:       r.Title,
			Publisher:   r.Publisher,
			PISSN:       r.PISSN,
			EISSN:       r.EISSN,
			Subject:     strings.Join(labels, GarudaSubjectSep),
			MatchedBy:   MatchedByEISSN,
			Confidence:  1.0,
		}
		if err := tulisHasil(store, t, StatusMatched, m); err != nil {
			return StatusNotFound, err
		}
		if m.Subject != "" {
			if err := store.MergeProvenance(t.ID, "subject_area", storage.ProvEntry{
				Value: m.Subject, Source: "garuda", Confidence: 1.0,
			}); err != nil {
				return StatusNotFound, err
			}
		}
		return StatusMatched, nil
	case 0:
		return StatusNotFound, tulisHasil(store, t, StatusNotFound, storage.GarudaMatch{
			JournalID: t.ID, Status: string(StatusNotFound),
			RetrievedAt: time.Now().UTC().Format(time.RFC3339),
		})
	default:
		// >1 baris dgn E-ISSN sama = ISSN ganda → jangan menebak (§13.6)
		return StatusAmbiguous, tulisHasil(store, t, StatusAmbiguous, storage.GarudaMatch{
			JournalID: t.ID, Status: string(StatusAmbiguous),
			RetrievedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// tulisHasil = capture + checkpoint phase (SEARCHED/MATCHED) + flag.
func tulisHasil(store *storage.Store, t storage.HarvestTarget, status Status, m storage.GarudaMatch) error {
	if err := store.UpsertGarudaMatch(m); err != nil {
		return err
	}
	phase, flags := PhaseSearchDone, []string(nil)
	switch status {
	case StatusMatched:
		phase = PhaseMatched
	case StatusAmbiguous:
		flags = []string{FlagAmbiguous}
	}
	return store.SetPhase2(t.ID, phase, flags, nil, "")
}

// exactMatches = baris dgn E-ISSN kanonik sama persis dgn kunci q.
func exactMatches(rows []SearchRow, q string) []SearchRow {
	var out []SearchRow
	for _, r := range rows {
		if CanonicalISSN(r.EISSN) == q {
			out = append(out, r)
		}
	}
	return out
}

func fixturePath(dir string, t storage.HarvestTarget, page int) string {
	name := fmt.Sprintf("search-j%d-%s.html", t.ID, CanonicalISSN(t.EISSN))
	if page > 1 {
		name = fmt.Sprintf("search-j%d-%s-p%d.html", t.ID, CanonicalISSN(t.EISSN), page)
	}
	return filepath.Join(dir, name)
}
