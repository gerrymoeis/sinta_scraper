package garuda

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sinta-scraper/internal/sinta"
	"time"
)

// Client HTTP skeleton Garuda (doc 30 §13.5 — dipakai Q3 utk harvest &
// probe; Q4/E1–E6 TINGKAT MENAMBAH, bukan menulis ulang): limiter jeda
// acak global, retry backoff, UA hibrida, referer setara kebiasaan project.
const DefaultReferer = "https://garuda.kemdiktisaintek.go.id/journal"

type Client struct {
	http     *http.Client
	ua       string
	limiter  *sinta.Limiter
	backoffs []time.Duration // jeda antar percobaan (pola Session sinta)
}

// NewClient membuat client dgn stage metrics, delay acak [minDelay, maxDelay],
// dan backoff retry {2s, 5s, 15s}.
func NewClient(stage, userAgent string, minDelay, maxDelay time.Duration) *Client {
	return &Client{
		http:     sinta.NewHTTPClient(stage),
		ua:       userAgent,
		limiter:  sinta.NewLimiter(minDelay, maxDelay),
		backoffs: []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second},
	}
}

// Get melakukan GET penuh: limiter → request → retry bila jaringan/429/5xx.
// Mengembalikan body, status HTTP (0 = gagal jaringan), dan error.
// Status non-200 selain 429/5xx TIDAK diretry (langsung dilaporkan —
// jujur utk probe/E1).
func (c *Client) Get(rawURL, referer string) ([]byte, int, error) {
	var lastErr error
	var status int
	for attempt := 0; ; attempt++ {
		c.limiter.Wait()
		req, err := sinta.NewGET(rawURL, c.ua, referer)
		if err != nil {
			return nil, 0, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr, status = err, 0
		} else {
			body, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			status = resp.StatusCode
			switch {
			case rerr != nil:
				lastErr = fmt.Errorf("baca body: %w", rerr)
			case resp.StatusCode == http.StatusOK:
				return body, status, nil
			case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
				lastErr = fmt.Errorf("GET %s: status %d", rawURL, resp.StatusCode)
			default: // 403/404/dll. — bukan kandidat retry
				return nil, status, fmt.Errorf("GET %s: status %d", rawURL, resp.StatusCode)
			}
		}
		if attempt >= len(c.backoffs) {
			return nil, status, fmt.Errorf("GET %s: habis retry: %w", rawURL, lastErr)
		}
		time.Sleep(c.backoffs[attempt])
	}
}

// SearchURL membangun URL /journal?q=… (+page bila >1).
func SearchURL(q string, page int) string {
	u := "https://garuda.kemdiktisaintek.go.id/journal?q=" + url.QueryEscape(q)
	if page > 1 {
		u += fmt.Sprintf("&page=%d", page)
	}
	return u
}

// AreaURL = halaman taxonomi /area (1 GET utk 40 label).
const AreaURL = "https://garuda.kemdiktisaintek.go.id/area"

// ViewURL = halaman detail jurnal Garuda /journal/view/{N} (E3/Q4).
func ViewURL(garudaID int) string {
	return fmt.Sprintf("https://garuda.kemdiktisaintek.go.id/journal/view/%d", garudaID)
}
