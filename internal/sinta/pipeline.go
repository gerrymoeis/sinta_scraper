package sinta

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// JournalStore = kontrak storage yang dibutuhkan stage sinta. *storage.Store
// memenuhi secara struktural — dicek compiler saat dipassing dari main.
// Pakai interface supaya pipeline bisa diuji offline dengan fake in-memory.
type JournalStore interface {
	UpsertJournals(journals []Journal) (UpsertReport, error)
	MarkPageCompleted(runKey string, page int) error
	CompletedPages(runKey string) (map[int]bool, error)
	ClearCheckpoint(runKey string) error
	RankCounts() (map[int]int, error) // distribusi sinta_rank di DB — sanity GAGAL-FILTER (doc 20)
}

type StageConfig struct {
	BaseURL       string
	FilterData    string        // hasil final: -filter | BuildFilterForm(-rank) | "" (mode query/all)
	ExtraQuery    string        // -query, di-merge ke URL tiap halaman
	RunKey        string        // namespace checkpoint (RunKeyFor)
	Refresh       bool          // -refresh: wipe checkpoint dulu, semua halaman discrape ulang (doc 16)
	MaxPages      int           // 0 = semua halaman (auto-detect)
	MinDelay      time.Duration // hanya untuk estimasi durasi di log
	MaxDelay      time.Duration
	Workers       int // jumlah goroutine fetch; tulis DB tetap di koordinator
	Logf          func(format string, args ...any)
	ExpectedRanks []int // level -rank yang diminta (kosong = tanpa sanity; doc 20 Bagian 2.4)
}

type StageResult struct {
	TotalPages        int // dari server (auto-detect)
	TotalRecords      int // dari server (auto-detect)
	PagesSaved        int // halaman yang di-upsert run ini
	PagesSkipped      int // dilewati karena checkpoint
	PagesFailed       int // fetch gagal setelah retry → rerun akan mengulangnya
	JournalsSaved     int // Σ kartu halaman yang DIPROSES run ini (= New+Updated+Unchanged)
	JournalsNew       int // belum ada di db → INSERT
	JournalsUpdated   int // konten beda → ditimpa + field-nya dicatat di log [ubah]
	JournalsUnchanged int // konten identik → hanya sentuh last_scraped_at (tanpa tulis ulang)
	Verified          bool
	VerifyMsg         string // selalu terisi — alasan verifikasi OK/dilewati/gagal
	UniqueIDs         int    // ID unik terlihat run ini — dasar verifikasi (doc 19 Bagian 9)
	RepairRounds      int    // round perbaikan setelah defisit unik terdeteksi
	RepairPages       int    // total halaman yang di-refetch saat repair

	// Internal (bukan bagian kontrak keluar): tracker coverage.
	seen     map[int]bool // ID unik yang sudah terlihat run ini
	dupPages map[int]bool // halaman yang memunculkan ID sudah-terlihat (kandidat repair)
	runRanks map[int]int  // distribusi sinta_rank BARIS YANG DITULIS run ini (sanity doc 20)
}

// pageResult = outcome satu halaman dari worker → koordinator (single writer).
type pageResult struct {
	page int
	pr   *FilterPageResult
	err  error
}

// RunSintaStage menjalankan stage sinta: benar & tahan gangguan dulu
// (halaman 1 sekuensial), sisanya via worker pool. Alur (doc 12 Bagian 5):
// POST filter → auto-detect halaman 1 → pool halaman belum-done →
// batch upsert + checkpoint per halaman (single writer) → verifikasi A1.
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
	workers := cfg.Workers
	if workers < 1 {
		workers = 1
	}
	avgDelay := (cfg.MinDelay + cfg.MaxDelay) / 2
	est := time.Duration(targetPages) * avgDelay
	logf("[stage sinta] auto-detect: %d halaman | %d total record | target %d halaman | estimasi ~%v",
		first.TotalPages, first.TotalJournals, targetPages, est.Round(time.Second))

	// 3. Checkpoint: baca sekali, perbarui sesudah tiap halaman sukses.
	//    -refresh = wipe dulu (generasi konsisten, doc 16 Bagian 3.2) → baca jadi kosong.
	if cfg.Refresh {
		if err := store.ClearCheckpoint(cfg.RunKey); err != nil {
			return nil, fmt.Errorf("wipe checkpoint (-refresh): %w", err)
		}
	}
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

	// 4. Sisa halaman (2..target) lewat worker pool: fetch paralel oleh `workers`
	//    goroutine, TULIS tetap di goroutine ini (single-writer SQLite).
	start := time.Now()
	lastProgress := start

	pending := make([]int, 0, targetPages-1)
	for page := 2; page <= targetPages; page++ {
		if done[page] {
			res.PagesSkipped++
			continue
		}
		pending = append(pending, page)
	}

	// Mode run (doc 16 Bagian 3.3 — pertimbangan user A): tegas antara
	// fresh / lanjutan (bukan gagal) / refresh.
	scrapeNow := targetPages - res.PagesSkipped
	switch {
	case cfg.Refresh:
		logf("[mode] REFRESH (-refresh) — checkpoint di-wipe; semua %d halaman discrape ulang; perubahan konten dicatat via [ubah]", scrapeNow)
	case res.PagesSkipped > 0:
		logf("[mode] LANJUTAN (checkpoint) — %d halaman sudah ada dari run sebelumnya, dilewati BUKAN gagal; %d halaman discrape; hasil run ini tetap sah",
			res.PagesSkipped, scrapeNow)
	default:
		logf("[mode] BARU (fresh) — tanpa checkpoint; %d halaman discrape dari nol", scrapeNow)
	}
	logf("[stage sinta] worker pool: %d workers | %d halaman antre (%d dilewati checkpoint)",
		workers, len(pending), res.PagesSkipped)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := make(chan int)                    // dispatcher → worker (backpressure alami)
	results := make(chan pageResult, workers) // worker → koordinator (buffer = workers)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for page := range jobs {
				if ctx.Err() != nil {
					continue // dibatalkan (gagal sistemik) — jangan mulai fetch baru
				}
				pr, err := sess.FetchPage(cfg.BaseURL, cfg.ExtraQuery, page)
				results <- pageResult{page: page, pr: pr, err: err}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()
	go func() { // dispatcher
		defer close(jobs)
		for _, page := range pending {
			select {
			case jobs <- page:
			case <-ctx.Done(): // berhenti antre; worker menghabiskan sisa lalu keluar
				return
			}
		}
	}()

	var dbErr error
	for r := range results { // koordinator = SATU-SATUNYA penulis DB
		if r.err != nil {
			res.PagesFailed++
			logf("[gagal] halaman %d: %v (akan diulang pada run berikutnya)", r.page, r.err)
			continue // checkpoint memastikan rerun hanya mengulang yang gagal
		}
		if err := savePage(store, cfg, r.page, r.pr.Journals, res); err != nil {
			dbErr = err
			cancel() // sistemik → worker berhenti fetch BARU
			continue // PENTING: tetap menguras results sampai close — inilah anti-deadlocknya
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
	if dbErr != nil {
		return nil, dbErr // kegagalan DB = sistemik → hentikan run
	}

	// 5. Perbaikan defisit kecil (doc 19 Bagian 9.5): sort=Citations membuat
	//    defisit unik LANGKA (T13: 261/261 tiga round). Bila tetap defisit dan
	//    ada duplikat → re-fetch halaman duplikat ±1, maks 2 round. Jendela
	//    sempit = ±1: pergerakan terkonsentrasi lokal (T10b), perbaikan
	//    murah. Bila masih kurang → jujur: UNRESOLVED, bukan klaim lengkap.
	const maxRepairRounds = 2
	fullRun := cfg.MaxPages == 0 && res.PagesSkipped == 0 && res.PagesFailed == 0
	for round := 1; fullRun && len(res.dupPages) > 0 &&
		res.uniqueIDs() < res.TotalRecords && round <= maxRepairRounds; round++ {
		pages := repairPages(res.dupPages, targetPages)
		res.RepairRounds = round
		logf("[repair] round %d: defisit unik %d/%d → re-fetch %d halaman %v",
			round, res.TotalRecords-res.uniqueIDs(), res.TotalRecords, len(pages), pages)
		for _, p := range pages {
			pr, err := sess.FetchPage(cfg.BaseURL, cfg.ExtraQuery, p)
			if err != nil {
				logf("[repair] halaman %d gagal: %v", p, err)
				continue
			}
			if err := saveRepair(store, cfg, p, pr.Journals, res); err != nil {
				return nil, err
			}
			res.RepairPages++
		}
	}
	res.UniqueIDs = res.uniqueIDs()

	// 6. Verifikasi berbasis ID UNIK (doc 19 Bagian 9.1): JournalsSaved
	//    (jumlah kartu diproses) VACUOUS — duplikat offset kehilangan terbukti
	//    di T10 (261 kartu ≠ 261 unik). Pembanding = |ID unik| vs Total
	//    Records (angka T0, auto-detect per run).
	switch {
	case cfg.MaxPages > 0:
		res.VerifyMsg = fmt.Sprintf("dilewati (run penuh tidak diminta, -max-pages=%d); server=%d, unik run ini=%d",
			cfg.MaxPages, res.TotalRecords, res.UniqueIDs)
	case res.PagesSkipped > 0:
		res.VerifyMsg = fmt.Sprintf("dilewati (resume: %d halaman dari run sebelumnya); server=%d, unik run ini=%d",
			res.PagesSkipped, res.TotalRecords, res.UniqueIDs)
	case res.PagesFailed > 0:
		res.VerifyMsg = fmt.Sprintf("dilewati (%d halaman gagal) — jalankan ulang untuk mengulangnya; server=%d",
			res.PagesFailed, res.TotalRecords)
	case res.UniqueIDs == res.TotalRecords:
		res.Verified = true
		res.VerifyMsg = fmt.Sprintf("OK: %d ID unik = %d total records server", res.UniqueIDs, res.TotalRecords)
	case res.UniqueIDs > res.TotalRecords:
		res.VerifyMsg = fmt.Sprintf("GAGAL: ID unik %d > %d server — parser/scope tidak konsisten!",
			res.UniqueIDs, res.TotalRecords)
	default:
		res.VerifyMsg = fmt.Sprintf("UNRESOLVED: ID unik %d < %d server (kurang %d) — server tak menjamin coverage; jalankan ulang atau inspeksi manual",
			res.UniqueIDs, res.TotalRecords, res.TotalRecords-res.UniqueIDs)
	}

	// 6b. Sanity FILTER (doc 20 Bagian 2.4): verifikasi cakupan di atas hanya
	//     membuktikan "semua yang server berikan diambil" — TIDAK membuktikan
	//     "server memberi yang diminta". Bandingkan distribusi sinta_rank BARIS
	//     RUN INI vs -rank. Sengaja pakai baris run-ini, bukan seluruh db:
	//     satu db sah menampung banyak rank dari run berbeda (checkpoint
	//     ter-namespaces per run-key) → cek db penuh akan false-positive.
	if len(cfg.ExpectedRanks) > 0 && len(res.runRanks) > 0 {
		allowed := make(map[int]bool, len(cfg.ExpectedRanks))
		for _, r := range cfg.ExpectedRanks {
			allowed[r] = true
		}
		var asing, hilang []string
		for r, n := range res.runRanks {
			if !allowed[r] {
				asing = append(asing, fmt.Sprintf("S%d×%d", r, n))
			}
		}
		for _, r := range cfg.ExpectedRanks {
			if res.runRanks[r] == 0 {
				hilang = append(hilang, fmt.Sprintf("S%d", r))
			}
		}
		sort.Strings(asing)
		switch {
		case len(asing) > 0:
			// rank asing = filter salah sasaran → SELALU fatal (termasuk resume)
			res.Verified = false
			res.VerifyMsg = fmt.Sprintf("GAGAL-FILTER: %d jurnal run ini di luar rank yang diminta %v (ditulis: %s)",
				totalRows(res.runRanks), cfg.ExpectedRanks, strings.Join(asing, ", "))
		case res.Verified && res.PagesSkipped == 0 && len(hilang) > 0:
			// rank diminta hilang → hanya saat cakupan penuh TANPA resume
			// (run resume boleh tak menulis rank yang barisnya dilewati checkpoint)
			res.Verified = false
			res.VerifyMsg = fmt.Sprintf("GAGAL-FILTER: rank %s diminta tapi tidak ada di hasil run (%s)",
				strings.Join(hilang, ","), formatRankCounts(res.runRanks))
		}
	}
	// Observasi distribusi db (tanpa vonis — db boleh multi-rank lintas run).
	if counts, err := store.RankCounts(); err != nil {
		logf("[peringatan] baca distribusi sinta_rank db gagal: %v", err)
	} else if len(counts) > 0 {
		logf("[rank-db] distribusi sinta_rank di db: %s", formatRankCounts(counts))
	}

	logf("[verifikasi] %s", res.VerifyMsg)
	// Status jujur (doc 16 Bagian 3.3): lanjutan dengan 0 gagal = BERHASIL,
	// bukan terlihat seperti run yang gagal.
	status := "BERHASIL"
	switch {
	case res.PagesFailed > 0:
		status = fmt.Sprintf("GAGAL-SEBAGIAN (%d halaman gagal)", res.PagesFailed)
	case strings.HasPrefix(res.VerifyMsg, "GAGAL-FILTER"):
		status = fmt.Sprintf("GAGAL-FILTER (rank diminta %v, isi run tak cocok)", cfg.ExpectedRanks)
	case strings.HasPrefix(res.VerifyMsg, "GAGAL"):
		status = "GAGAL-VERIFIKASI (jumlah tidak cocok dengan server)"
	case strings.HasPrefix(res.VerifyMsg, "UNRESOLVED"):
		status = fmt.Sprintf("UNRESOLVED (%d unik dari %d server)", res.UniqueIDs, res.TotalRecords)
	}
	logf("[stage sinta] selesai [%s]: saved=%d skipped=%d failed=%d | jurnal: %d diproses (baru %d, diperbarui %d, tidak berubah %d)",
		status, res.PagesSaved, res.PagesSkipped, res.PagesFailed, res.JournalsSaved,
		res.JournalsNew, res.JournalsUpdated, res.JournalsUnchanged)
	return res, nil
}

// savePage = catat seen + upsert batch (1 transaksi per halaman) + checkpoint.
// Urutan penting: data dulu, checkpoint kemudian — checkpoint gagal hanya
// berarti halaman ini diulang pada run berikutnya (idempoten, aman).
func savePage(store JournalStore, cfg StageConfig, page int, journals []Journal, res *StageResult) error {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if len(journals) == 0 {
		logf("[peringatan] halaman %d: 0 kartu jurnal terdeteksi", page)
	}
	rep, err := store.UpsertJournals(journals)
	if err != nil {
		return fmt.Errorf("simpan halaman %d: %w", page, err)
	}
	res.JournalsNew += rep.New
	res.JournalsUpdated += rep.Updated
	res.JournalsUnchanged += rep.Unchanged
	logChanges(logf, page, rep.Changes)
	catatSeen(res, page, journals)
	catatRank(res, journals)
	if err := store.MarkPageCompleted(cfg.RunKey, page); err != nil && cfg.Logf != nil {
		cfg.Logf("[peringatan] checkpoint halaman %d gagal: %v", page, err)
	}
	res.PagesSaved++
	res.JournalsSaved += len(journals)
	return nil
}

// saveRepair = upsert + catat seen untuk hasil re-fetch perbaikan. TIDAK
// menyentuh checkpoint/PagesSaved/JournalsSaved — halaman sudah "saved" dari
// pass utama; ini re-fetch semata supaya statistik pass tetap jujur.
func saveRepair(store JournalStore, cfg StageConfig, page int, journals []Journal, res *StageResult) error {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if len(journals) == 0 {
		logf("[repair] halaman %d: 0 kartu jurnal terdeteksi", page)
	}
	rep, err := store.UpsertJournals(journals)
	if err != nil {
		return fmt.Errorf("repair halaman %d: %w", page, err)
	}
	res.JournalsNew += rep.New
	res.JournalsUpdated += rep.Updated
	res.JournalsUnchanged += rep.Unchanged
	logChanges(logf, page, rep.Changes)
	catatSeen(res, page, journals)
	catatRank(res, journals)
	return nil
}

// catatRank mencatat distribusi sinta_rank baris yang ditulis run ini — bahan
// sanity GAGAL-FILTER (doc 20). Sengaja BUKAN dari seluruh db: satu db sah
// menampung banyak rank dari run berbeda (checkpoint ter-namespaces per run-key).
func catatRank(res *StageResult, journals []Journal) {
	if res.runRanks == nil {
		res.runRanks = map[int]int{}
	}
	for _, j := range journals {
		res.runRanks[j.SintaRank]++
	}
}

// catatSeen menandai ID terlihat run ini; ID yang terulang → halaman ini
// dicatat sebagai kandidat repair (defisit unik biasanya menyertainya).
func catatSeen(res *StageResult, page int, journals []Journal) {
	if res.seen == nil {
		res.seen = map[int]bool{}
		res.dupPages = map[int]bool{}
	}
	for _, j := range journals {
		if res.seen[j.ID] {
			res.dupPages[page] = true
		}
		res.seen[j.ID] = true
	}
}

func (r *StageResult) uniqueIDs() int { return len(r.seen) }

// repairPages = daftar halaman re-fetch: tiap halaman duplikat ±1 (jendela
// sempit — pergerakan terkonsentrasi lokal, doc 19 Bagian 8), unik & terurut.
func repairPages(dup map[int]bool, target int) []int {
	set := map[int]bool{}
	for p := range dup {
		for _, q := range []int{p - 1, p, p + 1} {
			if q >= 1 && q <= target {
				set[q] = true
			}
		}
	}
	pages := make([]int, 0, len(set))
	for p := range set {
		pages = append(pages, p)
	}
	sort.Ints(pages)
	return pages
}

// logChanges mencatat perubahan field per jurnal (dipakai savePage & saveRepair).
func logChanges(logf func(string, ...any), page int, changes []JournalChange) {
	for _, ch := range changes {
		parts := make([]string, 0, len(ch.Fields))
		for _, f := range ch.Fields {
			parts = append(parts, fmt.Sprintf("%s: %s→%s", f.Field, f.Old, f.New))
		}
		logf("[ubah] jurnal id=%d %q (halaman %d): %s", ch.ID, ch.Name, page, strings.Join(parts, " | "))
	}
}

func totalRows(counts map[int]int) int {
	n := 0
	for _, c := range counts {
		n += c
	}
	return n
}

// formatRankCounts → "S1×261, S5×5395" (urut rank — pesan deterministik).
func formatRankCounts(counts map[int]int) string {
	parts := make([]string, 0, len(counts))
	for r := 1; r <= 6; r++ {
		if n := counts[r]; n > 0 {
			parts = append(parts, fmt.Sprintf("S%d×%d", r, n))
		}
	}
	for r, n := range counts { // rank di luar 1..6 (tak normal) tetap ditampilkan
		if r < 1 || r > 6 {
			parts = append(parts, fmt.Sprintf("S%d×%d", r, n))
		}
	}
	return strings.Join(parts, ", ")
}

// ParseRankSet menafsirkan sintaks -rank (doc 20 Bagian 2.1):
//
//	"all" → nil (tanpa filter) | "1" → [1] | "1-5" → [1..5]
//	"1,5" → [1 5] | "1,3-4" → [1 3 4] (token dipisah koma, boleh campur range)
//
// Level valid 1..6; hasil selalu unik & terurut naik.
func ParseRankSet(rank string) ([]int, error) {
	if rank == "all" {
		return nil, nil
	}
	bad := func() error { return fmt.Errorf("-rank tidak valid untuk filter: %q", rank) }
	levels := map[int]bool{}
	for _, tok := range strings.Split(rank, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			return nil, bad()
		}
		if p := strings.Split(tok, "-"); len(p) == 2 {
			a, errA := strconv.Atoi(p[0])
			b, errB := strconv.Atoi(p[1])
			if errA != nil || errB != nil || a < 1 || b > 6 || a > b {
				return nil, bad()
			}
			for i := a; i <= b; i++ {
				levels[i] = true
			}
			continue
		}
		n, err := strconv.Atoi(tok)
		if err != nil || n < 1 || n > 6 {
			return nil, bad()
		}
		levels[n] = true
	}
	if len(levels) == 0 {
		return nil, bad()
	}
	out := make([]int, 0, len(levels))
	for i := 1; i <= 6; i++ {
		if levels[i] {
			out = append(out, i)
		}
	}
	return out, nil
}

// BuildFilterForm menyusun payload POST filter dari -rank (doc 12 Bagian 2,
// doc 20 Bagian 2.2). VALUE = level akreditasi ITU SENDIRI — semantik server
// teruji 30 Sep 2026 (value selalu 1 → salah sasaran: -rank 5 terbaca S1).
// "all" → "" (tanpa POST — listing default server sudah seluruhnya).
func BuildFilterForm(rank string) (string, error) {
	levels, err := ParseRankSet(rank)
	if err != nil {
		return "", err
	}
	if levels == nil { // all
		return "", nil
	}
	parts := make([]string, 0, len(levels)+1)
	for _, lv := range levels {
		parts = append(parts, fmt.Sprintf("filter_accreditation[%d]=%d", lv, lv))
	}
	parts = append(parts, "filter_journals=1")
	return strings.Join(parts, "&"), nil
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
