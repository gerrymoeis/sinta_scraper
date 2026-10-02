// Program partition: alat Eksperimen C (doc 22) — pembuktian partition
// ranking per publisher. Untiap affiliation ID: scrape listing
// /journals/index/{id} dengan form filter akreditasi rank yang sama seperti
// listing global, ukur T0_i (count server per partisi) dan kumpulkan ID;
// di akhir cetak laporan Σ T0, union, irisan lintas-publisher, dan diff vs
// berkas ID ekspektasi. Bukan bagian dari pipeline.
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

	"sinta-scraper/internal/sinta"
)

type partResult struct {
	affID   int
	t0      int
	uniq    int
	pages   int
	intra   int // ID ganda di dalam partisi yang sama (seharusnya 0)
	errStr  string
	idCount map[int]int
}

func main() {
	affFile := flag.String("affids", "", "berkas daftar affiliation ID (satu per baris)")
	rank := flag.Int("rank", 6, "rank SINTA (1-6)")
	sortKey := flag.Int("sort", 0, "kunci urutan 1..5 (0 = default server; sort tidak relevan untuk partition karena semua halaman diambil)")
	expect := flag.String("expect", "", "berkas ID ekspektasi (satu per baris) untuk diff coverage — opsional")
	showIDs := flag.Bool("show-ids", false, "cetak daftar ID per partisi")
	basePrefix := flag.String("base-url", "https://sinta.kemdiktisaintek.go.id/journals/index/", "prefix URL partition")
	flag.Parse()

	if *affFile == "" {
		fmt.Fprintln(os.Stderr, "usage: partition -affids affids.txt [-rank 6] [-expect expected.txt]")
		os.Exit(2)
	}

	affIDs, err := readInts(*affFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baca affids: %v\n", err)
		os.Exit(1)
	}

	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)"
	form := fmt.Sprintf("filter_accreditation[%d]=%d&filter_journals=1", *rank, *rank)
	start := time.Now()
	results := make([]partResult, 0, len(affIDs))

	for i, affID := range affIDs {
		base := *basePrefix + strconv.Itoa(affID)
		res := partResult{affID: affID, idCount: map[int]int{}}
		run := func() error {
			// Sesi baru per partisi: server hanya mengirim Set-Cookie ci_session
			// saat sesi PHP dibuat; POST filter lanjutan pada sesi yang sama
			// direspons 303 tanpa cookie (ditolak initSessionLocked). Sesi segar
			// = semantik identik dengan satu run CLI per publisher, dan memastikan
			// state filter tidak terkontaminasi antar partisi.
			sess, err := sinta.NewSession("partition", ua, time.Second, 2*time.Second)
			if err != nil {
				return fmt.Errorf("NewSession: %w", err)
			}
			if *sortKey > 0 {
				if err := sess.SetSortKey(*sortKey); err != nil {
					return fmt.Errorf("SetSortKey: %w", err)
				}
			}
			if err := sess.InitFilter(base, form); err != nil {
				return fmt.Errorf("InitFilter: %w", err)
			}
			first, err := sess.FetchPage(base, "", 1)
			if err != nil {
				return fmt.Errorf("p1: %w", err)
			}
			res.t0 = first.TotalJournals
			pages := (res.t0 + 9) / 10
			if pages < 1 {
				pages = 1
			}
			if pages > 60 {
				return fmt.Errorf("T0=%d tidak wajar untuk satu publisher", res.t0)
			}
			res.pages = pages
			for p := 1; p <= pages; p++ {
				pr := first
				if p > 1 {
					if pr, err = sess.FetchPage(base, "", p); err != nil {
						return fmt.Errorf("p%d: %w", p, err)
					}
				}
				for _, j := range pr.Journals {
					res.idCount[j.ID]++
				}
			}
			return nil
		}
		if err := run(); err != nil {
			res.errStr = err.Error()
		}
		res.uniq = len(res.idCount)
		for _, c := range res.idCount {
			if c > 1 {
				res.intra++
			}
		}
		results = append(results, res)

		status := "ok"
		if res.errStr != "" {
			status = "ERR"
		} else if res.uniq != res.t0 {
			status = "DEFISIT"
		}
		fmt.Fprintf(os.Stderr, "[%d/%d] aff %d | T0=%d uniq=%d pages=%d %s\n",
			i+1, len(affIDs), affID, res.t0, res.uniq, res.pages, status)
	}

	agg := func() (sumT0, sumUniq, intraTotal, errs int) {
		for _, r := range results {
			sumT0 += r.t0
			sumUniq += r.uniq
			intraTotal += r.intra
			if r.errStr != "" {
				errs++
			}
		}
		return
	}
	sumT0, sumUniq, intraTotal, errs := agg()

	union := map[int][]int{}
	for _, r := range results {
		for id := range r.idCount {
			union[id] = append(union[id], r.affID)
		}
	}
	overlap := 0
	var overlapIDs []int
	for id, pubs := range union {
		if len(pubs) > 1 {
			overlap++
			overlapIDs = append(overlapIDs, id)
		}
	}
	sort.Ints(overlapIDs)

	fmt.Println("# partisi per publisher (affid T0 uniq pages status)")
	for _, r := range results {
		status := ""
		switch {
		case r.errStr != "":
			status = "ERR " + r.errStr
		case r.uniq != r.t0:
			status = "DEFISIT"
		case r.t0 == 0:
			status = "kosong"
		}
		fmt.Printf("%d\t%d\t%d\t%d\t%s", r.affID, r.t0, r.uniq, r.pages, status)
		if *showIDs && len(r.idCount) > 0 {
			ids := make([]int, 0, len(r.idCount))
			for id := range r.idCount {
				ids = append(ids, id)
			}
			sort.Ints(ids)
			fmt.Printf("\t%s", joinInts(ids))
		}
		fmt.Println()
	}

	fmt.Println("# ringkasan")
	fmt.Printf("partisi\t%d\n", len(results))
	fmt.Printf("sum_T0\t%d\n", sumT0)
	fmt.Printf("sum_uniq\t%d\n", sumUniq)
	fmt.Printf("union_unik\t%d\n", len(union))
	fmt.Printf("defisit_intra_total\t%d\n", sumT0-sumUniq)
	fmt.Printf("id_ganda_dalam_partisi\t%d\n", intraTotal)
	fmt.Printf("id_lintas_publisher\t%d\n", overlap)
	fmt.Printf("error_partisi\t%d\n", errs)
	if len(overlapIDs) > 0 {
		fmt.Printf("id_lintas_publisher_daftar\t%s\n", joinInts(overlapIDs))
	}

	if *expect != "" {
		exp, err := readInts(*expect)
		if err != nil {
			fmt.Fprintf(os.Stderr, "baca expect: %v\n", err)
			os.Exit(1)
		}
		expSet := map[int]bool{}
		for _, id := range exp {
			expSet[id] = true
		}
		var missing, extra []int
		for id := range expSet {
			if _, ok := union[id]; !ok {
				missing = append(missing, id)
			}
		}
		for id := range union {
			if !expSet[id] {
				extra = append(extra, id)
			}
		}
		sort.Ints(missing)
		sort.Ints(extra)
		fmt.Println("# diff vs ekspektasi")
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
	fmt.Printf("durasi\t%s\n", time.Since(start).Round(time.Second))
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
