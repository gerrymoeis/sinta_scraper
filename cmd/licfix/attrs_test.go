package main

// Unit test fase "attrs" (D3 — doc 43 §6.x.8): fungsi murni TANPA network
// & TANPA DB — findPDFLink (picking anchor), classifyHTML (judul landing),
// strongOA (② pernyataan unik), canonFromDOAJ (label → kanonik), isPDF.

import (
	"strings"
	"testing"

	"sinta-scraper/internal/corpus"
	"sinta-scraper/internal/storage"
)

// TestFindPDFLink — skor anchor: .pdf menang atas link nav; ambang <4 tolak.
func TestFindPDFLink(t *testing.T) {
	base := "https://jurnal.test/index.php/xyz/article/view/123"
	cases := []struct {
		name string
		body string
		want string // "" = tak boleh ketemu
	}{
		{"anchor .pdf + teks PDF",
			`<a href="https://jurnal.test/index.php/xyz/article/download/123/456.pdf">Download PDF</a>`,
			"https://jurnal.test/index.php/xyz/article/download/123/456.pdf"},
		{"href relatif (di-resolve thd base)",
			`<a href="/index.php/xyz/article/view/123/456">PDF</a>`,
			"https://jurnal.test/index.php/xyz/article/view/123/456"},
		{"galley view tanpa ekstensi .pdf + teks PDF (skor 4+4)",
			`<a href="https://jurnal.test/index.php/xyz/article/view/123/999">PDF</a>`,
			"https://jurnal.test/index.php/xyz/article/view/123/999"},
		{"hanya link nav (skor <4) → tolak",
			`<a href="/about">About</a><a href="/index.php/xyz/about/contact">Contact</a>`, ""},
		{"javascript: diabaikan",
			`<a href="javascript:void(0)">PDF</a>`, ""},
	}
	for _, c := range cases {
		got, ok := findPDFLink(base, []byte(c.body))
		if c.want == "" {
			if ok {
				t.Errorf("%s: seharusnya TOLAK, dapat %q", c.name, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%s: got (%q, %v), want %q", c.name, got, ok, c.want)
		}
	}
}

// TestClassifyHTML — klasifikasi HANYA dari <title> (bukan body penuh);
// tanpa indikator → ("", …) = unknown jujur.
func TestClassifyHTML(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // free|restricted|paid|"" (unknown)
	}{
		{"judul bayar → paid",
			`<html><head><title>Subscription &amp; Purchase Options</title></head></html>`, corpus.OAPaid},
		{"judul login → restricted",
			`<html><head><title>Sign In - Journal</title></head></html>`, corpus.OARestricted},
		{"kata login di navbar, judul normal → TETAP unknown",
			`<html><head><title>Article</title></head><body><a href="/login">Login</a></body></html>`, ""},
		{"tanpa title tag → unknown",
			`<html><body>oops</body></html>`, ""},
	}
	for _, c := range cases {
		got, why := classifyHTML([]byte(c.body))
		if got != c.want {
			t.Errorf("%s: got %q (%s), want %q", c.name, got, why, c.want)
		}
	}
}

// TestStrongOA — ② butuh sinyal KUAT free/paid yang UNIK; CC/lemah diabaikan;
// konflik → ("", …) (jangan nebak — validitas #1).
func TestStrongOA(t *testing.T) {
	strong := func(st string) pageHit {
		return pageHit{PolicyHit: corpus.PolicyHit{Status: st, Strong: true}, src: "home", page: "https://x.test/"}
	}
	// unik free → free
	if s, h := strongOA([]pageHit{
		strong(corpus.LicenseFreeNotOA),
		{PolicyHit: corpus.PolicyHit{Status: corpus.LicenseOpen, Strong: true}},
	}); s != corpus.OAFree || h.page == "" {
		t.Errorf("unik free: got (%q, page=%q)", s, h.page)
	}
	// konflik free+paid → tolak
	if s, _ := strongOA([]pageHit{strong(corpus.LicenseFreeNotOA), strong(corpus.LicensePaid)}); s != "" {
		t.Errorf("konflik harus tolak, dapat %q", s)
	}
	// hanya lemah → tolak
	weak := pageHit{PolicyHit: corpus.PolicyHit{Status: corpus.LicenseFreeNotOA, Strong: false}}
	if s, _ := strongOA([]pageHit{weak}); s != "" {
		t.Errorf("hanya lemah harus tolak, dapat %q", s)
	}
	// kosong → tolak
	if s, _ := strongOA(nil); s != "" {
		t.Errorf("kosong harus tolak, dapat %q", s)
	}
}

// TestCanonFromDOAJ — URL-dulu (ada versi 4.0 → konsisten dgn turunan D2) →
// label (tanpa versi = TIDAK dikarang versinya) → non-CC "" (jangan dipaksa).
func TestCanonFromDOAJ(t *testing.T) {
	if got := canonFromDOAJ("CC BY-SA", "https://creativecommons.org/licenses/by-sa/4.0/"); got != "CC BY-SA 4.0" {
		t.Errorf("URL dgn versi harus menang: got %q", got)
	}
	// label tanpa versi, URL non-CC → hasil TANPA versi karangan (jujur)
	if got := canonFromDOAJ("CC BY-SA", "https://jurnal.test/licence"); got != "CC BY-SA" {
		t.Errorf("label tanpa versi: got %q", got)
	}
	if got := canonFromDOAJ("CC0", ""); got != "CC0" {
		t.Errorf("CC0: got %q", got)
	}
	if got := canonFromDOAJ("Publisher exclusive copyright", ""); got != "" {
		t.Errorf("non-CC harus \"\", dapat %q", got)
	}
	// label kosong tapi URL CC → URL
	if got := canonFromDOAJ("", "https://creativecommons.org/licenses/by/4.0/"); got != "CC BY 4.0" {
		t.Errorf("fallback URL: got %q", got)
	}
}

// TestLicenseFromTexts — D3b-fix1: 2-pass (SEMUA teks → CC dulu, baru ARR),
// pola sama dgn LicenseTypeOf D1. Footer ARR + CC paragraf → CC MENANG.
func TestLicenseFromTexts(t *testing.T) {
	arr := pageText{"policy:other", "https://j.test/copyright", "Copyright 2024 Journal. All Rights Reserved."}
	ccPara := pageText{"home", "https://j.test/", "Authors retain copyright. This work is licensed under a Creative Commons Attribution 4.0 International License https://creativecommons.org/licenses/by/4.0/"}
	// 1. ARR + CC paragraf → CC (2-pass — ARR tak boleh menimpa)
	if t1, u, src, _, ev := licenseFromTexts([]pageText{arr, ccPara}); t1 != "CC BY 4.0" ||
		!strings.Contains(u, "creativecommons.org") || src != "text-home" || ev == "" {
		t.Errorf("ARR+CC-paragraf → CC: got (%q, %q, %q, ev=%q)", t1, u, src, ev)
	}
	// 2. hanya ARR → ARR
	if t1, u, src, _, _ := licenseFromTexts([]pageText{arr}); t1 != storage.LicenseTypeARR || u != "" || !strings.HasPrefix(src, "footer-") {
		t.Errorf("hanya ARR: got (%q, %q, %q)", t1, u, src)
	}
	// 3. ARR diblokir ("no all rights reserved") + tanpa CC → "" (jujur)
	blocked := pageText{"home", "https://j.test/", "The journal grants no all rights reserved to users."}
	if t1, _, _, _, _ := licenseFromTexts([]pageText{blocked}); t1 != "" {
		t.Errorf("ARR diblokir harus \"\", dapat %q", t1)
	}
	// 4. tanpa teks → ""
	if t1, _, _, _, _ := licenseFromTexts(nil); t1 != "" {
		t.Errorf("tanpa teks harus \"\", dapat %q", t1)
	}
}

// TestFindEmbedPDF — D3b-fix2: iframe/object/embed src|data → *.pdf (absolut).
func TestFindEmbedPDF(t *testing.T) {
	base := "https://j.test/index.php/xyz/article/view/123"
	cases := []struct {
		name string
		body string
		want string
	}{
		{"iframe src absolut",
			`<iframe src="https://cdn.j.test/pdf/123.pdf#view=FitH" width="100%"></iframe>`,
			"https://cdn.j.test/pdf/123.pdf#view=FitH"},
		{"object data relatif",
			`<object data="/pdf/123.pdf"></object>`, "https://j.test/pdf/123.pdf"},
		{"embed tanpa .pdf → tolak",
			`<embed src="/viewer/123">`, ""},
	}
	for _, c := range cases {
		got, ok := findEmbedPDF(base, []byte(c.body))
		if c.want == "" {
			if ok {
				t.Errorf("%s: seharusnya tolak, dapat %q", c.name, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%s: got (%q,%v), want %q", c.name, got, ok, c.want)
		}
	}
}

// TestFindViewerParam — D3b-fix2: ?file= URL-encoded (pdf.js dsb) → di-unescape.
func TestFindViewerParam(t *testing.T) {
	base := "https://j.test/viewer.html"
	body := []byte(`<a href="viewer.html?file=%2Farticles%2F2024%2F1.pdf">View</a>`)
	got, ok := findViewerParam(base, body)
	if !ok || got != "https://j.test/articles/2024/1.pdf" {
		t.Errorf("viewer param: got (%q,%v)", got, ok)
	}
	if _, ok := findViewerParam(base, []byte(`?file=nope.html`)); ok {
		t.Error("param tanpa .pdf harus tolak")
	}
}

// TestFindPDFAny — urutan: anchor (skor) → embed → param viewer.
func TestFindPDFAny(t *testing.T) {
	base := "https://j.test/article/view/1"
	// anchor menang atas embed
	body := []byte(`<a href="/d/1.pdf">PDF</a><iframe src="/i/1.pdf"></iframe>`)
	if got, ok := findPDFAny(base, body); !ok || got != "https://j.test/d/1.pdf" {
		t.Errorf("anchor prioritas: got (%q,%v)", got, ok)
	}
	// tanpa anchor → embed
	body2 := []byte(`<iframe src="/i/1.pdf"></iframe>`)
	if got, ok := findPDFAny(base, body2); !ok || got != "https://j.test/i/1.pdf" {
		t.Errorf("fallback embed: got (%q,%v)", got, ok)
	}
	// tanpa keduanya → tolak
	if _, ok := findPDFAny(base, []byte(`<html><body>halo</body></html>`)); ok {
		t.Error("tanpa pointer harus tolak")
	}
}

// TestIsPDF — magic byte offset 0 (bukan sekadar "ada teks %PDF di body").
func TestIsPDF(t *testing.T) {
	if !isPDF([]byte("%PDF-1.7\n...")) {
		t.Error("%PDF-1.7 harus true")
	}
	if isPDF([]byte("<html>this mentions %PDF inline</html>")) {
		t.Error("HTML dgn kata %PDF di tengah harus false")
	}
}

// TestShortSrc — jejak license_src ringkas ("policy:copyright" → "policy").
func TestShortSrc(t *testing.T) {
	for in, want := range map[string]string{
		"policy:copyright": "policy",
		"home":             "home",
		"doaj":             "doaj",
	} {
		if got := shortSrc(in); got != want {
			t.Errorf("shortSrc(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestFindPDFLinkSkorUrut — pemilihan DETERMINISTIK: anchor skor tertinggi
// menang walau urut di HTML terbalik (galley 8 vs .pdf+PDF+download 12).
func TestFindPDFLinkSkorUrut(t *testing.T) {
	base := "https://jurnal.test/index.php/xyz/article/view/123"
	body := `<a href="/index.php/xyz/article/view/123/9">PDF</a>` +
		`<a href="https://jurnal.test/download/123.pdf">PDF download</a>`
	got, ok := findPDFLink(base, []byte(body))
	if !ok || !strings.HasSuffix(got, ".pdf") {
		t.Errorf("skor tertinggi (.pdf+6/PDF+4/download+2=12) harus menang: got %q (ok=%v)", got, ok)
	}
}
