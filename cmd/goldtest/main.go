package main

// ==== cmd/goldtest — tool review korpus (doc 43 §5.6 + §6.0; fase C5/C6) ===
//
// Terpisah dari pipeline (pola cmd/probep4): baca DB + snapshot IDF lokal
// (0 GET) →
//   - `-audit <ids>` : tabel audit per-baris articles (C6 — verifikasi manual
//     hasil smoke: abstrak per bahasa, lisensi, keyword, complete);
//   - gold-test      : simulasi TF-IDF vs keyword author → P@8/recall/F1
//     per bahasa (fuzzy + blok STRICT O-A) + diagnosis miss per-item
//     (bentuk/stopword/vocab/peringkat) → user review lolos/tidak sebelum
//     roll-out penuh.
//
// Pakai:
//	go run ./cmd/goldtest -db data/stage2/sinta-r2-enrich.db
//	go run ./cmd/goldtest -db ... -audit 673,689
//	go run ./cmd/goldtest -db ... -miss-all > gold-miss.txt   (G13: semua baris miss)

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"sinta-scraper/internal/corpus"
	"sinta-scraper/internal/storage"
)

func main() {
	dbPath := flag.String("db", "data/stage2/sinta-r2-enrich.db", "path file SQLite korpus")
	keywordsDir := flag.String("keywords", corpus.DefaultKeywordsDir, "folder snapshot IDF (idf-v{N}.json)")
	audit := flag.String("audit", "", "opsional: daftar ID jurnal (mis. 673,689) — cetak tabel audit baris articles sebelum gold-test")
	missAll := flag.Bool("miss-all", false, "cetak SEMUA baris miss diagnosis (default: ringkas — cap 10/kategori, G13)")
	flag.Parse()

	st, err := storage.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GAGAL buka DB: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	// ── Audit baris (opsional, selalu jalan bila diminta — gold-test gagal
	// pun audit tetap tercetak utk diagnosis) ──
	if strings.TrimSpace(*audit) != "" {
		ids, err := parseAuditIDs(*audit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "GAGAL -audit: %v\n", err)
			os.Exit(1)
		}
		rows, err := corpus.CorpusAudit(st, ids)
		if err != nil {
			fmt.Fprintf(os.Stderr, "GAGAL audit: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(rows.String())
	}

	rep, err := corpus.RunGoldTest(st, *keywordsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GAGAL gold-test: %v\n", err)
		os.Exit(1)
	}
	if *missAll {
		fmt.Print(rep.StringAll()) // penuh — utk redirect ke file (G13)
	} else {
		fmt.Print(rep.String())
	}
}

// parseAuditIDs = "673,689" → []int64 (angka positif; whitespace aman).
func parseAuditIDs(s string) ([]int64, error) {
	var ids []int64
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%q bukan ID positif (pakai daftar koma, mis. 673,689)", p)
		}
		ids = append(ids, n)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("tanpa ID valid")
	}
	return ids, nil
}
