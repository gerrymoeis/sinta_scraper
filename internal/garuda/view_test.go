package garuda

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// snippetView mewakili struktur halaman view/N asli (E3, doc 31 §2).
const snippetView = `<!DOCTYPE html><html><body>
<div class="j-meta-title">JURNAL CONTOH ILMU</div>
<div class="j-meta-pub">Published by
  <a href="https://garuda.kemdikbud.go.id/publisher/1">Penerbit Contoh</a><br>
  ISSN : <a href="#">2442-1101</a> ;
  EISSN : <a href="#">2580-9912</a> ;
  DOI : <a href="#">10.1234/contoh</a>
</div>
<div class="j-meta-area">
  <a href="/area/index/17">Social Sciences</a>
  <a href="/area/index/17">Social Sciences</a>
</div>
<h5><a href="https://ojs.contoh.org" target="_blank"><i class="angle right icon"></i>Home Page</a></h5>
<h5><a href="https://ojs.contoh.org/index2/oai" target="_blank"><i class="angle right icon"></i>OAI Link</a></h5>
</body></html>`

func TestParseViewPage(t *testing.T) {
	v, err := ParseViewPage(strings.NewReader(snippetView))
	if err != nil {
		t.Fatalf("ParseViewPage: %v", err)
	}
	if v.NotFound {
		t.Error("NotFound seharusnya false")
	}
	if v.Title != "JURNAL CONTOH ILMU" {
		t.Errorf("Title = %q", v.Title)
	}
	if v.Publisher != "Penerbit Contoh" {
		t.Errorf("Publisher = %q", v.Publisher)
	}
	if v.PrintISSN != "2442-1101" {
		t.Errorf("PrintISSN = %q (lookbehind E gagal?)", v.PrintISSN)
	}
	if v.EISSN != "2580-9912" {
		t.Errorf("EISSN = %q", v.EISSN)
	}
	if v.DOIURL != "10.1234/contoh" {
		t.Errorf("DOIURL = %q", v.DOIURL)
	}
	if v.HomeURL != "https://ojs.contoh.org" {
		t.Errorf("HomeURL = %q", v.HomeURL)
	}
	if v.OAIURL != "https://ojs.contoh.org/index2/oai" {
		t.Errorf("OAIURL = %q", v.OAIURL)
	}
	if len(v.Areas) != 1 || v.Areas[0] != "Social Sciences" {
		t.Errorf("Areas = %v (duplikat harusnya terbuang)", v.Areas)
	}
}

func TestParseViewPagePlaceholderDanNotFound(t *testing.T) {
	// placeholder "-" → string kosong; Record Not Found → flag true.
	doc := `<div class="j-meta-title">Judul</div>
	ISSN : <a href="#">-</a> ; EISSN : <a href="#">-</a> ; DOI : <a href="#">-</a>
	Record Not Found`
	v, err := ParseViewPage(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("ParseViewPage: %v", err)
	}
	if !v.NotFound {
		t.Error("NotFound harus true")
	}
	if v.PrintISSN != "" || v.EISSN != "" || v.DOIURL != "" {
		t.Errorf("placeholder '-' harus jadi kosong: pissn=%q eissn=%q doi=%q",
			v.PrintISSN, v.EISSN, v.DOIURL)
	}
	if v.HomeURL != "" || v.OAIURL != "" {
		t.Errorf("Record Not Found seharusnya tanpa link: home=%q oai=%q", v.HomeURL, v.OAIURL)
	}
}

// TestParseViewPageFixture memverifikasi parser pada fixture view asli E3
// (diluar git — skip bila tak ada; doc 31 §2).
func TestParseViewPageFixture(t *testing.T) {
	path := filepath.Join("..", "..", "data", "stage2", "fixtures", "view-7211.html")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixture %s tak ada (data lokal E3): %v", path, err)
	}
	v, err := ParseViewPage(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("ParseViewPage fixture: %v", err)
	}
	if v.NotFound {
		t.Error("view-7211 tidak boleh Record Not Found")
	}
	if v.Title == "" {
		t.Error("Title kosong pada fixture asli")
	}
	if v.HomeURL == "" || v.OAIURL == "" {
		t.Errorf("link hilang pada fixture asli: home=%q oai=%q", v.HomeURL, v.OAIURL)
	}
}
