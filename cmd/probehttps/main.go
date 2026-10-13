// Program probehttps: alat diagnosis umum — GET tunggal per URL (argumen CLI)
// via garuda.Client.GetPlain (jalur representatif pipeline: redirect otomatis,
// delay 1–3s, timeout internal client). Cetak status/bytes/final/err per URL.
// Pola probep1 (alat sekali pakai diagnosis); BUKAN bagian pipeline (-stages).
package main

import (
	"fmt"
	"os"
	"time"

	"sinta-scraper/internal/garuda"
)

func main() {
	urls := os.Args[1:]
	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "pakai: go run ./cmd/probehttps <url> [url...]")
		os.Exit(2)
	}
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)"
	c := garuda.NewClient("probehttps", ua, time.Second, 3*time.Second)

	fmt.Println("status|bytes|redirect|error|url|final")
	for _, u := range urls {
		body, st, final, err := c.GetPlain(u, "")
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		fmt.Printf("%d|%d|%v|%s|%s|%s\n", st, len(body), final != "" && final != u, errStr, u, final)
	}
}
