package corpus

import (
	"strings"
	"testing"
)

// TestCorpusAudit — filter jid + deteksi sumber keyword + tabel String.
func TestCorpusAudit(t *testing.T) {
	st := seedGoldDB(t) // jurnal 1: g1/g2 author-id · g3 crossref-en · g4 kosong

	rows, err := CorpusAudit(st, nil)
	if err != nil {
		t.Fatalf("CorpusAudit: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d — mau 4 (semua jurnal)", len(rows))
	}
	byRec := map[string]AuditRow{}
	for _, r := range rows {
		byRec[r.RecordID] = r
	}
	// g4 tanpa keyword → KWSource=kosong · g3 crossref + abs_en terisi.
	if g4 := byRec["g4"]; g4.KeywordN != 0 || g4.KWSource != "kosong" {
		t.Errorf("g4 nkw/src = %d/%q — mau 0/kosong", g4.KeywordN, g4.KWSource)
	}
	if g3 := byRec["g3"]; g3.KWSource != "crossref" || g3.AbsENLen == 0 || g3.AbsIDLen != 0 {
		t.Errorf("g3 src/absEN/absID = %q/%d/%d — mau crossref/>0/0",
			g3.KWSource, g3.AbsENLen, g3.AbsIDLen)
	}
	if g1 := byRec["g1"]; g1.KeywordN != 3 || g1.KWSource != "author" || g1.Language != LanguageID {
		t.Errorf("g1 nkw/src/lang = %d/%q/%q — mau 3/author/id", g1.KeywordN, g1.KWSource, g1.Language)
	}
	// Filter jid.
	only999, err := CorpusAudit(st, []int64{999})
	if err != nil {
		t.Fatalf("filter 999: %v", err)
	}
	if len(only999) != 0 {
		t.Errorf("filter 999 = %d baris — mau 0", len(only999))
	}
	// Tabel String memuat header + record id.
	out := rows.String()
	for _, want := range []string{"AUDIT KORPUS", "record_id", "kw_src", "g1", "g3"} {
		if !strings.Contains(out, want) {
			t.Errorf("String() tanpa %q:\n%s", want, out)
		}
	}
}

// TestTruncateRunes — potong aman-rune (judul multi-byte tak dipoteng).
func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("abstrak gizi balita", 8); got != "abstrak…" {
		t.Errorf("trunc = %q — mau %q", got, "abstrak…")
	}
	if got := truncateRunes("pendek", 10); got != "pendek" {
		t.Errorf("trunc pendek = %q — mau utuh", got)
	}
	// Judul unicode (multi-byte): tak boleh belah karakter.
	if got := truncateRunes("Analisis—Kualitas", 9); got != "Analisis…" {
		t.Errorf("trunc unicode = %q — mau %q", got, "Analisis…")
	}
}
