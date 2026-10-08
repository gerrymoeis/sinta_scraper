// Program vdac: alat Eksperimen D (doc 23) — prototipe Verifier-Driven
// Adaptive Crawl sesuai belajar/archive/penting/analisa-eksperimen.md: pass global
// awal (sort awal, default 5) → fixpoint repair pada unstable region
// (dupPages ±1, halaman frozen dikecualikan) → bila defisit masih tersisa,
// sesi BARU dengan sort alternatif hanya pada region yang sama → verifikasi
// jujur unique==T0 (VERIFIED_COMPLETE / INCOMPLETE). Menghitung permintaan
// HTTP per fase sebagai metrik efisiensi. Bukan bagian dari pipeline.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"sinta-scraper/internal/metrics"
	"sinta-scraper/internal/sinta"
)

const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)"

type state struct {
	seen     map[int]bool
	dupPages map[int]bool
	refetch  map[int]bool
	excluded map[int]bool
	t0       int
	pages    int
}

func (st *state) unique() int { return len(st.seen) }

type roundStat struct {
	phase   string
	round   int
	region  int
	before  int
	after   int
	gain    int
	reqHTTP int
}

func main() {
	rank := flag.Int("rank", 6, "rank SINTA (1-6)")
	sortKey := flag.Int("sort", 5, "kunci urutan pass awal (0..5)")
	altSort := flag.Int("alt-sort", 4, "sort alternatif fase kedua setelah plateau (0 = nonaktif)")
	frozen := flag.Int("frozen", 11, "halaman 1..N dibekukan dari refetch (0 = tanpa freeze)")
	maxRounds := flag.Int("max-rounds", 6, "maksimum round fixpoint per fase sort")
	maxRequests := flag.Int("max-requests", 400, "anggaran maksimum permintaan HTTP total")
	pagesOverride := flag.Int("pages", 0, "paksa jumlah halaman (0 = auto-detect)")
	expectPath := flag.String("expect", "", "berkas ID ekspektasi untuk diff coverage")
	outPath := flag.String("out", "", "berkas output daftar ID unik (satu per baris)")
	base := flag.String("base-url", "https://sinta.kemdiktisaintek.go.id/journals", "URL listing")
	flag.Parse()

	form := fmt.Sprintf("filter_accreditation[%d]=%d&filter_journals=1", *rank, *rank)
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	budgetOK := func() bool { return httpRequests() < *maxRequests }

	st := &state{
		seen:     map[int]bool{},
		dupPages: map[int]bool{},
		refetch:  map[int]bool{},
		excluded: map[int]bool{},
	}
	var rounds []roundStat
	start := time.Now()

	// FASE PASS: pass global awal dengan sort awal (sesi tunggal, serial).
	sess, err := newSession()
	if err != nil {
		fatal("NewSession: %v", err)
	}
	if err := sess.SetSortKey(*sortKey); err != nil {
		fatal("SetSortKey: %v", err)
	}
	if err := sess.InitFilter(*base, form); err != nil {
		fatal("InitFilter: %v", err)
	}

	passReq0 := httpRequests()
	first, err := sess.FetchPage(*base, "", 1)
	if err != nil {
		fatal("fetch p1: %v", err)
	}
	st.t0 = first.TotalJournals
	st.pages = first.TotalPages
	if *pagesOverride > 0 {
		st.pages = *pagesOverride
	}
	record(st, 1, first)
	logf("[pass] p1/%d | unik %d | T0=%d | sort=%d", st.pages, st.unique(), st.t0, *sortKey)
	for p := 2; p <= st.pages; p++ {
		if !budgetOK() {
			logf("[pass] berhenti: anggaran HTTP %d tercapai di p%d", *maxRequests, p)
			break
		}
		if err := fetchInto(sess, st, *base, p); err != nil {
			logf("[pass] p%d gagal: %v", p, err)
			continue
		}
		logf("[pass] p%d/%d | unik %d | T0=%d", p, st.pages, st.unique(), st.t0)
	}
	passReq := httpRequests() - passReq0

	// FASE FIX: fixpoint pada sort awal — lanjut selama masih ada progres.
	stop1, req1 := fixpoint(sess, st, *base, fmt.Sprintf("fix(s%d)", *sortKey),
		*maxRounds, *maxRequests, *frozen, &rounds, logf)

	// FASE ALT: bila defisit tersisa & sort alternatif diminta → sesi baru
	// (cookie & POST filter segar), hanya refetch unstable region yang sama.
	stop2, req2 := "", 0
	if st.t0-st.unique() > 0 && *altSort > 0 && httpRequests() < *maxRequests {
		sess2, err := newSession()
		if err != nil {
			fatal("NewSession alt: %v", err)
		}
		if err := sess2.SetSortKey(*altSort); err != nil {
			fatal("SetSortKey alt: %v", err)
		}
		if err := sess2.InitFilter(*base, form); err != nil {
			fatal("InitFilter alt: %v", err)
		}
		stop2, req2 = fixpoint(sess2, st, *base, fmt.Sprintf("alt(s%d)", *altSort),
			*maxRounds, *maxRequests, *frozen, &rounds, logf)
	}

	// Status jujur berbasis verifier unique==T0.
	status := ""
	switch {
	case st.unique() > st.t0:
		status = fmt.Sprintf("GAGAL-SCOPE: unik %d > T0 %d — parser/scope tidak konsisten", st.unique(), st.t0)
	case st.unique() == st.t0:
		status = "VERIFIED_COMPLETE"
	default:
		reason := stop1
		if stop2 != "" {
			reason = stop1 + " → " + stop2
		}
		status = fmt.Sprintf("INCOMPLETE (defisit %d) — berhenti: %s", st.t0-st.unique(), reason)
	}

	// Laporan ringkas ke stdout (bisa di-redirect ke berkas hasil).
	fmt.Println("# vdac — laporan eksperimen D")
	fmt.Printf("rank\t%d\n", *rank)
	fmt.Printf("sort_awal\t%d\n", *sortKey)
	fmt.Printf("sort_alt\t%d\n", *altSort)
	fmt.Printf("frozen\t%d\n", *frozen)
	fmt.Printf("T0\t%d\n", st.t0)
	fmt.Printf("unik\t%d\n", st.unique())
	fmt.Printf("status\t%s\n", status)
	fmt.Printf("req_pass\t%d\n", passReq)
	fmt.Printf("req_fix\t%d\n", req1)
	fmt.Printf("req_alt\t%d\n", req2)
	fmt.Printf("req_http_total\t%d\n", httpRequests())
	fmt.Printf("durasi\t%s\n", time.Since(start).Round(time.Second))

	fmt.Println("# per-round")
	fmt.Println("fase\tround\tregion\tunik_sebelum\tunik_sesudah\tgain\treq_http_kumulatif")
	for _, r := range rounds {
		fmt.Printf("%s\t%d\t%d\t%d\t%d\t+%d\t%d\n", r.phase, r.round, r.region, r.before, r.after, r.gain, r.reqHTTP)
	}

	refetched := keysOf(st.refetch)
	fmt.Printf("refetch_pages\t%s\n", joinInts(refetched))
	fmt.Printf("excluded_by_freeze\t%s\n", joinInts(keysOf(st.excluded)))

	if *expectPath != "" {
		exp, err := readInts(*expectPath)
		if err != nil {
			fatal("baca expect: %v", err)
		}
		expSet := map[int]bool{}
		for _, id := range exp {
			expSet[id] = true
		}
		var missing, extra []int
		for id := range expSet {
			if !st.seen[id] {
				missing = append(missing, id)
			}
		}
		for id := range st.seen {
			if !expSet[id] {
				extra = append(extra, id)
			}
		}
		sort.Ints(missing)
		sort.Ints(extra)
		fmt.Printf("expected\t%d\n", len(expSet))
		fmt.Printf("kurang\t%d\n", len(missing))
		fmt.Printf("lebih\t%d\n", len(extra))
		if len(missing) > 0 {
			fmt.Printf("kurang_daftar\t%s\n", joinInts(missing))
		}
		if len(extra) > 0 {
			fmt.Printf("lebih_daftar\t%s\n", joinInts(extra))
		}
	}

	if *outPath != "" {
		ids := keysOf(st.seen)
		var b strings.Builder
		for _, id := range ids {
			b.WriteString(strconv.Itoa(id))
			b.WriteByte('\n')
		}
		if err := os.WriteFile(*outPath, []byte(b.String()), 0o644); err != nil {
			fatal("tulis out: %v", err)
		}
	}

	fmt.Println(metrics.Default.Report())
}

// fixpoint menjalankan round perbaikan selama masih ada progres (bukan cap
// tetap), pada unstable region = dupPages ±1 tanpa halaman frozen. Berhenti
// saat verifikasi OK, plateau, maks-round, region kosong, atau anggaran HTTP.
func fixpoint(sess *sinta.Session, st *state, base, phase string, maxRounds, maxReq, frozen int,
	rounds *[]roundStat, logf func(string, ...any)) (stop string, reqDelta int) {
	req0 := httpRequests()
	stop = "maks-round"
	for round := 1; round <= maxRounds; round++ {
		if st.unique() >= st.t0 {
			return "verifikasi-ok", httpRequests() - req0
		}
		if frozen > 0 {
			for _, q := range regionOf(st.dupPages, st.pages, 0) {
				if q <= frozen {
					st.excluded[q] = true
				}
			}
		}
		reg := regionOf(st.dupPages, st.pages, frozen)
		if len(reg) == 0 {
			return "region-kosong", httpRequests() - req0
		}
		before := st.unique()
		reqRound0 := httpRequests()
		for _, p := range reg {
			if httpRequests() >= maxReq {
				return "anggaran-http", httpRequests() - req0
			}
			if err := fetchInto(sess, st, base, p); err != nil {
				logf("[%s] round %d: p%d gagal: %v", phase, round, p, err)
				continue
			}
			st.refetch[p] = true
		}
		gain := st.unique() - before
		*rounds = append(*rounds, roundStat{
			phase: phase, round: round, region: len(reg),
			before: before, after: st.unique(), gain: gain,
			reqHTTP: httpRequests(),
		})
		logf("[%s] round %d: region %s (%d hal) | unik %d→%d (+%d) | T0=%d | req round %d",
			phase, round, joinInts(reg), len(reg), before, st.unique(), gain, st.t0, httpRequests()-reqRound0)
		if gain == 0 {
			return "plateau", httpRequests() - req0
		}
	}
	return stop, httpRequests() - req0
}

func fetchInto(sess *sinta.Session, st *state, base string, page int) error {
	pr, err := sess.FetchPage(base, "", page)
	if err != nil {
		return err
	}
	record(st, page, pr)
	return nil
}

func record(st *state, page int, pr *sinta.FilterPageResult) {
	for _, j := range pr.Journals {
		if st.seen[j.ID] {
			st.dupPages[page] = true
		}
		st.seen[j.ID] = true
	}
}

func regionOf(dup map[int]bool, pages, frozen int) []int {
	set := map[int]bool{}
	for p := range dup {
		for _, q := range []int{p - 1, p, p + 1} {
			if q >= 1 && q <= pages && q > frozen {
				set[q] = true
			}
		}
	}
	return keysOf(set)
}

func keysOf(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func newSession() (*sinta.Session, error) {
	return sinta.NewSession("vdac", ua, time.Second, 3*time.Second)
}

func httpRequests() int {
	n := 0
	for _, s := range metrics.Default.Export() {
		n += s.Requests
	}
	return n
}

func readInts(path string) ([]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		n, err := strconv.Atoi(line)
		if err != nil {
			return nil, fmt.Errorf("%s: baris %q bukan angka", path, line)
		}
		out = append(out, n)
	}
	return out, sc.Err()
}

func joinInts(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
