// Command scraper mengambil metadata jurnal dari listing SINTA
// (https://sinta.kemdiktisaintek.go.id/journals) untuk satu filter/rentang
// halaman tertentu, dan menyimpannya ke SQLite lokal secara resumable.
//
// Contoh untuk full scrape semua jurnal (16.772 records, 1.678 halaman):
//
//	go run . -run-key all -filter "filter_journals=1" -db data/sinta.db
//
// Contoh untuk SINTA 1 saja:
//
//	go run . -run-key sinta1 -filter "filter_accreditation[1]=1&filter_journals=1" -db data/sinta.db
package main

import (
	"flag"
	"log"
	"sync"
	"time"

	"sinta-scraper/internal/sinta"
	"sinta-scraper/internal/storage"
)

type pageOutcome struct {
	page   int
	result *sinta.PageResult
	err    error
}

func main() {
	baseURL := flag.String("base-url", "https://sinta.kemdiktisaintek.go.id/journals",
		"URL dasar halaman listing jurnal SINTA")
	extraQuery := flag.String("query", "",
		`query string tambahan dari address bar browser (contoh: "sinta=6"). Kosongkan jika pakai -filter.`)
	filterData := flag.String("filter", "",
		`raw POST form data untuk filter (contoh: "filter_accreditation[1]=1&filter_journals=1"). `+
			`Jika diisi, scraper POST dulu untuk aktifkan filter sebelum scraping.`)
	runKey := flag.String("run-key", "default",
		"namespace checkpoint/resume (contoh: sinta1, all)")
	maxPages := flag.Int("max-pages", 0,
		`Jumlah halaman. Jika 0, otomatis dideteksi dari "Page 1 of N".`)
	workers := flag.Int("workers", 1,
		`jumlah worker fetch konkuren. Untuk scraping masif (>1000 halaman), `+
			`satu worker sudah cukup karena rate limiting global mengatur irama.`)
	dbPath := flag.String("db", "data/sinta.db", "path file database SQLite")
	minDelay := flag.Duration("min-delay", 3*time.Second,
		`delay minimum antar request (global, bukan per-worker). `+
			`Untuk scraping masif, 3-5 detik direkomendasikan.`)
	maxDelay := flag.Duration("max-delay", 8*time.Second,
		`delay maksimum antar request. Randomisasi agar pola tidak prediktabel.`)
	userAgent := flag.String("user-agent",
		"sinta-scraper-riset-akademik/0.1 (+kontak-email-anda@domain.com)",
		"header User-Agent — WAJIB diisi identitas & kontak yang sebenarnya")
	flag.Parse()

	if *workers <= 0 {
		log.Fatal("-workers harus > 0")
	}

	store, err := storage.Open(*dbPath)
	if err != nil {
		log.Fatalf("gagal membuka storage: %v", err)
	}
	defer store.Close()

	client := sinta.NewClient(*minDelay, *maxDelay, *userAgent)
	defer client.Close()

	if *filterData != "" {
		if err := client.InitFilter(*baseURL, *filterData); err != nil {
			log.Fatalf("gagal inisialisasi filter: %v", err)
		}
	}

	// ── Auto-detect max-pages dari halaman 1 ──────────────────────────
	var autoDetectJournals int
	maxPagesExplicit := *maxPages > 0 // true jika user input -max-pages manual
	if *maxPages <= 0 {
		log.Println("auto-detect jumlah halaman dari page 1...")
		firstPage, err := client.FetchPage(*baseURL, *extraQuery, 1)
		if err != nil {
			log.Fatalf("gagal fetch page 1 untuk auto-detect: %v", err)
		}
		if firstPage.TotalPages <= 0 {
			log.Fatalf("tidak bisa deteksi jumlah halaman (total_pages=%d, total_records=%d)",
				firstPage.TotalPages, firstPage.TotalRecords)
		}
		*maxPages = firstPage.TotalPages
		log.Printf("auto-detect: %d halaman, %d total records", firstPage.TotalPages, firstPage.TotalRecords)

		done, err := store.CompletedPages(*runKey)
		if err != nil {
			log.Fatalf("gagal baca checkpoint: %v", err)
		}
		if !done[1] {
			if err := store.UpsertJournals(firstPage.Journals, 1); err != nil {
				log.Printf("[peringatan] gagal simpan page 1 auto-detect: %v", err)
			} else if err := store.MarkPageCompleted(*runKey, 1); err != nil {
				log.Printf("[peringatan] checkpoint page 1 gagal: %v", err)
			} else {
				autoDetectJournals = len(firstPage.Journals)
				log.Printf("[ok] halaman 1 (auto-detect): %d jurnal tersimpan", autoDetectJournals)
			}
		}
	}

	// ── Hitung sisa halaman yang perlu di-scrape ──────────────────────
	done, err := store.CompletedPages(*runKey)
	if err != nil {
		log.Fatalf("gagal baca checkpoint: %v", err)
	}

	var pagesToFetch []int
	for p := 1; p <= *maxPages; p++ {
		if !done[p] {
			pagesToFetch = append(pagesToFetch, p)
		}
	}
	if len(pagesToFetch) == 0 {
		log.Printf("run-key %q: semua %d halaman sudah pernah discrape. Tidak ada yang dikerjakan.",
			*runKey, *maxPages)
		return
	}

	totalTarget := *maxPages
	remaining := len(pagesToFetch)
	estDuration := estimateDuration(remaining, *minDelay, *maxDelay)
	log.Printf("═══════════════════════════════════════════════════════════════")
	log.Printf("  run-key %q: %d/%d halaman perlu di-scrape", *runKey, remaining, totalTarget)
	log.Printf("  delay: %v–%v antar request | workers: %d", *minDelay, *maxDelay, *workers)
	log.Printf("  estimasi selesai: ~%v", estDuration.Round(time.Minute))
	log.Printf("═══════════════════════════════════════════════════════════════")

	// ── Worker pool ───────────────────────────────────────────────────
	jobs := make(chan int, remaining)
	results := make(chan pageOutcome, remaining)

	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for page := range jobs {
				pr, err := client.FetchPage(*baseURL, *extraQuery, page)
				results <- pageOutcome{page: page, result: pr, err: err}
			}
		}()
	}

	for _, p := range pagesToFetch {
		jobs <- p
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	// ── Terima hasil & tulis ke SQLite (single writer) ───────────────
	var (
		okPages         int
		failPages       int
		totalJournals   int
		expectedRecords int
		startTime       = time.Now()
		lastProgressAt  = time.Now()
	)

	for r := range results {
		if r.err != nil {
			log.Printf("[gagal] halaman %d: %v", r.page, r.err)
			failPages++
			continue
		}
		if len(r.result.Journals) == 0 {
			log.Printf("[peringatan] halaman %d: 0 kartu jurnal ditemukan", r.page)
		}
		if expectedRecords == 0 && r.result.TotalRecords > 0 {
			expectedRecords = r.result.TotalRecords
		}
		if err := store.UpsertJournals(r.result.Journals, r.page); err != nil {
			log.Printf("[gagal] halaman %d: simpan ke db: %v", r.page, err)
			failPages++
			continue
		}
		if err := store.MarkPageCompleted(*runKey, r.page); err != nil {
			log.Printf("[peringatan] halaman %d checkpoint gagal: %v", r.page, err)
		}
		okPages++
		totalJournals += len(r.result.Journals)

		// Progress setiap 10 halaman atau setiap 30 detik.
		doneCount := okPages
		pct := float64(doneCount) / float64(totalTarget) * 100
		elapsed := time.Since(startTime)
		avgPer := elapsed / time.Duration(doneCount)
		remaining := totalTarget - doneCount
		eta := time.Duration(remaining) * avgPer

		// Log progress setiap 10 halaman atau minimal setiap 30 detik.
		if doneCount%10 == 0 || time.Since(lastProgressAt) > 30*time.Second || doneCount == totalTarget {
			log.Printf("[progress] %d/%d (%.1f%%) | jurnal: %d | ETA: %v | rata-rata: %v/halaman",
				doneCount, totalTarget, pct, totalJournals+autoDetectJournals,
				eta.Round(time.Minute), avgPer.Round(time.Millisecond))
			lastProgressAt = time.Now()
		}

		// Log detail setiap halaman (rate-limited: tidak terlalu verbose).
		if doneCount%10 != 0 {
			log.Printf("[ok] halaman %d: %d jurnal", r.page, len(r.result.Journals))
		}
	}

	// ── Ringkasan akhir ──────────────────────────────────────────────
	elapsed := time.Since(startTime)
	grandTotal := totalJournals + autoDetectJournals

	log.Printf("═══════════════════════════════════════════════════════════════")
	log.Printf("  SELESAI dalam %v", elapsed.Round(time.Second))
	log.Printf("  halaman sukses: %d | gagal: %d | total jurnal: %d",
		okPages, failPages, grandTotal)
	if autoDetectJournals > 0 {
		log.Printf("  (termasuk %d jurnal dari auto-detect page 1)", autoDetectJournals)
	}

	// Verifikasi: hanya jika semua halaman target berhasil di-scrape
	// DAN user tidak membatasi -max-pages secara manual.
	totalHandled := okPages
	if autoDetectJournals > 0 {
		totalHandled++ // page 1 dari auto-detect
	}
	shouldVerify := totalHandled == totalTarget && expectedRecords > 0 && !maxPagesExplicit
	if shouldVerify {
		if grandTotal != expectedRecords {
			log.Printf("  [VERIFIKASI GAGAL] di-scrape=%d, expected=%d — filter mungkin tidak benar!",
				grandTotal, expectedRecords)
		} else {
			log.Printf("  [VERIFIKASI OK] %d jurnal = %d total records ✓", grandTotal, expectedRecords)
		}
	} else if totalHandled == totalTarget && maxPagesExplicit {
		log.Printf("  [INFO] partial run (-max-pages=%d), totalRecords server=%d, di-scrape=%d",
			totalTarget, expectedRecords, grandTotal)
	}
	log.Printf("═══════════════════════════════════════════════════════════════")

	if failPages > 0 {
		log.Printf("Jalankan ulang command yang sama untuk mencoba lagi halaman yang gagal (checkpoint otomatis skip yang sudah sukses).")
	}
}

// estimateDuration menghitung estimasi waktu berdasarkan jumlah halaman
// dan delay rata-rata. Asumsi: setiap halaman = 1 request + delay.
func estimateDuration(pages int, minDelay, maxDelay time.Duration) time.Duration {
	avgDelay := (minDelay + maxDelay) / 2
	return time.Duration(pages) * avgDelay
}
