package garuda

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KandidatDariFixture memuat kandidat hasil search utk satu jurnal dari
// fixture Q3 (D2: nol request) — pola file search-j{ID}-*.html. Kandidat
// di-dedup per garuda_id; nilai field RAW apa adanya (K1) → langsung
// dipakai sbg Candidate utk Match.
//
// Mengembalikan: kandidat unik, jumlah baris mentah (0 = fixture tak ada
// atau found=0 → butuh live E4 langkah 2), dan error parse. File tak ada
// bukan error (nol kandidat = hasil sah "butuh live").
func KandidatDariFixture(fixturesDir string, journalID int64) ([]Candidate, int, error) {
	matches, err := filepath.Glob(filepath.Join(fixturesDir, fmt.Sprintf("search-j%d-*.html", journalID)))
	if err != nil {
		return nil, 0, fmt.Errorf("glob fixture j%d: %w", journalID, err)
	}
	seen := map[int64]bool{}
	var cands []Candidate
	total := 0
	for _, path := range matches {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, total, fmt.Errorf("baca %s: %w", filepath.Base(path), err)
		}
		page, err := ParseSearchPage(strings.NewReader(string(b)))
		if err != nil {
			return nil, total, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
		}
		for _, r := range page.Rows {
			total++
			if seen[r.GarudaID] {
				continue
			}
			seen[r.GarudaID] = true
			cands = append(cands, Candidate{
				GarudaID:  r.GarudaID,
				URL:       ViewURL(int(r.GarudaID)),
				Title:     r.Title,
				Publisher: r.Publisher,
				PISSN:     r.PISSN,
				EISSN:     r.EISSN,
			})
		}
	}
	return cands, total, nil
}
