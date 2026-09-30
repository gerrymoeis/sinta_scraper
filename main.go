package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sinta-scraper/internal/metrics"
	"sinta-scraper/internal/sinta"
	"sinta-scraper/internal/storage"
	"strconv"
	"strings"
	"time"
)

func main() {
	// Seleksi apa yang di scrape
	stages := flag.String("stages", "all", "stage pipeline, dipisah koma: sinta,ojs,pdf,all")
	rank := flag.String("rank", "1", "filter SINTA: 1-6 | rentang A-B (mis. 1-3) | daftar (mis. 1,5 — di PowerShell WAJIB kutip: -rank \"1,5\") | all")
	year := flag.String("year", "latest", "filter tahun issue OJS: latest | tahun (2024) | all")
	latestVol := flag.String("latest-vol", "1", "jumlah volume terbaru dari cakupan -year: angka (1) | all")
	// Teknis bagaimana scrape berjalan
	workers := flag.Int("workers", 4, "jumlah worker goroutine")
	minDelay := flag.Duration("min-delay", 1*time.Second, "delay minimum global antar request")
	maxDelay := flag.Duration("max-delay", 3*time.Second, "delay maksimum global antar request")
	userAgent := flag.String("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)", "header User-Agent — hibrida: prefix browser (filter server) + identitas & kontak asli")
	// Output folder
	dbPath := flag.String("db", "data/sinta.db", "path file SQLite output")
	logPath := flag.String("log", "data/runs/{run_id}/log.txt", "path file log; {run_id} akan diganti dengan kode waktu run")
	// Advanced Settings (optional)
	baseURL := flag.String("base-url", "https://sinta.kemdiktisaintek.go.id/journals", "base URL SINTA Listing Jurnal")
	filterData := flag.String("filter", "", "override raw POST filter (menimpa -rank)")
	extraQuery := flag.String("query", "", "query string GET alternatif dari address bar (mis. sinta=6)")
	maxPages := flag.Int("max-pages", 0, "batas halaman SINTA (0 = auto-detect)")
	sortKey := flag.Int("sort", 4, "kunci urutan SINTA via POST changesort (tersimpan di sesi; GET ?sort= diabaikan): 1=Impact 2=H5 3=H 4=Citations 5=Citations-5yr | 0=urutan default server (doc 19 Bagian 9)")
	noDelay := flag.Bool("no-delay", false, "matikan jeda etika global (paksa min-delay=max-delay=0s) — HANYA untuk eksperimen/load-test terkontrol; risiko throttling/blokir ditanggung pengguna")
	refresh := flag.Bool("refresh", false, "abaikan + wipe checkpoint: semua halaman discrape ulang, perubahan data terdeteksi & dicatat (doc 16)")

	flag.Parse()
	if *noDelay {
		*minDelay = 0
		*maxDelay = 0
	}
	start := time.Now()
	runID := start.Format("20060102_150405")
	*logPath = strings.ReplaceAll(*logPath, "{run_id}", runID)

	if err := os.MkdirAll(filepath.Dir(*logPath), 0755); err != nil {
		log.Fatalf("GAGAL: tidak bisa membuat folder log: %v", err)
	}
	logFile, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("GAGAL: tidak bisa membuka file log: %v", err)
	}
	defer logFile.Close()
	log.SetOutput(io.MultiWriter(os.Stdout, logFile))

	for _, err := range []error{
		validStages(*stages),
		validRank(*rank),
		validYear(*year),
		validLatestVol(*latestVol),
	} {
		if err != nil {
			log.Fatalf("GAGAL: %v", err)
		}
	}
	if *workers < 1 {
		log.Fatalf("GAGAL: -workers harus >= 1 (dapat %d)", *workers)
	}
	if *minDelay > *maxDelay {
		log.Fatalf("GAGAL: -min-delay (%v) harus <= -max-delay (%v)", *minDelay, *maxDelay)
	}
	if *maxPages < 0 {
		log.Fatalf("GAGAL: -max-pages harus >= 0 (dapat %d)", *maxPages)
	}
	if *userAgent == "" {
		log.Fatalf("GAGAL: -user-agent tidak boleh kosong")
	}

	log.Println("=========== CONFIG ===========")
	log.Printf("   stages   =   %s", *stages)
	log.Printf("   rank     =   %s", *rank)
	log.Printf("   year     =   %s", *year)
	log.Printf("   latestVol=   %s", *latestVol)
	log.Printf("   workers  =   %d", *workers)
	log.Printf("   delay    =   %v - %v", *minDelay, *maxDelay)
	log.Printf("   userAgent=   %s", *userAgent)
	log.Printf("   dbPath  =   %s", *dbPath)
	log.Printf("   logPath =   %s", *logPath)
	log.Printf("   baseURL =   %s", *baseURL)
	log.Printf("   filterData= %q", *filterData)
	log.Printf("   extraQuery= %q", *extraQuery)
	log.Printf("   maxPages =   %d", *maxPages)
	log.Printf("   refresh  =   %v", *refresh)
	if *noDelay {
		log.Print("[PERINGATAN] -no-delay AKTIF — jeda etika global DIMATIKAN (0s) untuk semua request.")
		log.Print("[PERINGATAN] Murni untuk eksperimen/load-test terkontrol; risiko throttling/429/blokir server ditanggung pengguna (doc 14).")
	}

	// ── Stage pipeline ────────────────────────────────────────────────
	runKey := sinta.RunKeyFor(*filterData, *extraQuery, *rank)
	var stageErr error
	var sintaRes *sinta.StageResult
	var stageDur time.Duration

	if stagesInclude(*stages, "sinta") {
		store, err := storage.Open(*dbPath)
		if err != nil {
			log.Fatalf("GAGAL: buka db: %v", err)
		}
		defer store.Close()

		sess, err := sinta.NewSession("sinta", *userAgent, *minDelay, *maxDelay)
		if err != nil {
			log.Fatalf("GAGAL: buat session: %v", err)
		}

		formData, err := resolveFilterForm(*filterData, *extraQuery, *rank)
		if err != nil {
			log.Fatalf("GAGAL: %v", err)
		}
		// ExpectedRanks (sanity GAGAL-FILTER, doc 20) hanya untuk jalur -rank:
		// -filter/-query memakai payload bebas → tanpa cek rank.
		var expectedRanks []int
		if *filterData == "" && *extraQuery == "" {
			expectedRanks, err = sinta.ParseRankSet(*rank)
			if err != nil {
				log.Fatalf("GAGAL: %v", err)
			}
		}
		if err := sess.SetSortKey(*sortKey); err != nil {
			log.Fatalf("GAGAL: -sort: %v", err)
		}
		log.Printf("run-key = %s | filter POST = %q | rank = %v | sort = %d", runKey, formData, expectedRanks, *sortKey)
		stageStart := time.Now()

		sintaRes, stageErr = sinta.RunSintaStage(sess, store, sinta.StageConfig{
			BaseURL:       *baseURL,
			FilterData:    formData,
			ExtraQuery:    *extraQuery,
			RunKey:        runKey,
			Refresh:       *refresh,
			MaxPages:      *maxPages,
			Workers:       *workers,
			MinDelay:      *minDelay,
			MaxDelay:      *maxDelay,
			Logf:          log.Printf,
			ExpectedRanks: expectedRanks,
		})
		stageDur = time.Since(stageStart)
		if stageErr != nil {
			log.Printf("stage sinta GAGAL: %v", stageErr)
		}
	} else {
		log.Println("stage sinta tidak diminta (-stages), dilewati")
	}

	if stagesInclude(*stages, "ojs") || stagesInclude(*stages, "pdf") {
		log.Println("[info] stage ojs/pdf belum diimplementasi (Fase C/D) — dilewati pada build ini")
	}

	log.Print(metrics.Default.Report())

	// ── Metrik SELALU ditulis di akhir (termasuk saat stage gagal) ────
	metricsPath := filepath.Join(filepath.Dir(*logPath), "metrics.json")
	payload := map[string]any{
		"run_id": runID,
		"config": map[string]any{
			"stages":     *stages,
			"rank":       *rank,
			"year":       *year,
			"latest-vol": *latestVol,
			"workers":    *workers,
			"min-delay":  minDelay.String(),
			"max-delay":  maxDelay.String(),
			"max-pages":  *maxPages,
			"sort":       *sortKey,
			"no-delay":   *noDelay,
			"refresh":    *refresh,
			"base-url":   *baseURL,
			"user-agent": *userAgent,
		},
		"env": map[string]any{
			"go":      runtime.Version(),
			"os":      runtime.GOOS,
			"arch":    runtime.GOARCH,
			"num_cpu": runtime.NumCPU(),
		},
		"timing": map[string]any{
			"start":        start.Format(time.RFC3339),
			"end":          time.Now().Format(time.RFC3339),
			"duration_sec": time.Since(start).Seconds(),
		},
		"http": metrics.Default.Export(),
	}
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		log.Fatalf("GAGAL: marshal metrik: %v", err)
	}
	if err := os.WriteFile(metricsPath, out, 0644); err != nil {
		log.Fatalf("GAGAL: tulis file metrik: %v", err)
	}
	log.Printf("metrik ditulis: %s", metricsPath)

	// ── Ringkasan akhir (spesifikasi doc 15 Bagian 2) ────────────────
	durasi := time.Since(start)
	log.Print("════════════════ [RINGKASAN] ═══════════════════════════════")
	log.Printf("[RINGKASAN] run-id   : %s | run-key %s | %d workers", runID, runKey, *workers)
	mode := "stage sinta tidak dijalankan"
	if sintaRes != nil {
		mode = "baru (fresh)"
		switch {
		case *refresh:
			mode = "refresh (-refresh, checkpoint di-wipe)"
		case sintaRes.PagesSkipped > 0:
			mode = fmt.Sprintf("lanjutan (%d halaman checkpoint dilewati, bukan gagal)", sintaRes.PagesSkipped)
		}
	}
	log.Printf("[RINGKASAN] mode     : %s", mode)
	if stageDur > 0 {
		log.Printf("[RINGKASAN] durasi   : %.1fs total | stage sinta %.1fs", durasi.Seconds(), stageDur.Seconds())
	} else {
		log.Printf("[RINGKASAN] durasi   : %.1fs total", durasi.Seconds())
	}
	if sintaRes != nil {
		verif := "dilewati"
		switch {
		case sintaRes.Verified:
			verif = "OK"
		case strings.HasPrefix(sintaRes.VerifyMsg, "GAGAL-FILTER"):
			verif = "GAGAL-FILTER"
		case strings.HasPrefix(sintaRes.VerifyMsg, "UNRESOLVED"):
			verif = "UNRESOLVED"
		case strings.HasPrefix(sintaRes.VerifyMsg, "GAGAL"):
			verif = "GAGAL"
		}
		log.Printf("[RINGKASAN] halaman  : %d/%d tersimpan | %d dilewati | %d gagal",
			sintaRes.PagesSaved, sintaRes.TotalPages, sintaRes.PagesSkipped, sintaRes.PagesFailed)
		log.Printf("[RINGKASAN] jurnal   : %d diproses (baru %d | diperbarui %d | tidak berubah %d) | server umumkan %d | unik %d | verifikasi %s",
			sintaRes.JournalsSaved, sintaRes.JournalsNew, sintaRes.JournalsUpdated, sintaRes.JournalsUnchanged,
			sintaRes.TotalRecords, sintaRes.UniqueIDs, verif)
		if sintaRes.RepairRounds > 0 {
			log.Printf("[RINGKASAN] repair   : %d round, %d halaman di-refetch", sintaRes.RepairRounds, sintaRes.RepairPages)
		}
		if !sintaRes.Verified && sintaRes.VerifyMsg != "" {
			log.Printf("[RINGKASAN] verifikasi detail: %s", sintaRes.VerifyMsg)
		}
	} else {
		log.Print("[RINGKASAN] stage sinta : tidak ada hasil (lihat error/GAGAL di atas)")
	}
	if st, ok := metrics.Default.Export()["sinta"]; ok && st.Requests > 0 {
		log.Printf("[RINGKASAN] http     : %d request | %.2f MB | avg %.0fms p95 %.0fms",
			st.Requests, float64(st.Bytes)/(1024*1024), st.Latency.Avg, st.Latency.P95)
	}
	log.Printf("[RINGKASAN] output   : %s", *dbPath)
	log.Printf("[RINGKASAN] metrik   : %s", metricsPath)
	log.Print("══════════════════════════════════════════════════════════════")

	// ── Exit code jujur ───────────────────────────────────────────────
	if stageErr != nil {
		os.Exit(1)
	}
	if sintaRes != nil && sintaRes.PagesFailed > 0 {
		log.Printf("jalankan ulang command yang sama — checkpoint akan mengulang hanya %d halaman yang gagal", sintaRes.PagesFailed)
		os.Exit(1)
	}
}

func validStages(v string) error {
	if v == "all" {
		return nil
	}
	valid := map[string]bool{"sinta": true, "ojs": true, "pdf": true}
	seen := map[string]bool{}
	for _, tok := range strings.Split(v, ",") {
		tok = strings.TrimSpace(tok)
		if !valid[tok] {
			return fmt.Errorf("-stages tidak valid: %q (pakai sinta,ojs,pdf | all)", tok)
		}
		if seen[tok] {
			return fmt.Errorf("-stages stage %q duplikat", tok)
		}
		seen[tok] = true
	}
	return nil
}

// validRank memvalidasi sintaks -rank. Satu sumber kebenaran = ParseRankSet
// di paket sinta (single | rentang A-B | daftar "1,5" | campuran | all).
func validRank(v string) error {
	if _, err := sinta.ParseRankSet(v); err != nil {
		return fmt.Errorf("-rank tidak valid: %q (pakai A, mis. 2 | A-B, mis. 1-3 | daftar, mis. \"1,5\" | all)", v)
	}
	return nil
}

func validYear(v string) error {
	if v == "latest" || v == "all" {
		return nil
	}
	yearOK := func(s string) (int, bool) {
		if len(s) != 4 {
			return 0, false
		}
		n, err := strconv.Atoi(s)
		return n, err == nil && n >= 1900 && n <= 2100
	}
	parts := strings.Split(v, "-")
	if len(parts) == 1 {
		if _, ok := yearOK(v); ok {
			return nil
		}
	}
	if len(parts) == 2 {
		a, okA := yearOK(parts[0])
		b, okB := yearOK(parts[1])
		if okA && okB && a <= b {
			return nil
		}
	}
	return fmt.Errorf("-year tidak valid: %q (pakai YYYY-YYYY, mis. 2024-2025 (A <= B) | latest | all)", v)
}

func validLatestVol(v string) error {
	if v == "all" {
		return nil
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 1 {
		return nil
	}
	return fmt.Errorf("-latest-vol tidak valid: %q (pakai angka >= 1 | all)", v)
}

func stagesInclude(stages, want string) bool {
	if stages == "all" {
		return true
	}
	for _, tok := range strings.Split(stages, ",") {
		if strings.TrimSpace(tok) == want {
			return true
		}
	}
	return false
}

// resolveFilterForm menerapkan prioritas doc 12: -filter > -query > -rank.
// -query dijalankan lewat GET (tanpa POST/cookie); -rank disusun jadi form POST.
func resolveFilterForm(filterRaw, query, rank string) (string, error) {
	if filterRaw != "" {
		return filterRaw, nil
	}
	if query != "" {
		return "", nil
	}
	return sinta.BuildFilterForm(rank)
}
