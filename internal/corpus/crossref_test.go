package corpus

import (
	"strings"
	"testing"
	"time"
)

// TestParseCrossrefWorks — merge lisensi/subject/abstract per DOI (§5.5).
func TestParseCrossrefWorks(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	body := []byte(`{"status":"ok","message-type":"work-list","message":{
		"total-results":4,"items":[
		{"DOI":"10.1/OPEN","license":[{"URL":"https://creativecommons.org/licenses/by/4.0/","start":{"date-time":"2020-01-01T00:00:00Z"}}],"subject":["Public Health"]},
		{"DOI":"10.1/EMBARGO","license":[{"URL":"https://example.org/lic","start":{"date-time":"2027-01-01T00:00:00Z"}}]},
		{"DOI":"10.1/FREETOREAD","free-to-read":{"start-date":"2024-01-01"}},
		{"DOI":"10.1/KOSONG","subject":["Epidemiology"],"abstract":"<jats:p>The study <italic>analyzes</italic> data.</jats:p>"}
	]}}`)
	w, err := ParseCrossrefWorks(body, now)
	if err != nil {
		t.Fatalf("ParseCrossrefWorks: %v", err)
	}
	if w.Rows != 4 || len(w.Items) != 4 {
		t.Fatalf("rows/items = %d/%d — mau 4/4", w.Rows, len(w.Items))
	}
	// Open: start ≤ now.
	ci := w.Items["10.1/open"]
	if !ci.LicenseOpen || ci.Embargo || ci.LicenseURL == "" {
		t.Errorf("OPEN = %+v — mau LicenseOpen dgn URL", ci)
	}
	// Embargo: start > now.
	ci = w.Items["10.1/embargo"]
	if ci.LicenseOpen || !ci.Embargo {
		t.Errorf("EMBARGO = %+v — mau Embargo saja", ci)
	}
	// Free-to-read tanpa license.
	ci = w.Items["10.1/freetoread"]
	if !ci.FreeToRead || ci.LicenseOpen || ci.Embargo {
		t.Errorf("FREETOREAD = %+v — mau FreeToRead saja", ci)
	}
	// Kosong: subject + abstract strip JATS.
	ci = w.Items["10.1/kosong"]
	if len(ci.Subject) != 1 || ci.Subject[0] != "Epidemiology" {
		t.Errorf("KOSONG subject = %v", ci.Subject)
	}
	if !strings.Contains(ci.Abstract, "analyzes") || strings.Contains(ci.Abstract, "<") {
		t.Errorf("abstract JATS = %q — mau teks polos", ci.Abstract)
	}
	// licenseFromCrossref 4 kasus.
	if s, _, ok := licenseFromCrossref(w.Items["10.1/open"]); !ok || s != LicenseOpen {
		t.Errorf("open → %q/%v", s, ok)
	}
	if s, _, ok := licenseFromCrossref(w.Items["10.1/embargo"]); !ok || s != LicenseFreeNotOA {
		t.Errorf("embargo → %q/%v (mau free_not_open)", s, ok)
	}
	if s, _, ok := licenseFromCrossref(w.Items["10.1/freetoread"]); !ok || s != LicenseFreeNotOA {
		t.Errorf("freetoread → %q/%v", s, ok)
	}
	if _, _, ok := licenseFromCrossref(w.Items["10.1/kosong"]); ok {
		t.Error("kosong → ok=true (mau false → turun OAI)")
	}
	// Body rusak.
	if _, err := ParseCrossrefWorks([]byte("bukan json"), now); err == nil {
		t.Error("body rusak harusnya error")
	}
}
