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

func NewGET(rawURL, userAgent string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en;q=0.8")
	return req, nil
}

// Session = klien HTTP bersesi: POST filter menghasilkan cookie ci_session
// yang dibawa otomatis oleh cookie jar pada semua GET berikutnya.
type Session struct {
	getClient  *http.Client // GET: ikuti redirect normal
	postClient *http.Client // POST filter: 303 TIDAK diikuti (diset di konstruktor)
	ua         string

	mu           sync.Mutex // lindungi 3 field di bawah (worker pool = paralel)
	filterBase   string
	filterForm   string // "" = mode all (tanpa filter)
	filterInitAt time.Time

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

// InitFilter mengirim POST form filter ke baseURL. Server membalas 303 +
// Set-Cookie ci_session. formData kosong → mode all, tanpa cookie, no-op.
func (s *Session) InitFilter(baseURL, formData string) error {
	if formData == "" {
		return nil
	}
	s.mu.Lock() // serialisasi seluruh operasi filter
	defer s.mu.Unlock()

	req, err := http.NewRequest(http.MethodPost, baseURL, strings.NewReader(formData))
	if err != nil {
		return fmt.Errorf("buat request filter: %w", err)
	}
	req.Header.Set("User-Agent", s.ua)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	s.limiter.Wait()
	resp, err := s.postClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST filter: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // baca bersih → koneksi keep-alive bisa dipakai ulang

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("POST filter: status %d", resp.StatusCode)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "ci_session" {
			s.filterBase = baseURL
			s.filterForm = formData
			s.filterInitAt = time.Now()
			return nil
		}
	}
	return fmt.Errorf("POST filter: cookie ci_session tidak ada di response")
}

// refreshCookieIfNeeded re-POST bila cookie hampir lewat umur. Dipanggil
// sebelum tiap GET.
func (s *Session) refreshCookieIfNeeded() error {
	s.mu.Lock()
	need := s.filterForm != "" && time.Since(s.filterInitAt) >= cookieRefreshAfter
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
	if form == "" {
		return nil // tanpa filter → tidak ada cookie yang bisa diperbarui
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
		body, status, err := s.get(target)
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
// agar FetchPage bisa memutuskan reinit filter pada 403.
func (s *Session) get(target string) ([]byte, int, error) {
	req, err := NewGET(target, s.ua)
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
