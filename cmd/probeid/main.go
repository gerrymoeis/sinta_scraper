// Program probeid: scan serial seluruh halaman satu rank via parser & sesi
// resmi internal/sinta, cetak union ID terurut ke stdout (progress ke stderr).
// Dipakai sebagai alat diagnosis doc 21 (identifikasi ID yang tak tertangkap
// scrape karena drift paginasi). Bukan bagian dari pipeline.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"sinta-scraper/internal/sinta"
)

func main() {
	rank := flag.Int("rank", 6, "rank SINTA (1-6)")
	pages := flag.Int("pages", 31, "jumlah halaman listing")
	base := flag.String("base-url", "https://sinta.kemdiktisaintek.go.id/journals", "URL listing")
	flag.Parse()

	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)"
	sess, err := sinta.NewSession("sinta", ua, time.Second, 3*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewSession: %v\n", err)
		os.Exit(1)
	}
	if err := sess.SetSortKey(4); err != nil {
		fmt.Fprintf(os.Stderr, "SetSortKey: %v\n", err)
		os.Exit(1)
	}
	form := fmt.Sprintf("filter_accreditation[%d]=%d&filter_journals=1", *rank, *rank)
	if err := sess.InitFilter(*base, form); err != nil {
		fmt.Fprintf(os.Stderr, "InitFilter: %v\n", err)
		os.Exit(1)
	}

	seen := map[int]int{}
	for p := 1; p <= *pages; p++ {
		res, err := sess.FetchPage(*base, "", p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERR p%d: %v\n", p, err)
			continue
		}
		for _, j := range res.Journals {
			if _, ok := seen[j.ID]; !ok {
				seen[j.ID] = p
			}
		}
		fmt.Fprintf(os.Stderr, "p%d: %d kartu | kumulatif unik %d | T0=%d\n", p, len(res.Journals), len(seen), res.TotalJournals)
	}

	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	fmt.Fprintf(os.Stderr, "UNION: %d\n", len(ids))
	for _, id := range ids {
		fmt.Println(id)
	}
}
