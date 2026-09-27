package sinta

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestInitFilterDanFetchPage(t *testing.T) {
	var posts, gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			posts++
			if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
				t.Errorf("Content-Type POST = %q", got)
			}
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			if got := r.Form.Get("filter_accreditation[1]"); got != "1" {
				t.Errorf("filter_accreditation[1] = %q, want 1", got)
			}
			http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok123", Path: "/"})
			w.WriteHeader(http.StatusSeeOther) // tiru server asli: 303

		case http.MethodGet:
			gets++
			ck, err := r.Cookie("ci_session")
			if err != nil || ck.Value != "tok123" {
				t.Errorf("GET #%d tanpa cookie ci_session valid — jar tidak bekerja?", gets)
				http.Error(w, "cookie hilang", http.StatusUnauthorized)
				return
			}
			switch gets {
			case 1:
				w.WriteHeader(http.StatusForbidden) // simulasi cookie mati
			case 2:
				w.WriteHeader(http.StatusServiceUnavailable) // gangguan sementara
			default:
				data, err := os.ReadFile("testdata/page1.html")
				if err != nil {
					t.Errorf("baca fixture: %v", err)
					return
				}
				w.Write(data)
			}
		}
	}))
	defer srv.Close()

	s, err := NewSession("test", "test-ua/1.0")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	s.backoffs = []time.Duration{0, 0, 0} // tanpa jeda saat test

	if err := s.InitFilter(srv.URL, "filter_accreditation[1]=1&filter_journals=1"); err != nil {
		t.Fatalf("InitFilter: %v", err)
	}
	res, err := s.FetchPage(srv.URL, "", 2)
	if err != nil {
		t.Fatalf("FetchPage: %v", err)
	}

	if posts != 2 {
		t.Errorf("POST filter = %d, want 2 (init + reinit setelah 403)", posts)
	}
	if gets != 3 {
		t.Errorf("GET = %d, want 3 (403, 503, sukses)", gets)
	}
	if res.CurrentPage != 2 {
		t.Errorf("CurrentPage = %d, want 2", res.CurrentPage)
	}
	if res.TotalPages != 1678 || res.TotalJournals != 16772 {
		t.Errorf("pagination = %d/%d, want 1678/16772", res.TotalPages, res.TotalJournals)
	}
	if len(res.Journals) != 10 {
		t.Errorf("jumlah kartu = %d, want 10", len(res.Journals))
	}
}

func TestBuildURL(t *testing.T) {
	got, err := buildURL("https://contoh.go.id/journals", "sinta=6", 3)
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse hasil: %v", err)
	}
	q := u.Query()
	if q.Get("page") != "3" || q.Get("sinta") != "6" {
		t.Errorf("query = page:%q sinta:%q, want 3/6", q.Get("page"), q.Get("sinta"))
	}
	if _, err := buildURL("https://x/y", "%%%rusak", 1); err == nil {
		t.Error("extraQuery rusak harusnya mengembalikan error")
	}
}
