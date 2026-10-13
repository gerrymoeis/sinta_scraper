package ojs

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// ==== fees.go — ekstraksi blok author fee (doc 42 §7.3 + doc 11 Bagian 2) ====
//
// Aturan P3 (LOCKED): (2) blok fee HANYA dinilai dari halaman policy — feed/
// abstrak diabaikan (false-positive j16 "charge rates" & j3974 dari rss) →
// tanggung jawab driver: hanya panggil ExtractFees utk halaman lolos
// FindPolicyLinks — (3) deteksi blok dgn keyword word-boundary → "free of
// charge"/"tidak dipungut" = is_free SAH (bukan gagal) → nominal IDR|USD|Rp
// + jenis dari kata menempel — (4) representasi Opsi C (K6-A §11.3): =
// {submission, review, publish[], raw, source_url} — (5) sebagian boleh
// kosong = jujur (bukti P3: 2 pola nyata — APC tunggal IDR j26 · multi-
// kategori USD j681 submission=0.00 + publication>0).
//
// Revisi 12 Okt 2026 (approve user — konfirmasi desain fee):
//   - Amount float64 → int64 (duit = bilangan bulat; "870.00"→870,
//     desimal langka dibulatkan, teks asli di raw).
//   - publish[] +IsFree — publication TIDAK selalu berbayar: "Publication
//     Fee: 0.00" & frasa free yg mencakup publishing (bkti ADDIN) = is_free,
//     bukan kosong/ambigu.
//
// Revisi 13 Okt 2026 (approve user — recall frasa gratis + split gabungan):
//   - reFree diperluas kurasi (charges no fees/no cost/does not charge/
//     without charge/gratis…) + reFreePub frasa gratis skop publikasi
//     ("No publication charges"/"no APC") — publish is_free tanpa menular
//     ke submission/review.
//   - "Submission and Review: IDR 300,000" → split rata 150k+150k (jumlah
//     = teks asli; frontend tinggal jumlahkan).

// reFeeKeyword = detektor blok fee (word-boundary — bukti substring "apc" di
// "bootstrapcdn" & "fee" di "webfeed" TIDAK match). "biaya" utk theme ID.
var reFeeKeyword = regexp.MustCompile(`(?i)\b(fee|fees|charge|charges|apc|biaya)\b`)

// reFree = frasa "gratis" UMUM — menutup seluruh proses (§7.3 butir 3).
// Revisi 13 Okt 2026 (approve user): +kurasi pola frasa nyata — "charges no
// fees whatsoever" (IJID) · "no cost are borne" · "does not charge the
// author" (ADDIN) · "without charge" · varian ID: gratis/bebas biaya/tanpa
// biaya. DITOLAK: regex generik "no … fee" — FP thd "no hidden/additional
// fees" (transparansi pricing, BUKAN gratis) → #1 Validitas.
var reFree = regexp.MustCompile(`(?i)free[ -]of[ -]charge|tidak dipungut|charges? no fees?|\bno costs?\b|(?:does not|do not|doesn't|don't) charge|without (?:any )?charge|\bgratis\b|\bbebas biaya\b|\btanpa biaya\b`)

// reFreePub = frasa "gratis" SKOP PUBLIKASI saja (revisi 13 Okt — "No
// publication charges" · "No article processing charges (APCs) required" ·
// "no APC"): publish ikut is_free TANPA menular ke submission/review (jujur
// — teks hanya menyebut publikasi).
var reFreePub = regexp.MustCompile(`(?i)no publication (?:fees?|charges?)|no article processing (?:charges?|apcs?)|\bno apcs?\b`)

// Nominal: "IDR 1,500,000" · "Rp9,000,000.00" · "100 USD" · "0.00 (USD)".
var reNominal = regexp.MustCompile(
	`(?i)\b(idr|usd|rp)\.?\s*\(?\s*([\d][\d.,]*)\b|\b([\d][\d.,]*)\s*\)?\s*\(?\s*(idr|usd|rupiah)\b`)

// Jenis dari kata menempel (jarak terdekat ke nominal dalam 1 baris teks —
// pola P3: baris per tarif; fallback = window global). reSubmRev = frasa
// berpasangan "Submission and Review: 0.00" (bukti j681 BEEI — satu nominal
// utk dua jenis; solo "Review" di dlm frasa di-drop via overlap).
var (
	reSubm    = regexp.MustCompile(`(?i)\b(submission|submit|manuscript)\b`)
	reRev     = regexp.MustCompile(`(?i)\b(review|referee|peer)\b`)
	rePub     = regexp.MustCompile(`(?i)\b(publish(?:ing)?|publication|apc|article processing|processing charge|page charge)\b`)
	reSubmRev = regexp.MustCompile(`(?i)\b(submission|submit)\b[^.]{0,48}\b(review|referee)\b|\b(review|referee)\b[^.]{0,48}\b(submission|submit)\b`)
	reBlock   = regexp.MustCompile(`(?i)</(p|li|tr|h[1-6]|div)>|<br\s*/?>`)
	reTag     = regexp.MustCompile(`(?s)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<[^>]+>`)
)

// FeePart = submission/review (objek tunggal; §11.3 — revisi 12 Okt: amount
// int64, bukan float64 — nominal duit = bilangan bulat, "870.00"→870).
type FeePart struct {
	IsFree   bool   `json:"is_free,omitempty"`
	Amount   int64  `json:"amount,omitempty"`
	Currency string `json:"currency,omitempty"`
}

// FeeValue = entri publish (array — multi-kategori/multi-mata-uang; §11.3 —
// revisi 12 Okt: +IsFree — publication TIDAK selalu berbayar, "0.00"/frasa
// free yg mencakup publishing = is_free sah, bukan kosong/ambigu).
type FeeValue struct {
	IsFree   bool   `json:"is_free,omitempty"`
	Amount   int64  `json:"amount,omitempty"`
	Currency string `json:"currency,omitempty"`
	Category string `json:"category,omitempty"` // domestic|foreign[-first]
}

// FeeEntry = isi kolom journals.author_fees (K6-A Opsi C §11.3).
type FeeEntry struct {
	Submission *FeePart   `json:"submission,omitempty"`
	Review     *FeePart   `json:"review,omitempty"`
	Publish    []FeeValue `json:"publish,omitempty"`
	Raw        string     `json:"raw,omitempty"`        // snippet audit (cap 500)
	SourceURL  string     `json:"source_url,omitempty"` // halaman policy asal
}

// ExtractFees mengekstrak blok fee dari halaman policy (pageURL = final_url
// utk audit). Return nil = TIDAK ada keyword fee (bukan halaman fee → driver
// catat NO_FEE_PAGE / lanjut). Keyword ada tapi ekstraksi kosong → entry
// dgn Raw saja (jujur — teks tak terstruktur, jangan nebak nominal).
//
// Jenis fee = kata MENEMPEL TERDEKAT pada baris teks yang sama (pola P3:
// baris per tarif; "Submission and Review: 0.00" = dua jenis, "Publication
// Fee: USD 870" meski baris juga memuat kata "submission" = publish).
func ExtractFees(pageURL string, body []byte) *FeeEntry {
	// Strip HTML dgn mempertahankan baris (</p></li>/br → \n) — pola P3: tiap
	// baris = satu tarif/kategori.
	text := reBlock.ReplaceAllString(string(body), "\n")
	text = html.UnescapeString(reTag.ReplaceAllString(text, " "))
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		lines[i] = strings.Join(strings.Fields(ln), " ")
	}
	text = strings.Join(lines, "\n")

	locs := reFeeKeyword.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return nil
	}
	// Window blok = keyword pertama−200 … terakhir+400 (heading ± paragraf;
	// cap 1600 char — P3: pola "Author Fees" + paragraf/list tarif).
	start := locs[0][0] - 200
	if start < 0 {
		start = 0
	}
	end := locs[len(locs)-1][1] + 400
	if end > len(text) {
		end = len(text)
	}
	window := text[start:end]
	raw := window
	if len(raw) > 500 {
		raw = raw[:500]
	}

	entry := &FeeEntry{Raw: raw, SourceURL: pageURL}
	isFree := reFree.MatchString(window)
	isFreePub := reFreePub.MatchString(window)

	for _, m := range reNominal.FindAllStringSubmatchIndex(window, -1) {
		var cur, num string
		if m[2] >= 0 { // branch 1: currency-first
			cur, num = window[m[2]:m[3]], window[m[4]:m[5]]
		} else { // branch 2: number-first
			num, cur = window[m[6]:m[7]], window[m[8]:m[9]]
		}
		amount, ok := parseAmountNum(num)
		if !ok {
			continue
		}
		cur = normCurrency(cur)

		// Ctx baris utk jenis; ±60 char DALAM BARIS YG SAMA utk kategori
		// penulis (jangan bocor ke klausa nominal tetangga — bukti unit test
		// JUTIF/BEEI).
		ctx, posNom := barisCtx(window, m[0])
		kctx := ctxKategori(window, m[0], m[1])

		switch pilihJenis(ctx, posNom, window) {
		case "submission+review":
			// Revisi 13 Okt 2026 (approve user): nominal gabungan dibagi
			// rata — "Submission and Review: IDR 300,000" → submission
			// 150,000 + review 150,000 (jumlah = teks asli; frontend
			// tinggal jumlahkan). Ganjil → sisa 1 ke review; 0.00 →
			// keduanya is_free (bukti BEEI); 1 → lama (tak terbagi).
			if amount >= 2 {
				half := amount / 2
				entry.setPart(&entry.Submission, half, cur)
				entry.setPart(&entry.Review, amount-half, cur)
			} else {
				entry.setPart(&entry.Submission, amount, cur)
				entry.setPart(&entry.Review, amount, cur)
			}
		case "submission":
			entry.setPart(&entry.Submission, amount, cur)
		case "review":
			entry.setPart(&entry.Review, amount, cur)
		case "publish":
			if amount == 0 {
				// "Publication Fee: 0.00" = publication gratis (revisi 12
				// Okt — sebelumnya di-skip; publication tdk selalu berbayar).
				entry.Publish = append(entry.Publish, FeeValue{
					IsFree:   true,
					Currency: cur,
					Category: classifyCategory(kctx, cur),
				})
				continue
			}
			entry.Publish = append(entry.Publish, FeeValue{
				Amount: amount, Currency: cur,
				Category: classifyCategory(kctx, cur),
			})
			// "" = tak terklasifikasi → dilewati (sebagian boleh kosong = jujur)
		}
	}

	// "free of charge" (§7.3 butir 3) utk kategori BELUM terisi nominal —
	// nominal nyata > frasa umum bila keduanya ada. Publish ikut gratis HANYA
	// bila frasa free-nya mencakup publishing (bukti ADDIN "from submission
	// to publishing are free of charge"); frasa free tuk submission/review
	// doang (JUTIF: "…free of charge, BUT …publication fee") TIDAK menular —
	// JUTIF dobel aman krn publish nominalnya sudah terisi.
	if isFree {
		if entry.Submission == nil {
			entry.Submission = &FeePart{IsFree: true}
		}
		if entry.Review == nil {
			entry.Review = &FeePart{IsFree: true}
		}
		if len(entry.Publish) == 0 && freeCoversPublish(window) {
			entry.Publish = append(entry.Publish, FeeValue{IsFree: true})
		}
	}
	// Frasa gratis skop publikasi (revisi 13 Okt): "No publication charges"
	// / "no APC" → publish is_free saja; submission/review TIDAK menular
	// (teks tak menyebutnya — jujur).
	if isFreePub && len(entry.Publish) == 0 {
		entry.Publish = append(entry.Publish, FeeValue{IsFree: true})
	}
	return entry
}

// freeCoversPublish = apakah frasa free of charge benar menutup publishing?
// (±60 char sekitar tiap match reFree ada kata publish) — DITOLAK bila di
// antara frasa free & kata publish ada adversative (but/however/except/
// namun/kecuali): bukti JUTIF "…free of charge, BUT … publication fee" =
// publication TIDAK gratis. ADDIN "…to publishing are free of charge" lolos
// (tak ada adversative).
func freeCoversPublish(window string) bool {
	for _, m := range reFree.FindAllStringIndex(window, -1) {
		a, b := m[0]-60, m[1]+60
		if a < 0 {
			a = 0
		}
		if b > len(window) {
			b = len(window)
		}
		ctx := window[a:b]
		freeStart, freeEnd := m[0]-a, m[1]-a // posisi frasa free dlm ctx
		for _, p := range rePub.FindAllStringIndex(ctx, -1) {
			// Teks di antara frasa free & kata publish (dua arah — ADDIN:
			// publish SEBELUM free; JUTIF: publish SESUDAH free).
			var between string
			if p[0] >= freeEnd {
				between = ctx[freeEnd:p[0]]
			} else {
				between = ctx[p[1]:freeStart]
			}
			if reAdv.MatchString(between) {
				continue // "free … but … publication" — publish tdk gratis
			}
			return true
		}
	}
	return false
}

// reAdv = adversative pemisah frasa free & publish (guard freeCoversPublish).
var reAdv = regexp.MustCompile(`(?i)\bbut\b|\bhowever\b|\bexcept\b|\bnamun\b|\bkecuali\b`)

// barisCtx = baris teks mengandung pos + posisi relatif di dalamnya.
func barisCtx(s string, pos int) (string, int) {
	a := strings.LastIndex(s[:pos], "\n") + 1
	b := strings.Index(s[pos:], "\n")
	if b < 0 {
		b = len(s)
	} else {
		b += pos
	}
	return s[a:b], pos - a
}

// ctxKategori = jendela ±60 dari nominal, DIBATASI baris teksnya (kategori
// penulis tak boleh bocor dari klausa nominal tetangga).
func ctxKategori(window string, m0, m1 int) string {
	baris, posNom := barisCtx(window, m0)
	cs := posNom - 60
	if cs < 0 {
		cs = 0
	}
	ce := posNom + (m1 - m0) + 60
	if ce > len(baris) {
		ce = len(baris)
	}
	return baris[cs:ce]
}

// pilihJenis menentukan jenis fee dari posisi KATA TERDEKAT ke nominal pada
// barisnya (posNom = posisi nominal relatif dalam ctx). Frasa berpasangan
// "submission…review" (bukti j681 "Article Submission and Review: 0.00")
// diutamakan atas solo-solo di dlmnya (overlap di-drop); baris tanpa label
// → fallback window global (publish dulu — pola APC umum), "" = tak
// terklasifikasi.
func pilihJenis(ctx string, posNom int, window string) string {
	type hit struct {
		nama  string
		jarak int
	}
	fr := reSubmRev.FindAllStringIndex(ctx, -1)
	var hits []hit
	for _, j := range []struct {
		nama string
		re   *regexp.Regexp
	}{{"submission", reSubm}, {"review", reRev}, {"publish", rePub}} {
		for _, m := range j.re.FindAllStringIndex(ctx, -1) {
			if (j.nama == "submission" || j.nama == "review") && overlapAny(m, fr) {
				continue // solo dlm frasa berpasangan → biar frasa yg menang
			}
			hits = append(hits, hit{j.nama, jarakBoundary(m, posNom)})
		}
	}
	for _, m := range fr {
		hits = append(hits, hit{"submission+review", jarakBoundary(m, posNom)})
	}
	sort.SliceStable(hits, func(i, k int) bool {
		if hits[i].jarak != hits[k].jarak {
			return hits[i].jarak < hits[k].jarak
		}
		return hits[i].nama < hits[k].nama
	})
	if len(hits) == 0 {
		return pilihJenisGlobal(window)
	}
	return hits[0].nama
}

// pilihJenisGlobal = fallback baris tanpa label: publish dulu (pola APC
// umum — JUTIF "IDR 1,500,000-. for domestic…" di label lewat frasa APC di
// window), lalu submission, lalu review.
func pilihJenisGlobal(window string) string {
	switch {
	case rePub.MatchString(window):
		return "publish"
	case reSubm.MatchString(window):
		return "submission"
	case reRev.MatchString(window):
		return "review"
	}
	return ""
}

// jarakBoundary = jarak terdekat boundary match (awal/akhir) ke posNom.
func jarakBoundary(m []int, posNom int) int {
	d0, d1 := m[0]-posNom, m[1]-posNom
	if d0 < 0 {
		d0 = -d0
	}
	if d1 < 0 {
		d1 = -d1
	}
	if d0 < d1 {
		return d0
	}
	return d1
}

func overlapAny(m []int, groups [][]int) bool {
	for _, g := range groups {
		if m[0] < g[1] && g[0] < m[1] {
			return true
		}
	}
	return false
}

// setPart mengisi FeePart: amount 0 = is_free (bukti j681 "0.00 (USD)").
// Nominal nyata TIDAK ditimpa free-frasa umum (dipanggil sebelum isFree).
func (e *FeeEntry) setPart(p **FeePart, amount int64, cur string) {
	if *p != nil && (*p).Amount > 0 {
		return // nominal pertama >0 menang; jangan timpa
	}
	if amount == 0 {
		*p = &FeePart{IsFree: true, Currency: cur}
		return
	}
	*p = &FeePart{Amount: amount, Currency: cur}
}

func normCurrency(c string) string {
	switch strings.ToUpper(strings.TrimSpace(c)) {
	case "IDR", "RP", "RUPIAH":
		return "IDR"
	case "USD", "US$":
		return "USD"
	default:
		return strings.ToUpper(strings.TrimSpace(c))
	}
}

// parseAmountNum = "1,500,000"→1500000 · "9,000,000.00"→9000000 ·
// "1.500.000"→1500000 · "870"→870 · "25.50"→26 (desimal langka dibulatkan —
// nominal duit = bilangan bulat, revisi 12 Okt; teks asli tetap di raw).
func parseAmountNum(raw string) (int64, bool) {
	s := strings.Trim(strings.TrimSpace(raw), "-.")
	if s == "" {
		return 0, false
	}
	// Sep terakhir = desimal hanya bila ekornya 1–2 digit ("870.00");
	// ekor 3 digit = pemisah ribuan ("1.500.000").
	if i := strings.LastIndexAny(s, ",."); i >= 0 && i < len(s)-1 {
		if tail := s[i+1:]; len(tail) <= 2 {
			intPart := strings.NewReplacer(",", "", ".", "").Replace(s[:i])
			f, err := strconv.ParseFloat(intPart+"."+tail, 64)
			if err != nil {
				return 0, false
			}
			return int64(math.Round(f)), true
		}
	}
	f, err := strconv.ParseFloat(strings.NewReplacer(",", "", ".", "").Replace(s), 64)
	if err != nil {
		return 0, false
	}
	return int64(f), true
}

// classifyCategory = kategori penulis (domestic/foreign[-first]) dari kata
// menempel; TANPA label teks → default dari mata uang (IDR=domestic,
// USD=foreign — masuk akal utk tarif; kasus tanpa label langka di P3).
func classifyCategory(ctx string, currency string) string {
	low := strings.ToLower(ctx)
	var base string
	switch {
	case strings.Contains(low, "abroad"), strings.Contains(low, "foreign"),
		strings.Contains(low, "overseas"), strings.Contains(low, "international"),
		strings.Contains(low, "asing"), strings.Contains(low, "outside"):
		base = "foreign"
	case strings.Contains(low, "domestic"), strings.Contains(low, "indonesia"),
		strings.Contains(low, "lokal"):
		base = "domestic"
	default:
		if currency == "IDR" {
			base = "domestic"
		} else {
			base = "foreign"
		}
	}
	if strings.Contains(low, "first") {
		base += "-first"
	}
	return base
}
