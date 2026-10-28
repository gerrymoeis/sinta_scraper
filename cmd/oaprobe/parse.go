package main

// ==== parse.go — deteksi pointer PDF di HTML (rantai ③ oaprobe) ====
//
// 4 bentuk pointer, urut keandalan (semua dikumpulkan — dicoba berurutan):
//
//	1. meta citation_pdf_url  — pola paling eksplisit (OJS3/publisher)
//	2. anchor dgn skor        — tombol "PDF/Download" (skor pola D3 licfix)
//	3. embed iframe|object    — viewer menyematkan .pdf
//	4. param viewer ?file=    — pdf.js dsb (URL-encoded)
//
// Semua di-resolve thd base (URL final setelah redirect — penting utk
// doi.org → landing lintas-host, link relatif di sana mustahil tanpa ini).

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"
)

var (
	reMetaPDF    = regexp.MustCompile(`(?is)<meta[^>]*name=["']citation_pdf_url["'][^>]*content=["']([^"']+)["']`)
	reMetaPDFRev = regexp.MustCompile(`(?is)<meta[^>]*content=["']([^"']+)["'][^>]*name=["']citation_pdf_url["']`)
	reAnchor     = regexp.MustCompile(`(?is)<a\b[^>]*href=["']([^"'#]+)["'][^>]*>(.*?)</a>`)
	reEmbedPDF   = regexp.MustCompile(`(?is)<(?:iframe|object|embed)\b[^>]*?\b(?:src|data)\s*=\s*["']([^"']+\.pdf[^"']*)["']`)
	reViewPrm    = regexp.MustCompile(`(?i)[?&](?:file|url|src)=([^"'&\s]+\.pdf[^"'&\s]*)`)
	reGalleyPath = regexp.MustCompile(`(?i)article/view/\d+/\d+`)
	reTitle      = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reTagStrip   = regexp.MustCompile(`(?s)<[^>]+>`)
	rePaywTitle  = regexp.MustCompile(`(?i)\b(subscribe|subscription|langganan|berlangganan|purchase|checkout|premium|order\s+now|beli)\b`)
	reLoginTitle = regexp.MustCompile(`(?i)\b(log[\s-]?in|sign[\s-]?in|authentication|masuk)\b`)
)

// ptr = SATU kandidat PDF (sumber + URL absolut) utk diuji.
type ptr struct {
	via string // meta|anchor|embed|param|hop
	url string
}

// pointersOf = seluruh bentuk pointer PDF di HTML, urut keandalan.
func pointersOf(base string, body []byte) []ptr {
	var out []ptr
	if u, ok := metaPDF(base, body); ok {
		out = append(out, ptr{"meta", u})
	}
	if u, ok := findPDFLink(base, body); ok {
		out = append(out, ptr{"anchor", u})
	}
	if u, ok := findEmbedPDF(base, body); ok {
		out = append(out, ptr{"embed", u})
	}
	if u, ok := findViewerParam(base, body); ok {
		out = append(out, ptr{"param", u})
	}
	return out
}

// metaPDF = <meta name="citation_pdf_url" content="…"> (dua urutan atribut).
func metaPDF(base string, body []byte) (string, bool) {
	if m := reMetaPDF.FindSubmatch(body); m != nil {
		return resolveRef(base, string(m[1]))
	}
	if m := reMetaPDFRev.FindSubmatch(body); m != nil {
		return resolveRef(base, string(m[1]))
	}
	return "", false
}

// findEmbedPDF = <iframe|object|embed> dgn src/data menuju *.pdf.
func findEmbedPDF(base string, body []byte) (string, bool) {
	m := reEmbedPDF.FindSubmatch(body)
	if m == nil {
		return "", false
	}
	return resolveRef(base, string(m[1]))
}

// findViewerParam = param ?file=…pdf (URL-encoded — viewer pdf.js dsb).
func findViewerParam(base string, body []byte) (string, bool) {
	m := reViewPrm.FindSubmatch(body)
	if m == nil {
		return "", false
	}
	v, err := url.QueryUnescape(string(m[1]))
	if err != nil {
		return "", false
	}
	return resolveRef(base, v)
}

// findPDFLink = anchor dgn skor tertinggi (skor pola D3 licfix — fakta
// smoke 27 Okt): href .pdf (+6) / /download/ (+5) / galley view (+4) /
// article/view (+1) · teks "PDF" (+4) / "download|unduh" (+2) · ambang ≥4
// (menahan link nav acak).
func findPDFLink(base string, body []byte) (string, bool) {
	best, bestScore := "", 0
	for _, m := range reAnchor.FindAllSubmatch(body, -1) {
		href := string(m[1])
		h := strings.ToLower(href)
		if strings.HasPrefix(h, "javascript:") || strings.HasPrefix(h, "mailto:") {
			continue
		}
		txt := strings.ToLower(string(reTagStrip.ReplaceAll(m[2], []byte(" "))))
		score := 0
		switch {
		case strings.Contains(h, ".pdf"):
			score += 6
		case strings.Contains(h, "/download/"):
			score += 5
		case reGalleyPath.MatchString(h):
			score += 4
		case strings.Contains(h, "/article/view/"):
			score += 1
		}
		if strings.Contains(txt, "pdf") {
			score += 4
		}
		if strings.Contains(txt, "download") || strings.Contains(txt, "unduh") {
			score += 2
		}
		if score > bestScore {
			best, bestScore = href, score
		}
	}
	if bestScore < 4 {
		return "", false
	}
	return resolveRef(base, best)
}

// resolveRef = ref (absolut/relatif/protocol-relative) → URL absolut thd base.
func resolveRef(base, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	bu, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	ru, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	return bu.ResolveReference(ru).String(), true
}

// isPDF = magic byte standar PDF (offset 0 — pola D3 licfix).
func isPDF(b []byte) bool { return bytes.HasPrefix(b, []byte("%PDF")) }
