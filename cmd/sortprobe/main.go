// sortprobe — alat verifikasi Tahap I3 (doc 24): apakah POST changesort
// pada sesi HIDUP (tanpa buat sesi baru / re-POST filter) benar-benar
// mengubah urutan halaman di server SINTA?
//
// Alur: InitFilter(rank + sort A) → GET halaman 1..N (signature ID per
// halaman) → ChangeSort(B) di sesi yang sama → GET ulang halaman 1..N →
// verdict:
//
//	BERUBAH  = server menerima perubahan sort in-place → Opsi B layak
//	           dipakai pipeline (murah: 1 POST, sesi tak terputus)
//	IDENTIK  = server mengabaikan → pipeline harus pakai Opsi A
//	           (sesi baru via SetSortKey + InitFilter ulang)
//
// Exit code: 0 = BERUBAH, 2 = IDENTIK, 1 = error.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"sinta-scraper/internal/sinta"
)

func main() {
	base := flag.String("base", "https://sinta.kemdiktisaintek.go.id/journals", "base URL SINTA Listing Jurnal")
	rank := flag.String("rank", "6", "level filter (argumen BuildFilterForm, mis. 6)")
	sortA := flag.Int("sort", 5, "sort awal saat InitFilter")
	sortB := flag.Int("alt", 4, "sort tujuan ChangeSort pada sesi hidup")
	pages := flag.Int("pages", 2, "jumlah halaman yang dibandingkan (1..)")
	ua := flag.String("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)", "header User-Agent")
	flag.Parse()

	form, err := sinta.BuildFilterForm(*rank)
	if err != nil {
		fatal("BuildFilterForm(%q): %v", *rank, err)
	}
	sess, err := sinta.NewSession("sortprobe", *ua, time.Second, 3*time.Second)
	if err != nil {
		fatal("NewSession: %v", err)
	}
	if err := sess.SetSortKey(*sortA); err != nil {
		fatal("SetSortKey: %v", err)
	}
	if err := sess.InitFilter(*base, form); err != nil {
		fatal("InitFilter: %v", err)
	}

	sigA, err := fetchSig(sess, *base, *pages)
	if err != nil {
		fatal("GET fase A (sort %d): %v", *sortA, err)
	}
	fmt.Printf("== fase A: sort %d (setelah InitFilter) ==\n%s\n", *sortA, sigA)

	if err := sess.ChangeSort(*base, *sortB); err != nil {
		fatal("ChangeSort(%d): %v", *sortB, err)
	}
	fmt.Printf("ChangeSort(%d) di sesi hidup: OK (POST diterima, sesi lanjut)\n", *sortB)

	sigB, err := fetchSig(sess, *base, *pages)
	if err != nil {
		fatal("GET fase B (sort %d): %v", *sortB, err)
	}
	fmt.Printf("== fase B: sort %d (sesi yang sama) ==\n%s\n", *sortB, sigB)

	if sigA == sigB {
		fmt.Println("VERDICT: IDENTIK — server mengabaikan changesort in-place → pakai Opsi A (sesi baru)")
		os.Exit(2)
	}
	fmt.Println("VERDICT: BERUBAH — server menerima changesort in-place → Opsi B layak (1 POST, sesi tak terputus)")
}

func fetchSig(sess *sinta.Session, base string, pages int) (string, error) {
	var b strings.Builder
	for p := 1; p <= pages; p++ {
		pr, err := sess.FetchPage(base, "", p)
		if err != nil {
			return "", fmt.Errorf("halaman %d: %w", p, err)
		}
		ids := make([]string, 0, len(pr.Journals))
		for _, j := range pr.Journals {
			ids = append(ids, fmt.Sprint(j.ID))
		}
		fmt.Fprintf(&b, "  hal %d (%d kartu): %s\n", p, len(ids), strings.Join(ids, " "))
	}
	return b.String(), nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "GAGAL: "+format+"\n", args...)
	os.Exit(1)
}
