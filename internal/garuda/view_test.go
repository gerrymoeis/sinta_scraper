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
  <a href="https://garuda.kemdiktisaintek.go.id/publisher/1">Penerbit Contoh</a><br>
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

// snippetFilterTahun = potongan HTML ASLI yang dikirim user (halaman view
// AGRARIS 35522 — 4 Okt 2026): blok "Filter by Year" embed di HTML response
// (tanpa XHR), tahun 2015–2026 → penentu entri duplikat aktif (doc 32 §4).
const snippetFilterTahun = `<div class="ui segment padded">
            <a class="ui top attached label">Filter by Year</a>
            <p>
                2015                <span style="float: right"> 2026</span>
            </p> 
            <div id="slider-range" class="ui-slider ui-corner-all ui-slider-horizontal ui-widget ui-widget-content"><div class="ui-slider-range ui-corner-all ui-widget-header" style="left: 0%; width: 100%;"></div><span tabindex="0" class="ui-slider-handle ui-corner-all ui-state-default" style="left: 0%;"></span><span tabindex="0" class="ui-slider-handle ui-corner-all ui-state-default" style="left: 100%;"></span></div>
            <br>

            <form id="filter_year" class="ui mini form" action="" method="get">
                                <div class="equal width fields">
                    <div class="field">
                        <label>From</label>
                        <input type="text" id="from" name="from">
                    </div>
                    <div class="field">
                        <label>To</label>
                        <input type="text" id="to" name="to">
                    </div>
                </div>
                <div class="equal width fields">
                    <div class="field">
                        <button class="ui mini fluid button" type="submit">Filter</button>
                    </div>
                    <div class="field">
                        <a class="ui mini fluid button basic red" href="/journal/view/35522">Reset</a>
                    </div>
                </div>
                
            </form>
        </div>`

func TestParseViewPageFilterTahun(t *testing.T) {
	v, err := ParseViewPage(strings.NewReader(snippetFilterTahun))
	if err != nil {
		t.Fatalf("ParseViewPage: %v", err)
	}
	if v.YearFrom != 2015 || v.YearTo != 2026 {
		t.Errorf("tahun = %d–%d (diharapkan 2015–2026)", v.YearFrom, v.YearTo)
	}
}

func TestParseViewPageTanpaFilterTahun(t *testing.T) {
	// halaman tanpa blok tahun (Record Not Found / jurnal tanpa artikel)
	// → 0,0 agar penelusur bisa membedakan "tak ada" vs "tanpa konten".
	v, err := ParseViewPage(strings.NewReader(`<div class="j-meta-title">X</div>Record Not Found`))
	if err != nil {
		t.Fatalf("ParseViewPage: %v", err)
	}
	if v.YearFrom != 0 || v.YearTo != 0 {
		t.Errorf("tahun = %d–%d (diharapkan 0–0)", v.YearFrom, v.YearTo)
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
