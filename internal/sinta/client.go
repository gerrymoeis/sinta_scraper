package sinta

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

func NewHTTPClient(stage string) *http.Client {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		MaxConnsPerHost:     20,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	return &http.Client{
		Transport: &MetricsTransport{inner: transport, stage: stage},
		Timeout:   30 * time.Second,
	}
}

// NewGET membangun GET dengan header MINIMUM sesuai keputusan doc 14
// Bagian 3 (keputusan user 29 Sep 2026 — opsi B): UA hibrida + Accept +
// Accept-Language sebagai praktik baik, plus Referer yang secara harfiah
// benar (kita datang dari listing base). TANPA sec-fetch-*/sec-ch-ua/
// Upgrade-Insecure-Requests — tes doc 14 Bagian 1 membuktikan header itu
// tidak diperiksa server, dan kita tidak meniru identitas browser.
func NewGET(rawURL, userAgent, referer string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en;q=0.8")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	return req, nil
}

// Session = klien HTTP bersesi: POST filter menghasilkan cookie ci_session
// yang dibawa otomatis oleh cookie jar pada semua GET berikutnya.
type Session struct {
	getClient  *http.Client // GET: ikuti redirect normal
	postClient *http.Client // POST filter: 303 TIDAK diikuti (diset di konstruktor)
	ua         string

	mu           sync.Mutex // lindungi 4 field di bawah (worker pool = paralel)
	filterBase   string
	filterForm   string // "" = mode all / sort-only (tanpa filter)
	filterInitAt time.Time
	sortKey      int // 0 = default server; 1..5 = POST changesort (doc 19 Bagian 9)

	backoffs []time.Duration // jeda antar percobaan; test mengesankan jadi 0
	limiter  *Limiter        // limiter untuk jeda antar request
}

// Cookie server hidup ±2 jam; refresh di 90 menit supaya aman.
const cookieRefreshAfter = 70 * time.Minute

func NewSession(stage, userAgent string, minDelay, maxDelay time.Duration) (*Session, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("gagal buat cookie jar: %w", err)
	}
	ref := NewHTTPClient(stage) // transport + metrics + timeout dipakai bersama
	return &Session{
		getClient: &http.Client{
			Transport: ref.Transport,
			Timeout:   ref.Timeout,
			Jar:       jar,
		},
		postClient: &http.Client{
			Transport: ref.Transport,
			Timeout:   ref.Timeout,
			Jar:       jar,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		ua:       userAgent,
		backoffs: []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second},
		limiter:  NewLimiter(minDelay, maxDelay),
	}, nil
}

// SetSortKey mengatur kunci urutan yang diterapkan lewat POST changesort pada
// tiap init/refresh sesi (doc 19 Bagian 9): 1=Impact … 4=Citations,
// 5=Citations-5yr; 0 = biarkan default server. Urutan disimpan DI SESI server
// (GET ?sort= diabaikan — teruji T4), jadi WAJIB ikut diulang saat cookie
// di-refresh, bukan sekali di awal. Dipanggil sebelum InitFilter.
func (s *Session) SetSortKey(n int) error {
	if n < 0 || n > 5 {
		return fmt.Errorf("sort tidak valid: %d (harus 0..5)", n)
	}
	s.mu.Lock()
	s.sortKey = n
	s.mu.Unlock()
	return nil
}

// InitFilter mengirim POST form filter ke baseURL (dan POST changesort bila
// sortKey>0). Server membalas 303 + Set-Cookie ci_session. formData kosong +
// sortKey 0 → mode all, no-op.
func (s *Session) InitFilter(baseURL, formData string) error {
	s.mu.Lock() // serialisasi seluruh operasi init sesi
	defer s.mu.Unlock()
	return s.initSessionLocked(baseURL, formData)
}

// initSessionLocked = POST filter (bila ada) → POST changesort (bila sortKey>0).
// Semua jalur refresh/reinit memanggil ini supaya sort TETAP terpasang saat
// cookie diperbarui — tanpa ini, refresh di tengah crawl melempar urutan kembali
// ke default (Impact) dan instabilitas tie kembali (doc 19 Bagian 9.4).
// Dipanggil dengan s.mu SUDAH dipegang.
func (s *Session) initSessionLocked(baseURL, formData string) error {
	if formData == "" && s.sortKey == 0 {
		return nil
	}
	if formData != "" {
		ok, err := s.postFormLocked(baseURL, formData)
		if err != nil {
			return fmt.Errorf("POST filter: %w", err)
		}
		if !ok {
			return fmt.Errorf("POST filter: cookie ci_session tidak ada di response")
		}
	}
	if s.sortKey > 0 {
		body := fmt.Sprintf("changesort=1&page=1&sort=%d", s.sortKey)
		ok, err := s.postFormLocked(baseURL, body)
		if err != nil {
			return fmt.Errorf("POST changesort: %w", err)
		}
		if !ok && formData == "" {
			return fmt.Errorf("POST changesort: cookie ci_session tidak ada di response")
		}
	}
	s.filterBase = baseURL
	s.filterForm = formData
	s.filterInitAt = time.Now()
	return nil
}

// postFormLocked mengirim 1 POST urlencoded ke baseURL dengan header navigasi
// (Referer/Origin fakta navigasi — doc 14 Opsi B). Mengembalikan true bila
// response membawa cookie ci_session. Dipanggil dengan s.mu SUDAH dipegang.
func (s *Session) postFormLocked(baseURL, body string) (bool, error) {
	req, err := http.NewRequest(http.MethodPost, baseURL, strings.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("buat request: %w", err)
	}
	req.Header.Set("User-Agent", s.ua)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", baseURL)
	if req.URL.Scheme != "" && req.URL.Host != "" {
		req.Header.Set("Origin", req.URL.Scheme+"://"+req.URL.Host)
	}

	s.limiter.Wait()
	resp, err := s.postClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("kirim: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // baca bersih → koneksi keep-alive bisa dipakai ulang

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return false, fmt.Errorf("status %d", resp.StatusCode)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "ci_session" {
			return true, nil
		}
	}
	return false, nil
}

// refreshCookieIfNeeded re-POST bila cookie hampir lewat umur. Dipanggil
// sebelum tiap GET.
func (s *Session) refreshCookieIfNeeded() error {
	s.mu.Lock()
	need := s.filterBase != "" && time.Since(s.filterInitAt) >= cookieRefreshAfter
	base, form := s.filterBase, s.filterForm
	s.mu.Unlock()
	if !need {
		return nil
	}
	return s.InitFilter(base, form)
}

// reinitFilterIfActive dipanggil saat GET kena 403 — dicurigai cookie mati.
func (s *Session) reinitFilterIfActive() error {
	s.mu.Lock()
	base, form := s.filterBase, s.filterForm
	s.mu.Unlock()
	if base == "" {
		return nil // tanpa POST sesi (mode all polos) → tidak ada yang bisa diperbarui
	}
	return s.InitFilter(base, form)
}

// FetchPage mengambil 1 halaman listing (GET ?page=N) dengan retry lalu
// ParsePage. 403 saat filter aktif → re-POST filter, lalu coba lagi.
func (s *Session) FetchPage(baseURL, extraQuery string, page int) (*FilterPageResult, error) {
	if err := s.refreshCookieIfNeeded(); err != nil {
		return nil, fmt.Errorf("refresh cookie filter: %w", err)
	}
	target, err := buildURL(baseURL, extraQuery, page)
	if err != nil {
		return nil, err
	}

	const maxAttempt = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempt; attempt++ {
		if attempt > 1 {
			idx := attempt - 2 // 0, 1
			if idx >= len(s.backoffs) {
				idx = len(s.backoffs) - 1
			}
			time.Sleep(s.backoffs[idx])
		}

		s.limiter.Wait()
		body, status, err := s.get(target, baseURL)
		if err == nil {
			return ParsePage(bytes.NewReader(body), page)
		}
		lastErr = err
		if status == http.StatusForbidden {
			if rfErr := s.reinitFilterIfActive(); rfErr != nil {
				lastErr = fmt.Errorf("%w (reinit filter juga gagal: %v)", err, rfErr)
			}
		}
	}
	return nil, fmt.Errorf("halaman %d gagal setelah %d percobaan: %w", page, maxAttempt, lastErr)
}

// get melakukan GET penuh. status dikembalikan (0 = gagal di jaringan)
// agar FetchPage bisa memutuskan reinit filter pada 403. referer = base
// listing (browser mengirim Referer saat navigasi antar halaman).
func (s *Session) get(target, referer string) ([]byte, int, error) {
	req, err := NewGET(target, s.ua, referer)
	if err != nil {
		return nil, 0, err
	}
	resp, err := s.getClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("GET %s: %w", target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, resp.StatusCode, fmt.Errorf("GET %s: status %d", target, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("GET %s: baca body: %w", target, err)
	}
	return body, resp.StatusCode, nil
}

// buildURL = baseURL + ?page=N + gabungan extraQuery (flag -query).
func buildURL(baseURL, extraQuery string, page int) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("base-url tidak valid: %w", err)
	}
	q := u.Query()
	q.Set("page", strconv.Itoa(page))
	if extraQuery != "" {
		extra, err := url.ParseQuery(extraQuery)
		if err != nil {
			return "", fmt.Errorf("query filter (-query) tidak valid: %w", err)
		}
		for k, vs := range extra {
			for _, v := range vs {
				q.Set(k, v)
			}
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
