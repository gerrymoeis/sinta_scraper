package sinta

import (
	"sync"
	"testing"
	"time"
)

// 4 slot berjarak tetap 40ms → minimal 3×40ms = 120ms. Hanya lower-bound
// lebar (100ms) — tanpa batas atas ketat — supaya tidak flaky di mesin lambat.
func TestLimiterMenjagaJedaGlobal(t *testing.T) {
	l := NewLimiter(40*time.Millisecond, 40*time.Millisecond)
	start := time.Now()
	for i := 0; i < 4; i++ {
		l.Wait()
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("4 Wait jeda 40ms = %v, want >= 100ms", elapsed)
	}
}

// min=max=0 → Wait tidak menahan (dipakai untuk test & run lokal).
func TestLimiterNol(t *testing.T) {
	l := NewLimiter(0, 0)
	start := time.Now()
	for i := 0; i < 100; i++ {
		l.Wait()
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("100 Wait tanpa jeda = %v, want < 1s", elapsed)
	}
}

// 5 goroutine bersaing ambil slot: tujuan utama semua selesai + -race bersih
// (tanpa asersi timing — pacing sudah diuji tes pertama).
func TestLimiterKonkuren(t *testing.T) {
	l := NewLimiter(10*time.Millisecond, 10*time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Wait()
		}()
	}
	wg.Wait()
}
