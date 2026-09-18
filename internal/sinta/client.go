package sinta

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client mengambil dan mem-parsing halaman listing jurnal SINTA secara sopan:
// rate limiting global (satu request pada satu waktu di seluruh worker),
// cookie refresh otomatis, retry dengan backoff, dan User-Agent yang bisa
// diidentifikasi.
type Client struct {
	http               *http.Client
	minDelay, maxDelay time.Duration
	userAgent          string

	// Filter & cookie management
	filterBaseURL string // baseURL untuk re-POST filter jika cookie expiry
	filterData    string // raw POST form data untuk filter
	cookie        string // ci_session cookie header value
	cookieInitAt  time.Time

	// Global rate limiter — token bucket via goroutine.
	// Workers must call Wait() before EACH request.
	tokens chan struct{}
	stopCh chan struct{}

	mu  sync.Mutex
	rng *rand.Rand
}

const cookieRefreshBefore = 70 * time.Minute // refresh sebelum cookie expiry (2 jam)

// NewClient membuat Client baru. minDelay/maxDelay menentukan rentang delay
// antar request secara global (bukan per-worker). Satu goroutine token bucket
// berjalan di background untuk mengatur irama request agar sopan ke server.
func NewClient(minDelay, maxDelay time.Duration, userAgent string) *Client {
	c := &Client{
		http:      &http.Client{Timeout: 30 * time.Second},
		minDelay:  minDelay,
		maxDelay:  maxDelay,
		userAgent: userAgent,
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())),
		tokens:    make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
	}
	go c.tokenFeeder()
	return c
}

// tokenFeeder adalah goroutine background yang mengisi token ke channel
// dengan interval random [minDelay, maxDelay]. Ini memastikan SELALU ada
// jeda antar request di SEMUA worker, bukan per-worker.
// Resource: goroutine sleeping (0% CPU), channel buffer 1.
func (c *Client) tokenFeeder() {
	for {
		// Isi token satu per satu.
		select {
		case c.tokens <- struct{}{}:
		default:
			// token belum diambil — tunggu dulu
		}

		// Tidur dengan durasi random sebelum mengisi token berikutnya.
		c.mu.Lock()
		d := c.minDelay
		if c.maxDelay > c.minDelay {
			d += time.Duration(c.rng.Int63n(int64(c.maxDelay - c.minDelay)))
		}
		c.mu.Unlock()

		select {
		case <-c.stopCh:
			return
		case <-time.After(d):
		}
	}
}

// Wait memblok sampai token tersedia — dipanggil SEBELUM setiap HTTP request.
// Ini menjamin minimal minDelay antar request di seluruh worker.
func (c *Client) Wait() {
	<-c.tokens
}

// Close menghentikan token feeder goroutine.
func (c *Client) Close() {
	close(c.stopCh)
}

// InitFilter mengirim POST request ke baseURL dengan formData untuk
// mengaktifkan filter di sisi server. Cookie ci_session disimpan.
func (c *Client) InitFilter(baseURL, formData string) error {
	if formData == "" {
		return nil
	}
	c.filterBaseURL = baseURL
	c.filterData = formData
	return c.doInitFilter()
}

func (c *Client) doInitFilter() error {
	req, err := http.NewRequest(http.MethodPost, c.filterBaseURL, strings.NewReader(c.filterData))
	if err != nil {
		return fmt.Errorf("gagal buat request filter: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	c.http.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	defer func() { c.http.CheckRedirect = nil }()

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("gagal kirim filter POST: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("filter POST: status tidak diharapkan: %d", resp.StatusCode)
	}

	for _, cookie := range resp.Cookies() {
		if cookie.Name == "ci_session" {
			c.cookie = cookie.Name + "=" + cookie.Value
			c.cookieInitAt = time.Now()
			log.Printf("[filter] session cookie diterima, filter aktif (cookie valid ~2 jam)")
			return nil
		}
	}
	return fmt.Errorf("filter POST: cookie ci_session tidak ditemukan di response")
}

// refreshCookieIfNeeded memeriksa apakah cookie sudah hampir expired dan
// melakukan re-POST jika perlu. Dipanggil SEBELUM setiap request.
func (c *Client) refreshCookieIfNeeded() error {
	if c.filterData == "" {
		return nil // tanpa filter, tidak perlu cookie
	}
	if time.Since(c.cookieInitAt) < cookieRefreshBefore {
		return nil // masih valid
	}
	log.Printf("[filter] cookie hampir expired (%.0f menit), merefresh...",
		time.Since(c.cookieInitAt).Minutes())
	return c.doInitFilter()
}

// FetchPage mengambil dan mem-parsing satu halaman listing.
func (c *Client) FetchPage(baseURL, extraQuery string, page int) (*PageResult, error) {
	// Refresh cookie jika hampir expired.
	if err := c.refreshCookieIfNeeded(); err != nil {
		return nil, fmt.Errorf("refresh cookie: %w", err)
	}

	target, err := buildURL(baseURL, extraQuery, page)
	if err != nil {
		return nil, err
	}

	// Tunggu giliran dari global rate limiter.
	c.Wait()

	const maxRetries = 4
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 5s, 15s, 30s, 60s — lebih sabar.
			backoffs := []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}
			wait := backoffs[attempt-1]
			if attempt >= len(backoffs) {
				wait = backoffs[len(backoffs)-1]
			}
			log.Printf("[retry] halaman %d: menunggu %v sebelum percobaan %d/%d",
				page, wait, attempt+1, maxRetries+1)
			time.Sleep(wait)
		}

		body, err := c.doRequest(target)
		if err != nil {
			lastErr = err
			// Jika cookie expired (403), refresh dan retry.
			if strings.Contains(err.Error(), "403") {
				log.Printf("[filter] kemungkinan cookie expired, merefresh...")
				if refreshErr := c.doInitFilter(); refreshErr != nil {
					log.Printf("[filter] gagal refresh: %v", refreshErr)
				}
			}
			continue
		}

		pr, err := ParsePage(bytes.NewReader(body), page)
		if err != nil {
			lastErr = err
			continue
		}

		return pr, nil
	}
	return nil, fmt.Errorf("gagal fetch halaman %d setelah %d percobaan: %w", page, maxRetries+1, lastErr)
}

func (c *Client) doRequest(target string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html")
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, fmt.Errorf("status sementara %d dari server", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("status %d (kemungkinan cookie expired)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status HTTP tidak diharapkan: %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

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
