// Program oaprobe — POA fase-1/fase-2 (doc 44 §5, approve user 29 Okt 2026):
// probe AKSES per artikel → oa_status (free|paid|restricted|unknown) dgn
// FULL RANTAI (approve user: "tes PDF = fallback terakhir"):
//
//	① GET halaman view/landing (GetPlain — final URL dipakai utk resolve
//	   link relatif; 403/404 dilaporkan apa adanya — jujur)
//	 landing balik langsung %PDF → free (terminal)
//	② pernyataan KUAT di halaman (corpus.PolicyHits — ≥10 kata; CC diabaikan
//	   — lisensi ≠ bukti akses, aturan hybrid D1) → dicatat sbg cadangan
//	③ TES PDF (bukti tertinggi — selalu dijalankan bila ada pointer):
//	   pointer PDF (meta citation_pdf_url → anchor skor → embed → param
//	   viewer) → GET → 200 + magic %PDF → FREE · HTML balasan → klasifikasi
//	   judul (bayar→paid · login→restricted) → pointer lanjutan (maks 2 hop)
//	④ bila ③ tak mungkin/tak jelas: pernyataan kuat (free/paid) → klasifikasi
//	   judul view → UNKNOWN (jujur — jangan ditebak)
//
// BUKTI LANGSUNG > PERNYATAAN: tes PDF 200+%PDF MENANG walau ada pernyataan
// lain (prinsip user: "PDF bisa diunduh = free" — validitas #1).
//
// DRY-RUN default: tulis artefak JSON (-out) + tabel review (0 tulis DB).
// -apply: baca artefak PERSIS (TANPA re-probe — pola licfix D4) → UPDATE
// idempoten HANYA bila masih 'unknown' + provenance "oa_src":"access_probe".
//
// Dua fase lewat -phase:
//
//	-phase journal (default, fase-1): 1 artikel TERBARU (year DESC, id DESC)
//	  per jurnal dgn ≥1 unknown — bukti pola murah (±2-4 GET/jurnal).
//	-phase article (fase-2 O1): SEMUA artikel unknown — wajib tes PDF tiap
//	  artikel (rantai penuh), termasuk URL doi.org (resolve → landing).
//
// Pakai (POLA LICFIX — backup manual sebelum -apply):
//
//	go run ./cmd/oaprobe -db data/stage2/sinta-r2-enrich.db             (dry-run f1)
//	go run ./cmd/oaprobe -db ... -journal 688,9155                      (smoke subset)
//	go run ./cmd/oaprobe -db ... -apply -out data/oaprobe-f1.json       (tulis f1)
//	go run ./cmd/oaprobe -db ... -phase article -out data/oaprobe-f2.json
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"

	_ "modernc.org/sqlite"
)

// target = 1 baris artikel unknown utk diprobe.
type target struct {
	id        int64
	journalID int64
	journal   string
	url       string
}

// res = hasil probe 1 artikel (isi artefak JSON — bahan review + apply).
type res struct {
	JournalID  int64    `json:"journal_id"`
	Journal    string   `json:"journal,omitempty"`
	ArticleID  int64    `json:"article_id"`
	URL        string   `json:"url"`
	FinalURL   string   `json:"final_url,omitempty"`
	ViewStatus int      `json:"view_status"`
	Tried      []string `json:"tried,omitempty"` // jejak tes pointer (via:url) — jujur
	Decided    string   `json:"oa_status"`       // free|paid|restricted|unknown
	Evidence   string   `json:"evidence,omitempty"`
	Note       string   `json:"note,omitempty"`
}

func main() {
	dbPath := flag.String("db", "data/stage2/sinta-r2-enrich.db", "path file SQLite korpus")
	phase := flag.String("phase", "journal", "fase: journal (fase-1: 1 art terbaru/jurnal) | article (fase-2 O1: semua unknown)")
	applyFlag := flag.Bool("apply", false, "TULIS ke DB dari artefak persis (default = DRY-RUN 0 tulis; backup manual dulu; TANPA re-probe)")
	jflag := flag.String("journal", "", "opsional: subset ID jurnal utk smoke (mis. 688,9155)")
	out := flag.String("out", "data/oaprobe-f1.json", "path artefak JSON hasil dry-run")
	delay := flag.Duration("delay", 700*time.Millisecond, "delay etis antar GET")
	pagu := flag.Int("pagu", 0, "pagu GET total (0 = tanpa pagu; habis → stop jujur, artefak = yg terprobe)")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("GAGAL buka DB: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("GAGAL ping DB: %v", err)
	}

	if *applyFlag {
		if err := runApply(db, *out); err != nil {
			log.Fatalf("GAGAL apply: %v", err)
		}
		return
	}

	subset, err := parseSubset(*jflag)
	if err != nil {
		log.Fatalf("GAGAL -journal: %v", err)
	}
	targets, err := loadTargets(db, *phase, subset)
	if err != nil {
		log.Fatalf("GAGAL load target: %v", err)
	}
	log.Printf("[oaprobe] phase=%s · target = %d artikel unknown (pagu=%d)", *phase, len(targets), *pagu)
	if len(targets) == 0 {
		log.Print("[oaprobe] tidak ada target — selesai (jujur)")
		return
	}

	p := &prober{c: garuda.NewClient("oaprobe",
		"sinta-scraper/1.0 (+https://github.com/gerrymoeis/sinta_scraper; polite OA access probe)",
		*delay, *delay), pagu: *pagu}

	results := make([]res, 0, len(targets))
	for i := range targets {
		if p.paguHit {
			log.Printf("[oaprobe] PAGU HABIS di %d/%d — stop jujur (sisa tak diprobe; rerun lanjut, idempoten)", i, len(targets))
			break
		}
		results = append(results, p.probe(&targets[i]))
		if (i+1)%25 == 0 {
			log.Printf("[oaprobe] … %d/%d selesai (GET %d)", i+1, len(targets), p.gets)
		}
	}

	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		log.Fatalf("GAGAL marshal artefak: %v", err)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		log.Fatalf("GAGAL tulis artefak %s: %v", *out, err)
	}
	report(results, p.gets, *out)
	log.Print("[oaprobe] DRY-RUN — 0 tulis DB. Review tabel di atas, lalu: -apply (backup manual dulu)")
}

// parseSubset = "688,9155" → map id. "" = semua (tanpa filter).
func parseSubset(s string) (map[int64]bool, error) {
	out := map[int64]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("id %q tak valid (angka positif)", p)
		}
		out[n] = true
	}
	return out, nil
}

// loadTargets = artikel unknown sesuai fase:
//
//	journal → 1 TERBARU per jurnal (year DESC, id DESC) — window function.
//	article → SEMUA unknown (fase-2 O1 — wajib tes PDF tiap artikel).
func loadTargets(db *sql.DB, phase string, subset map[int64]bool) ([]target, error) {
	var q string
	switch phase {
	case "journal":
		q = `SELECT t.id, t.journal_id, COALESCE(j.name, ''), t.article_url
		     FROM (SELECT a.*, ROW_NUMBER() OVER (PARTITION BY journal_id
		           ORDER BY COALESCE(year, 0) DESC, id DESC) rn
		           FROM articles a WHERE oa_status = 'unknown') t
		     LEFT JOIN journals j ON j.id = t.journal_id
		     WHERE t.rn = 1 ORDER BY t.journal_id`
	case "article":
		q = `SELECT a.id, a.journal_id, COALESCE(j.name, ''), a.article_url
		     FROM articles a LEFT JOIN journals j ON j.id = a.journal_id
		     WHERE a.oa_status = 'unknown' ORDER BY a.journal_id, a.id`
	default:
		return nil, fmt.Errorf("phase %q tak dikenal (journal|article)", phase)
	}
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.journalID, &t.journal, &t.url); err != nil {
			return nil, err
		}
		if len(subset) > 0 && !subset[t.journalID] {
			continue
		}
		if strings.TrimSpace(t.url) == "" {
			continue // tanpa URL = mustahil diprobe (jujur — dilewati)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// report = rekap keputusan + jejak GET + tabel bila ringkas (fase-1/smoke).
func report(results []res, gets int, outPath string) {
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Decided]++
	}
	fmt.Printf("\n══ oaprobe DRY-RUN ══\n")
	fmt.Printf("artikel: %d · GET: %d · keputusan: free=%d paid=%d restricted=%d unknown=%d\n",
		len(results), gets, counts["free"], counts["paid"], counts["restricted"], counts["unknown"])
	if len(results) <= 200 {
		fmt.Println("jID | aID | view | keputusan | bukti")
		for _, r := range results {
			ev := r.Evidence
			if ev == "" {
				ev = r.Note
			}
			fmt.Printf("j%d | %d | %d | %s | %s\n", r.JournalID, r.ArticleID, r.ViewStatus, r.Decided, trim(ev, 90))
		}
	}
	fmt.Printf("artefak: %s\n", outPath)
}

func trim(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
