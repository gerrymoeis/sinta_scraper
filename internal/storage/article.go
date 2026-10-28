package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
)

// Fungsi stage `corpus` (doc 43 §5 — spec di-approve user 14 Okt):
// SATU PINTU tulis/baca tabel `articles` (pola UpsertVolumeStats).
// Whitelist ketat + JSON guard — baris parsial tulis dgn is_complete=0
// (resume retry, pola stage).

// License status 4 kelas (doc 43 §5.5 — approve 14 Okt):
// open = CC license (baca+pakai bebas, wajib sitasi) · free_not_open =
// gratis tanpa open license (kasus user) · paid · unknown (antrean Fase 3.5).
const (
	LicenseOpen      = "open"
	LicenseFreeNotOA = "free_not_open"
	LicensePaid      = "paid"
	LicenseUnknown   = "unknown"
	LanguageID       = "id"
	LanguageEN       = "en"
	LanguageMixed    = "mixed"
	LanguageUnknown  = "unknown"
	ArticleSourceOAI = "oai"
	// Sumber API (C3b opsi A — approve user 27 Okt 2026): OAI gagal →
	// artikel primer datang dari API publik resmi. Kolom DB tanpa CHECK
	// (whitelist murni Go) → penambahan ini tanpa migrasi skema.
	ArticleSourceCrossref = "crossref"
	ArticleSourceDOAJ     = "doaj"
	ArticleSourceLand     = "landing"
)

// ==== C7 B-additive (doc 43 §6.x.7-8 — approve user 27 Okt 2026) ====
// 2 atribut TERPISAH (skala #1: 2 dimensi beda = beda kolom):
//   - oa_status = AKSES pembaca: free (baca gratis) · paid (ada tarif) ·
//     restricted (ditutup TANPA tarif: login/embargo) · unknown (probe gagal
//     — JANGAN ditebak; 403-WAF = unknown, bukan restricted).
//   - license_type = LISENSI yang benar2 TERTULIS: tipe CC kanonik
//     ("CC BY-SA 4.0" / "CC0") · all_rights_reserved (HANYA bila eksplisit) ·
//     unknown (Not Stated — absennya bukti ≠ bukti ARR; §6.x.7 putaran 3).
const (
	OAFree       = "free"
	OAPaid       = "paid"
	OARestricted = "restricted"
	OAUnknown    = "unknown"
	// LicenseTypeARR all_rights_reserved — teks eksplisit ("Copyright © … All
	// Rights Reserved"), bukan asumsi hukum. LicenseTypeUnknown = Not Stated.
	LicenseTypeARR     = "all_rights_reserved"
	LicenseTypeUnknown = "unknown"
)

// reCCTypeLabel = whitelist tipe CC kanonik (murni Go, pola whitelist
// license_status). Urutan alternatif terpanjang-dulu (RE2 leftmost-first):
// "CC BY-NC-SA 4.0" jangan terpotong jadi "CC BY-NC". Versi = token angka
// ("4.0", "3.0", "1.0") — cc0/CC0 punya versi sendiri ("CC0 1.0").
var reCCTypeLabel = regexp.MustCompile(
	`^(?:CC BY(?:-(?:NC-ND|NC-SA|NC|ND|SA))?(?: \d+(?:\.\d+)*)?|CC0(?: \d+(?:\.\d+)*)?)$`)

// ValidLicenseType = whitelist license_type (doc 43 §6.x.8 D1): tipe CC
// kanonik · all_rights_reserved · unknown. Nilai liar DITOLAK sebelum masuk DB.
func ValidLicenseType(t string) bool {
	return t == LicenseTypeUnknown || t == LicenseTypeARR || reCCTypeLabel.MatchString(t)
}

// ValidOAStatus = whitelist oa_status: free|paid|restricted|unknown.
func ValidOAStatus(s string) bool {
	return s == OAFree || s == OAPaid || s == OARestricted || s == OAUnknown
}

// ArticleRow = input tulis 1 baris korpus artikel.
type ArticleRow struct {
	JournalID     int64
	RecordID      string // OAI identifier / URL landing
	VolumeNo      int64  // 0 = null (citation gagal parse — jujur)
	IssueNo       int64
	Year          int64
	Title         string
	DOI           string // "" = null
	ArticleURL    string
	AbstractID    string // "" = null
	AbstractEN    string // "" = null
	Language      string // id|en|mixed|unknown
	KeywordsJSON  string // `[{"kw","src","lang"}]` — "[]" bila kosong
	LicenseStatus string // open|free_not_open|paid|unknown (LEGACY arsip)
	LicenseURL    string // "" = null
	OAStatus      string // C7: free|paid|restricted|unknown ("" → "unknown")
	LicenseType   string // C7: "CC BY-SA 4.0"|CC0|all_rights_reserved|unknown ("" → "unknown")
	Source        string // oai|crossref|doaj|landing
	IsComplete    bool
	Provenance    string // JSON object — "{}" bila kosong
}

// UpsertArticle tulis 1 baris artikel (idempotent: rerun timpa penuh dgn
// retrieved_at segar — pola UpsertVolumeStats).
func (s *Store) UpsertArticle(a ArticleRow) error {
	if a.LicenseStatus != LicenseOpen && a.LicenseStatus != LicenseFreeNotOA &&
		a.LicenseStatus != LicensePaid && a.LicenseStatus != LicenseUnknown {
		return fmt.Errorf("articles: license_status %q di luar whitelist 4 kelas (journal %d rec %q)",
			a.LicenseStatus, a.JournalID, a.RecordID)
	}
	if a.Language != LanguageID && a.Language != LanguageEN &&
		a.Language != LanguageMixed && a.Language != LanguageUnknown {
		return fmt.Errorf("articles: language %q di luar whitelist id|en|mixed|unknown (journal %d rec %q)",
			a.Language, a.JournalID, a.RecordID)
	}
	if a.Source != ArticleSourceOAI && a.Source != ArticleSourceCrossref &&
		a.Source != ArticleSourceDOAJ && a.Source != ArticleSourceLand {
		return fmt.Errorf("articles: source %q di luar whitelist oai|crossref|doaj|landing (journal %d rec %q)",
			a.Source, a.JournalID, a.RecordID)
	}
	// C7: default jujur "" → unknown (db lama / pemanggil lama tanpa atribut),
	// lalu whitelist ketat — nilai liar ditolak sebelum menyentuh DB.
	if a.OAStatus == "" {
		a.OAStatus = OAUnknown
	}
	if !ValidOAStatus(a.OAStatus) {
		return fmt.Errorf("articles: oa_status %q di luar whitelist free|paid|restricted|unknown (journal %d rec %q)",
			a.OAStatus, a.JournalID, a.RecordID)
	}
	if a.LicenseType == "" {
		a.LicenseType = LicenseTypeUnknown
	}
	if !ValidLicenseType(a.LicenseType) {
		return fmt.Errorf("articles: license_type %q di luar whitelist CC-kanonik|all_rights_reserved|unknown (journal %d rec %q)",
			a.LicenseType, a.JournalID, a.RecordID)
	}
	if a.KeywordsJSON == "" {
		a.KeywordsJSON = "[]" // G4-style default valid
	}
	if !json.Valid([]byte(a.KeywordsJSON)) {
		return fmt.Errorf("articles: keywords bukan JSON valid (journal %d rec %q)", a.JournalID, a.RecordID)
	}
	if a.Provenance == "" {
		a.Provenance = "{}"
	}
	if !json.Valid([]byte(a.Provenance)) {
		return fmt.Errorf("articles: provenance bukan JSON valid (journal %d rec %q)", a.JournalID, a.RecordID)
	}
	complete := 0
	if a.IsComplete {
		complete = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO articles
			(journal_id, record_id, volume_no, issue_no, year, title, doi,
			 article_url, abstract_id, abstract_en, language, keywords_json,
			 license_status, license_url, oa_status, license_type, source,
			 is_complete, provenance, retrieved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(journal_id, record_id) DO UPDATE SET
			volume_no = excluded.volume_no,
			issue_no = excluded.issue_no,
			year = excluded.year,
			title = excluded.title,
			doi = excluded.doi,
			article_url = excluded.article_url,
			abstract_id = excluded.abstract_id,
			abstract_en = excluded.abstract_en,
			language = excluded.language,
			keywords_json = excluded.keywords_json,
			license_status = excluded.license_status,
			license_url = excluded.license_url,
			oa_status = excluded.oa_status,
			license_type = excluded.license_type,
			source = excluded.source,
			is_complete = excluded.is_complete,
			provenance = excluded.provenance,
			retrieved_at = excluded.retrieved_at`,
		a.JournalID, a.RecordID, intOrZero(a.VolumeNo), intOrZero(a.IssueNo),
		intOrZero(a.Year), a.Title, nilIfEmpty(a.DOI), a.ArticleURL,
		nilIfEmpty(a.AbstractID), nilIfEmpty(a.AbstractEN), a.Language,
		a.KeywordsJSON, a.LicenseStatus, nilIfEmpty(a.LicenseURL),
		a.OAStatus, a.LicenseType,
		a.Source, complete, a.Provenance, nowUTC(),
	)
	if err != nil {
		return fmt.Errorf("articles: tulis journal %d rec %q: %w", a.JournalID, a.RecordID, err)
	}
	return nil
}

// CountArticles = total + complete utk 1 jurnal (audit resume pass-1).
func (s *Store) CountArticles(journalID int64) (total, complete int, err error) {
	err = s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(is_complete), 0)
		FROM articles WHERE journal_id = ?`, journalID,
	).Scan(&total, &complete)
	return
}

// Articles = baca baris 1 jurnal (gold-test & audit); urut record_id.
func (s *Store) Articles(journalID int64) ([]ArticleRow, error) {
	rows, err := s.db.Query(`
		SELECT journal_id, record_id, volume_no, issue_no, year, title,
		       COALESCE(doi, ''), article_url, COALESCE(abstract_id, ''),
		       COALESCE(abstract_en, ''), language, keywords_json,
		       license_status, COALESCE(license_url, ''),
		       oa_status, license_type, source,
		       is_complete, provenance
		FROM articles WHERE journal_id = ? ORDER BY record_id`, journalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArticleRow
	for rows.Next() {
		var a ArticleRow
		// volume_no/issue_no/year NULL = gagal parse citation (jujur G2) —
		// Scan via NullInt64 (artikel tanpa vol wajar utk korpus).
		var vol, iss, yr, comp sql.NullInt64
		if err := rows.Scan(&a.JournalID, &a.RecordID, &vol, &iss, &yr,
			&a.Title, &a.DOI, &a.ArticleURL, &a.AbstractID, &a.AbstractEN,
			&a.Language, &a.KeywordsJSON, &a.LicenseStatus, &a.LicenseURL,
			&a.OAStatus, &a.LicenseType, &a.Source, &comp, &a.Provenance); err != nil {
			return nil, err
		}
		a.VolumeNo, a.IssueNo, a.Year = vol.Int64, iss.Int64, yr.Int64
		a.IsComplete = comp.Valid && comp.Int64 == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateArticleKeywords = tulis keywords_json utk pass 2 (TF-IDF) —
// hanya kolom keywords + provenance + retrieved_at (idempotent).
func (s *Store) UpdateArticleKeywords(journalID int64, recordID, keywordsJSON, provenance string) error {
	if !json.Valid([]byte(keywordsJSON)) {
		return fmt.Errorf("articles: keywords pass-2 bukan JSON valid (journal %d rec %q)", journalID, recordID)
	}
	if !json.Valid([]byte(provenance)) {
		return fmt.Errorf("articles: provenance pass-2 bukan JSON valid (journal %d rec %q)", journalID, recordID)
	}
	res, err := s.db.Exec(`
		UPDATE articles SET keywords_json = ?, provenance = ?, retrieved_at = ?
		WHERE journal_id = ? AND record_id = ?`,
		keywordsJSON, provenance, nowUTC(), journalID, recordID)
	if err != nil {
		return fmt.Errorf("articles: update keywords journal %d rec %q: %w", journalID, recordID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("articles: rec %q tidak ada di journal %d", recordID, journalID)
	}
	return nil
}

// AllArticles = baca seluruh korpus (pass 2 TF-IDF & gold-test) — urut
// journal_id, record_id (determinisme).
func (s *Store) AllArticles() ([]ArticleRow, error) {
	jids, err := s.articleJournalIDs()
	if err != nil {
		return nil, err
	}
	var out []ArticleRow
	for _, id := range jids {
		as, err := s.Articles(id)
		if err != nil {
			return nil, err
		}
		out = append(out, as...)
	}
	return out, nil
}

// CorpusTarget = baris stage corpus pass 1 (pola VolumeTarget): scope -rank
// + kanonik (basis URL OAI) + ISSN utk Crossref + resume (EXISTS artikel
// complete — jurnal di-skip bila sudah terisi penuh).
type CorpusTarget struct {
	ID        int64
	Name      string
	ISSN      string // print_issn (utama Crossref)
	EISSN     string // fallback bila print kosong
	Canonical string // source_profile.canonical_url — fallback OAI (+"/oai"); G17b: kosong → COALESCE ke journals.ojs_url (URL resmi dari stage sinta)
	OAIResmi  string // journal_enrichment.garuda_oai_url — URL OAI RESMI Garuda (prioritas E5; 252 terisi; "" = fallback canonical)
	Complete  bool   // resume: ada baris articles is_complete=1
}

// CorpusTargets = jurnal scope -rank utk driver corpus, urut id. Termasuk
// TANPA kanonik/resmi (driver kategori NoCanonical jujur — 0 GET).
func (s *Store) CorpusTargets(ranks []int) ([]CorpusTarget, error) {
	where, args := rankWhere(ranks)
	rows, err := s.db.Query(`
		SELECT j.id, j.name, COALESCE(j.print_issn, ''), COALESCE(j.electronic_issn, ''),
		       COALESCE(NULLIF(sp.canonical_url, ''), NULLIF(j.ojs_url, ''), ''),
		       COALESCE(e.garuda_oai_url, ''),
		       CASE WHEN EXISTS (SELECT 1 FROM articles a
		                         WHERE a.journal_id = j.id AND a.is_complete = 1)
		            THEN 1 ELSE 0 END
		FROM journals j
		LEFT JOIN journal_source_profile sp ON sp.journal_id = j.id
		LEFT JOIN journal_enrichment e ON e.journal_id = j.id`+where+`
		ORDER BY j.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CorpusTarget
	for rows.Next() {
		var t CorpusTarget
		var comp int
		if err := rows.Scan(&t.ID, &t.Name, &t.ISSN, &t.EISSN, &t.Canonical, &t.OAIResmi, &comp); err != nil {
			return nil, err
		}
		t.Complete = comp == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) articleJournalIDs() ([]int64, error) {
	rows, err := s.db.Query(`SELECT DISTINCT journal_id FROM articles ORDER BY journal_id`)
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
