package garuda

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
)

func TestKlasifikasiE7g(t *testing.T) {
	cases := []struct {
		name   string
		status int
		err    error
		want   string
	}{
		{"200", 200, nil, ClsOK},
		{"200 body gagal", 200, errors.New("baca body"), ClsOK}, // status menang
		{"404", 404, fmt.Errorf("GET x: status 404"), Cls404},
		{"410", 410, fmt.Errorf("GET x: status 410"), Cls410},
		{"403 waf", 403, fmt.Errorf("GET x: status 403"), ClsWAF},
		{"429 waf", 429, fmt.Errorf("GET x: status 429"), ClsWAF},
		{"503", 503, fmt.Errorf("GET x: status 503"), ClsServer},
		{"400 lain", 400, fmt.Errorf("GET x: status 400"), ClsLain},
		{"dns", 0, &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}, ClsDNS},
		{"dns wrapped", 0, fmt.Errorf("do: %w", &net.DNSError{Err: "no such host"}), ClsDNS},
		{"timeout ctx", 0, context.DeadlineExceeded, ClsTimeout},
		{"timeout wrapped", 0, fmt.Errorf("Get: %w", context.DeadlineExceeded), ClsTimeout},
		{"unreachable", 0, errors.New("dial tcp: connection refused"), ClsUnreach},
	}
	for _, c := range cases {
		if got := KlasifikasiE7g(c.status, c.err); got != c.want {
			t.Errorf("%s: KlasifikasiE7g(%d, %v) = %q, want %q",
				c.name, c.status, c.err, got, c.want)
		}
	}
}

func TestE7gMatiRetry(t *testing.T) {
	mati := []string{Cls404, Cls410, ClsDNS, ClsTimeout, ClsUnreach}
	for _, c := range mati {
		if !E7gMati(c) {
			t.Errorf("E7gMati(%q) = false, want true", c)
		}
	}
	bukan := []string{ClsOK, ClsWAF, ClsServer, ClsLain, ""}
	for _, c := range bukan {
		if E7gMati(c) {
			t.Errorf("E7gMati(%q) = true, want false", c)
		}
	}
	retry := []string{ClsServer, ClsTimeout, ClsUnreach}
	for _, c := range retry {
		if !E7gRetry(c) {
			t.Errorf("E7gRetry(%q) = false, want true", c)
		}
	}
	// DNS deterministik & WAF/404 final — TIDAK retry (hemat pagu)
	for _, c := range []string{ClsDNS, ClsWAF, Cls404, ClsOK, ClsLain} {
		if E7gRetry(c) {
			t.Errorf("E7gRetry(%q) = true, want false", c)
		}
	}
}

func TestParseRobotsDanBoleh(t *testing.T) {
	robots := []byte(`# komentar
User-agent: *
Disallow: /private/
Disallow: /tmp
Allow: /private/public.html

User-agent: Googlebot
Disallow: /g/

Sitemap: https://x/sitemap.xml
`)
	r := ParseRobots(robots)
	if r == nil {
		t.Fatal("ParseRobots = nil")
	}
	const ua = "sinta-scraper/0.1 (riset)"
	cases := []struct {
		uri  string
		want bool
	}{
		{"/", true},
		{"/index.php/ijip", true},
		{"/private/x", false},
		{"/private/public.html", true}, // Allow menang (seri dgn prefix? — pola lebih panjang)
		{"/tmp", false},
		{"/tmp/a/b", false},  // prefiks
		{"/tmpx", false},     // pola prefiks tanpa batas segmen (spesifikasi Google)
		{"/g/makalah", true}, // UA bukan Googlebot → kelompok /g/ TAK dipakai
		{"/page/2", true},
	}
	for _, c := range cases {
		if got := RobotsBoleh(r, c.uri, ua); got != c.want {
			t.Errorf("Boleh(%q) = %v, want %v", c.uri, got, c.want)
		}
	}
	// UA Googlebot → pakai kelompok spesifiknya
	if RobotsBoleh(r, "/g/x", "Mozilla/5.0 (compatible; Googlebot/2.1)") {
		t.Error("Googlebot /g/x harusnya ditolak")
	}
	if !RobotsBoleh(r, "/private/x", "Mozilla/5.0 (compatible; Googlebot/2.1)") {
		// kelompok Googlebot tanpa aturan private → boleh
		t.Error("Googlebot /private/x harusnya diizinkan (kelompoknya sendiri)")
	}
}

func TestParseRobotsKosongDanTanpaBintang(t *testing.T) {
	if ParseRobots(nil) != nil {
		t.Error("body kosong harus nil")
	}
	r := ParseRobots([]byte("User-agent: Googlebot\nDisallow: /\n"))
	// UA kita tak cocok & tanpa kelompok "*": tanpa pembatasan
	if !RobotsBoleh(r, "/x", "sinta-scraper/0.1") {
		t.Error("tanpa kelompok cocok harusnya boleh")
	}
	// Disallow kosong = izinkan semua
	r2 := ParseRobots([]byte("User-agent: *\nDisallow:\n"))
	if !RobotsBoleh(r2, "/x", "sinta-scraper") {
		t.Error("Disallow kosong harusnya boleh")
	}
	// tanpa file = nil = boleh
	if !RobotsBoleh(nil, "/x", "ua") {
		t.Error("Robots nil harusnya boleh")
	}
}

func TestRobotsBolehBintangFallback(t *testing.T) {
	r := ParseRobots([]byte("User-agent: *\nDisallow: /\n"))
	if RobotsBoleh(r, "/x", "sinta-scraper/0.1") {
		t.Error("* Disallow / harusnya menolak semua")
	}
	// pola $ akhir
	r2 := ParseRobots([]byte("User-agent: *\nDisallow: /halaman$\n"))
	if RobotsBoleh(r2, "/halaman", "ua") {
		t.Error("/halaman$ harus menolak /halaman persis")
	}
	if !RobotsBoleh(r2, "/halaman/2", "ua") {
		t.Error("/halaman$ tak boleh menolak /halaman/2")
	}
	// wildcard *
	r3 := ParseRobots([]byte("User-agent: *\nDisallow: /*/privat/\n"))
	if RobotsBoleh(r3, "/a/privat/b", "ua") {
		t.Error("/*/privat/ harus menolak /a/privat/b")
	}
	if !RobotsBoleh(r3, "/privat/b", "ua") {
		t.Error("/*/privat/ tak cocok dgn /privat/b (butuh prefiks sebelum)")
	}
}

func TestRobotsOriginURI(t *testing.T) {
	if got := RobotsOrigin("https://Jurnal.X.Id/index.php/ijip?r=1"); got != "https://jurnal.x.id" {
		t.Errorf("Origin = %q", got)
	}
	if got := RobotsOrigin("nonsense"); got != "" {
		t.Errorf("Origin tanpa scheme = %q, want \"\"", got)
	}
	if got := RobotsURI("https://x.id/index.php/a?b=1"); got != "/index.php/a?b=1" {
		t.Errorf("URI = %q", got)
	}
	if got := RobotsURI("https://x.id"); got != "/" {
		t.Errorf("URI root = %q, want /", got)
	}
}

func TestOpenAlexHomepage(t *testing.T) {
	body := []byte(`{"id":"https://openalex.org/S123","homepage_url":" https://journal.ac.id "}`)
	got, err := OpenAlexHomepage(body)
	if err != nil || got != "https://journal.ac.id" {
		t.Errorf("got %q err %v", got, err)
	}
	if _, err := OpenAlexHomepage([]byte(`{"error":"Not Found"}`)); err == nil {
		t.Error("error field harus menghasilkan error")
	}
	if _, err := OpenAlexHomepage([]byte(`{`)); err == nil {
		t.Error("JSON rusak harus error")
	}
	// homepage_url null → kosong, tanpa error
	got, err = OpenAlexHomepage([]byte(`{"homepage_url":null}`))
	if err != nil || got != "" {
		t.Errorf("null → got %q err %v", got, err)
	}
}

func TestDoajRefJournal(t *testing.T) {
	body := []byte(`{"results":[{"bibjson":{"ref":{"journal":"http://ojs.x.ac.id/"}}},
		{"bibjson":{"ref":{"journal":"http://dua/"}}}]}`)
	got, err := DoajRefJournal(body)
	if err != nil || got != "http://ojs.x.ac.id/" {
		t.Errorf("got %q err %v", got, err)
	}
	// tanpa ref → kosong tanpa error
	got, err = DoajRefJournal([]byte(`{"results":[{"bibjson":{"title":"x"}}]}`))
	if err != nil || got != "" {
		t.Errorf("tanpa ref → got %q err %v", got, err)
	}
	if _, err := DoajRefJournal([]byte(`{`)); err == nil {
		t.Error("JSON rusak harus error")
	}
}
