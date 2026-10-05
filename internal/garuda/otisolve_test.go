package garuda

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/network"
)

// otiFlags harus = mirror nodriver: 13 flag wajib ada, sinyal deteksi
// (enable-automation, headless, dst.) TIDAK boleh ada.
func TestOTIFlagsMirrorNodriver(t *testing.T) {
	want := map[string]string{
		"remote-allow-origins":                "*",
		"no-first-run":                        "",
		"no-service-autorun":                  "",
		"no-default-browser-check":            "",
		"homepage":                            "about:blank",
		"no-pings":                            "",
		"password-store":                      "basic",
		"disable-infobars":                    "",
		"disable-breakpad":                    "",
		"disable-dev-shm-usage":               "",
		"disable-session-crashed-bubble":      "",
		"disable-search-engine-choice-screen": "",
		"disable-features":                    "IsolateOrigins,site-per-process",
	}
	forbidden := []string{
		"enable-automation", "headless", "hide-scrollbars", "mute-audio",
		"disable-extensions", "disable-background-networking", "disable-sync",
	}
	got := map[string]string{}
	for _, f := range otiFlags() {
		got[f.Name] = f.Value
		if v, ok := want[f.Name]; ok && v != f.Value {
			t.Errorf("flag %s = %q, want %q", f.Name, f.Value, v)
		}
		if f.Name == "enable-automation" || f.Name == "headless" {
			t.Errorf("flag deteksi %q tidak boleh ada", f.Name)
		}
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("flag nodriver wajib %q hilang", name)
		}
	}
	for _, name := range forbidden {
		if _, ok := got[name]; ok {
			t.Errorf("flag %q tidak boleh ada", name)
		}
	}
}

func TestBrowserCandidatesLintasOS(t *testing.T) {
	cases := map[string]struct {
		goos string
		must []string // substring yg harus ada di salah satu kandidat
	}{
		"windows": {"windows", []string{`chrome.exe`, `msedge.exe`, `chromium`}},
		"darwin":  {"darwin", []string{"Google Chrome", "Microsoft Edge", "Chromium"}},
		"linux":   {"linux", []string{"google-chrome", "microsoft-edge", "chromium"}},
	}
	for name, c := range cases {
		cands := browserCandidates(c.goos)
		if len(cands) == 0 {
			t.Fatalf("%s: kandidat kosong", name)
		}
		joined := strings.Join(cands, "\n")
		for _, m := range c.must {
			if !strings.Contains(joined, m) {
				t.Errorf("%s: kandidat tak memuat %q", name, m)
			}
		}
	}
	// adaptif (approve #4): Chrome urut pertama lalu Edge lalu Chromium
	if first := browserCandidates("windows")[0]; !strings.Contains(first, "chrome.exe") {
		t.Errorf("prioritas windows = Chrome (adaptif), dapat %q", first)
	}
	if first := browserCandidates("darwin")[0]; !strings.Contains(first, "Google Chrome") {
		t.Errorf("prioritas darwin = Chrome (adaptif), dapat %q", first)
	}
	if first := browserCandidates("linux")[0]; !strings.Contains(first, "google-chrome") {
		t.Errorf("prioritas linux = Chrome (adaptif), dapat %q", first)
	}
}

// Port debug WAJIB spesifik ≠ "0" — fakta MDN/Chromium issue 40685960:
// port=0/pipe → navigator.webdriver=true (Turnstile tolak); spesifik = false.
func TestFreeDebugPortSpesifik(t *testing.T) {
	p, err := freeDebugPort()
	if err != nil {
		t.Fatalf("freeDebugPort: %v", err)
	}
	if p == "" || p == "0" {
		t.Fatalf("port = %q — harus spesifik ≠ 0 (wd=true kalau 0)", p)
	}
	if _, err := strconv.Atoi(p); err != nil {
		t.Fatalf("port %q bukan angka: %v", p, err)
	}
	opts, err := otiAllocOpts("chrome")
	if err != nil {
		t.Fatalf("otiAllocOpts: %v", err)
	}
	if len(opts) != len(otiFlags())+3 { // ExecPath + remote.WebSocket + port
		t.Errorf("jumlah opsi = %d, want %d", len(opts), len(otiFlags())+3)
	}
}

func TestJoinCookies(t *testing.T) {
	cks := []*network.Cookie{
		{Domain: ".joiv.org", Name: "cf_clearance", Value: "abc123"},
		{Domain: "joiv.org", Name: "__cf_bm", Value: "bm1"},
		{Domain: ".other.org", Name: "leak", Value: "x"},
	}
	got := joinCookies(cks, "joiv.org")
	if got != "cf_clearance=abc123; __cf_bm=bm1" {
		t.Errorf("joinCookies = %q", got)
	}
	if s := joinCookies(cks, "unrelated.io"); s != "" {
		t.Errorf("cookie lintas-host harus kosong, dapat %q", s)
	}
}

// FindBrowser utk GOOS berjalan harus member pesan error jujur & jelas bila
// kosong (daftar "dicek:" + hint env) — jalur sukses diuji manual via probe live.
func TestFindBrowserErrorJelas(t *testing.T) {
	if p, err := FindBrowser(); err == nil {
		t.Logf("browser terdeteksi: %s", p)
	} else if p := os.Getenv(browserEnv); p == "" {
		// mesin tanpa browser Chromium = wajar; pesan harus memuat hint env
		// + daftar kandidat yg dicek (jujur, bukan cuma "tidak bisa")
		if !strings.Contains(err.Error(), browserEnv) {
			t.Errorf("error tanpa hint %s: %v", browserEnv, err)
		}
		if !strings.Contains(err.Error(), "dicek:") {
			t.Errorf("error tanpa daftar kandidat yg dicek: %v", err)
		}
	}
}
