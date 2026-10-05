package garuda

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sinta-scraper/internal/metrics"
	"sinta-scraper/internal/sinta"
	"time"
)

// Client HTTP skeleton Garuda (doc 30 §13.5 — dipakai Q3 utk harvest &
// probe; Q4/E1–E6 TINGKAT MENAMBAH, bukan menulis ulang): limiter jeda
// acak global, retry backoff, UA hibrida, referer setara kebiasaan project.
// Q4-integrasi hybrid (doc 35, approve 4 Okt): jalur std (UA riset D2)
// tetap primer; balasan 403 di-fallback ke Hybrid (tls-client + solver
// on-demand) — lihat Get.
const DefaultReferer = "https://garuda.kemdiktisaintek.go.id/journal"

type Client struct {
	http     *http.Client
	ua       string
	stage    string // nama stage metrics (jalur hibrida dicatat manual)
	limiter  *sinta.Limiter
	backoffs []time.Duration // jeda antar percobaan (pola Session sinta)
	hyb      *Hybrid         // fallback anti-WAF (dipakai hanya saat 403)
}

// NewClient membuat client dgn stage metrics, delay acak [minDelay, maxDelay],
// dan backoff retry {2s, 5s, 15s}.
func NewClient(stage, userAgent string, minDelay, maxDelay time.Duration) *Client {
	return &Client{
		http:     sinta.NewHTTPClient(stage),
		ua:       userAgent,
		stage:    stage,
		limiter:  sinta.NewLimiter(minDelay, maxDelay),
		backoffs: []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second},
		hyb:      NewHybrid(),
	}
}

// stdGet = satu GET jalur riset (UA proyek; metrics dicatat MetricsTransport
// di dalam sinta.NewHTTPClient). Mengembalikan body, status (0 = gagal
// jaringan), dan error jaringan/baca-body saja — klasifikasi status ada di Get.
func (c *Client) stdGet(rawURL, referer string) ([]byte, int, error) {
	req, err := sinta.NewGET(rawURL, c.ua, referer)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		return nil, resp.StatusCode, fmt.Errorf("baca body: %w", rerr)
	}
	return body, resp.StatusCode, nil
}

// hybridGet = fallback anti-WAF (tls-client + solver). Metrics dicatat di
// sini karena jalur ini tidak lewat MetricsTransport.
func (c *Client) hybridGet(rawURL, referer string) (int, []byte, error) {
	start := time.Now()
	status, body, err := c.hyb.Fetch(rawURL, referer)
	metrics.Default.Record(c.stage, time.Since(start), status, len(body))
	return status, body, err
}

// Get melakukan GET penuh: limiter → std GET → retry bila jaringan/429/5xx.
// Balasan 403 dicoba sekali lewat jalur hybrid (doc 35); gagal solve/challenge
// = terminal (tidak di-backoff — bukan gangguan jaringan).
// Status non-200 selain 403/429/5xx TIDAK diretry (langsung dilaporkan —
// jujur utk probe/E1).
func (c *Client) Get(rawURL, referer string) ([]byte, int, error) {
	var lastErr error
	var status int
	for attempt := 0; ; attempt++ {
		c.limiter.Wait()
		body, st, err := c.stdGet(rawURL, referer)
		status = st

		if err == nil && st == http.StatusForbidden {
			// Fallback anti-WAF (approve user 4 Okt 2026 — doc 35).
			hs, hb, herr := c.hybridGet(rawURL, referer)
			status = hs
			switch {
			case herr != nil && hs == http.StatusForbidden:
				return nil, hs, herr // solver gagal / masih challenge → terminal
			case herr != nil:
				err = herr // jaringan → jatuh ke backoff di bawah
			case hs == http.StatusOK:
				return hb, hs, nil
			case hs == http.StatusTooManyRequests || hs >= 500:
				err = fmt.Errorf("GET %s (hybrid): status %d", rawURL, hs)
			default:
				return nil, hs, fmt.Errorf("GET %s (hybrid): status %d", rawURL, hs)
			}
		}

		switch {
		case err != nil:
			lastErr = err
		case st == http.StatusOK:
			return body, st, nil
		case st == http.StatusTooManyRequests || st >= 500:
			lastErr = fmt.Errorf("GET %s: status %d", rawURL, st)
		default: // 404/dll. — bukan kandidat retry
			return nil, st, fmt.Errorf("GET %s: status %d", rawURL, st)
		}
		if attempt >= len(c.backoffs) {
			return nil, status, fmt.Errorf("GET %s: habis retry: %w", rawURL, lastErr)
		}
		time.Sleep(c.backoffs[attempt])
	}
}

// Solves = jumlah eksekusi solver utk client ini (observability -wafsmoke).
func (c *Client) Solves() int { return c.hyb.Solves() }

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
