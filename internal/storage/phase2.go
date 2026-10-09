package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Fungsi Tahap 2 (Q3 — doc 30 §13.4 langkah 4–5): target harvest, resume
// phase2_progress, capture baris search, rebuild subject_map, dan hasil
// harmonisasi. journals tidak ditulis oleh fase enrichment ini (sinkronisasi
// Tahap 2 → journals = E7; AMENDMEN K1/K5, doc 30 Bagian 0).

// HarvestTarget = baris input harvest (dari journals — baca saja).
type HarvestTarget struct {
	ID          int64
	Name        string
	EISSN       string
	PISSN       string
	SubjectArea string // raw SINTA (K1)
}

// HarvestTargets seluruh jurnal urut id.
func (s *Store) HarvestTargets() ([]HarvestTarget, error) {
	return s.HarvestTargetsRank(nil)
}

// HarvestTargetsRank = HarvestTargets terfilter sinta_rank (rankWhere:
// ranks nil/kosong = tanpa filter — ParseRankSet "all" → nil) — scope
// stage garuda produksi (-rank), doc 40.
func (s *Store) HarvestTargetsRank(ranks []int) ([]HarvestTarget, error) {
	where, args := rankWhere(ranks)
	rows, err := s.db.Query(`
		SELECT id, name, COALESCE(electronic_issn, ''), COALESCE(print_issn, ''),
		       COALESCE(subject_area, '')
		FROM journals`+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HarvestTarget
	for rows.Next() {
		var t HarvestTarget
		if err := rows.Scan(&t.ID, &t.Name, &t.EISSN, &t.PISSN, &t.SubjectArea); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Phase2State = peta journalID → phase (resume: hindari request ganda §13.5).
func (s *Store) Phase2State() (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT journal_id, phase FROM phase2_progress`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var ph string
		if err := rows.Scan(&id, &ph); err != nil {
			return nil, err
		}
		out[id] = ph
	}
	return out, rows.Err()
}

// SetPhase2 upsert checkpoint per-jurnal: phase (kosong = pertahankan lama),
// merge flags JSON (add=true set, remove=false hapus), lastError ("" = bersih).
// Pola resume = checkpoint Tahap 1 (scrape_progress).
func (s *Store) SetPhase2(journalID int64, phase string, addFlags, removeFlags []string, lastError string) error {
	var rawPhase, rawFlags string
	err := s.db.QueryRow(
		`SELECT phase, COALESCE(flags, '{}') FROM phase2_progress WHERE journal_id = ?`,
		journalID,
	).Scan(&rawPhase, &rawFlags)
	exists := err == nil
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("phase2: baca journal %d: %w", journalID, err)
	}
	if phase == "" && exists {
		phase = rawPhase
	}
	if phase == "" {
		phase = "pending"
	}

	flags := map[string]bool{}
	if rawFlags != "" {
		_ = json.Unmarshal([]byte(rawFlags), &flags) // JSON rusak → mulai bersih
	}
	for _, f := range addFlags {
		flags[f] = true
	}
	for _, f := range removeFlags {
		delete(flags, f)
	}
	out, err := json.Marshal(flags)
	if err != nil {
		return fmt.Errorf("phase2: marshal flags journal %d: %w", journalID, err)
	}

	_, err = s.db.Exec(`
		INSERT INTO phase2_progress (journal_id, phase, flags, updated_at, last_error)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(journal_id) DO UPDATE SET
			phase = excluded.phase,
			flags = excluded.flags,
			updated_at = excluded.updated_at,
			last_error = excluded.last_error`,
		journalID, phase, string(out), time.Now().UTC().Format(time.RFC3339), lastError,
	)
	if err != nil {
		return fmt.Errorf("phase2: tulis journal %d: %w", journalID, err)
	}
	return nil
}

// GarudaMatch = hasil capture satu jurnal dari halaman search (§13.4
// langkah 4: seluruh field baris = SATU capture konsisten, §13.5).
type GarudaMatch struct {
	JournalID   int64
	Status      string // matched|not_found|ambiguous
	RetrievedAt string // RFC3339
	GarudaID    int64  // 0 = tanpa capture
	GarudaURL   string
	Title       string
	Publisher   string
	PISSN       string
	EISSN       string
	Subject     string  // label area join " | " (konvensi GarudaSubjectSep)
	MatchedBy   string  // 'eissn' | ''
	Confidence  float64 // exact binding = 1.0
}

// UpsertGarudaMatch menulis capture/ hasil search ke journal_enrichment.
// Capture kolom selalu ditulis penuh (matched → nilai; selain itu NULL)
// supaya rerun -refresh TIDAK meninggalkan data basi. provenance
// subject_area ditulis lewat MergeProvenance (satu pintu tulis Q1).
func (s *Store) UpsertGarudaMatch(m GarudaMatch) error {
	if m.RetrievedAt == "" {
		m.RetrievedAt = time.Now().UTC().Format(time.RFC3339)
	}
	var gid, gurl, gtitle, gpub, gpissn, geissn, gsubj, mby, conf any
	if m.Status == "matched" {
		gid = m.GarudaID
		gurl, gtitle, gpub = m.GarudaURL, m.Title, m.Publisher
		gpissn, geissn, gsubj = m.PISSN, m.EISSN, m.Subject
		mby = m.MatchedBy
		conf = m.Confidence
	}
	_, err := s.db.Exec(`
		INSERT INTO journal_enrichment
			(journal_id, garuda_id, garuda_url, garuda_title, garuda_publisher,
			 garuda_pissn, garuda_eissn, garuda_subject,
			 matched_by, match_confidence, match_status, garuda_retrieved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(journal_id) DO UPDATE SET
			garuda_id = excluded.garuda_id,
			garuda_url = excluded.garuda_url,
			garuda_title = excluded.garuda_title,
			garuda_publisher = excluded.garuda_publisher,
			garuda_pissn = excluded.garuda_pissn,
			garuda_eissn = excluded.garuda_eissn,
			garuda_subject = excluded.garuda_subject,
			matched_by = excluded.matched_by,
			match_confidence = excluded.match_confidence,
			match_status = excluded.match_status,
			garuda_retrieved_at = excluded.garuda_retrieved_at`,
		m.JournalID, gid, gurl, gtitle, gpub, gpissn, geissn, gsubj,
		mby, conf,
		m.Status, m.RetrievedAt,
	)
	if err != nil {
		return fmt.Errorf("capture journal %d: %w", m.JournalID, err)
	}
	return nil
}

// AmbigousMatchedIDs = baris matched yang masih membawa flag AMBIGUOUS lama
// (inkonsistensi flags vs match_status — mis. E4b me-resolve ambiguous jadi
// matched tanpa merapikan flag). Dipakai utk cleanup konsistensi.
func (s *Store) AmbigousMatchedIDs() ([]int64, error) {
	rows, err := s.db.Query(`
		SELECT e.journal_id FROM journal_enrichment e
		JOIN phase2_progress p ON p.journal_id = e.journal_id
		WHERE e.match_status = 'matched' AND p.flags LIKE '%AMBIGUOUS%'
		ORDER BY e.journal_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SubjectMapRow = baris input rebuild subject_map (padanan storage dari
// garuda.MapRow — tanpa impor silang utk hindari cycle).
type SubjectMapRow struct {
	System     string
	Term       string
	Key        string
	Canonical  string
	Method     string
	Support    int
	Confidence float64
	Origin     string
}

// ReplaceSubjectMap membangun ulang SELURUH kamus idempoten (DELETE+INSERT
// dalam satu tx) — pola rebuild dari sumber (§13.4 langkah 5).
func (s *Store) ReplaceSubjectMap(rows []SubjectMapRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM subject_map`); err != nil {
		return fmt.Errorf("subject_map: kosongkan: %w", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO subject_map
			(source_system, source_term, source_key, canonical_term,
			 method, support, confidence, origin, built_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range rows {
		if _, err := stmt.Exec(r.System, r.Term, r.Key, r.Canonical,
			r.Method, r.Support, r.Confidence, r.Origin, now); err != nil {
			return fmt.Errorf("subject_map: insert %s/%s: %w", r.System, r.Key, err)
		}
	}
	return tx.Commit()
}

// SubjectMapSnapshot membaca isi subject_map (audit/verifikasi).
func (s *Store) SubjectMapSnapshot() ([]SubjectMapRow, error) {
	rows, err := s.db.Query(`
		SELECT source_system, source_term, source_key, canonical_term,
		       method, support, confidence, origin
		FROM subject_map ORDER BY source_system, source_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubjectMapRow
	for rows.Next() {
		var r SubjectMapRow
		if err := rows.Scan(&r.System, &r.Term, &r.Key, &r.Canonical,
			&r.Method, &r.Support, &r.Confidence, &r.Origin); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SubjectRunPair = pasangan raw per jurnal utk bukti run (L1) & harmonisasi (L3).
type SubjectRunPair struct {
	JournalID int64
	SintaRaw  string // journals.subject_area (K1 — tak diubah)
	GarudaRaw string // journal_enrichment.garuda_subject
}

// SubjectRunPairs = 261 pasangan (LEFT JOIN — jurnal tanpa enrichment tetap
// ada, dgn GarudaRaw kosong).
func (s *Store) SubjectRunPairs() ([]SubjectRunPair, error) {
	rows, err := s.db.Query(`
		SELECT j.id, COALESCE(j.subject_area, ''), COALESCE(e.garuda_subject, '')
		FROM journals j
		LEFT JOIN journal_enrichment e ON e.journal_id = j.id
		ORDER BY j.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubjectRunPair
	for rows.Next() {
		var p SubjectRunPair
		if err := rows.Scan(&p.JournalID, &p.SintaRaw, &p.GarudaRaw); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetSubjectCanonical menulis subject_area_canonical hasil harmonisasi
// (kosong → NULL; jurnal tanpa enrichment dibuat barisnya dulu).
func (s *Store) SetSubjectCanonical(journalID int64, canonical string) error {
	if _, err := s.db.Exec(
		`INSERT INTO journal_enrichment (journal_id) VALUES (?) ON CONFLICT DO NOTHING`,
		journalID,
	); err != nil {
		return fmt.Errorf("canonical: siapkan baris journal %d: %w", journalID, err)
	}
	var v any
	if canonical != "" {
		v = canonical
	}
	_, err := s.db.Exec(
		`UPDATE journal_enrichment SET subject_area_canonical = ? WHERE journal_id = ?`,
		v, journalID,
	)
	if err != nil {
		return fmt.Errorf("canonical: tulis journal %d: %w", journalID, err)
	}
	return nil
}

// rankWhere membangun klausul WHERE sinta_rank IN (…) utk scope -rank
// (ranks nil/kosong = tanpa filter — ParseRankSet "all" → nil). Placeholder
// "?" meniru pola sqlite.go (JournalListByRank); args kembali urut dgn
// placeholder.
func rankWhere(ranks []int) (string, []any) {
	if len(ranks) == 0 {
		return "", nil
	}
	ph := make([]string, 0, len(ranks))
	args := make([]any, 0, len(ranks))
	for _, r := range ranks {
		ph = append(ph, "?")
		args = append(args, r)
	}
	return ` WHERE sinta_rank IN (` + strings.Join(ph, ",") + `)`, args
}

// SyncTarget = bahan sinkronisasi fill-if-absent Tahap 2 → journals
// (sub-fase 3 stage garuda — doc 40).
type SyncTarget struct {
	ID        int64
	SubjNow   string // journals.subject_area sekarang
	Canonical string // journal_enrichment.subject_area_canonical (kosong = belum)
	URLNow    string // journals.garuda_url sekarang
	URLCap    string // journal_enrichment.garuda_url (capture match; kosong = belum matched)
}

// SyncTargets seluruh baris scope utk sub-fase sync (LEFT JOIN — baris
// tanpa enrichment tetap ada dgn nilai kosong).
func (s *Store) SyncTargets(ranks []int) ([]SyncTarget, error) {
	where, args := rankWhere(ranks)
	rows, err := s.db.Query(`
		SELECT j.id, COALESCE(j.subject_area, ''), COALESCE(e.subject_area_canonical, ''),
		       COALESCE(j.garuda_url, ''), COALESCE(e.garuda_url, '')
		FROM journals j
		LEFT JOIN journal_enrichment e ON e.journal_id = j.id`+where+` ORDER BY j.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncTarget
	for rows.Next() {
		var t SyncTarget
		if err := rows.Scan(&t.ID, &t.SubjNow, &t.Canonical, &t.URLNow, &t.URLCap); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ViewTarget = baris sub-fase view (matched dgn garuda_id — stage opsi B,
// doc 40). Resume: flag VIEW_DONE (terminal) atau link sudah ada.
type ViewTarget struct {
	ID       int64
	GarudaID int64
	ViewDone bool   // flag VIEW_DONE — halaman sudah di-fetch (found/notfound/tanpa link)
	LinkNow  bool   // garuda_home_url / garuda_oai_url sudah terisi
	SubjNow  string // journals.subject_area sekarang (guard fill-if-absent)
	PrintNow string // journals.print_issn sekarang (guard fill-if-absent)
}

// ViewTargets = matched dgn garuda_id dalam scope -rank, urut id.
func (s *Store) ViewTargets(ranks []int) ([]ViewTarget, error) {
	where, args := rankWhere(ranks)
	if where == "" {
		where = ` WHERE`
	} else {
		where += ` AND`
	}
	rows, err := s.db.Query(`
		SELECT j.id, e.garuda_id,
		       CASE WHEN COALESCE(p.flags, '') LIKE '%VIEW_DONE%' THEN 1 ELSE 0 END,
		       CASE WHEN COALESCE(e.garuda_home_url, '') <> '' OR COALESCE(e.garuda_oai_url, '') <> '' THEN 1 ELSE 0 END,
		       COALESCE(j.subject_area, ''), COALESCE(j.print_issn, '')
		FROM journals j
		JOIN journal_enrichment e ON e.journal_id = j.id
		LEFT JOIN phase2_progress p ON p.journal_id = j.id`+where+`
			e.match_status = 'matched' AND COALESCE(e.garuda_id, 0) <> 0
		ORDER BY j.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ViewTarget
	for rows.Next() {
		var t ViewTarget
		var done, link int
		if err := rows.Scan(&t.ID, &t.GarudaID, &done, &link, &t.SubjNow, &t.PrintNow); err != nil {
			return nil, err
		}
		t.ViewDone, t.LinkNow = done == 1, link == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountMatchStatus = jumlah baris scope dgn match_status terisi — verifikasi
// akhir stage: setiap target wajib punya keputusan jujur (matched/not_found/
// ambiguous), kecuali harvest error yang checkpoint-nya belum ditandai.
func (s *Store) CountMatchStatus(ranks []int) (int, error) {
	where, args := rankWhere(ranks)
	if where == "" {
		where = ` WHERE`
	} else {
		where += ` AND`
	}
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM journals j
		JOIN journal_enrichment e ON e.journal_id = j.id`+where+`
			COALESCE(e.match_status, '') <> ''`, args...).Scan(&n)
	return n, err
}
