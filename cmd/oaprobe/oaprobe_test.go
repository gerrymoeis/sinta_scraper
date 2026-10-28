package main

// ==== test oaprobe — parse pointer PDF + klasifikasi + pernyataan kuat ====

import (
	"strings"
	"testing"

	"sinta-scraper/internal/corpus"
)

const ojsView = `<!DOCTYPE html><html><head>
	<title>Jurnal Contoh Vol 1 No 1</title>
	<meta name="citation_pdf_url" content="/index.php/contoh/article/download/101/88"/>
	</head><body>
	<a href="/index.php/contoh/article/view/101">Detail</a>
	<a href="/index.php/contoh/article/download/101/88">PDF</a>
	<a href="/index.php/contoh/about">About</a>
	</body></html>`

// TestPointersOf — meta menang (urut keandalan) + anchor galley + resolve absolut.
func TestPointersOf(t *testing.T) {
	ptrs := pointersOf("https://j.test/index.php/contoh/article/view/101", []byte(ojsView))
	if len(ptrs) < 2 {
		t.Fatalf("pointers = %d — mau ≥2 (meta+anchor)", len(ptrs))
	}
	if ptrs[0].via != "meta" || !strings.Contains(ptrs[0].url, "/download/101/88") {
		t.Errorf("ptr[0] = %+v — mau meta /download/101/88", ptrs[0])
	}
	if !strings.HasPrefix(ptrs[0].url, "https://j.test/") {
		t.Errorf("resolve = %q — mau absolut", ptrs[0].url)
	}
	if ptrs[1].via != "anchor" {
		t.Errorf("ptr[1].via = %q — mau anchor", ptrs[1].via)
	}
}

// TestFindPDFLinkSkor — nav link tanpa skor tak menang; tombol PDF menang.
func TestFindPDFLinkSkor(t *testing.T) {
	html := `<a href="/about">About</a><a href="/index.php/k/article/view/9">Detail</a>` +
		`<a href="/unduh/12.pdf">Simpan</a>`
	u, ok := findPDFLink("https://j.test/index.php/k/article/view/9", []byte(html))
	if !ok || !strings.HasSuffix(u, "/unduh/12.pdf") {
		t.Errorf("findPDFLink = %q,%v — mau /unduh/12.pdf", u, ok)
	}
}

// TestFindPDFLinkAmbang — anchor skor <4 (nav biasa) → tolak (jujur).
func TestFindPDFLinkAmbang(t *testing.T) {
	html := `<a href="/index.php/k/article/view/9">Detail</a>`
	if _, ok := findPDFLink("https://j.test/", []byte(html)); ok {
		t.Error("anchor article/view tanpa teks PDF tak boleh lolos ambang")
	}
}

// TestEmbedDanParam — iframe .pdf + param ?file= (URL-encoded).
func TestEmbedDanParam(t *testing.T) {
	if u, ok := findEmbedPDF("https://j.test/a", []byte(`<iframe src="/pdf/x.pdf"></iframe>`)); !ok || !strings.HasSuffix(u, "/pdf/x.pdf") {
		t.Errorf("embed = %q,%v", u, ok)
	}
	if u, ok := findViewerParam("https://j.test/a", []byte(`viewer.html?file=%2Fart%2F1.pdf`)); !ok || !strings.HasSuffix(u, "/art/1.pdf") {
		t.Errorf("param = %q,%v", u, ok)
	}
}

// TestIsPDF — magic byte offset 0.
func TestIsPDF(t *testing.T) {
	if !isPDF([]byte("%PDF-1.7\n...")) || isPDF([]byte("<html>")) {
		t.Error("isPDF = prefix %PDF saja")
	}
}

// TestClassifyHTMLTitle — judul login → restricted · langganan → paid · netral → "".
func TestClassifyHTMLTitle(t *testing.T) {
	if s, _ := classifyHTMLTitle([]byte(`<title>Silahkan Login untuk Akses</title>`)); s != corpus.OARestricted {
		t.Errorf("judul login → %q, mau restricted", s)
	}
	if s, _ := classifyHTMLTitle([]byte(`<title>Berlangganan Jurnal Ini</title>`)); s != corpus.OAPaid {
		t.Errorf("judul langganan → %q, mau paid", s)
	}
	if s, _ := classifyHTMLTitle([]byte(`<title>Vol 1 No 1 (2024)</title>`)); s != "" {
		t.Errorf("judul netral → %q, mau kosong (unknown jujur)", s)
	}
}

// TestStrongStatement — pernyataan ≥10 kata kuat; label menu lemah ditahan.
func TestStrongStatement(t *testing.T) {
	// Baris panjang (≥10 kata dgn baris berikut — pola evidenceAround).
	free := `<p>Jurnal ini menerapkan kebijakan akses terbuka, sehingga seluruh` +
		` artikel dapat diunduh gratis oleh pembaca tanpa biaya apa pun sama sekali.</p>`
	if s, _ := strongStatement([]byte(free)); s != corpus.OAFree {
		t.Errorf("pernyataan akses-terbuka → %q, mau free", s)
	}
	// "OPEN ACCESS STATEMENT" sbg label sidebar = LEMAH (fakta smoke C7-a) → "".
	if s, _ := strongStatement([]byte(`<h3>OPEN ACCESS STATEMENT</h3>`)); s != "" {
		t.Errorf("label menu lemah → %q, mau kosong", s)
	}
}
