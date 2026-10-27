package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Fungsi K6 REVISED (doc 41 §13.7 + gap-A/B + refine-C/D approve user 14 Okt):
// SATU PINTU tulis baca journal_volume_stats. Pola: writer khusus per tabel
// (pola UpsertSourceProfile/UpsertPolicies) — driver stage volume (nanti)
// memanggil; tanpa GET di sini.

// VolumeStatsRow = input tulis journal_volume_stats (1 baris/jurnal).
type VolumeStatsRow struct {
	JournalID            int64
	AnchorYear           int64 // 0 = null (gap-B: Crossref+archive gagal total)
	WindowStart          int64
	WindowEnd            int64
	VolPerYearAvg        float64
	ArticlesPerVolumeAvg float64
	ReleaseMonths        string // "2,6,10"; "" = null (tak ada bulan ter-parse)
	IssuesJSON           string // wajib JSON array (nullable field di dalamnya)
	Source               string // ojs|crossref
	IsComplete           bool   // false = parsial → resume RETRY (refine-C)
	ProvenanceJSON       string // wajib JSON object
}

// UpsertVolumeStats menulis 1 baris statistik volume per jurnal (idempotent:
// rerun menimpa penuh dgn hasil terbaru + retrieved_at segar).
func (s *Store) UpsertVolumeStats(v VolumeStatsRow) error {
	if v.Source != "ojs" && v.Source != "crossref" {
		return fmt.Errorf("volume_stats: source %q di luar whitelist ojs|crossref (journal %d)",
			v.Source, v.JournalID)
	}
	if !json.Valid([]byte(v.IssuesJSON)) {
		return fmt.Errorf("volume_stats: issues bukan JSON valid (journal %d)", v.JournalID)
	}
	if !json.Valid([]byte(v.ProvenanceJSON)) {
		return fmt.Errorf("volume_stats: provenance bukan JSON valid (journal %d)", v.JournalID)
	}
	isComplete := 0
	if v.IsComplete {
		isComplete = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO journal_volume_stats
			(journal_id, anchor_year, window_start, window_end,
			 vol_per_year_avg, articles_per_volume_avg, release_months,
			 issues, source, is_complete, provenance, retrieved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(journal_id) DO UPDATE SET
			anchor_year = excluded.anchor_year,
			window_start = excluded.window_start,
			window_end = excluded.window_end,
			vol_per_year_avg = excluded.vol_per_year_avg,
			articles_per_volume_avg = excluded.articles_per_volume_avg,
			release_months = excluded.release_months,
			issues = excluded.issues,
			source = excluded.source,
			is_complete = excluded.is_complete,
			provenance = excluded.provenance,
			retrieved_at = excluded.retrieved_at`,
		v.JournalID, intOrZero(v.AnchorYear), intOrZero(v.WindowStart),
		intOrZero(v.WindowEnd), v.VolPerYearAvg, v.ArticlesPerVolumeAvg,
		nilIfEmpty(v.ReleaseMonths), v.IssuesJSON, v.Source, isComplete,
		v.ProvenanceJSON, nowUTC(),
	)
	if err != nil {
		return fmt.Errorf("volume_stats: tulis journal %d: %w", v.JournalID, err)
	}
	return nil
}

// VolumeStatsComplete = status resume utk stage volume (refine-C approve
// user): skip bila baris ADA & is_complete=1; RETRY bila is_complete=0
// (parsial tak final); proses bila belum ada baris sama sekali.
func (s *Store) VolumeStatsComplete(journalID int64) (bool, error) {
	var complete int
	err := s.db.QueryRow(
		`SELECT is_complete FROM journal_volume_stats WHERE journal_id = ?`,
		journalID,
	).Scan(&complete)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return complete == 1, nil
}

// VolumeStats = baca 1 baris (test & audit); ok=false bila belum ada.
func (s *Store) VolumeStats(journalID int64) (VolumeStatsRow, bool, error) {
	var v VolumeStatsRow
	var relMonths sql.NullString
	var anchor, wStart, wEnd sql.NullInt64
	var isComplete int
	err := s.db.QueryRow(`
		SELECT journal_id, anchor_year, window_start, window_end,
		       vol_per_year_avg, articles_per_volume_avg, release_months,
		       issues, source, is_complete, provenance
		FROM journal_volume_stats WHERE journal_id = ?`, journalID,
	).Scan(&v.JournalID, &anchor, &wStart, &wEnd, &v.VolPerYearAvg,
		&v.ArticlesPerVolumeAvg, &relMonths, &v.IssuesJSON, &v.Source,
		&isComplete, &v.ProvenanceJSON)
	if err == sql.ErrNoRows {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	v.AnchorYear, v.WindowStart, v.WindowEnd = anchor.Int64, wStart.Int64, wEnd.Int64
	if relMonths.Valid {
		v.ReleaseMonths = relMonths.String
	}
	v.IsComplete = isComplete == 1
	return v, true, nil
}

// VolumeTarget = baris stage volume: jurnal scope -rank + kanonik + platform +
// ISSN utk Crossref + resume (journal_volume_stats is_complete=1 → skip).
type VolumeTarget struct {
	ID        int64
	Name      string
	Platform  string // ojs|wordpress|custom|static|unknown
	ISSN      string // print_issn (utama Crossref — pola probep4)
	EISSN     string // fallback bila print kosong
	Canonical string // source_profile.canonical_url (SOURCE_RESOLVED)
	Complete  bool   // resume: baris journal_volume_stats is_complete=1
}

// VolumeTargets = jurnal scope -rank utk driver stage volume, urut id. Target
// termasuk TANPA canonical (driver kategori NoCanonical jujur — 0 GET).
func (s *Store) VolumeTargets(ranks []int) ([]VolumeTarget, error) {
	where, args := rankWhere(ranks)
	rows, err := s.db.Query(`
		SELECT j.id, j.name, COALESCE(j.print_issn, ''), COALESCE(j.electronic_issn, ''),
		       COALESCE(sp.platform, ''), COALESCE(sp.canonical_url, ''),
		       CASE WHEN vs.is_complete = 1 THEN 1 ELSE 0 END
		FROM journals j
		LEFT JOIN journal_source_profile sp ON sp.journal_id = j.id
		LEFT JOIN journal_volume_stats vs ON vs.journal_id = j.id`+where+`
		ORDER BY j.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VolumeTarget
	for rows.Next() {
		var t VolumeTarget
		var comp int
		if err := rows.Scan(&t.ID, &t.Name, &t.ISSN, &t.EISSN,
			&t.Platform, &t.Canonical, &comp); err != nil {
			return nil, err
		}
		t.Complete = comp == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

func intOrZero(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
