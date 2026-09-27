package sinta

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// JournalStore = kontrak storage yang dibutuhkan stage sinta. *storage.Store
// memenuhi secara struktural — dicek compiler saat dipassing dari main.
// Pakai interface supaya pipeline bisa diuji offline dengan fake in-memory.
type JournalStore interface {
	UpsertJournals(journals []Journal) error
	MarkPageCompleted(runKey string, page int) error
	CompletedPages(runKey string) (map[int]bool, error)
}

type StageConfig struct {
	BaseURL    string
	FilterData string        // hasil final: -filter | BuildFilterForm(-rank) | "" (mode query/all)
	ExtraQuery string        // -query, di-merge ke URL tiap halaman
	RunKey     string        // namespace checkpoint (RunKeyFor)
	MaxPages   int           // 0 = semua halaman (auto-detect)
	MinDelay   time.Duration // hanya untuk estimasi durasi di log
	MaxDelay   time.Duration
	Logf       func(format string, args ...any)
}

type StageResult struct {
	TotalPages    int // dari server (auto-detect)
	TotalRecords  int // dari server (auto-detect)
	PagesSaved    int // halaman yang di-upsert run ini
	PagesSkipped  int // dilewati karena checkpoint
	PagesFailed   int // fetch gagal setelah retry → rerun akan mengulangnya
	JournalsSaved int // Σ kartu halaman yang di-upsert run ini
	Verified      bool
	VerifyMsg     string // selalu terisi — alasan verifikasi OK/dilewati/gagal
}

// RunSintaStage menjalankan stage sinta SEKUENSIAL: benar & tahan gangguan
// dulu, paralelisasi menyusul. Alur (doc 12 Bagian 5):
// POST filter → auto-detect halaman 1 → loop halaman belum-done →
// batch upsert + checkpoint per halaman → verifikasi A1.
func RunSintaStage(sess *Session, store JournalStore, cfg StageConfig) (*StageResult, error) {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	res := &StageResult{}

	// 1. POST filter (no-op bila "" — mode all atau mode query)
	if err := sess.InitFilter(cfg.BaseURL, cfg.FilterData); err != nil {
		return nil, fmt.Errorf("init filter: %w", err)
	}

	// 2. Auto-detect dari halaman 1 — hasil fetch ini JUGA dipakai sebagai
	//    data halaman 1 (tidak diambil ulang)
	first, err := sess.FetchPage(cfg.BaseURL, cfg.ExtraQuery, 1)
	if err != nil {
		return nil, fmt.Errorf("fetch halaman 1 (auto-detect): %w", err)
	}
	if first.TotalPages <= 0 || first.TotalJournals <= 0 {
		return nil, fmt.Errorf("auto-detect gagal: total_pages=%d total_records=%d — struktur halaman berubah?",
			first.TotalPages, first.TotalJournals)
	}
	res.TotalPages, res.TotalRecords = first.TotalPages, first.TotalJournals

	targetPages := first.TotalPages
	if cfg.MaxPages > 0 && cfg.MaxPages < targetPages {
		targetPages = cfg.MaxPages
	}
	avgDelay := (cfg.MinDelay + cfg.MaxDelay) / 2
	est := time.Duration(targetPages) * avgDelay
	logf("[stage sinta] auto-detect: %d halaman | %d total record | target %d halaman | estimasi ~%v",
		first.TotalPages, first.TotalJournals, targetPages, est.Round(time.Second))

	// 3. Checkpoint: baca sekali, perbarui sesudah tiap halaman sukses
	done, err := store.CompletedPages(cfg.RunKey)
	if err != nil {
		return nil, fmt.Errorf("baca checkpoint: %w", err)
	}

	if done[1] {
		res.PagesSkipped++
	} else {
		if err := savePage(store, cfg, 1, first.Journals, res); err != nil {
			return nil, err
		}
	}

	start := time.Now()
	lastProgress := start
	for page := 2; page <= targetPages; page++ {
		if done[page] {
			res.PagesSkipped++
			continue
		}
		pr, err := sess.FetchPage(cfg.BaseURL, cfg.ExtraQuery, page)
		if err != nil {
			res.PagesFailed++
			logf("[gagal] halaman %d: %v (akan diulang pada run berikutnya)", page, err)
			continue // checkpoint memastikan rerun hanya mengulang yang gagal
		}
		if err := savePage(store, cfg, page, pr.Journals, res); err != nil {
			return nil, err // kegagalan DB = sistemik → hentikan run
		}

		// Progres ringan: tiap 10 halaman atau tiap 30 detik — bukan tiap halaman
		handled := res.PagesSaved + res.PagesSkipped
		if handled%10 == 0 || time.Since(lastProgress) > 30*time.Second {
			elapsed := time.Since(start)
			avgPer := elapsed / time.Duration(res.PagesSaved)
			remaining := targetPages - (handled + res.PagesFailed)
			eta := time.Duration(remaining) * avgPer
			logf("[progres] %d/%d halaman (%.1f%%) | jurnal: %d | rata-rata %v/halaman | ETA %v",
				handled, targetPages, float64(handled)/float64(targetPages)*100,
				res.JournalsSaved, avgPer.Round(time.Millisecond), eta.Round(time.Second))
			lastProgress = time.Now()
		}
	}

	// 4. Verifikasi (doc 13, A1): hanya run penuh yang bisa diverifikasi.
	//    Pembanding = Total Records yang diumumkan server PER RUN (bukan angka
	//    mati) → non-flaky dan menangkap perubahan layout/filter di runtime.
	switch {
	case cfg.MaxPages > 0:
		res.VerifyMsg = fmt.Sprintf("dilewati (run penuh tidak diminta, -max-pages=%d); server=%d, tersimpan run ini=%d",
			cfg.MaxPages, res.TotalRecords, res.JournalsSaved)
	case res.PagesSkipped > 0:
		res.VerifyMsg = fmt.Sprintf("dilewati (resume: %d halaman dari run sebelumnya); server=%d, tersimpan run ini=%d",
			res.PagesSkipped, res.TotalRecords, res.JournalsSaved)
	case res.PagesFailed > 0:
		res.VerifyMsg = fmt.Sprintf("dilewati (%d halaman gagal) — jalankan ulang untuk mengulangnya; server=%d",
			res.PagesFailed, res.TotalRecords)
	case res.JournalsSaved == res.TotalRecords:
		res.Verified = true
		res.VerifyMsg = fmt.Sprintf("OK: %d jurnal = %d total records server", res.JournalsSaved, res.TotalRecords)
	default:
		res.VerifyMsg = fmt.Sprintf("GAGAL: tersimpan %d, server mengumumkan %d — filter/parser tidak konsisten!",
			res.JournalsSaved, res.TotalRecords)
	}
	logf("[verifikasi] %s", res.VerifyMsg)
	logf("[stage sinta] selesai: saved=%d skipped=%d failed=%d jurnal=%d",
		res.PagesSaved, res.PagesSkipped, res.PagesFailed, res.JournalsSaved)
	return res, nil
}

// savePage = upsert batch (1 transaksi per halaman) + checkpoint. Urutan
// penting: data dulu, checkpoint kemudian — checkpoint gagal hanya berarti
// halaman ini diulang pada run berikutnya (idempoten, aman).
func savePage(store JournalStore, cfg StageConfig, page int, journals []Journal, res *StageResult) error {
	if len(journals) == 0 && cfg.Logf != nil {
		cfg.Logf("[peringatan] halaman %d: 0 kartu jurnal terdeteksi", page)
	}
	if err := store.UpsertJournals(journals); err != nil {
		return fmt.Errorf("simpan halaman %d: %w", page, err)
	}
	if err := store.MarkPageCompleted(cfg.RunKey, page); err != nil && cfg.Logf != nil {
		cfg.Logf("[peringatan] checkpoint halaman %d gagal: %v", page, err)
	}
	res.PagesSaved++
	res.JournalsSaved += len(journals)
	return nil
}

// BuildFilterForm menyusun payload POST filter dari -rank (doc 12 Bagian 2).
// "all" → "" (tanpa POST — listing default server sudah seluruhnya).
func BuildFilterForm(rank string) (string, error) {
	if rank == "all" {
		return "", nil
	}
	join := func(a, b int) string {
		var parts []string
		for i := a; i <= b; i++ {
			parts = append(parts, fmt.Sprintf("filter_accreditation[%d]=1", i))
		}
		parts = append(parts, "filter_journals=1")
		return strings.Join(parts, "&")
	}
	if len(rank) == 1 && rank >= "1" && rank <= "6" {
		return join(int(rank[0]-'0'), int(rank[0]-'0')), nil
	}
	if p := strings.Split(rank, "-"); len(p) == 2 {
		a, errA := strconv.Atoi(p[0])
		b, errB := strconv.Atoi(p[1])
		if errA == nil && errB == nil && a >= 1 && b <= 6 && a <= b {
			return join(a, b), nil
		}
	}
	return "", fmt.Errorf("-rank tidak valid untuk filter: %q", rank)
}

// RunKeyFor membuat namespace checkpoint (doc 12 Bagian 4):
// -rank=1 → "rank-1" | -filter X → "filter-<hash8>" | -query X → "query-<hash8>".
func RunKeyFor(filterRaw, query, rank string) string {
	switch {
	case filterRaw != "":
		return "filter-" + shortHash(filterRaw)
	case query != "":
		return "query-" + shortHash(query)
	default:
		return "rank-" + rank
	}
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4]) // 8 karakter hex
}
