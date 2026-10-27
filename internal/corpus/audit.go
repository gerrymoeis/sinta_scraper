package corpus

// ==== audit.go — audit baris korpus (doc 43 §6.0; fase C6) ====
//
// Murni baca (0 GET): tabel ringkas per-baris articles utk verifikasi
// manual hasil smoke — panjang abstrak per bahasa, lisensi, jumlah &
// asal keyword, status complete. Dipanggil `cmd/goldtest -audit <ids>`.

import (
	"fmt"
	"strings"

	"sinta-scraper/internal/storage"
)

// AuditRow = 1 baris ringkas utk review manual.
type AuditRow struct {
	JournalID int64
	RecordID  string
	Vol       int64 // 0 = null (citation gagal — jujur)
	Year      int64
	Title     string
	Language  string
	AbsIDLen  int
	AbsENLen  int
	License   string
	KeywordN  int
	KWSource  string // author|crossref|tfidf|kosong (src entri pertama — 1 artikel = 1 sumber)
	Complete  bool
}

// AuditRows = slice dgn String() tabel.
type AuditRows []AuditRow

// CorpusAudit = baca baris articles (filter jid; kosong/ketik semua =
// seluruh korpus) → AuditRows. Error hanya kegagalan DB.
func CorpusAudit(store *storage.Store, jids []int64) (AuditRows, error) {
	all, err := store.AllArticles()
	if err != nil {
		return nil, fmt.Errorf("audit: baca korpus: %w", err)
	}
	want := map[int64]bool{}
	for _, id := range jids {
		want[id] = true
	}
	rows := make(AuditRows, 0, len(all))
	for _, a := range all {
		if len(want) > 0 && !want[a.JournalID] {
			continue
		}
		kws := parseKeywordEntries(a.KeywordsJSON)
		src := "kosong"
		if len(kws) > 0 {
			src = kws[0].Src
		}
		rows = append(rows, AuditRow{
			JournalID: a.JournalID,
			RecordID:  a.RecordID,
			Vol:       a.VolumeNo,
			Year:      a.Year,
			Title:     truncateRunes(a.Title, 44),
			Language:  a.Language,
			AbsIDLen:  len(a.AbstractID),
			AbsENLen:  len(a.AbstractEN),
			License:   a.LicenseStatus,
			KeywordN:  len(kws),
			KWSource:  src,
			Complete:  a.IsComplete,
		})
	}
	return rows, nil
}

// String = tabel audit per-baris (lebar kolom rapi utk terminal).
func (rows AuditRows) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== AUDIT KORPUS (%d baris, 0 GET) ===\n", len(rows))
	fmt.Fprintf(&b, "%-6s %-34s %4s %5s %-44s %4s %6s %6s %-13s %4s %9s %4s\n",
		"jid", "record_id", "vol", "year", "title", "lang",
		"abs_id", "abs_en", "license", "nkw", "kw_src", "ok")
	for _, r := range rows {
		fmt.Fprintf(&b, "%-6d %-34s %4d %5d %-44s %4s %6d %6d %-13s %4d %9s %4v\n",
			r.JournalID, truncateRunes(r.RecordID, 34), r.Vol, r.Year, r.Title, r.Language,
			r.AbsIDLen, r.AbsENLen, r.License, r.KeywordN, r.KWSource, r.Complete)
	}
	return b.String()
}

// truncateRunes = potong aman-rune (tak membelah karakter multi-byte).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
