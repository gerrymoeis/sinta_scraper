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
	BaseURL         string
	FilterData      string        // hasil final: -filter | BuildFilterForm(-rank) | "" (mode query/all)
	ExtraQuery      string        // -query, di-merge ke URL tiap halaman
	RunKey          string        // namespace checkpoint (RunKeyFor)
	Refresh         bool          // -refresh: wipe checkpoint dulu, semua halaman discrape ulang (doc 16)
	MaxPages        int           // 0 = semua halaman (auto-detect)
	MinDelay        time.Duration // hanya untuk estimasi durasi di log
	MaxDelay        time.Duration
	Workers         int // jumlah goroutine fetch; tulis DB tetap di koordinator
	Logf            func(format string, args ...any)
	ExpectedRanks   []int       // level -rank yang diminta (kosong = tanpa sanity; doc 20 Bagian 2.4)
	MaxRepairRounds int         // 0 = default (6); -1 = one-pass (pass-1 murni tanpa repair, mode ukur L4 doc 28); >0 = batas atas round repair fixpoint (doc 24 Tahap I1)
	RepairFreeze    int         // 0 = tanpa freeze; N = halaman 1..N ditahan dari refetch normal, dibuka otomatis saat plateau (soft-freeze, doc 24 Tahap I2)
	AltSort         int         // 0 = mati; 1..5 = sort alternatif dicoba saat plateau lewat ChangeSort in-place (doc 24 Tahap I3)
	RegionMode      string      // "" / "kumulatif" = default; "recompute" = dupPages di-reset tiap round, region hanya dari duplikat round terakhir (doc 25 L1)
	T0Recheck       bool        // true = re-check TotalRecords (+1 request) saat fixpoint berhenti dgn defisit (plateau/maks-round/tanpa-kandidat) — guard perubahan total server di tengah run, bukan blocker (doc 26 L2)
	RecoverCatalog  map[int]int // L3: peta id katalog → affiliation ID (dari -recover-katalog; kosong = publisher recovery mati — first-run buta dilewati, R9 doc 23; doc 27)
	RecoverMaxPages int         // 0 = default (20); cap total halaman partition selama recovery (doc 27)
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
	RepairStop        string // alasan repair berhenti: verifikasi-ok | plateau | maks-round | tanpa-kandidat | "" (tak jalan) (doc 24 Tahap I1)
	AltRounds         int    // round yang berjalan dengan sort alternatif (fallback plateau, doc 24 Tahap I3)
	T0Recheck         string // "" = tak dijalankan; hasil re-check T0 saat plateau: "konfirmasi ..." | "BERUBAH ..." | "gagal: ..." (doc 26 L2)
	RecoveryMissing   int    // missing ID = katalog \ run — kandidat di-recover (0 = recovery tak jalan) (doc 27 L3)
	RecoveryPartisi   int    // partisi /journals/index/{affid} yang berhasil di-crawl
	RecoveryPages     int    // halaman partition yang di-fetch (dibatasi RecoverMaxPages)
	RecoveryFound     int    // missing ID yang ditemukan di partition & disimpan

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

	// 5. Perbaikan defisit — FIXPOINT + SOFT-FREEZE + FALLBACK ALT-SORT
	//    (doc 24 Tahap I1-I3; mengganti cap keras 2 round doc 19 Bagian 9.5):
	//    round baru SELAMA masih ada progres; berhenti saat unik == T0
	//    (verifikasi-ok), plateau +0, kandidat habis (tanpa-kandidat), atau
	//    -max-repair-rounds (maks-round). Urutan fallback saat plateau:
	//    (1) buka freeze bila masih ada kandidat tertahan (I2) → (2) ganti
	//    sort lewat ChangeSort in-place, 1+ round cadangan (I3; teruji probe:
	//    server menerima perubahan sort di sesi hidup) → (3) stop plateau.
	//    Eksperimen D (doc 23): 8/8 run mencapai T0; jendela tetap ±1 (T10b),
	//    region dinamis dari dupPages.
	const defaultRepairRounds = 6
	maxRounds := cfg.MaxRepairRounds
	if maxRounds == 0 {
		maxRounds = defaultRepairRounds
	}
	// ONE-PASS (doc 28 L4): -max-repair-rounds -1 = pass-1 murni tanpa
	// repair/alt-sort/freeze — mode ukur untuk eksperimen superset shielding;
	// loop di bawah tak akan jalan (round <= -1 selalu false).
	onePass := maxRounds < 0
	freeze := cfg.RepairFreeze
	if freeze < 0 {
		freeze = 0
	}
	alt := cfg.AltSort
	if alt < 0 {
		alt = 0
	}
	// MODE REGION (doc 25 L1): kumulatif (default) = dupPages menumpuk dari
	// pass → region round N = ±1 dari SEMUA duplikat yang pernah terlihat.
	// recompute = tracker di-reset tiap round → region round N+1 hanya dari
	// duplikat round N (hipotesis: region lebih kecil → request dipangkas).
	recount := cfg.RegionMode == "recompute"
	if recount {
		logf("[repair] region-mode: recompute — dupPages di-reset tiap round (kumulatif = default)")
	}
	thawed := false  // freeze sudah dibuka (fallback I2) — berlaku sisa run
	altUsed := false // sort sudah diganti (fallback I3) — sekali saja
	// ESCALATION REGION BERTINGKAT (L5 F1, doc 29): plateau TIDAK langsung
	// menyerah ke alt-sort — region diperluas bertahap dulu: ±1 → ±2 → span
	// component (min-2..max+2) → baru alt-sort. Escape hatch utk kasus di
	// mana defisit tak selalu contiguous/ekor (R2: instability tersebar).
	// Alt-sort me-reset pad ke ±1: urutan baru → dupPages baru.
	regionPad := 1
	fullRun := cfg.MaxPages == 0 && res.PagesSkipped == 0 && res.PagesFailed == 0
	for round := 1; fullRun && len(res.dupPages) > 0 &&
		res.uniqueIDs() < res.TotalRecords && round <= maxRounds; round++ {
		var regFull []int
		if regionPad >= 3 {
			regFull = spanPages(res.dupPages, targetPages)
		} else {
			regFull = repairPages(res.dupPages, targetPages, regionPad)
		}
		pages := make([]int, 0, len(regFull))
		blocked := false // ada kandidat tertahan freeze pada round ini?
		for _, p := range regFull {
			if !thawed && p <= freeze {
				blocked = true
				continue
			}
			pages = append(pages, p)
		}
		if len(pages) == 0 {
			// Semua kandidat tertahan freeze → buka sekarang (fallback I2);
			// thawed sudah true berarti tak ada kandidat sama sekali.
			if thawed {
				res.RepairStop = "tanpa-kandidat"
				break
			}
			thawed = true
			pages = regFull
			logf("[repair] freeze p1..%d menahan semua kandidat — membuka freeze (region penuh %d halaman)",
				freeze, len(pages))
		}
		if recount {
			// RECOMPUTE REGION (L1): region round ini sudah dihitung dari
			// dupPages — reset SEBELUM fetch agar dup selama round ini menjadi
			// SATU-SATUNYA dasar region round berikutnya.
			res.dupPages = map[int]bool{}
		}
		res.RepairRounds = round
		if altUsed {
			res.AltRounds++
		}
		before := res.uniqueIDs()
		logf("[repair] round %d: defisit unik %d/%d → re-fetch %d halaman %v",
			round, res.TotalRecords-before, res.TotalRecords, len(pages), pages)
		// Bounded parallel repair (L5 F3, doc 29): pool cfg.Workers seperti
		// pass-1; limiter global Session tetap gate etika (paralelisme hanya
		// menutupi latensi HTTP — laju request tak naik).
		if err := fetchRepairBatch(sess, cfg, store, res, pages); err != nil {
			return nil, err
		}
		gain := res.uniqueIDs() - before
		if gain == 0 {
			if !thawed && blocked {
				// SOFT-FREEZE FALLBACK (I2): plateau saat masih ada kandidat
				// tertahan → freeze dibuka untuk round cadangan region penuh.
				// L5 F2 (doc 29): freeze/thaw = OPTIMIZATION murni, bukan
				// correctness rule; thaw permanen (tak pernah re-freeze).
				thawed = true
				logf("[repair] plateau dengan kandidat tertahan freeze — membuka freeze p1..%d untuk round cadangan",
					freeze)
				continue
			}
			// L5 F1 (doc 29): eskalasi region SEBELUM alt-sort — plateau
			// pertama ±1 → ±2 (tier 2/3), kedua → span component (tier 3/3),
			// baru alt-sort saat tier habis. tiap kenaikan = round cadangan
			// (region rehit di atas loop) — safety escape hatch, terutama utk
			// defisit tak-contiguous (R2); pad TIDAK berubah selama masih ada
			// gain (fixpoint lama identik).
			if regionPad == 1 {
				regionPad = 2
				logf("[repair] plateau — region diperluas ±1 → ±2 (tier 2/3; escape hatch L5)")
				continue
			}
			if regionPad == 2 {
				regionPad = 3
				logf("[repair] plateau — region diperluas ±2 → span component (tier 3/3; escape hatch L5)")
				continue
			}
			if alt > 0 && !altUsed {
				// FALLBACK ALT-SORT (I3): ganti urutan di sesi HIDUP
				// (ChangeSort —1 POST, sesi tak terputus; teruji probe) lalu
				// round cadangan dengan sort baru. Gagal POST → matikan alt.
				// L5: pad di-reset ±1 — urutan baru → dupPages baru (kumulatif
				// dibiarkan; perilaku alt-sort lama tetap saat pad sudah ±1).
				if err := sess.ChangeSort(cfg.BaseURL, alt); err != nil {
					logf("[repair] gagal ganti sort ke s%d: %v — fallback alt dimatikan", alt, err)
					alt = 0
				} else {
					altUsed = true
					regionPad = 1
					logf("[repair] plateau — sort diganti ke s%d (fallback alt); pad reset ±1; round cadangan dengan urutan baru", alt)
					continue
				}
			}
			res.RepairStop = "plateau"
			logf("[repair] round %d: plateau (+0 unik) — defisit %d tersisa; berhenti (fixpoint)",
				round, res.TotalRecords-res.uniqueIDs())
			break
		}
		logf("[repair] round %d: +%d unik → %d/%d", round, gain, res.uniqueIDs(), res.TotalRecords)
	}
	switch {
	case res.RepairStop != "": // sudah diputus di dalam loop (plateau)
	case onePass:
		// one-pass eksplisit (L4): laporkan apa adanya; verifier tetap
		// menentukan VERIFIED_COMPLETE/INCOMPLETE dari unik == T0.
		res.RepairStop = "one-pass"
	case !fullRun:
	case res.uniqueIDs() >= res.TotalRecords:
		if res.RepairRounds > 0 {
			res.RepairStop = "verifikasi-ok"
		}
	case len(res.dupPages) == 0:
		res.RepairStop = "tanpa-kandidat" // defisit tapi tak ada halaman dup → tak ada region untuk direpair
	default:
		res.RepairStop = "maks-round" // defisit masih ada setelah habisnya round
	}
	res.UniqueIDs = res.uniqueIDs()

	// 5b. T0 RE-CHECK (L2 / R7 doc 23 — guard BUKAN blocker, doc 26): T0
	//     diambil sekali dari p1 saat pass awal; saat fixpoint berhenti dgn
	//     defisit tersisa — plateau, maks-round, atau tanpa-kandidat (semua
	//     fallback I2/I3 sudah dicoba; keputusan user 2 Okt: diperluas dari
	//     "plateau saja" agar kasus nyata maks-round Run A ikut ter-guard) —
	//     1 request ulang p1 mendeteksi apakah TotalRecords server berubah di
	//     tengah run. T0 berubah → verifier memakai angka terbaru. TIDAK
	//     memaksa round baru; run resume/max-pages/failed tak kena (loop tak
	//     jalan → RepairStop "").
	t0Note := ""
	stopWithDefisit := res.RepairStop == "plateau" || res.RepairStop == "maks-round" || res.RepairStop == "tanpa-kandidat"
	if cfg.T0Recheck && stopWithDefisit && res.UniqueIDs < res.TotalRecords {
		t0Awal := res.TotalRecords
		pr, err := sess.FetchPage(cfg.BaseURL, cfg.ExtraQuery, 1)
		switch {
		case err != nil:
			res.T0Recheck = fmt.Sprintf("gagal: %v", err)
			logf("[recheck] re-check T0 saat %s gagal: %v — memakai T0 awal %d", res.RepairStop, err, t0Awal)
		case pr.TotalJournals == t0Awal:
			res.T0Recheck = fmt.Sprintf("konfirmasi saat %s: server masih %d", res.RepairStop, t0Awal)
			t0Note = fmt.Sprintf("re-check T0: server masih %d", t0Awal)
			logf("[recheck] T0 saat %s: server masih %d (awal %d) — defisit %d dikonfirmasi",
				res.RepairStop, t0Awal, t0Awal, t0Awal-res.UniqueIDs)
		default:
			res.TotalRecords = pr.TotalJournals
			res.T0Recheck = fmt.Sprintf("BERUBAH saat %s: %d → %d", res.RepairStop, t0Awal, pr.TotalJournals)
			t0Note = fmt.Sprintf("T0 berubah %d→%d saat re-check %s", t0Awal, pr.TotalJournals, res.RepairStop)
			logf("[recheck] T0 server BERUBAH di tengah run saat %s: %d → %d — verifier memakai T0 terbaru",
				res.RepairStop, t0Awal, pr.TotalJournals)
		}
	}

	// 5c. PUBLISHER-ASSISTED RECOVERY (L3 / R9 doc 23 — BACKSTOP, doc 27):
	//     jendela sama dgn guard L2 (fixpoint berhenti + defisit, mode -rank).
	//     Tanpa katalog → skip (R9: "bukan first-run buta"). Dengan katalog:
	//     missing = katalog \ run-ini → resolve id→affid dari affiliation_url
	//     (offline, tanpa request detail) → crawl /journals/index/{affid} satu
	//     sesi SEGAR per partisi (Sibling — pola cmd/partition, doc 22) dgn
	//     form filter sama → kartu disimpan via saveRepair (rank terkontrol →
	//     sanity GAGAL-FILTER tetap berlaku) → verifier bagian 6 menilai ulang
	//     dgn angka unik & T0 terbaru. Cap total halaman partition = RecoverMaxPages.
	recNote := ""
	if len(cfg.RecoverCatalog) > 0 && stopWithDefisit && res.UniqueIDs < res.TotalRecords {
		if cfg.FilterData == "" {
			logf("[recovery] dilewati: tanpa -rank (FilterData kosong) — partisi tak terfilter rank bisa menarik rank asing")
		} else {
			missing := map[int]int{} // id → affID (kandidat yang belum terlihat run ini)
			for id, aff := range cfg.RecoverCatalog {
				if aff > 0 && !res.seen[id] {
					missing[id] = aff
				}
			}
			res.RecoveryMissing = len(missing)
			if len(missing) == 0 {
				logf("[recovery] katalog tidak menambah ID di luar run ini — dilewati")
			} else {
				byAff := map[int][]int{}
				for id, aff := range missing {
					byAff[aff] = append(byAff[aff], id)
				}
				affs := make([]int, 0, len(byAff))
				for a := range byAff {
					affs = append(affs, a)
				}
				sort.Ints(affs) // deterministik
				maxPages := cfg.RecoverMaxPages
				if maxPages <= 0 {
					maxPages = 20
				}
				pagesLeft := maxPages
				logf("[recovery] %d missing ID dari katalog → %d partisi (cap %d halaman)",
					len(missing), len(affs), maxPages)
				for _, aff := range affs {
					if pagesLeft <= 0 {
						logf("[recovery] cap %d halaman habis — %d missing tersisa", maxPages, len(missing))
						break
					}
					if err := recoverPartition(sess, store, cfg, res, aff, missing, &pagesLeft); err != nil {
						logf("[recovery] partisi aff %d gagal: %v — lanjut partisi berikutnya", aff, err)
					}
					if len(missing) == 0 {
						break
					}
				}
				res.UniqueIDs = res.uniqueIDs() // verifier (bagian 6) memakai angka terbaru
				if res.UniqueIDs < res.TotalRecords {
					recNote = fmt.Sprintf("publisher recovery: %d/%d missing ketemu",
						res.RecoveryFound, res.RecoveryMissing)
				}
				logf("[recovery] selesai: %d/%d missing ketemu | unik %d/%d",
					res.RecoveryFound, res.RecoveryMissing, res.UniqueIDs, res.TotalRecords)
			}
		}
	}

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
		// Kontrak status (doc 24 Tahap I4): VERIFIED_COMPLETE hanya setelah
		// verifier mencapai T0 — bukan klaim "100% guaranteed by server".
		res.VerifyMsg = fmt.Sprintf("VERIFIED_COMPLETE: %d ID unik = %d total records server", res.UniqueIDs, res.TotalRecords)
	case res.UniqueIDs > res.TotalRecords:
		res.VerifyMsg = fmt.Sprintf("GAGAL: ID unik %d > %d server — parser/scope tidak konsisten!",
			res.UniqueIDs, res.TotalRecords)
	default:
		// INCOMPLETE (dahulu UNRESOLVED — doc 24 Tahap I4): status INI yang
		// dijamin jujur: laporan TIDAK dianggap lengkap sampai T0 tercapai.
		res.VerifyMsg = fmt.Sprintf("INCOMPLETE: ID unik %d < %d server (kurang %d) — laporan tidak dianggap lengkap; jalankan ulang atau inspeksi manual",
			res.UniqueIDs, res.TotalRecords, res.TotalRecords-res.UniqueIDs)
	}
	// Konteks re-check T0 (L2) & publisher recovery (L3) hanya menyusul status
	// defisit/inkonsistensi — teks kontrak VERIFIED_COMPLETE & dilewati tidak
	// disentuh.
	if t0Note != "" && (strings.HasPrefix(res.VerifyMsg, "INCOMPLETE") || strings.HasPrefix(res.VerifyMsg, "GAGAL:")) {
		res.VerifyMsg += "; " + t0Note
	}
	if recNote != "" && (strings.HasPrefix(res.VerifyMsg, "INCOMPLETE") || strings.HasPrefix(res.VerifyMsg, "GAGAL:")) {
		res.VerifyMsg += "; " + recNote
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
	// Status jujur (doc 16 Bagian 3.3 + kontrak I4 doc 24): VERIFIED_COMPLETE
	// hanya bila verifier mencapai T0; defisit tersisa = INCOMPLETE; sisanya
	// tetap kelas status lama (GAGAL-*, dilewati/resume = BERHASIL).
	status := "BERHASIL"
	switch {
	case res.PagesFailed > 0:
		status = fmt.Sprintf("GAGAL-SEBAGIAN (%d halaman gagal)", res.PagesFailed)
	case strings.HasPrefix(res.VerifyMsg, "GAGAL-FILTER"):
		status = fmt.Sprintf("GAGAL-FILTER (rank diminta %v, isi run tak cocok)", cfg.ExpectedRanks)
	case strings.HasPrefix(res.VerifyMsg, "GAGAL"):
		status = "GAGAL-VERIFIKASI (jumlah tidak cocok dengan server)"
	case strings.HasPrefix(res.VerifyMsg, "INCOMPLETE"):
		status = fmt.Sprintf("INCOMPLETE (%d unik dari %d server)", res.UniqueIDs, res.TotalRecords)
	case res.Verified:
		status = "VERIFIED_COMPLETE"
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

// recoverPartition (L3 doc 27) memcrawl SATU partisi publisher
// /journals/index/{affid} dengan sesi SEGAR (Sibling — server hanya mengirim
// Set-Cookie saat sesi PHP dibuat, pola cmd/partition doc 22) dan form filter
// yang sama dengan run global. Semua halaman partisi diambil sampai budget
// *pagesLeft habis; kartu disimpan lewat saveRepair (statistik + seen + rank
// ikut tercatat → verifier & GAGAL-FILTER tetap konsisten). ID yang termasuk
// `missing` dihapus dari map dan dihitung ke res.RecoveryFound.
func recoverPartition(sess *Session, store JournalStore, cfg StageConfig, res *StageResult,
	aff int, missing map[int]int, pagesLeft *int) error {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	psess, err := sess.Sibling("recovery")
	if err != nil {
		return fmt.Errorf("sesi partition: %w", err)
	}
	base := strings.TrimRight(cfg.BaseURL, "/") + "/index/" + strconv.Itoa(aff)
	if err := psess.InitFilter(base, cfg.FilterData); err != nil {
		return fmt.Errorf("init filter: %w", err)
	}
	first, err := psess.FetchPage(base, "", 1)
	if err != nil {
		return fmt.Errorf("fetch p1: %w", err)
	}
	res.RecoveryPartisi++
	pages := first.TotalPages
	if pages < 1 {
		pages = 1
	}
	taken := 0
	for p := 1; p <= pages && *pagesLeft > 0; p++ {
		pr := first
		if p > 1 {
			if pr, err = psess.FetchPage(base, "", p); err != nil {
				// Partisi kecil (1–4 halaman); p1 sudah disimpan sebelum gagal
				// di sini — biarkan partisi berikutnya mencoba.
				logf("[recovery] aff %d p%d gagal: %v", aff, p, err)
				break
			}
		}
		*pagesLeft--
		res.RecoveryPages++
		taken++
		if err := saveRepair(store, cfg, p, pr.Journals, res); err != nil {
			return err
		}
		for _, j := range pr.Journals {
			if _, ok := missing[j.ID]; ok {
				delete(missing, j.ID)
				res.RecoveryFound++
			}
		}
	}
	logf("[recovery] aff %d: T0 partisi=%d, %d halaman, kumulatif %d missing ketemu",
		aff, first.TotalJournals, taken, res.RecoveryFound)
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

// repairPages = daftar halaman re-fetch: tiap halaman duplikat ±pad (jendela
// dasar pad=1 ala T10b, doc 19 Bagian 8; L5 F1 doc 29 memperluas ke pad=2 saat
// plateau tier-2), unik & terurut.
func repairPages(dup map[int]bool, target, pad int) []int {
	if pad < 1 {
		pad = 1
	}
	set := map[int]bool{}
	for p := range dup {
		for d := -pad; d <= pad; d++ {
			q := p + d
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

// spanPages = tier-3 eskalasi L5 (doc 29): SATU blok kontigu
// min(dup)-2 .. max(dup)+2 (clamped [1..target]) — menutup gap antar duplikat
// yang tersebar; defisit tak selalu contiguous/ekor (saran user pasca-L4).
func spanPages(dup map[int]bool, target int) []int {
	if len(dup) == 0 {
		return nil
	}
	minP, maxP := 0, 0
	for p := range dup {
		if minP == 0 || p < minP {
			minP = p
		}
		if p > maxP {
			maxP = p
		}
	}
	lo, hi := minP-2, maxP+2
	if lo < 1 {
		lo = 1
	}
	if hi > target {
		hi = target
	}
	pages := make([]int, 0, hi-lo+1)
	for p := lo; p <= hi; p++ {
		pages = append(pages, p)
	}
	return pages
}

// fetchRepairBatch = BOUNDED PARALLEL repair (L5 F3, doc 29): re-fetch halaman
// region satu round lewat pool cfg.Workers — pola identik pass-1 (jobs/results/
// koordinator). Limiter global Session TETAP jadi gate etika: paralelisme hanya
// menutupi latensi HTTP, tidak menaik laju request. PENTING: hasil diproses
// IN-ORDER mengikuti `pages` (buffer results → drain berurutan) — dupPages/
// statistik identik persis dgn versi sekuensial (tanpa ini, arrival-order
// paralel membuat region tak deterministik). Error DB membatalkan fetch baru
// (ctx) lalu results tetap dikuras sampai close (anti-deadlock ala pass-1).
func fetchRepairBatch(sess *Session, cfg StageConfig, store JournalStore, res *StageResult, pages []int) error {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	workers := cfg.Workers
	if workers < 1 {
		workers = 1
	}
	if workers > len(pages) {
		workers = len(pages)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := make(chan int)
	results := make(chan pageResult, workers)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for page := range jobs {
				if ctx.Err() != nil {
					continue
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
	go func() {
		defer close(jobs)
		for _, page := range pages {
			select {
			case jobs <- page:
			case <-ctx.Done():
				return
			}
		}
	}()

	var dbErr error
	next := 0 // indeks pages berikutnya yang harus diproses (urut)
	buf := map[int]pageResult{}
	handle := func(r pageResult) {
		if r.err != nil {
			logf("[repair] halaman %d gagal: %v (akan diulang pada run berikutnya)", r.page, r.err)
			return
		}
		if err := saveRepair(store, cfg, r.page, r.pr.Journals, res); err != nil {
			dbErr = err
			cancel()
			return
		}
		res.RepairPages++
	}
	for r := range results {
		buf[r.page] = r
		for next < len(pages) {
			r, ok := buf[pages[next]]
			if !ok {
				break
			}
			delete(buf, pages[next])
			next++
			handle(r)
		}
	}
	return dbErr
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
