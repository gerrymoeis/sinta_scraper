package ojs

import (
	"encoding/json"
	"testing"
)

// Fixture = bukti P3 (doc 42 §7.3) + JUTIF verbatim (doc 11 §2 — contoh
// rank 2, fixture parser): pola (a) APC tunggal IDR (j26 Millah) · (b) multi-
// kategori USD submission=0.00 + publication>0 (j681 BEEI) · free-of-charge
// (j817) · noise substring (bootstrapcdn/WebFeed TIDAK match word-boundary).

// JUTIF verbatim — input user 8 Okt 2026 (doc 11 §2).
const jutifHTML = `<html><body><p>
The submission and review of manuscripts is free of charge, but if your
manuscript is accepted for publication, you are required to pay to cover
the publication fee because the JUTIF journal is independently managed.
The current Article Processing Charge (APC), is as follows:
</p><ul>
<li>IDR 1,500,000-. for domestic (Indonesian) author</li>
<li>100 USD for the first author abroad (outside Indonesia).</li>
</ul></body></html>`

// Pola j26 Millah (P3): APC tunggal IDR.
const millahHTML = `<html><body><h2>Author Fees</h2><p>
This journal charges only article processing charge of Rp9,000,000.00.
</p></body></html>`

// Pola j681 BEEI (P3): submission/review 0.00 USD + publication USD.
const beeiHTML = `<html><body><h2>Author Fees</h2><p>
Article Submission and Review: 0.00 (USD). Publication Fee:
USD 870.00 for the first article, USD 50.00 for subsequent articles.
</p></body></html>`

// Pola j817 ADDIN (P3): free of charge tanpa nominal.
const addinHTML = `<html><body><p>
This journal does not charge the author. All processes from submission
to publishing are free of charge.
</p></body></html>`

func TestExtractFeesJUTIF(t *testing.T) {
	got := ExtractFees("https://journal.ut.ac.id/author/fees", []byte(jutifHTML))
	if got == nil {
		t.Fatal("entry = nil, want ditemukan")
	}
	if got.Submission == nil || !got.Submission.IsFree {
		t.Errorf("submission = %+v, want is_free", got.Submission)
	}
	if got.Review == nil || !got.Review.IsFree {
		t.Errorf("review = %+v, want is_free", got.Review)
	}
	if len(got.Publish) != 2 {
		t.Fatalf("publish = %v, want 2 nilai", got.Publish)
	}
	p1, p2 := got.Publish[0], got.Publish[1]
	if p1.Amount != 1_500_000 || p1.Currency != "IDR" || p1.Category != "domestic" {
		t.Errorf("publish[0] = %+v (want 1500000/IDR/domestic)", p1)
	}
	if p2.Amount != 100 || p2.Currency != "USD" || p2.Category != "foreign-first" {
		t.Errorf("publish[1] = %+v (want 100/USD/foreign-first)", p2)
	}
	if got.SourceURL == "" || got.Raw == "" {
		t.Error("raw & source_url wajib terisi (audit)")
	}
}

// Pola submisi & review BERBAYAR (konfirmasi user 12 Okt — submisi tdk
// selalu gratis; FeePart = {is_free | amount+currency} menopang ketiganya).
const submReviewBerbayarHTML = `<html><body><h2>Author Fees</h2><p>
Article Submission Fee: IDR 500,000. Review Fee: IDR 250,000.
Publication Fee: IDR 1,500,000.
</p></body></html>`

func TestExtractFeesSubmisiReviewBerbayar(t *testing.T) {
	got := ExtractFees("https://x.id/author/fees", []byte(submReviewBerbayarHTML))
	if got == nil {
		t.Fatal("entry = nil")
	}
	if got.Submission == nil || got.Submission.IsFree ||
		got.Submission.Amount != 500_000 || got.Submission.Currency != "IDR" {
		t.Errorf("submission = %+v, want {amount 500000 IDR}", got.Submission)
	}
	if got.Review == nil || got.Review.IsFree ||
		got.Review.Amount != 250_000 || got.Review.Currency != "IDR" {
		t.Errorf("review = %+v, want {amount 250000 IDR}", got.Review)
	}
	if len(got.Publish) != 1 || got.Publish[0].Amount != 1_500_000 {
		t.Errorf("publish = %v, want [{1500000 IDR}]", got.Publish)
	}
}

// Pola campuran: submisi berbayar, review gratis — 1 JSON menopang 2 state
// berbeda dlm 1 halaman (bkti independensi per-bagian).
const campuranHTML = `<html><body><h2>Author Fees</h2><p>
The submission fee is IDR 300,000. Peer review is free of charge.
</p></body></html>`

func TestExtractFeesCampuranSubmBerbayarReviewGratis(t *testing.T) {
	got := ExtractFees("https://x.id/fees", []byte(campuranHTML))
	if got == nil {
		t.Fatal("entry = nil")
	}
	if got.Submission == nil || got.Submission.IsFree ||
		got.Submission.Amount != 300_000 {
		t.Errorf("submission = %+v, want {amount 300000}", got.Submission)
	}
	if got.Review == nil || !got.Review.IsFree {
		t.Errorf("review = %+v, want is_free", got.Review)
	}
}

func TestExtractFeesMillah(t *testing.T) {
	got := ExtractFees("https://millah.id/Millah/principles/fees", []byte(millahHTML))
	if got == nil {
		t.Fatal("entry = nil")
	}
	if got.Submission != nil || got.Review != nil {
		t.Errorf("submission/review = %+v/%+v (want nil — hanya APC)", got.Submission, got.Review)
	}
	if len(got.Publish) != 1 {
		t.Fatalf("publish = %v, want 1", got.Publish)
	}
	p := got.Publish[0]
	if p.Amount != 9_000_000 || p.Currency != "IDR" || p.Category != "domestic" {
		t.Errorf("publish[0] = %+v (want 9000000/IDR/domestic)", p)
	}
}

func TestExtractFeesBEEI(t *testing.T) {
	got := ExtractFees("https://beei.org/index.php/EEI/about/submissions", []byte(beeiHTML))
	if got == nil {
		t.Fatal("entry = nil")
	}
	// 0.00 USD submission & review = is_free (bukan nominal — bukti §11.3).
	if got.Submission == nil || !got.Submission.IsFree || got.Submission.Amount != 0 {
		t.Errorf("submission = %+v, want is_free amount=0", got.Submission)
	}
	if got.Review == nil || !got.Review.IsFree {
		t.Errorf("review = %+v, want is_free", got.Review)
	}
	if len(got.Publish) != 2 {
		t.Fatalf("publish = %v, want 2", got.Publish)
	}
	if got.Publish[0].Amount != 870 || got.Publish[0].Currency != "USD" {
		t.Errorf("publish[0] = %+v (want 870/USD)", got.Publish[0])
	}
	if got.Publish[1].Amount != 50 {
		t.Errorf("publish[1] = %+v (want 50)", got.Publish[1])
	}
}

func TestExtractFeesFreeOfCharge(t *testing.T) {
	got := ExtractFees("https://addin.or.id/about", []byte(addinHTML))
	if got == nil {
		t.Fatal("entry = nil")
	}
	if got.Submission == nil || !got.Submission.IsFree {
		t.Errorf("submission = %+v, want is_free", got.Submission)
	}
	if got.Review == nil || !got.Review.IsFree {
		t.Errorf("review = %+v, want is_free", got.Review)
	}
	// Revisi 12 Okt: "…to publishing are free of charge" — publish free
	// sah diwakili entri is_free (bukan kosong/ambigu).
	if len(got.Publish) != 1 || !got.Publish[0].IsFree {
		t.Errorf("publish = %v, want [{is_free:true}]", got.Publish)
	}
}

// Revisi 12 Okt: "Publication Fee: 0.00" = publication gratis (bukan
// di-skip) — submisi boleh berbayar/null, publication boleh gratis.
const pubFreeHTML = `<html><body><h2>Fees</h2><p>
Article Processing Charge (APC): Publication Fee: 0.00 (USD).
</p></body></html>`

func TestExtractFeesPublikasiGratis(t *testing.T) {
	got := ExtractFees("https://x.id/author/fees", []byte(pubFreeHTML))
	if got == nil {
		t.Fatal("entry = nil")
	}
	if len(got.Publish) != 1 || !got.Publish[0].IsFree || got.Publish[0].Currency != "USD" {
		t.Errorf("publish = %v, want [{is_free currency USD}]", got.Publish)
	}
}

func TestExtractFeesTanpaKeyword(t *testing.T) {
	// Halaman biasa + noise substring — TIDAK ada keyword word-boundary → nil
	// (bukti: "apc" di bootstrapcdn & "fee" di webfeed tak match dgn \b).
	if got := ExtractFees("https://x.id/", []byte(
		`<html><body><p>Situs CDN bootstrapcdn dan WebFeedGatewayPlugin.</p></body></html>`)); got != nil {
		t.Errorf("entry = %+v, want nil", got)
	}
	if got := ExtractFees("https://x.id/", []byte(
		`<html><body><p>Tentang jurnal kami.</p></body></html>`)); got != nil {
		t.Errorf("entry = %+v, want nil", got)
	}
}

// Revisi 12 Okt: frasa free + adversative "but" + publish tanpa nominal
// terparse — publish TIDAK boleh jadi free (jujur: kosong).
const freeTapiButHTML = `<html><body><p>
The submission is free of charge, but the publication fee applies.
</p></body></html>`

func TestExtractFeesFreeTapiBut(t *testing.T) {
	got := ExtractFees("https://x.id/about", []byte(freeTapiButHTML))
	if got == nil {
		t.Fatal("entry = nil")
	}
	if got.Submission == nil || !got.Submission.IsFree {
		t.Errorf("submission = %+v, want is_free", got.Submission)
	}
	if len(got.Publish) != 0 {
		t.Errorf("publish = %v, want kosong (adversative but)", got.Publish)
	}
}

// ==== Revisi 13 Okt 2026 (approve user) — recall frasa gratis + split ====

// 4 contoh frasa gratis nyata (input user) — kurasi pola, BUKAN regex generik.
const frasaGratisHTML = `<html><body><h2>Author Fees</h2>
<p>IJID charges no fees whatsoever. From submission to publication, no cost
are borne by the author. This is because the journal is fully supported by
UIN Sunan Kalijaga Yogyakarta, Indonesia.</p>
<p>This is a diamond open access journal. No article processing charges
(APCs) are required.</p>
<p>APC: No publication charges</p>
<p>Note that Information Research does not charge publication fees.</p>
</body></html>`

func TestExtractFeesFrasaGratisVariasi(t *testing.T) {
	got := ExtractFees("https://x.id/about/fees", []byte(frasaGratisHTML))
	if got == nil {
		t.Fatal("entry = nil")
	}
	if got.Submission == nil || !got.Submission.IsFree {
		t.Errorf("submission = %+v, want is_free", got.Submission)
	}
	if got.Review == nil || !got.Review.IsFree {
		t.Errorf("review = %+v, want is_free", got.Review)
	}
	// "no cost … to publication" & "No article processing charges" — kedua
	// frasa menutup publishing → publish is_free (bukan kosong).
	if len(got.Publish) != 1 || !got.Publish[0].IsFree {
		t.Errorf("publish = %v, want [{is_free:true}]", got.Publish)
	}
}

// Frasa gratis SKOP PUBLIKASI saja — "no APC"/"no publication charges"
// menular ke publish TANPA mengisi submission/review (jujur).
const pubScopedHTML = `<html><body><h2>Fees</h2><p>
This journal levies no APC for accepted articles.
</p></body></html>`

func TestExtractFeesFrasaPublikasiSkop(t *testing.T) {
	got := ExtractFees("https://x.id/publishing", []byte(pubScopedHTML))
	if got == nil {
		t.Fatal("entry = nil")
	}
	if got.Submission != nil || got.Review != nil {
		t.Errorf("submission/review = %+v/%+v, want nil (skop publikasi only)",
			got.Submission, got.Review)
	}
	if len(got.Publish) != 1 || !got.Publish[0].IsFree {
		t.Errorf("publish = %v, want [{is_free:true}]", got.Publish)
	}
}

// Guard anti-FP: "no hidden/additional fees" BUKAN gratis (transparansi
// pricing) — regex generik "no … fee" DITOLAK krn ini.
const noHiddenFeesHTML = `<html><body><h2>Author Fees</h2><p>
We charge IDR 500,000 for submission. There are no hidden fees.
</p></body></html>`

func TestExtractFeesNoHiddenFeesBukanGratis(t *testing.T) {
	got := ExtractFees("https://x.id/fees", []byte(noHiddenFeesHTML))
	if got == nil {
		t.Fatal("entry = nil")
	}
	if got.Submission == nil || got.Submission.IsFree ||
		got.Submission.Amount != 500_000 {
		t.Errorf("submission = %+v, want {amount 500000} (bukan is_free)", got.Submission)
	}
	if got.Review != nil && got.Review.IsFree {
		t.Errorf("review = %+v, want TIDAK is_free", got.Review)
	}
}

// Split gabungan (revisi 13 Okt — approve user b): "Submission and Review:
// IDR 300,000" → submission 150k + review 150k (jumlah = teks asli).
const gabunganBerbayarHTML = `<html><body><h2>Author Fees</h2><p>
Article Submission and Review: IDR 300,000.
</p></body></html>`

func TestExtractFeesGabunganSplitRata(t *testing.T) {
	got := ExtractFees("https://x.id/fees", []byte(gabunganBerbayarHTML))
	if got == nil {
		t.Fatal("entry = nil")
	}
	if got.Submission == nil || got.Submission.Amount != 150_000 ||
		got.Submission.Currency != "IDR" {
		t.Errorf("submission = %+v, want {150000 IDR}", got.Submission)
	}
	if got.Review == nil || got.Review.Amount != 150_000 ||
		got.Review.Currency != "IDR" {
		t.Errorf("review = %+v, want {150000 IDR}", got.Review)
	}
}

// Split ganjil: sisa 1 ke review — jumlah tetap utuh = teks asli.
const gabunganGanjilHTML = `<html><body><h2>Fees</h2><p>
Submission and Review Fee: IDR 300,001.
</p></body></html>`

func TestExtractFeesGabunganSplitGanjil(t *testing.T) {
	got := ExtractFees("https://x.id/fees", []byte(gabunganGanjilHTML))
	if got == nil {
		t.Fatal("entry = nil")
	}
	if got.Submission == nil || got.Submission.Amount != 150_000 {
		t.Errorf("submission = %+v, want {150000}", got.Submission)
	}
	if got.Review == nil || got.Review.Amount != 150_001 {
		t.Errorf("review = %+v, want {150001} (sisa ganjil)", got.Review)
	}
}

func TestParseAmountNum(t *testing.T) {
	kasus := map[string]int64{
		"1,500,000":    1_500_000,
		"9,000,000.00": 9_000_000,
		"1.500.000":    1_500_000,
		"870":          870,
		"100":          100,
		"0.00":         0,
		"1,500,000-":   1_500_000,
		"25.50":        26, // desimal langka → dibulatkan (revisi 12 Okt)
	}
	for raw, want := range kasus {
		got, ok := parseAmountNum(raw)
		if !ok || got != want {
			t.Errorf("parseAmountNum(%q) = %v, %v (want %v)", raw, got, ok, want)
		}
	}
}

// TestFeeEntryJSONShape = kunci struktur kolom journals.author_fees persis
// §11.3 (approve K6-A + revisi 12 Okt) — cegah regresi tag JSON/omitempty.
func TestFeeEntryJSONShape(t *testing.T) {
	e := &FeeEntry{
		Submission: &FeePart{IsFree: true},
		Review:     &FeePart{Amount: 250_000, Currency: "IDR"},
		Publish: []FeeValue{
			{Amount: 1_500_000, Currency: "IDR", Category: "domestic"},
			{IsFree: true},
		},
		Raw:       "Author Fees ...",
		SourceURL: "https://x.id/fees",
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"submission":{"is_free":true},"review":{"amount":250000,"currency":"IDR"},` +
		`"publish":[{"amount":1500000,"currency":"IDR","category":"domestic"},{"is_free":true}],` +
		`"raw":"Author Fees ...","source_url":"https://x.id/fees"}`
	if string(b) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", b, want)
	}
}
