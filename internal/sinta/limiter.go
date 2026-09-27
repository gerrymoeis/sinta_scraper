package sinta

import (
	"math/rand/v2"
	"sync"
	"time"
)

// Limiter membatasi laju request SECARA GLOBAL (lintas worker): tiap pemanggil
// mengambil satu slot waktu di masa depan lalu tidur sampai slotnya tiba.
// Jarak antar slot = durasi acak seragam [min, max].
type Limiter struct {
	mu   sync.Mutex
	min  time.Duration
	max  time.Duration
	next time.Time // awal slot berikutnya yang belum terambil
}

// NewLimiter membuat limiter jeda acak [min, max]. Durasi negatif di-klamp ke 0;
// min > max ditukar agar selalu sah (validasi utama tetap di flag).
func NewLimiter(min, max time.Duration) *Limiter {
	if min < 0 {
		min = 0
	}
	if max < 0 {
		max = 0
	}
	if min > max {
		min, max = max, min
	}
	return &Limiter{min: min, max: max}
}

// Wait mengambil satu slot lalu tidur sampai waktunya. Kunci dilepas SEBELUM
// tidur sehingga pemanggil lain boleh mengambil slot berikutnya segera.
func (l *Limiter) Wait() {
	l.mu.Lock()
	now := time.Now()
	start := l.next
	if start.Before(now) {
		start = now // idle terlalu lama → reset, tanpa burst pengejaran
	}
	l.next = start.Add(l.randDelay())
	l.mu.Unlock()

	if wait := time.Until(start); wait > 0 {
		time.Sleep(wait)
	}
}

// randDelay = acak seragam [min, max]. math/rand/v2 aman untuk konkuren.
func (l *Limiter) randDelay() time.Duration {
	if l.min == l.max {
		return l.min
	}
	return l.min + time.Duration(rand.Int64N(int64(l.max-l.min)+1))
}
