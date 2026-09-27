package sinta

import (
	"io"
	"net/http"
	"sinta-scraper/internal/metrics"
	"sync"
	"time"
)

type MetricsTransport struct {
	inner http.RoundTripper
	stage string
}

func (t *MetricsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		metrics.Default.Record(t.stage, time.Since(start), 0, 0)
		return nil, err
	}
	resp.Body = &bodyTracker{
		body:   resp.Body,
		start:  start,
		stage:  t.stage,
		status: resp.StatusCode,
	}
	return resp, nil
}

type bodyTracker struct {
	body   io.ReadCloser
	start  time.Time
	stage  string
	status int
	n      int
	once   sync.Once
}

func (b *bodyTracker) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	b.n += int(n)
	if err != nil {
		b.finish()
	}
	return n, err
}

func (b *bodyTracker) Close() error {
	err := b.body.Close()
	b.finish()
	return err
}

func (b *bodyTracker) finish() {
	b.once.Do(func() {
		metrics.Default.Record(b.stage, time.Since(b.start), b.status, b.n)
	})
}
