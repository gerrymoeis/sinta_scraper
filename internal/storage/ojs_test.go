package storage

import (
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"testing"
)

// TestUpsertSourceProfile — idempotent (rerun timpa penuh) + guard evidence.
func TestUpsertSourceProfile(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "ojsp.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: 1, Name: "Jurnal A", SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}

	p := SourceProfile{
		JournalID: 1, Platform: "ojs",
		Evidence: "open-journal-systems; citation_", Confidence: "high",
		CanonicalURL: "https://a.id/",
	}
	if err := st.UpsertSourceProfile(p); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Rerun (platform beda) = timpa penuh, bukan baris ganda.
	p.Platform, p.Evidence, p.Confidence = "wordpress", "wp-json", "high"
	if err := st.UpsertSourceProfile(p); err != nil {
		t.Fatalf("upsert ulang: %v", err)
	}

	var plat, ev, conf string
	var n int
	if err := st.db.QueryRow(
		`SELECT platform, detection_evidence, platform_confidence,
			(SELECT COUNT(*) FROM journal_source_profile WHERE journal_id = 1)
		FROM journal_source_profile WHERE journal_id = 1`,
	).Scan(&plat, &ev, &conf, &n); err != nil {
		t.Fatal(err)
	}
	if plat != "wordpress" || ev != "wp-json" || conf != "high" || n != 1 {
		t.Errorf("got platform=%q evidence=%q conf=%q rows=%d (want wordpress/wp-json/high/1)", plat, ev, conf, n)
	}

	// Guard: platform kosong & evidence kosong utk platform berbukti.
	if err := st.UpsertSourceProfile(SourceProfile{JournalID: 1, Platform: "ojs"}); err == nil {
		t.Error("evidence kosong utk ojs harus ditolak")
	}
	if err := st.UpsertSourceProfile(SourceProfile{JournalID: 1, Platform: ""}); err == nil {
		t.Error("platform kosong harus ditolak")
	}
	// unknown = jujur tanpa marker → evidence boleh keterangan status.
	if err := st.UpsertSourceProfile(SourceProfile{
		JournalID: 1, Platform: "unknown", Evidence: "status=403",
	}); err != nil {
		t.Errorf("unknown dgn evidence status: %v", err)
	}
}

// TestUpsertPolicies — idempotent per (jurnal, tipe, url) + guard whitelist.
func TestUpsertPolicies(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "ojspol.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: 1, Name: "Jurnal B", SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}

	rows := []PolicyRow{
		{PolicyType: "fee", URL: "https://b.id/about/fees", HTTPStatus: 200, RawSnippet: "Author Fees"},
		{PolicyType: "copyright", URL: "https://b.id/about", HTTPStatus: 200},
	}
	if err := st.UpsertPolicies(1, rows); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Ulang identik dgn status berubah → update, bukan ganda.
	rows[0].HTTPStatus = 403
	rows[0].RawSnippet = "WAF"
	if err := st.UpsertPolicies(1, rows); err != nil {
		t.Fatalf("upsert ulang: %v", err)
	}

	var n, feeStatus int
	var snippet string
	if err := st.db.QueryRow(
		`SELECT COUNT(*),
			COALESCE(MAX(CASE WHEN policy_type='fee' THEN http_status END), 0),
			COALESCE(MAX(CASE WHEN policy_type='fee' THEN raw_snippet END), '')
		FROM journal_policy WHERE journal_id = 1`,
	).Scan(&n, &feeStatus, &snippet); err != nil {
		t.Fatal(err)
	}
	if n != 2 || feeStatus != 403 || snippet != "WAF" {
		t.Errorf("got rows=%d feeStatus=%d snippet=%q (want 2/403/WAF)", n, feeStatus, snippet)
	}

	// Guard tipe di luar whitelist & url kosong.
	if err := st.UpsertPolicies(1, []PolicyRow{{PolicyType: "feed", URL: "https://b.id/x"}}); err == nil {
		t.Error("tipe 'feed' di luar whitelist harus ditolak")
	}
	if err := st.UpsertPolicies(1, []PolicyRow{{PolicyType: "fee", URL: ""}}); err == nil {
		t.Error("url kosong harus ditolak")
	}
}

// TestPolicyPrevMap — snapshot status/checked_at per URL (bahan deteksi
// persisten Opsi 2): status 0 = network gagal, checked_at terisi = sudah
// dicek run lalu; journal lain tidak ikut bocor.
func TestPolicyPrevMap(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "ojsprev.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertJournals([]sinta.Journal{
		{ID: 1, Name: "A", SintaRank: 1, SourcePage: 1},
		{ID: 2, Name: "B", SintaRank: 1, SourcePage: 1},
	}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}
	if err := st.UpsertPolicies(1, []PolicyRow{
		{PolicyType: "fee", URL: "https://a.id/fees", HTTPStatus: 0},
		{PolicyType: "about", URL: "https://a.id/about", HTTPStatus: 200},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPolicies(2, []PolicyRow{
		{PolicyType: "fee", URL: "https://b.id/fees", HTTPStatus: 404},
	}); err != nil {
		t.Fatal(err)
	}

	prev, err := st.PolicyPrevMap(1)
	if err != nil {
		t.Fatalf("PolicyPrevMap: %v", err)
	}
	if len(prev) != 2 {
		t.Fatalf("len = %d, want 2", len(prev))
	}
	p, ok := prev["https://a.id/fees"]
	if !ok || p.HTTPStatus != 0 || p.CheckedAt == "" {
		t.Errorf("fees: %+v ok=%v — want status 0 + checked_at terisi (network run lalu)", p, ok)
	}
	if p2 := prev["https://a.id/about"]; p2.HTTPStatus != 200 {
		t.Errorf("about: %+v, want 200", p2)
	}
	if _, ok := prev["https://b.id/fees"]; ok {
		t.Error("baris journal 2 tidak boleh bocor ke map journal 1")
	}
}

// TestSetAuthorFees — validasi JSON + overwrite + rows-affected jujur.
func TestSetAuthorFees(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "ojsfee.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: 1, Name: "Jurnal C", SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}

	const fees1 = `{"submission":{"is_free":true},"review":{"is_free":true},"publish":[{"amount":870,"currency":"USD","category":"foreign"}]}`
	n, err := st.SetAuthorFees(1, fees1)
	if err != nil || n != 1 {
		t.Fatalf("insert: n=%d err=%v (want 1, nil)", n, err)
	}
	// Overwrite = rerun membawa hasil segar.
	const fees2 = `{"publish":[{"amount":9000000,"currency":"IDR","category":"domestic"}],"raw":"Author Fees"}`
	if n, err = st.SetAuthorFees(1, fees2); err != nil || n != 1 {
		t.Fatalf("overwrite: n=%d err=%v", n, err)
	}
	var got string
	if err := st.db.QueryRow(`SELECT author_fees FROM journals WHERE id = 1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != fees2 {
		t.Errorf("author_fees = %q (want %q)", got, fees2)
	}
	// JSON rusak & journal tak ada → jujur.
	if _, err := st.SetAuthorFees(1, `{tidak valid`); err == nil {
		t.Error("JSON rusak harus ditolak")
	}
	if n, err = st.SetAuthorFees(999, `{}`); err != nil || n != 0 {
		t.Errorf("journal 999: n=%d err=%v (want 0, nil)", n, err)
	}
}

// TestMarkCanonicalURL — pemenang =1, kalah =0 (riwayat E7g tetap), url bukan
// kandidat → error.
func TestMarkCanonicalURL(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "ojscan.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertJournals([]sinta.Journal{{ID: 1, Name: "Jurnal D", SintaRank: 1, SourcePage: 1}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}
	const ts = "2026-10-12T08:00:00Z"
	// Kandidat: sinta http (kalah) + garuda https (menang).
	if _, err := st.UpsertJournalURL(1, "http://d.id/", "ojs", "sinta", statusPtr(200), "https://d.id/", ts); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertJournalURL(1, "https://d.id/", "ojs", "garuda", statusPtr(200), "https://d.id/", ts); err != nil {
		t.Fatal(err)
	}

	if err := st.MarkCanonicalURL(1, "https://d.id/", statusPtr(200), "https://d.id/", ts); err != nil {
		t.Fatalf("mark: %v", err)
	}
	type baris struct {
		url   string
		canon int
	}
	rows, err := st.db.Query(`SELECT url, is_canonical FROM journal_urls WHERE journal_id = 1 ORDER BY url`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var semua []baris
	for rows.Next() {
		var b baris
		if err := rows.Scan(&b.url, &b.canon); err != nil {
			t.Fatal(err)
		}
		semua = append(semua, b)
	}
	if len(semua) != 2 {
		t.Fatalf("jumlah baris = %d, want 2", len(semua))
	}
	if semua[0].url != "http://d.id/" || semua[0].canon != 0 {
		t.Errorf("kalah: %+v (want http…/0)", semua[0])
	}
	if semua[1].url != "https://d.id/" || semua[1].canon != 1 {
		t.Errorf("menang: %+v (want https…/1)", semua[1])
	}
	// Url bukan kandidat → error (driver wajib UpsertJournalURL dulu).
	if err := st.MarkCanonicalURL(1, "https://lain.id/", statusPtr(200), "", ts); err == nil {
		t.Error("url bukan kandidat harus ditolak")
	}
}
