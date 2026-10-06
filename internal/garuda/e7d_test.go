package garuda

import "testing"

func TestHyphenISSN(t *testing.T) {
	cases := map[string]string{
		"25799320":  "2579-9320", // 8 digit
		"2355813X":  "2355-813X", // 8 dgn X
		"01260537":  "0126-0537", // leading zero
		"2579-9320": "2579-9320", // input sudah berstrip → dinormalkan dulu
		"25799":     "",          // terlalu pendek
		"":          "",          // kosong
		"AB@DEFGH!": "",          // karakter terlarang
	}
	for in, want := range cases {
		if got := HyphenISSN(in); got != want {
			t.Errorf("HyphenISSN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestE7dKlaim(t *testing.T) {
	// 3 FOUND klaim user
	for _, k := range []string{"25799320", "20878575", "23381353"} {
		if E7dKlaim(k) != "found" {
			t.Errorf("E7dKlaim(%s) = %q, want found", k, E7dKlaim(k))
		}
	}
	// 8 nol klaim user
	for _, k := range []string{"24778516", "25024760", "25030310", "25025457",
		"25494600", "27222594", "25027913", "2355813X"} {
		if E7dKlaim(k) != "nol" {
			t.Errorf("E7dKlaim(%s) = %q, want nol", k, E7dKlaim(k))
		}
	}
	// 5 lampiran §2 = tanpa klaim
	for _, k := range []string{"20893272", "25976052", "26849240", "26143380", "2684740X"} {
		if E7dKlaim(k) != "" {
			t.Errorf("E7dKlaim(%s) = %q, want kosong", k, E7dKlaim(k))
		}
	}
}

func TestE7dCrossLabel(t *testing.T) {
	if got := E7dCrossLabel("found", true); got != "sesuai" {
		t.Errorf("klaim found + hasil found = %q", got)
	}
	if got := E7dCrossLabel("nol", false); got != "sesuai" {
		t.Errorf("klaim nol + hasil nol = %q", got)
	}
	if got := E7dCrossLabel("found", false); got != "SELISIH:klaim=found,hasil=nol" {
		t.Errorf("mismatch = %q", got)
	}
	if got := E7dCrossLabel("", true); got != "" {
		t.Errorf("tanpa klaim harus kosong, got %q", got)
	}
}

func TestHitungE7d(t *testing.T) {
	items := []E7dHasil{
		{Kelompok: "sweep16", Klaim: "found", Doaj: E6DOAJ{Tersedia: true, HTTP: 200}},
		{Kelompok: "sweep16", Klaim: "found", Doaj: E6DOAJ{HTTP: 200}}, // selisih
		{Kelompok: "sweep16", Doaj: E6DOAJ{Tersedia: true, HTTP: 200}}, // §2 found
		{Kelompok: "miss8", Doaj: E6DOAJ{HTTP: 200}},
		{Kelompok: "miss8", Doaj: E6DOAJ{Tersedia: true, HTTP: 200}},
	}
	r := HitungE7d(items)
	if r.SweepFound != 2 || r.SweepNol != 1 {
		t.Errorf("sweep found/nol = %d/%d", r.SweepFound, r.SweepNol)
	}
	if r.KlaimDibanding != 2 || r.KlaimSelisih != 1 {
		t.Errorf("klaim banding/selisih = %d/%d", r.KlaimDibanding, r.KlaimSelisih)
	}
	if r.Miss8Found != 1 || r.Miss8Nol != 1 {
		t.Errorf("miss8 found/nol = %d/%d", r.Miss8Found, r.Miss8Nol)
	}
	if r.DoajE6Final != 10 { // 8 + 2 sweep found
		t.Errorf("doaj_e6_final = %d, want 10", r.DoajE6Final)
	}
}
