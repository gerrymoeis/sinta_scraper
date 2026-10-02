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

	s, err := NewSession("test", "test-ua/1.0", 0, 0)
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

// TestInitFilterDenganSort memastikan: (1) urutan POST = filter lalu changesort,
// (2) refresh cookie MENGULANG sort (tanpa ini order kembali ke default di
// tengah crawl — doc 19 Bagian 9), (3) SetSortKey menolak nilai di luar 0..5.
func TestInitFilterDenganSort(t *testing.T) {
	var labels []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if r.Form.Get("changesort") == "1" {
			labels = append(labels, "sort:"+r.Form.Get("sort"))
		} else {
			labels = append(labels, "filter")
		}
		http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok", Path: "/"})
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer srv.Close()

	s, err := NewSession("test", "test-ua/1.0", 0, 0)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := s.SetSortKey(6); err == nil {
		t.Error("SetSortKey(6) seharusnya error")
	}
	if err := s.SetSortKey(4); err != nil {
		t.Fatalf("SetSortKey(4): %v", err)
	}

	if err := s.InitFilter(srv.URL, "filter_accreditation[1]=1&filter_journals=1"); err != nil {
		t.Fatalf("InitFilter: %v", err)
	}
	want := []string{"filter", "sort:4"}
	if len(labels) != len(want) {
		t.Fatalf("POST = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("POST #%d = %q, want %q", i+1, labels[i], want[i])
		}
	}

	// Paksa cookie "lewat umur" → refresh harus mengulang filter DAN sort.
	s.mu.Lock()
	s.filterInitAt = time.Now().Add(-cookieRefreshAfter - time.Minute)
	s.mu.Unlock()
	if err := s.refreshCookieIfNeeded(); err != nil {
		t.Fatalf("refreshCookieIfNeeded: %v", err)
	}
	want = []string{"filter", "sort:4", "filter", "sort:4"}
	if len(labels) != len(want) {
		t.Fatalf("setelah refresh: POST = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("POST #%d setelah refresh = %q, want %q", i+1, labels[i], want[i])
		}
	}
}

// TestChangeSort memastikan (doc 24 Tahap I3): (1) POST changesort terkirim
// ke sesi hidup dengan sort baru, (2) response TANPA cookie ci_session BUKAN
// error (pola asli server pada sesi hidup — sesi sudah ada, tak diganti),
// (3) sortKey di-memory ikut diperbarui → refresh cookie berikutnya memakai
// sort BARU, (4) nilai di luar 0..5 ditolak.
func TestChangeSort(t *testing.T) {
	var labels []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if r.Form.Get("changesort") == "1" {
			labels = append(labels, "sort:"+r.Form.Get("sort"))
			w.WriteHeader(http.StatusSeeOther) // sesi hidup: TANPA Set-Cookie
			return
		}
		labels = append(labels, "filter")
		http.SetCookie(w, &http.Cookie{Name: "ci_session", Value: "tok", Path: "/"})
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer srv.Close()

	s, err := NewSession("test", "test-ua/1.0", 0, 0)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := s.SetSortKey(4); err != nil {
		t.Fatalf("SetSortKey: %v", err)
	}
	if err := s.InitFilter(srv.URL, "filter_accreditation[1]=1"); err != nil {
		t.Fatalf("InitFilter: %v", err)
	}
	if err := s.ChangeSort(srv.URL, 9); err == nil {
		t.Error("ChangeSort(9) seharusnya error")
	}
	if err := s.ChangeSort(srv.URL, 2); err != nil {
		t.Fatalf("ChangeSort (tanpa cookie baru): %v", err)
	}
	want := []string{"filter", "sort:4", "sort:2"}
	if len(labels) != len(want) {
		t.Fatalf("POST = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("POST #%d = %q, want %q", i+1, labels[i], want[i])
		}
	}
	s.mu.Lock()
	gotKey := s.sortKey
	s.mu.Unlock()
	if gotKey != 2 {
		t.Errorf("sortKey = %d, want 2 (harus ikut diperbarui untuk refresh)", gotKey)
	}

	// Refresh cookie wajib mengulang sort BARU (2), bukan sort awal (4).
	s.mu.Lock()
	s.filterInitAt = time.Now().Add(-cookieRefreshAfter - time.Minute)
	s.mu.Unlock()
	if err := s.refreshCookieIfNeeded(); err != nil {
		t.Fatalf("refreshCookieIfNeeded: %v", err)
	}
	want = []string{"filter", "sort:4", "sort:2", "filter", "sort:2"}
	if len(labels) != len(want) {
		t.Fatalf("setelah refresh: POST = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("POST #%d setelah refresh = %q, want %q", i+1, labels[i], want[i])
		}
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
