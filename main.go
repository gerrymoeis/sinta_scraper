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
	rank := flag.String("rank", "1", "filter SINTA: 1-6, range A-B (mis. 1-3) | all")
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

	flag.Parse()
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

	// ── Stage pipeline ────────────────────────────────────────────────
	var stageErr error
	var sintaRes *sinta.StageResult

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
		runKey := sinta.RunKeyFor(*filterData, *extraQuery, *rank)
		log.Printf("run-key = %s | filter POST = %q", runKey, formData)

		sintaRes, stageErr = sinta.RunSintaStage(sess, store, sinta.StageConfig{
			BaseURL:    *baseURL,
			FilterData: formData,
			ExtraQuery: *extraQuery,
			RunKey:     runKey,
			MaxPages:   *maxPages,
			Workers:    *workers,
			MinDelay:   *minDelay,
			MaxDelay:   *maxDelay,
			Logf:       log.Printf,
		})
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

func validRank(v string) error {
	if v == "all" {
		return nil
	}
	if len(v) == 1 && v >= "1" && v <= "6" {
		return nil
	}
	if parts := strings.Split(v, "-"); len(parts) == 2 {
		a, errA := strconv.Atoi(parts[0])
		b, errB := strconv.Atoi(parts[1])
		if errA == nil && errB == nil && a >= 1 && b <= 6 && a <= b {
			return nil
		}
	}
	return fmt.Errorf("-rank tidak valid: %q (pakai A, mis. 2 | A-B, mis. 1-3 | all)", v)
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
