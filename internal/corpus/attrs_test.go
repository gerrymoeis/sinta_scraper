package corpus

import (
	"strings"
	"testing"
)

// TestLicenseTypeFromURL — derive tipe CC dari URL (6+ pola §6.x.8 D1).
func TestLicenseTypeFromURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://creativecommons.org/licenses/by-sa/4.0", "CC BY-SA 4.0"},
		{"http://creativecommons.org/licenses/by-nc/4.0", "CC BY-NC 4.0"},
		{"https://creativecommons.org/licenses/by-nc-sa/4.0/", "CC BY-NC-SA 4.0"}, // trailing slash
		{"https://creativecommons.org/licenses/by-nc-nd/4.0", "CC BY-NC-ND 4.0"},
		{"https://creativecommons.org/licenses/by/4.0/legalcode", "CC BY 4.0"}, // /legalcode
		{"https://creativecommons.org/licenses/by-nd/", "CC BY-ND"},            // tanpa versi
		{"https://i.creativecommons.org/l/by/4.0/88x31.png", "CC BY 4.0"},      // badge
		{"https://creativecommons.org/publicdomain/zero/1.0/", "CC0 1.0"},      // CC0
		{"https://example.com/copyright.html", ""},                             // bukan CC
		{"https://creativecommons.org/about/", ""},                             // jalur non-lisensi
		{"", ""},
	}
	for _, c := range cases {
		if got := LicenseTypeFromURL(c.in); got != c.want {
			t.Errorf("LicenseTypeFromURL(%q) = %q — mau %q", c.in, got, c.want)
		}
	}
}

// TestLicenseTypeFromLabel — label teks (DOAJ bibjson.license dsb) → kanonik.
func TestLicenseTypeFromLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"CC BY-NC-SA 4.0", "CC BY-NC-SA 4.0"},
		{"cc by 4.0", "CC BY 4.0"},
		{"CC BY-NC", "CC BY-NC"},
		{"cc-by", "CC BY"},
		{"CC0", "CC0"},
		{"CC0 1.0", "CC0 1.0"},
		{"bukan lisensi", ""},
		{"CC BY-XYZ 9.9", ""}, // tipe di luar whitelist → jujur kosong
	}
	for _, c := range cases {
		if got := LicenseTypeFromLabel(c.in); got != c.want {
			t.Errorf("LicenseTypeFromLabel(%q) = %q — mau %q", c.in, got, c.want)
		}
	}
}

// TestLicenseTypeFromText — teks kebijakan/OAI: URL dulu → kata "CC …" →
// ejaan penuh tanpa singkatan = tak ada tipe eksplisit (jangan nebak).
func TestLicenseTypeFromText(t *testing.T) {
	if got := LicenseTypeFromText("Lisensi: https://creativecommons.org/licenses/by-sa/4.0/ utk semua"); got != "CC BY-SA 4.0" {
		t.Errorf("URL dlm teks = %q — mau CC BY-SA 4.0", got)
	}
	if got := LicenseTypeFromText("Artikel berlisensi CC BY-NC-SA 4.0 utk semua pembaca."); got != "CC BY-NC-SA 4.0" {
		t.Errorf("kata CC dlm teks = %q — mau CC BY-NC-SA 4.0", got)
	}
	if got := LicenseTypeFromText("This work is licensed under a Creative Commons Attribution 4.0 International License."); got != "" {
		t.Errorf("ejaan penuh tanpa singkatan = %q — mau '' (tipe tak eksplisit)", got)
	}
}

// TestARREvidence — ARR harus EKSPLISIT tertulis; "Some rights reserved"
// berarti kebalikannya → ditolak; tanpa frasa = tak ketemu (bukan ARR).
func TestARREvidence(t *testing.T) {
	if ev, ok := ARREvidence("Copyright © 2020 - All Rights Reserved - OSCM Journal"); !ok || !strings.Contains(ev, "All Rights Reserved") {
		t.Errorf("ARR eksplisit tak terdeteksi (ev=%q)", ev)
	}
	if _, ok := ARREvidence("Some rights reserved under CC BY"); ok {
		t.Error("'Some rights reserved' tak boleh jadi ARR (makna berlawanan)")
	}
	if _, ok := ARREvidence("Copyright © 2026 Critical Care and Shock Journal"); ok {
		t.Error("copyright biasa TANPA frasa ARR = bukan ARR")
	}
	if _, ok := ARREvidence(""); ok {
		t.Error("teks kosong = tak ketemu")
	}
}

// TestLicenseTypeOf — rantai bukti 0-GET: URL → teks CC → ARR eksplisit →
// unknown (Not Stated). Dua-pass: CC menang meski ARR muncul lebih dulu.
func TestLicenseTypeOf(t *testing.T) {
	if got := LicenseTypeOf("https://creativecommons.org/licenses/by/4.0"); got != "CC BY 4.0" {
		t.Errorf("URL = %q — mau CC BY 4.0", got)
	}
	if got := LicenseTypeOf("", "Footer: Copyright © 2020 - All Rights Reserved"); got != LicenseTypeARR {
		t.Errorf("ARR eksplisit = %q — mau all_rights_reserved", got)
	}
	// dua-pass: teks[0]=footer ARR, teks[1]=CC → CC menang (bukti lisensi
	// konkret mengungguli keterangan hak cipta umum)
	if got := LicenseTypeOf("", "All Rights Reserved", "CC BY-SA 4.0 utk semua artikel terbuka."); got != "CC BY-SA 4.0" {
		t.Errorf("pass ganda = %q — mau CC BY-SA 4.0", got)
	}
	if got := LicenseTypeOf("", "Copyright (c) 2025 Penerbit"); got != LicenseTypeUnknown {
		t.Errorf("tanpa bukti = %q — mau unknown (jujur)", got)
	}
}

// TestLicenseReuse — derive 3 case (putaran 4: kolom ke-3 ditolak).
func TestLicenseReuse(t *testing.T) {
	if got := LicenseReuse("CC BY-SA 4.0"); got != ReuseWithTerms {
		t.Errorf("CC = %q — mau reuse_with_terms", got)
	}
	if got := LicenseReuse(LicenseTypeARR); got != ReuseNo {
		t.Errorf("ARR = %q — mau no_reuse", got)
	}
	if got := LicenseReuse(LicenseTypeUnknown); got != ReuseNotAssumed {
		t.Errorf("unknown = %q — mau not_assumed (jangan diasumsikan bebas)", got)
	}
}

// TestOAAttr — aturan oa_status dari bukti (§6.x.8 D1 aturan 4: CC ≠ gratis).
func TestOAAttr(t *testing.T) {
	// Crossref: embargo → restricted · free-to-read → free · CC-saja → ("",false)
	if s, ok := OACrossref(CrossrefItem{Embargo: true}); !ok || s != OARestricted {
		t.Errorf("embargo = %q/ok=%v — mau restricted", s, ok)
	}
	if s, ok := OACrossref(CrossrefItem{FreeToRead: true}); !ok || s != OAFree {
		t.Errorf("free-to-read = %q/ok=%v — mau free", s, ok)
	}
	if _, ok := OACrossref(CrossrefItem{LicenseOpen: true, LicenseURL: "https://creativecommons.org/licenses/by/4.0"}); ok {
		t.Error("CC aktif TANPA sinyal akses → ('',false) — hybrid jangan ditebak free")
	}
	if _, ok := OACrossref(CrossrefItem{}); ok {
		t.Error("tanpa bukti → ('',false)")
	}
	// OAI: statement OA → free · paywall → paid · CC-saja → ("",false)
	if s, ok := OAFromOAI([]string{"Open Access Journal"}); !ok || s != OAFree {
		t.Errorf("statement OA = %q/ok=%v — mau free", s, ok)
	}
	if s, ok := OAFromOAI([]string{"Hanya dapat diakses pelanggan berlangganan"}); !ok || s != OAPaid {
		t.Errorf("paywall = %q/ok=%v — mau paid", s, ok)
	}
	if _, ok := OAFromOAI([]string{"https://creativecommons.org/licenses/by/4.0/"}); ok {
		t.Error("URL CC tanpa pernyataan akses → ('',false)")
	}
	if _, ok := OAFromOAI(nil); ok {
		t.Error("rights kosong → ('',false)")
	}
}
