package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Fungsi Fase 3 F3.2 (doc 42 §11 K6-A — approve user 9 Okt 2026): SATU PINTU
// tulis utk tabel/kolom hasil stage `ojs`. Pola: writer khusus per tabel
// (pola UpsertJournalURL/SetSubjectCanonical), tanpa GET — driver F3.3 yang
// memanggil. Provenance `journals_author_fees` ditulis driver lewat
// MergeProvenance (pola stage.go garuda: UpdateJournalsE7 + MergeProvenance
// dua langkah).

// SourceProfile = input tulis journal_source_profile (1 baris/jurnal;
// doc 42 §11.1). Evidence WAJIB (aturan §7.2 butir 7 — marker konten).
type SourceProfile struct {
	JournalID      int64
	Platform       string // ojs|wordpress|custom|static|unknown
	Evidence       string // daftar marker match (wajib, bukan tebak-URL)
	Confidence     string // high|medium
	OJSVersionHint string // nullable — dari body bila terbaca
	CanonicalURL   string // final_url hasil resolve (kosong = belum)
}

// UpsertSourceProfile menulis 1 baris profil sumber per jurnal (idempotent:
// rerun menimpa penuh dgn hasil deteksi terbaru + detected_at segar).
func (s *Store) UpsertSourceProfile(p SourceProfile) error {
	if p.Platform == "" {
		return fmt.Errorf("source_profile: platform kosong (journal %d)", p.JournalID)
	}
	if strings.TrimSpace(p.Evidence) == "" && p.Platform != "unknown" {
		// unknown = deteksi jujur (non-200/body kosong) tanpa marker — evidence
		// diisi keterangan status sbg gantinya oleh driver; guard ini hanya
		// menolak klaim platform BERBUKTI tanpa bukti sama sekali.
		return fmt.Errorf("source_profile: evidence kosong utk platform %q (journal %d)", p.Platform, p.JournalID)
	}
	_, err := s.db.Exec(`
		INSERT INTO journal_source_profile
			(journal_id, platform, canonical_url, detection_evidence,
			 platform_confidence, ojs_version_hint, detected_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(journal_id) DO UPDATE SET
			platform = excluded.platform,
			canonical_url = excluded.canonical_url,
			detection_evidence = excluded.detection_evidence,
			platform_confidence = excluded.platform_confidence,
			ojs_version_hint = excluded.ojs_version_hint,
			detected_at = excluded.detected_at`,
		p.JournalID, p.Platform, nilIfEmpty(p.CanonicalURL), p.Evidence,
		nilIfEmpty(p.Confidence), nilIfEmpty(p.OJSVersionHint), nowUTC(),
	)
	if err != nil {
		return fmt.Errorf("source_profile: tulis journal %d: %w", p.JournalID, err)
	}
	return nil
}

// PolicyPrev = status & waktu cek terakhir SATU baris journal_policy (bahan
// deteksi policy-GET persisten — Opsi 2 approve 13 Okt; pola OjsCand utk
// journal_urls). Konvensi journal_policy: http_status int — 0 = tak ada
// respons (jaringan gagal; berbeda urls.go yg pakai NULL) + checked_at terisi
// = sudah dicek tapi gagal jaringan.
type PolicyPrev struct {
	HTTPStatus int    // 0 = network gagal
	CheckedAt  string // "" = belum pernah
}

// PolicyPrevMap = snapshot baris policy SATU jurnal SEBELUM GET run ini
// (per URL) — bahan cabang POLICY_PARTIAL persisten driver (Opsi 2).
func (s *Store) PolicyPrevMap(journalID int64) (map[string]PolicyPrev, error) {
	rows, err := s.db.Query(`
		SELECT url, http_status, COALESCE(checked_at, '')
		FROM journal_policy WHERE journal_id = ?`, journalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PolicyPrev{}
	for rows.Next() {
		var u string
		var p PolicyPrev
		if err := rows.Scan(&u, &p.HTTPStatus, &p.CheckedAt); err != nil {
			return nil, err
		}
		out[u] = p
	}
	return out, rows.Err()
}

// PolicyRow = input tulis journal_policy (baris per tipe; doc 42 §11.2).
type PolicyRow struct {
	PolicyType string // about|author_guideline|copyright|privacy|fee|other
	URL        string
	HTTPStatus int    // jujur — 403 WAF tetap tercatat
	RawSnippet string // nullable (audit)
}

// policyTypes = whitelist tipe (K5: whitelist ketat, pencegah typo/drift).
var policyTypes = map[string]bool{
	"about": true, "author_guideline": true, "copyright": true,
	"privacy": true, "fee": true, "other": true,
}

// UpsertPolicies menulis sekaligus baris policy utk SATU jurnal (satu tx,
// idempotent via UNIQUE(journal_id, policy_type, url)) — rerun memperbarui
// status/snippet, tak menggandakan baris.
func (s *Store) UpsertPolicies(journalID int64, rows []PolicyRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT INTO journal_policy
			(journal_id, policy_type, url, http_status, raw_snippet, checked_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(journal_id, policy_type, url) DO UPDATE SET
			http_status = excluded.http_status,
			raw_snippet = excluded.raw_snippet,
			checked_at = excluded.checked_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	checked := nowUTC()
	for _, r := range rows {
		if !policyTypes[r.PolicyType] {
			return fmt.Errorf("policy: tipe %q di luar whitelist (journal %d)", r.PolicyType, journalID)
		}
		if strings.TrimSpace(r.URL) == "" {
			return fmt.Errorf("policy: url kosong (journal %d)", journalID)
		}
		if _, err := stmt.Exec(journalID, r.PolicyType, r.URL, r.HTTPStatus,
			nilIfEmpty(r.RawSnippet), checked); err != nil {
			return fmt.Errorf("policy: tulis %s journal %d: %w", r.PolicyType, journalID, err)
		}
	}
	return tx.Commit()
}

// SetAuthorFees menulis journals.author_fees (K6-A: 1 kolom JSON Opsi C,
// doc 42 §11.3). Validasi JSON dulu (drift ketahuan sejak awal); overwrite
// penuh = rerun membawa hasil segar (pola capture Garuda, bukan fill-absent —
// fee bisa berubah tiap tahun). K1 aman: kolom BARU, raw Tahap 1 tak disentuh.
// Return jumlah baris berubah (0 = journal tak ada).
func (s *Store) SetAuthorFees(journalID int64, feesJSON string) (int64, error) {
	if !json.Valid([]byte(feesJSON)) {
		return 0, fmt.Errorf("author_fees: JSON tidak valid (journal %d)", journalID)
	}
	res, err := s.db.Exec(
		`UPDATE journals SET author_fees = ? WHERE id = ?`, feesJSON, journalID)
	if err != nil {
		return 0, fmt.Errorf("author_fees: tulis journal %d: %w", journalID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("author_fees: rows affected journal %d: %w", journalID, err)
	}
	return n, nil
}

// MarkCanonicalURL menandai pemenang resolve di journal_urls:
// url menang → is_canonical=1 + hasil verifikasi (status/final/checked);
// kandidat lain jurnal itu → is_canonical=0 (riwayat E7g tetap utuh —
// aturan §7.1 butir 6). Pola: 2 statement dalam 1 tx.
func (s *Store) MarkCanonicalURL(journalID int64, url string, httpStatus *int, finalURL, checkedAt string) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("canonical: url kosong (journal %d)", journalID)
	}
	if checkedAt == "" {
		checkedAt = nowUTC()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE journal_urls SET is_canonical = 0 WHERE journal_id = ?`, journalID); err != nil {
		return fmt.Errorf("canonical: reset journal %d: %w", journalID, err)
	}
	res, err := tx.Exec(`
		UPDATE journal_urls SET is_canonical = 1, http_status = ?, final_url = ?, checked_at = ?
		WHERE journal_id = ? AND url = ?`,
		httpStatus, nilIfEmpty(finalURL), checkedAt, journalID, url)
	if err != nil {
		return fmt.Errorf("canonical: tulis journal %d: %w", journalID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("canonical: url %q bukan kandidat journal %d (UpsertJournalURL dulu)", url, journalID)
	}
	return tx.Commit()
}

// OjsCand = satu kandidat URL asal (journal_urls kind=ojs) utk GET driver.
// HTTPStatus/CheckedAt = nilai SEBELUM GET run ini (konvensi urls.go: checked_at
// terisi + http_status NULL = dicek, jaringan gagal/status 0; keduanya kosong =
// belum pernah di-GET) — bahan deteksi unreachable persisten driver (opsi B).
type OjsCand struct {
	URL        string
	Source     string // sinta|garuda|doaj|openalex|official (label audit)
	HTTPStatus *int   // respons terakhir; nil = tak ada respons (network)
	CheckedAt  string // kapan terakhir di-GET; "" = belum pernah
}

// OjsTarget = baris stage ojs (doc 42 §4 F3.3): jurnal dalam scope -rank +
// kandidat (journal_urls kind=ojs + garuda_home_url utk dedupe/driver) +
// flag resume SOURCE_RESOLVED (terminal — skip kecuali -refresh).
type OjsTarget struct {
	ID         int64
	Name       string
	Resolved   bool      // SOURCE_RESOLVED — canonical resolve sudah terminal
	HomeURL    string    // garuda_home_url (kandidat tambahan bila belum di journal_urls)
	Candidates []OjsCand // journal_urls kind=ojs, urut url (deterministik)
}

// OjsTargets = jurnal scope -rank utk driver stage ojs, urut id. Kandidat =
// journal_urls (kind=ojs, semua source — aturan §7.1 butir 1) + garuda_home_url
// (bila belum ada di journal_urls; driver UpsertJournalURL dulu sebelum
// MarkCanonicalURL). Resume: Resolved=true bila flags memuat SOURCE_RESOLVED.
func (s *Store) OjsTargets(ranks []int) ([]OjsTarget, error) {
	where, args := rankWhere(ranks)
	rows, err := s.db.Query(`
		SELECT j.id, j.name,
		       CASE WHEN COALESCE(p.flags, '') LIKE '%SOURCE_RESOLVED%' THEN 1 ELSE 0 END,
		       COALESCE(e.garuda_home_url, '')
		FROM journals j
		LEFT JOIN phase2_progress p ON p.journal_id = j.id
		LEFT JOIN journal_enrichment e ON e.journal_id = j.id`+where+`
		ORDER BY j.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OjsTarget
	for rows.Next() {
		var t OjsTarget
		var res int
		if err := rows.Scan(&t.ID, &t.Name, &res, &t.HomeURL); err != nil {
			return nil, err
		}
		t.Resolved = res == 1
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Kandidat journal_urls kind=ojs — query terpisah lalu merge di Go
	// (SQLite tanpa array aggregate; jumlah baris kecil). http_status/
	// checked_at diikutkan sbg bukti hasil GET SEBELUMNYA (deteksi
	// unreachable persisten — jangan ditimpa pemikiran hasil run ini).
	cq := `SELECT u.journal_id, u.url, u.source, u.http_status, COALESCE(u.checked_at, '')
		FROM journal_urls u JOIN journals j ON j.id = u.journal_id`
	cargs := args
	if where != "" {
		cq += where + ` AND u.kind = 'ojs'`
	} else {
		cq += ` WHERE u.kind = 'ojs'`
	}
	cq += ` ORDER BY u.journal_id, u.url`
	crows, err := s.db.Query(cq, cargs...)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	byID := map[int64]int{} // journal_id → indeks out
	for i, t := range out {
		byID[t.ID] = i
	}
	for crows.Next() {
		var jid int64
		var c OjsCand
		var st sql.NullInt64
		if err := crows.Scan(&jid, &c.URL, &c.Source, &st, &c.CheckedAt); err != nil {
			return nil, err
		}
		if st.Valid {
			v := int(st.Int64)
			c.HTTPStatus = &v
		}
		if i, ok := byID[jid]; ok {
			out[i].Candidates = append(out[i].Candidates, c)
		}
	}
	return out, crows.Err()
}

// CountSourceResolved = jumlah baris scope dgn flag SOURCE_RESOLVED — bahan
// stageVerify driver (pola CountMatchStatus garuda).
func (s *Store) CountSourceResolved(ranks []int) (int, error) {
	where, args := rankWhere(ranks)
	if where == "" {
		where = ` WHERE`
	} else {
		where += ` AND`
	}
	var n int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM journals j
		JOIN phase2_progress p ON p.journal_id = j.id`+where+`
			COALESCE(p.flags, '') LIKE '%SOURCE_RESOLVED%'`, args...).Scan(&n)
	return n, err
}

// AuthorFees = isi journals.author_fees (baca saja — audit/verifikasi test &
// F3.4; "" = belum terisi/NULL).
func (s *Store) AuthorFees(journalID int64) (string, error) {
	var raw sql.NullString
	if err := s.db.QueryRow(
		`SELECT author_fees FROM journals WHERE id = ?`, journalID,
	).Scan(&raw); err != nil {
		return "", fmt.Errorf("author_fees: baca j%d: %w", journalID, err)
	}
	return raw.String, nil
}

// Phase2Flags = JSON flags per-jurnal (baca saja — audit/verifikasi; "" = belum
// ada baris).
func (s *Store) Phase2Flags(journalID int64) (string, error) {
	var raw sql.NullString
	if err := s.db.QueryRow(
		`SELECT flags FROM phase2_progress WHERE journal_id = ?`, journalID,
	).Scan(&raw); err != nil {
		return "", fmt.Errorf("phase2 flags: baca j%d: %w", journalID, err)
	}
	return raw.String, nil
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
