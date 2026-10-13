package ojs

import (
	"strings"
	"testing"
)

// Fixture = marker nyata dari P2 (doc 42 §7.2): OJS teks/meta citation · pkp
// tunggal (j16) · WP j687 (wp-content + generator) · WAF 403 tanpa marker ·
// stub j681 non-HTML.

const htmlOJS = `<!DOCTYPE html><html><head>
<meta name="citation_title" content="Jurnal A" />
<meta name="generator" content="Open Journal Systems 3.3.0.11" />
</head><body><p>Open Journal Systems</p></body></html>`

const htmlPKPOnly = `<!DOCTYPE html><html><body>
<div>Published with Public Knowledge Project (PKP)</div></body></html>`

const htmlWP = `<!DOCTYPE html><html><head>
<meta name="generator" content="WordPress 6.4.2" />
<link href="/wp-content/themes/x/style.css" rel="stylesheet" />
</head><body>wp-json/wp/v2</body></html>`

const htmlCampuran = `<!DOCTYPE html><html><body>
Open Journal Systems <link href="/wp-content/x.css" /></body></html>`

const htmlPolos = `<!DOCTYPE html><html><head><title>X</title></head>
<body><p>Halo dunia tanpa marker platform.</p></body></html>`

func TestDetect(t *testing.T) {
	kasus := []struct {
		nama      string
		status    int
		body      string
		platform  string
		conf      string
		versi     string
		evidenceK string // substring wajib ada
	}{
		{"OJS lengkap", 200, htmlOJS, "ojs", "high", "3.3.0.11", "citation-meta"},
		{"pkp tunggal (j16)", 200, htmlPKPOnly, "ojs", "medium", "", "pkp"},
		{"wordpress (j687)", 200, htmlWP, "wordpress", "high", "", "wp-content"},
		{"campuran → unknown", 200, htmlCampuran, "unknown", "", "", "wp-content"},
		{"WAF 403 → unknown", 403, "<html>cloudflare</html>", "unknown", "", "", "status=403"},
		{"200 body kosong", 200, "", "unknown", "", "", "body kosong"},
		{"stub non-HTML (j681)", 200, "journal.portalgaruda.org", "static", "", "", "non-html"},
		{"HTML tanpa marker → custom", 200, htmlPolos, "custom", "", "", "html-tanpa-marker"},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			got := Detect(k.status, []byte(k.body))
			if got.Platform != k.platform {
				t.Errorf("platform = %q, want %q (evidence %q)", got.Platform, k.platform, got.Evidence)
			}
			if got.Confidence != k.conf {
				t.Errorf("confidence = %q, want %q", got.Confidence, k.conf)
			}
			if got.OJSVersion != k.versi {
				t.Errorf("version = %q, want %q", got.OJSVersion, k.versi)
			}
			if k.evidenceK != "" && !strings.Contains(got.Evidence, k.evidenceK) {
				t.Errorf("evidence %q tidak mengandung %q", got.Evidence, k.evidenceK)
			}
		})
	}
}
