package garuda

import (
	"strings"
	"testing"
)

// NormISSN: kanonik utk key/URL; format tak valid → "".
func TestNormISSN(t *testing.T) {
	cases := map[string]string{
		"25499904":   "25499904",
		"2549-9904":  "25499904",
		" 0215773x ": "0215773X",
		"0215773X":   "0215773X",
		"":           "",
		"12345":      "",
		"abc def!":   "",
		"-":          "", // placeholder garuda_pissn — dilarang masuk kolom (doc30 §6.2)
		"0":          "", // placeholder kolom lama
	}
	for in, want := range cases {
		if got := NormISSN(in); got != want {
			t.Errorf("NormISSN(%q) = %q, want %q", in, got, want)
		}
	}
}

// PilihSampelE6 = stratified deterministik 18/3/3, dedup ISSN lintas pool,
// GAGAL jujur bila pool kurang. Hasil harus identik antar panggilan.
func TestPilihSampelE6(t *testing.T) {
	var baris []E6Baris
	mk := func(rank, n, idawal int, sumber string) {
		for i := 0; i < n; i++ {
			id := int64(idawal + i)
			baris = append(baris, E6Baris{
				Sumber: sumber, ID: id, Rank: rank,
				Nama: "Jurnal " + string(rune('a'+i%26)) + string(rune('a'+i/26)),
				Key:  NormISSN(itoa(rank) + "99" + pad4(i)), // unik lintas rank (7–8 char)
			})
		}
	}
	mk(1, 40, 1000, "s1")
	mk(2, 10, 2000, "s23")
	mk(3, 10, 3000, "s23")

	s1, err := PilihSampelE6(baris, 18, 3, 3)
	if err != nil {
		t.Fatalf("PilihSampelE6: %v", err)
	}
	if len(s1) != 24 {
		t.Fatalf("sampel = %d, want 24", len(s1))
	}
	var n1, n2, n3 int
	seen := map[string]bool{}
	for _, b := range s1 {
		if seen[b.Key] {
			t.Errorf("duplikat Key %q di sampel", b.Key)
		}
		seen[b.Key] = true
		switch b.Rank {
		case 1:
			n1++
		case 2:
			n2++
		case 3:
			n3++
		}
	}
	if n1 != 18 || n2 != 3 || n3 != 3 {
		t.Errorf("komposisi = %d/%d/%d, want 18/3/3", n1, n2, n3)
	}
	// deterministik: run ulang identik
	s2, err := PilihSampelE6(baris, 18, 3, 3)
	if err != nil {
		t.Fatalf("run ulang: %v", err)
	}
	for i := range s1 {
		if s1[i].Key != s2[i].Key || s1[i].ID != s2[i].ID {
			t.Fatalf("tak deterministik di indeks %d", i)
		}
	}

	// pool kurang → error jujur
	if _, err := PilihSampelE6(baris, 18, 3, 99); err == nil {
		t.Error("pool S3 kurang harus GAGAL, bukan senyap")
	}
}

func pad4(i int) string {
	s := strings.Repeat("0", 4-len(itoa(i))) + itoa(i)
	return s
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// ParseCrossrefJournals: bentuk ASLI objek journal (message-type "journal")
// dgn counts + fractions coverage; ISSN dinormalisasi kanonik 8-huruf utk
// string & objek {type,value} (keputusan cek manual user 6 Okt); toleransi
// array; message-string = error.
func TestParseCrossrefJournals(t *testing.T) {
	ok := []byte(`{"status":"ok","message-type":"journal","message":{
	  "title":"SINERGI","publisher":"Universitas Mercu Buana",
	  "ISSN":["1410-2331","2460-1217"],
	  "counts":{"current-dois":172,"backfile-dois":340,"total-dois":512},
	  "coverage":{"abstracts-current":1.0,"licenses-current":0.994,
	              "orcids-current":0.186,"ror-ids-current":0.0}}}`)
	r, err := ParseCrossrefJournals(ok)
	if err != nil {
		t.Fatalf("parse ok: %v", err)
	}
	if !r.Tersedia || r.Judul != "SINERGI" || r.Penerbit != "Universitas Mercu Buana" {
		t.Errorf("hasil = %+v", r)
	}
	if r.DoiTotal != 512 || len(r.ISSNs) != 2 || r.ISSNs[1] != "24601217" {
		t.Errorf("issn harus kanonik tanpa strip: doi=%d issn=%v", r.DoiTotal, r.ISSNs)
	}
	if r.AbstractsFrac == nil || *r.AbstractsFrac != 1.0 || r.LicensesFrac == nil || r.RorFrac == nil {
		t.Errorf("fractions = %+v", r)
	}

	// bentuk objek {type,value} (endpoint Crossref lain) → tetap kanonik
	obj := []byte(`{"status":"ok","message-type":"journal","message":{
	  "title":"Ar-Raniry","ISSN":[{"type":"print","value":"2355-7885"},
	                              {"type":"electronic","value":"2355-813X"},
	                              {"type":"broken","value":""}]}}`)
	r0, err := ParseCrossrefJournals(obj)
	if err != nil {
		t.Fatalf("parse objek ISSN: %v", err)
	}
	if len(r0.ISSNs) != 2 || r0.ISSNs[0] != "23557885" || r0.ISSNs[1] != "2355813X" {
		t.Errorf("ISSN objek harus dinormalisasi+skip kosong, dapat %v", r0.ISSNs)
	}

	// toleransi cadangan: array collection
	arr := []byte(`{"status":"ok","message-type":"collection","message":[{"title":"X","publisher":"P"}]}`)
	r2, err := ParseCrossrefJournals(arr)
	if err != nil || !r2.Tersedia || r2.Judul != "X" {
		t.Errorf("array fallback = %+v err=%v", r2, err)
	}

	empty := []byte(`{"status":"ok","message-type":"collection","message":[]}`)
	r3, err := ParseCrossrefJournals(empty)
	if err != nil || r3.Tersedia {
		t.Errorf("array kosong = %+v err=%v — harus tak-terdaftar tanpa error", r3, err)
	}

	failure := []byte(`{"status":"failed","message-type":"failure","message":"Resource not found."}`)
	if _, err := ParseCrossrefJournals(failure); err == nil {
		t.Error("message-string harus = error parse (404 ditangani driver, bukan parser)")
	}
}

// ParseDOAJSearch: WAJIB verifikasi ISSN dlm results (noise total>0);
// total=0 = tak terdaftar; total>0 tanpa ISSN cocok = BUKAN covered.
func TestParseDOAJSearch(t *testing.T) {
	// noise: results[0] jurnal lain, results[1] = dicari (ISSN pakai strip)
	noise := []byte(`{"total":2,"results":[
	  {"bibjson":{"title":"Jurnal Lain","eissn":"1111-1111","pissn":"2222-2222"}},
	  {"bibjson":{"title":"JIPK","eissn":"2528-0759","pissn":"2085-5842",
	    "publisher":{"name":"Faculty of Fisheries"},
	    "subject":[{"term":"Aquaculture"}],
	    "license":[{"type":"CC BY-NC-SA"}],
	    "editorial":{"review_process":["double blind"]},
	    "board":[{"name":"A"}]}}]}`)
	d, err := ParseDOAJSearch(noise, "25280759")
	if err != nil {
		t.Fatalf("parse noise: %v", err)
	}
	if !d.Tersedia || d.Judul != "JIPK" || d.Penerbit != "Faculty of Fisheries" {
		t.Errorf("harus pilih hasil dgn ISSN cocok, dapat %+v", d)
	}
	if !d.AdaLisensi || !d.AdaEditorial || !d.AdaBoard || len(d.Subjek) != 1 {
		t.Errorf("presence = %+v", d)
	}

	none := []byte(`{"total":0,"results":[]}`)
	d2, err := ParseDOAJSearch(none, "25494600")
	if err != nil || d2.Tersedia {
		t.Errorf("total=0 = %+v err=%v — harus tak-terdaftar tanpa error", d2, err)
	}

	// total>0 TAPI tak ada ISSN cocok → BUKAN covered (+ catatan jujur)
	nomatch := []byte(`{"total":1,"results":[{"bibjson":{"title":"Beda","eissn":"9999-9999"}}]}`)
	d3, err := ParseDOAJSearch(nomatch, "25494600")
	if err != nil {
		t.Fatalf("parse nomatch: %v", err)
	}
	if d3.Tersedia || d3.Error == "" {
		t.Errorf("tanpa ISSN cocok harus tak-terdaftar + catatan, dapat %+v", d3)
	}

	anom := []byte(`{"total":3,"results":[]}`)
	if _, err := ParseDOAJSearch(anom, "12345678"); err == nil {
		t.Error("total>0 + results kosong harus = error anomali")
	}
}

// TitleSama: identik-normalisasi / contained ≥16 char; beda = false.
func TestTitleSama(t *testing.T) {
	if !TitleSama("JOIV: Jurnal of Information", "JOIV: Jurnal Of Information!") {
		t.Error("identik setelah normalisasi harus true")
	}
	if !TitleSama("International Journal of Technology", "International Journal of Technology (IJTech)") {
		t.Error("containment ≥16 harus true")
	}
	if TitleSama("Alpha Journal", "Beta Journal") {
		t.Error("beda judul harus false")
	}
	if TitleSama("", "anything") {
		t.Error("kosong harus false")
	}
}

// HitungE6Ringkasan: coverage dua sumber + strata + fill-rate field
// (fractions Crossref presence + presence DOAJ).
func TestHitungE6Ringkasan(t *testing.T) {
	f1 := 1.0
	items := []E6Hasil{
		{Rank: 1, Crossref: E6Crossref{Tersedia: true, Penerbit: "A", DoiTotal: 512, AbstractsFrac: &f1},
			Doaj: E6DOAJ{Tersedia: true, Judul: "X", Subjek: []string{"CS"}}, TitleSamaCrossref: true},
		{Rank: 2, Crossref: E6Crossref{Tersedia: false}, Doaj: E6DOAJ{Tersedia: true}},
		{Rank: 3, Crossref: E6Crossref{Tersedia: true, Penerbit: "B"}, Doaj: E6DOAJ{Tersedia: false}},
	}
	r := HitungE6Ringkasan(items)
	if r.CrossrefOK != 2 || r.DoajOK != 2 || r.Keduanya != 1 || r.SalahSatu != 2 || r.TidakAda != 0 {
		t.Errorf("coverage = %+v", r)
	}
	if s := r.PerStrata["S1"]; s.Sampel != 1 || s.CrossrefOK != 1 || s.DoajOK != 1 {
		t.Errorf("S1 = %+v", s)
	}
	if r.Field["crossref.doi"] != 1 || r.Field["crossref.abstract"] != 1 || r.Field["crossref.license"] != 0 {
		t.Errorf("field crossref = %v", r.Field)
	}
	if r.Field["doaj.subjek"] != 1 || r.Field["doaj.lisensi"] != 0 {
		t.Errorf("field doaj = %v", r.Field)
	}
	if r.TitleSamaCrossref != 1 {
		t.Errorf("title_sama_crossref = %d", r.TitleSamaCrossref)
	}
}
