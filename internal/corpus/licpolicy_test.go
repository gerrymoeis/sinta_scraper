package corpus

// Test lisensi Fase 3.5 (C7-a — doc 43 §6.x): parser halaman policy (Opsi A)
// + API DOAJ (Opsi B). Fixture teks murni (0 network, pola derive_test.go
// TestLicenseFromOAI) — tiap kasus aturan §6.x.2 + jebakan false-positive
// riil dari smoke C7-a (menu sidebar "OPEN ACCESS STATEMENT" = LEMAH).

import (
	"strings"
	"testing"
)

// TestPolicyPageText — strip HTML: baris dipertahankan, script/style/komentar
// DIBUANG penuh (keyword di JS ≠ bukti lisensi).
func TestPolicyPageText(t *testing.T) {
	in := `<html><head><style>.a{color:red}</style>
<script>var lic="CC BY 4.0"; /* palsu */</script></head>
<body><p>Journal is <b>open</b> access</p><!-- CC BY-NC palsu --></body></html>`
	got := PolicyPageText([]byte(in))
	if strings.Contains(got, "palsu") || strings.Contains(got, "color") {
		t.Errorf("script/style/komentar tidak terbuang: %q", got)
	}
	if !strings.Contains(got, "Journal is open access") {
		t.Errorf("tag <b> tidak dilepas dgn rapi: %q", got)
	}
}

// TestPolicyHits — aturan §6.x.2: CC kuat · OA/paywall kuat bila kalimat
// utuh · heading/menu LEMAH (bukti riil smoke C7-a) · fee tanpa sinyal.
func TestPolicyHits(t *testing.T) {
	// (a) CC URL → 1 hit open+URL, kuat.
	hits := PolicyHits("Lisensi artikel: https://creativecommons.org/licenses/by/4.0/")
	if len(hits) != 1 || hits[0].Status != LicenseOpen || !hits[0].Strong ||
		!strings.Contains(hits[0].URL, "creativecommons.org") {
		t.Errorf("CC URL: %+v", hits)
	}
	// (b) CC teks tanpa URL → open, URL kosong (jujur), kuat.
	hits = PolicyHits("Artikel berlisensi CC BY-NC-SA 4.0 utk semua pembaca.")
	if len(hits) != 1 || hits[0].Status != LicenseOpen || hits[0].URL != "" || !hits[0].Strong {
		t.Errorf("CC teks: %+v", hits)
	}
	// (c) MENU/heading pendek → ada hit tapi LEMAH (tak boleh jadi dasar).
	hits = PolicyHits("OPEN ACCESS STATEMENT\nCOPY RIGHT NOTICE")
	if len(hits) != 1 || hits[0].Strong {
		t.Errorf("menu sidebar harus lemah: %+v", hits)
	}
	// (d) heading + kalimat utuh di baris berikut → KUAT (evidence ambil 2 baris).
	hits = PolicyHits("Open Access Policy\nAll articles are freely available to readers immediately after publication without any charge.")
	if len(hits) != 1 || !hits[0].Strong || hits[0].Status != LicenseFreeNotOA {
		t.Errorf("heading+kalimat: %+v", hits)
	}
	if !strings.Contains(hits[0].Evidence, "freely available") {
		t.Errorf("evidence tidak memuat baris kalimat: %q", hits[0].Evidence)
	}
	// (e) paywall kalimat utuh → paid kuat.
	hits = PolicyHits("Full text hanya dapat diakses oleh pelanggan yang berlangganan tahunan di situs ini.")
	if len(hits) != 1 || hits[0].Status != LicensePaid || !hits[0].Strong {
		t.Errorf("paywall: %+v", hits)
	}
	// (f) tanpa sinyal → kosong.
	if hits = PolicyHits("Editorial team bertemu setiap kuartal."); len(hits) != 0 {
		t.Errorf("tanpa sinyal harus kosong: %+v", hits)
	}
	// (g) fee ≠ lisensi (ditolak §6.x.3) — "no article processing charge" = 0 hit.
	if hits = PolicyHits("No article processing charge for authors."); len(hits) != 0 {
		t.Errorf("fee masuk sbg lisensi: %+v", hits)
	}
	// (g2) ejaan penuh "Creative Commons …" tanpa CC BY & tanpa URL /licenses/
	// (fakta smoke C7-a j828: footer badge OJS) → kuat.
	hits = PolicyHits("This work is licensed under a Creative Commons Attribution 4.0 International License.")
	if len(hits) != 1 || hits[0].Status != LicenseOpen || !hits[0].Strong {
		t.Errorf("ejaan penuh Creative Commons: %+v", hits)
	}
	// (g3) badge <img src=...> — URL di atribut tag DIBUANG oleh strip; yang
	// tersisa teks alt/caption → reCCText yg menang (alur nyata j828).
	hits = PolicyHits(PolicyPageText([]byte(`<img src="https://i.creativecommons.org/l/by/4.0/88x31.png" alt="Creative Commons License"> This work is licensed under a Creative Commons Attribution 4.0 International License.`)))
	if len(hits) != 1 || hits[0].Status != LicenseOpen || !hits[0].Strong || hits[0].URL != "" {
		t.Errorf("badge OJS setelah strip: %+v", hits)
	}
	// (h) campuran menu lemah + CC di halaman sama → parser kembalikan SEMUA
	// (CC-URL, CC-teks, OA-lemah); keputusan = urusan caller (CC menang).
	hits = PolicyHits("OPEN ACCESS STATEMENT\nCOPY RIGHT NOTICE\nLisensi CC BY 4.0: https://creativecommons.org/licenses/by/4.0/")
	if len(hits) != 3 || !hits[0].Strong || hits[0].URL == "" ||
		hits[1].Status != LicenseOpen || hits[2].Strong {
		t.Errorf("campuran lemah+CC (ekspektasi 3 hit: CC-URL, CC-teks, OA-lemah): %+v", hits)
	}
}

// TestDoajLicense — Opsi B: CC open (+detail) · non-CC gagal · JSON rusak gagal.
func TestDoajLicense(t *testing.T) {
	cc := `{"results":[{"bibjson":{"license":[{"type":"CC BY","url":"https://creativecommons.org/licenses/by/4.0/"}]}}]}`
	if st, u, detail, ok := DoajLicense([]byte(cc)); !ok || st != LicenseOpen ||
		!strings.Contains(u, "creativecommons.org") || !strings.Contains(detail, "CC BY =>") {
		t.Errorf("CC: got (%q,%q,%q,ok=%v)", st, u, detail, ok)
	}
	noCC := `{"results":[{"bibjson":{"license":[{"type":"Publisher exclusive copyright"}]}}]}`
	if _, _, _, ok := DoajLicense([]byte(noCC)); ok {
		t.Error("non-CC tidak boleh di-OK-kan")
	}
	if _, _, _, ok := DoajLicense([]byte(`{oops`)); ok {
		t.Error("JSON rusak tidak boleh di-OK-kan")
	}
	if _, _, _, ok := DoajLicense([]byte(`{"results":[]}`)); ok {
		t.Error("results kosong tidak boleh di-OK-kan")
	}
}

// TestDoajEntry — D3 (fase attrs): PISAHKAN bukti akses (results>0) dari
// bukti lisensi (bibjson.license). Kasus kunci: jurnal KETEMU tapi DOAJ tak
// mengklaim CC → found=true, licType="" (DOAJ tetap sinyal ① oa=free).
func TestDoajEntry(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		found   bool
		licType string
		licURL  string
	}{
		{"found dgn CC",
			`{"results":[{"bibjson":{"license":[{"type":"CC BY-SA","url":"https://creativecommons.org/licenses/by-sa/4.0/"}]}}]}`,
			true, "CC BY-SA", "https://creativecommons.org/licenses/by-sa/4.0/"},
		{"found TANPA CC (lisensi tak diklaim)",
			`{"results":[{"bibjson":{"license":[{"type":"Publisher exclusive copyright"}]}}]}`,
			true, "", ""},
		{"found tanpa key license",
			`{"results":[{"bibjson":{}}]}`,
			true, "", ""},
		{"miss (results kosong)",
			`{"results":[]}`, false, "", ""},
		{"JSON rusak", `{oops`, false, "", ""},
		{"bentuk beda (bukan objek)", `{"results":null}`, false, "", ""},
	}
	for _, c := range cases {
		f, typ, u, _ := DoajEntry([]byte(c.body))
		if f != c.found || typ != c.licType || u != c.licURL {
			t.Errorf("%s: got (found=%v, %q, %q), want (found=%v, %q, %q)",
				c.name, f, typ, u, c.found, c.licType, c.licURL)
		}
	}
	// Konsistensi refactor: DoajLicense dgn CC ≡ DoajEntry (found+tipe sama).
	cc := `{"results":[{"bibjson":{"license":[{"type":"CC BY","url":"https://creativecommons.org/licenses/by/4.0/"}]}}]}`
	f, typ, u, _ := DoajEntry([]byte(cc))
	if st, lu, _, ok := DoajLicense([]byte(cc)); !ok || st != LicenseOpen || !f || typ != "CC BY" || lu != u {
		t.Errorf("DoajLicense ≠ DoajEntry utk kasus CC: entry=(%v,%q,%q) lic=(%q,%q,ok=%v)", f, typ, u, st, lu, ok)
	}
}
