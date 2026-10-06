package garuda

import (
	"strings"
	"testing"
)

func TestE7cPilihKandidat(t *testing.T) {
	cases := []struct {
		name string
		ks   []E7cKand
		want int
	}{
		{"tahun akhir besar menang", []E7cKand{
			{ID: 5276, YearFrom: 2009, YearTo: 2020},
			{ID: 7000, YearFrom: 2010, YearTo: 2026},
		}, 1},
		{"tie → data lengkap menang", []E7cKand{
			{ID: 100, YearFrom: 0, YearTo: 2026}, // rentang tak lengkap
			{ID: 200, YearFrom: 2015, YearTo: 2026},
		}, 1},
		{"tie lengkap-lengkap → id kecil", []E7cKand{
			{ID: 900, YearFrom: 2015, YearTo: 2026},
			{ID: 100, YearFrom: 2015, YearTo: 2026},
		}, 1},
		{"NotFound gugur", []E7cKand{
			{ID: 50, NotFound: true, YearTo: 2026},
			{ID: 60, YearTo: 2020},
		}, 1},
		{"semua NotFound → -1", []E7cKand{
			{ID: 50, NotFound: true}, {ID: 60, NotFound: true},
		}, -1},
		{"kandidat tunggal", []E7cKand{{ID: 5276, YearFrom: 2009, YearTo: 2026}}, 0},
	}
	for _, c := range cases {
		got, alasan := E7cPilihKandidat(c.ks)
		if got != c.want {
			t.Errorf("%s: menang=%d want=%d (alasan %q)", c.name, got, c.want, alasan)
		}
	}
}

func TestE7cISSNHyphen(t *testing.T) {
	cases := map[string]string{
		"23026766":  "2302-6766",
		"0215773X":  "0215-773X",
		"2302-6766": "2302-6766", // idempoten (sudah strip → norm → strip lagi)
		"":          "",
		"123":       "", // < 7-8 char = tak valid → NormISSN "" (jujur, bukan dipaksa)
	}
	for in, want := range cases {
		if got := E7cISSNHyphen(in); got != want {
			t.Errorf("E7cISSNHyphen(%q)=%q want %q", in, got, want)
		}
	}
}

func TestE7cNilaiCrossref(t *testing.T) {
	xr := E6Crossref{
		Tersedia: true,
		Judul:    "TEFLIN Journal",
		Penerbit: "Asosiasi Dosen Teknologi Pendidikan",
		ISSNs:    []string{"0215773X", "23562641"},
		DoiTotal: 120,
	}
	v, conf := E7cNilaiCrossref("TEFLIN Journal", xr, map[string]string{
		"0215773X": "print", "23562641": "electronic",
	})
	if conf != 0.9 {
		t.Errorf("judul sama: conf=%.2f want 0.9", conf)
	}
	if !containsAll(v, "TEFLIN Journal", "0215773X(print)", "23562641(electronic)", "dois=120") {
		t.Errorf("value tak lengkap: %q", v)
	}

	xr.Judul = "Nama Beda Total"
	_, conf = E7cNilaiCrossref("TEFLIN Journal", xr, nil)
	if conf != 0.6 {
		t.Errorf("judul beda: conf=%.2f want 0.6 (jujur, bukan tinggi)", conf)
	}
}

func TestE7cNilaiOAI(t *testing.T) {
	v, conf := E7cNilaiOAI("TEFLIN Journal", "http://x/oai", "TEFLIN Journal", []string{"oai_dc", "marc"})
	if conf != 0.9 {
		t.Errorf("repo sama: conf=%.2f want 0.9", conf)
	}
	if !containsAll(v, "http://x/oai", "TEFLIN Journal", "oai_dc,marc") {
		t.Errorf("value: %q", v)
	}
	_, conf = E7cNilaiOAI("TEFLIN Journal", "http://x/oai", "Lain Sekali", nil)
	if conf != 0.6 {
		t.Errorf("repo beda: conf=%.2f want 0.6", conf)
	}
}

func TestE7cNilaiView(t *testing.T) {
	v := &ViewInfo{Title: "DE JURE", Publisher: "UIN Malang", YearFrom: 2009, YearTo: 2026}
	// tanpa ISSN (kasus nyata j60: ISSN "-" → kosong) → title-sama 0.6
	val, conf := E7cNilaiView("De Jure: Jurnal Hukum dan Syar'iah", 5276, v, "20851618", "25281658")
	if conf != 0.6 {
		t.Errorf("title-sama tanpa issn: conf=%.2f want 0.6", conf)
	}
	if !containsAll(val, "view/5276", "tahun=2009-2026", "DE JURE") {
		t.Errorf("value: %q", val)
	}
	// ISSN cocok → 0.8
	v.PrintISSN = "20851618"
	if _, conf = E7cNilaiView("X Y Z Beda", 1, v, "20851618", ""); conf != 0.8 {
		t.Errorf("issn cocok: conf=%.2f want 0.8", conf)
	}
	// gugur semua → 0.5
	v2 := &ViewInfo{Title: "Beda Total"}
	if _, conf = E7cNilaiView("Sangat Beda Lagi", 1, v2, "", ""); conf != 0.5 {
		t.Errorf("gugur: conf=%.2f want 0.5", conf)
	}
	// NotFound → conf 0
	if _, conf = E7cNilaiView("X", 1, &ViewInfo{NotFound: true}, "", ""); conf != 0 {
		t.Errorf("notfound: conf=%.2f want 0", conf)
	}
}

func TestE7cParseIdentify(t *testing.T) {
	okBody := []byte(`<?xml version="1.0"?>
<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/">
<Identify><repositoryName>TEFLIN Journal</repositoryName><protocolVersion>2.0</protocolVersion></Identify>
</OAI-PMH>`)
	repo, ok := E7cParseIdentify(okBody)
	if !ok || repo != "TEFLIN Journal" {
		t.Errorf("identify: ok=%v repo=%q", ok, repo)
	}
	if _, ok = E7cParseIdentify([]byte("<html><body>misi</body></html>")); ok {
		t.Error("html harus ok=false")
	}
}

func TestE7cParseLMF(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<OAI-PMH><ListMetadataFormats>
<metadataFormat><metadataPrefix>oai_dc</metadataPrefix></metadataFormat>
<metadataFormat><metadataPrefix>marc</metadataPrefix></metadataFormat>
<metadataFormat><metadataPrefix>oai_dc</metadataPrefix></metadataFormat>
<metadataFormat><metadataPrefix>journalpublishing</metadataPrefix></metadataFormat>
</ListMetadataFormats></OAI-PMH>`)
	formats, msg, err := E7cParseLMF(body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if msg != "" || len(formats) != 3 { // oai_dc dup di-dedup
		t.Errorf("formats=%v msg=%q want 3 unik", formats, msg)
	}

	// endpoint hidup tapi verb error (badVerb — bukti endpoint hidup)
	_, msg, err = E7cParseLMF([]byte(`<OAI-PMH><error code="badVerb">illegal verb</error></OAI-PMH>`))
	if err != nil || msg == "" {
		t.Errorf("error OAI: err=%v msg=%q (harus errorMsg terisi, err=nil)", err, msg)
	}

	// HTML fallback = bukan respons OAI
	if _, _, err = E7cParseLMF([]byte("<html>404 page</html>")); err == nil {
		t.Error("html harus error")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
