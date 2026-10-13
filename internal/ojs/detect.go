package ojs

import (
	"fmt"
	"regexp"
	"strings"
)

// ==== detect.go — deteksi platform berbasis KONTEN (doc 42 §7.2 LOCKED) ====
//
// 8 aturan P2: (1) deteksi dari body final_url, bukan URL — (2) ojs bila teks
// "Open Journal Systems" | "pkp" | meta citation_* — (3) wordpress bila
// wp-json/wp-content ATAU generator WordPress — (4) campuran → unknown —
// (5) non-200/body kosong → unknown (retry transien = urusan driver) —
// (6) HTML tanpa marker → custom · non-HTML → static — (7) evidence wajib —
// (8) WAF/down tak perlu solve. Bukti: j687 criticalcareshock.com = WP
// pertama di 261; j16 cukup "pkp"; WAF 6/6 tanpa marker palsu.

// Deteksi marker (case-insensitive; compile sekali).
var (
	reCitationMeta = regexp.MustCompile(`(?i)name=["']citation_[a-z]`)
	reGenWordpress = regexp.MustCompile(`(?i)<meta[^>]+generator[^>]+wordpress`)
	reOJSVersion   = regexp.MustCompile(`(?i)open journal systems[^0-9<]{0,24}([0-9]+\.[0-9]+(?:\.[0-9]+)*)`)
)

// MinBodyHTML = ambang "body kecil tanpa HTML" utk static (§7.2 butir 6).
const MinBodyHTML = 512

// DetectResult = hasil deteksi platform utk SATU halaman (aturan §7.2).
type DetectResult struct {
	Platform   string // ojs|wordpress|custom|static|unknown
	Evidence   string // daftar marker match (";" — wajib, §7.2 butir 7)
	Confidence string // high|medium|"" (high = ≥2 marker, medium = 1)
	OJSVersion string // hint versi dari body bila terbaca (opsional, §11.1)
}

// Detect mengklasifikasi platform dari status + body final_url (aturan §7.2).
// Body = hasil GET penuh (cap 4 MB pola P2). Klasifikasi jujur: tak ditebak
// dari URL; non-200/body kosong = unknown (bukan vonis platform permanen —
// driver full-run berwenang retry 1× transien lalu panggil ulang).
func Detect(status int, body []byte) DetectResult {
	// (5) Non-200 / body kosong → unknown + evidence jujur.
	if status != 200 {
		return DetectResult{Platform: "unknown", Evidence: fmt.Sprintf("status=%d", status)}
	}
	if len(body) == 0 {
		return DetectResult{Platform: "unknown", Evidence: "body kosong"}
	}

	lower := strings.ToLower(string(body))

	// Kumpulkan SEMUA marker match (bukti disimpan penuh — aturan 4 & 7).
	var ojsMarkers, wpMarkers, semua []string
	add := func(petunjuk *[]string, nama string) {
		*petunjuk = append(*petunjuk, nama)
		semua = append(semua, nama)
	}
	if strings.Contains(lower, "open journal systems") {
		add(&ojsMarkers, "open-journal-systems")
	}
	if strings.Contains(lower, "pkp") {
		// (2) "pkp" = sig Public Knowledge Project — sah sbg marker tunggal
		// (bukti j16: cukup pkp tanpa teks OJS).
		add(&ojsMarkers, "pkp")
	}
	if reCitationMeta.MatchString(lower) {
		add(&ojsMarkers, "citation-meta")
	}
	if strings.Contains(lower, "wp-json") {
		add(&wpMarkers, "wp-json")
	}
	if strings.Contains(lower, "wp-content") {
		add(&wpMarkers, "wp-content")
	}
	if reGenWordpress.MatchString(lower) {
		add(&wpMarkers, "generator-wordpress")
	}

	evidence := strings.Join(semua, "; ")

	// (4) Campuran OJS+WP → unknown (jangan ditebak; evidence tetap disimpan).
	if len(ojsMarkers) > 0 && len(wpMarkers) > 0 {
		return DetectResult{Platform: "unknown", Evidence: evidence}
	}
	// (2)/(3) Marker murni → platform + confidence (≥2 = high, 1 = medium).
	if n := len(ojsMarkers); n > 0 {
		return DetectResult{
			Platform: "ojs", Evidence: evidence, Confidence: conf(n),
			OJSVersion: ojsVersion(lower),
		}
	}
	if n := len(wpMarkers); n > 0 {
		return DetectResult{Platform: "wordpress", Evidence: evidence, Confidence: conf(n)}
	}

	// (6) Tanpa marker: HTML → custom · non-HTML → static.
	if strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype") {
		return DetectResult{Platform: "custom", Evidence: "html-tanpa-marker"}
	}
	return DetectResult{Platform: "static", Evidence: "non-html"}
}

func conf(nMarker int) string {
	if nMarker >= 2 {
		return "high"
	}
	return "medium"
}

// ojsVersion = hint versi dari body ("Open Journal Systems 3.3.0.12" →
// "3.3.0.12"); "" bila tak terbaca (nullable, bukan kegagalan).
func ojsVersion(lowerBody string) string {
	if m := reOJSVersion.FindStringSubmatch(lowerBody); m != nil {
		return m[1]
	}
	return ""
}
