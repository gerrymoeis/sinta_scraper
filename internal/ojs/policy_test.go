package ojs

import (
	"strings"
	"testing"
)

// Fixture noise = bukti P3 (doc 42 §7.3): WebFeedGatewayPlugin · CDN eksternal
// dgn substring "apc" — KEDUANYA wajib terbuang sebelum GET full-run.

const homeHTML = `<!DOCTYPE html><html><body>
<a href="/index">Home</a>
<a href="/author/fees">Author Fees</a>
<a href="/index.php/Jurnal/about">About</a>
<a href="/instruction">Instructions</a>
<a href="/gateway/plugin/WebFeedGatewayPlugin/index">Web Feed</a>
<a href="https://cdn.example.com/bootstrapcdn-apc.css">CSS</a>
<a href="/lib/bootstrapcdn-apc.css">CSS lokal</a>
<a href="/copyright">Copyright</a>
<a href="#top">Top</a>
<a href="mailto:edit@x.id">Mail</a>
</body></html>`

func TestFindPolicyLinks(t *testing.T) {
	got := FindPolicyLinks("https://jurnal.ac.id/", []byte(homeHTML), 0)
	urls := map[string]int{}
	for _, l := range got {
		urls[l.URL] = l.Score
	}
	// Lolos: same-host non-gateway non-aset.
	if _, ok := urls["https://jurnal.ac.id/author/fees"]; !ok {
		t.Errorf("author/fees harus lolos: %v", urls)
	}
	if _, ok := urls["https://jurnal.ac.id/index.php/Jurnal/about"]; !ok {
		t.Errorf("about harus lolos: %v", urls)
	}
	if _, ok := urls["https://jurnal.ac.id/instruction"]; !ok {
		t.Errorf("instruction harus lolos: %v", urls)
	}
	// Tolak: gateway/feed · CDN eksternal · aset lokal dgn substring "apc".
	for _, tolak := range []string{
		"WebFeedGatewayPlugin", "cdn.example.com", "bootstrapcdn-apc.css",
	} {
		for u := range urls {
			if strings.Contains(u, tolak) {
				t.Errorf("%q tidak boleh lolos (ada di %q)", tolak, u)
			}
		}
	}
	// Skor: /author/fees (fee) > /index (tanpa keyword).
	if urls["https://jurnal.ac.id/author/fees"] == 0 {
		t.Error("author/fees harus skor > 0")
	}
	if urls["https://jurnal.ac.id/index"] != 0 {
		t.Error("index harus skor 0")
	}
}

func TestFindPolicyLinksMax(t *testing.T) {
	got := FindPolicyLinks("https://jurnal.ac.id/", []byte(homeHTML), 2)
	if len(got) != 2 {
		t.Fatalf("jumlah = %d, want 2 (max)", len(got))
	}
	// Dua teratas = skor tertinggi (fee paths).
	if got[0].Score < got[1].Score {
		t.Errorf("urutan salah: %v", got)
	}
}

func TestClassifyPolicyType(t *testing.T) {
	kasus := map[string]string{
		"https://x.id/author/fees":      "fee",
		"https://x.id/about":            "about",
		"https://x.id/about/submission": "about",
		"https://x.id/copyright":        "copyright",
		"https://x.id/privacy":          "privacy",
		"https://x.id/instruction":      "author_guideline",
		"https://x.id/index":            "other",
	}
	for url, want := range kasus {
		if got := ClassifyPolicyType(url); got != want {
			t.Errorf("ClassifyPolicyType(%q) = %q, want %q", url, got, want)
		}
	}
}

// TestFindPolicyLinksUpgradeSkema — Opsi 1 (approve 13 Okt): base https +
// href http SAME-HOST → hasil https (bukti j663: body memuat http, port80
// tutup, https hidup). http beda-host TETAP ditolak (host-check dulu);
// base http → http dipertahankan (host http-only sah).
func TestFindPolicyLinksUpgradeSkema(t *testing.T) {
	body := []byte(`<!DOCTYPE html><html><body>
<a href="http://jurnal.ac.id/index.php/about">About lama</a>
<a href="http://lain.example.com/fees">Eksternal</a>
</body></html>`)

	got := FindPolicyLinks("https://jurnal.ac.id/index.php/home", body, 0)
	urls := map[string]bool{}
	for _, l := range got {
		urls[l.URL] = true
	}
	if !urls["https://jurnal.ac.id/index.php/about"] {
		t.Errorf("http same-host harus di-upgrade ke https: %v", urls)
	}
	for u := range urls {
		if strings.Contains(u, "lain.example.com") {
			t.Errorf("http beda-host tidak boleh lolos: %q", u)
		}
	}

	// Base http (bukan https) → tak ada upgrade (http dipertahankan).
	gotHTTP := FindPolicyLinks("http://jurnal.ac.id/", body, 0)
	for _, l := range gotHTTP {
		if strings.HasPrefix(l.URL, "https://") {
			t.Errorf("base http tidak boleh meng-upgrade skema: %q", l.URL)
		}
	}
}
