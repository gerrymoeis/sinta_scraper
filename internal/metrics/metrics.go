package metrics

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Snapshot struct {
	Requests  int
	Bytes     int
	Status    map[int]int
	Latencies []time.Duration
}

type Collector struct {
	mu     sync.Mutex
	stages map[string]*Snapshot
}

var Default = &Collector{stages: map[string]*Snapshot{}}

func New() *Collector {
	return &Collector{stages: map[string]*Snapshot{}}
}

func (c *Collector) Record(stage string, d time.Duration, status, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stages[stage]
	if s == nil {
		s = &Snapshot{Status: map[int]int{}}
		c.stages[stage] = s
	}
	s.Requests++
	s.Bytes += bytes
	s.Status[status]++
	s.Latencies = append(s.Latencies, d)
}

func (c *Collector) Report() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.stages) == 0 {
		return "[METRIK] belum ada data"
	}
	keys := make([]string, 0, len(c.stages))
	for k := range c.stages {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, stage := range keys {
		s := c.stages[stage]
		lats, avg := sortedStats(s.Latencies)
		fmt.Fprintf(&b, "[METRIK] stage=%s requests=%d bytes=%d status=%v latency avg=%v p50=%v p95=%v p99=%v min=%v max=%v",
			stage, s.Requests, s.Bytes, s.Status,
			avg.Round(time.Millisecond),
			percentile(lats, 0.50).Round(time.Millisecond),
			percentile(lats, 0.95).Round(time.Millisecond),
			percentile(lats, 0.99).Round(time.Millisecond),
			minDur(lats).Round(time.Millisecond),
			maxDur(lats).Round(time.Millisecond))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(float64(len(sorted)-1)*p)]
}

func minDur(sorted []time.Duration) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[0]
}

func maxDur(sorted []time.Duration) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[len(sorted)-1]
}

type LatencyMS struct {
	Avg float64 `json:"avg"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

type ExportSnapshot struct {
	Requests int            `json:"requests"`
	Bytes    int            `json:"bytes"`
	Status   map[string]int `json:"status"`
	Latency  LatencyMS      `json:"latency_ms"`
}

func (c *Collector) Export() map[string]ExportSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]ExportSnapshot, len(c.stages))
	for stage, s := range c.stages {
		lats, avg := sortedStats(s.Latencies)
		st := make(map[string]int, len(s.Status))
		for k, v := range s.Status {
			st[strconv.Itoa(k)] = v
		}
		out[stage] = ExportSnapshot{
			Requests: s.Requests,
			Bytes:    s.Bytes,
			Status:   st,
			Latency: LatencyMS{
				Avg: ms(avg),
				P50: ms(percentile(lats, 0.50)),
				P95: ms(percentile(lats, 0.95)),
				P99: ms(percentile(lats, 0.99)),
				Min: ms(minDur(lats)),
				Max: ms(maxDur(lats)),
			},
		}
	}
	return out
}

func ms(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func sortedStats(in []time.Duration) (sorted []time.Duration, avg time.Duration) {
	sorted = append([]time.Duration(nil), in...)
	slices.Sort(sorted)
	if len(sorted) == 0 {
		return sorted, 0
	}
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	return sorted, sum / time.Duration(len(sorted))
}
