package main

// ==== attrfix — backfill 2 atribut C7 (doc 43 §6.x.8 tahap D2) ====
//
// Fase saat ini: -phase backfill — isi kolom BARU (oa_status/license_type)
// DARI BUKTI YANG SUDAH ADA (0 GET, 0 sumber baru):
//   license_type ← license_url (7.984 baris URL CC → "CC BY 4.0" dst)
//   oa_status    ← provenance.embargo == "embargo" → restricted (64 baris)
// Baris tanpa bukti DITINGGALKAN 'unknown' (jujur — probe D3 menyusul).
// Fase c7b (D4) menyusul — UPDATE legacy 812 + kolom baru dari artefak D3.
//
// Default = DRY-RUN (lapor rencana, 0 tulis). -apply = tulis, DENGAN syarat
// file backup .preC7.bak.db sudah ada (plan D2: 0 tulis tanpa backup).
// Idempoten: hanya baris yg masih 'unknown' yang disentuh → rerun = 0 update.

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"sinta-scraper/internal/corpus"
	"sinta-scraper/internal/storage"

	_ "modernc.org/sqlite"
)

var (
	dbPath  = flag.String("db", "data/stage2/sinta-r2-enrich.db", "path DB korpus")
	phase   = flag.String("phase", "backfill", "fase: backfill (D2) | c7b (D4 — menyusul)")
	apply   = flag.Bool("apply", false, "tulis ke DB (default = dry-run)")
	backupP = flag.String("backup", "", "path backup wajib utk -apply (default: <db>.preC7.bak.db)")
)

func main() {
	flag.Parse()
	if *phase != "backfill" {
		fmt.Fprintf(os.Stderr, "phase %q belum tersedia — fase c7b = tahap D4 (menyusul)\n", *phase)
		os.Exit(2)
	}

	// Migrasi skema lewat SATU PINTU storage.Open (CREATE IF NOT EXISTS +
	// ALTER idempotent addC7AttrColumns) — tanpa duplikasi aturan ALTER di sini.
	st, err := storage.Open(*dbPath)
	if err != nil {
		fatal("buka DB (migrasi C7): %v", err)
	}
	if err := st.Close(); err != nil {
		fatal("tutup DB: %v", err)
	}

	// Syarat tulis: backup pra-C7 harus sudah ada (restore = salin balik).
	bpath := *backupP
	if bpath == "" {
		bpath = strings.TrimSuffix(*dbPath, ".db") + ".preC7.bak.db"
	}
	if *apply {
		if _, err := os.Stat(bpath); err != nil {
			fatal("-apply butuh backup %q (buat dulu: sqlite3 %q \".backup '%s'\")", bpath, *dbPath, bpath)
		}
	}

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		fatal("sql.Open: %v", err)
	}
	defer db.Close()

	type art struct {
		id          int64
		licenseURL  string
		provenance  string
		oa, licType string
		oaTarget    string
		licTypeTgt  string
	}
	rows, err := db.Query(`
		SELECT id, COALESCE(license_url, ''), provenance, oa_status, license_type
		FROM articles ORDER BY id`)
	if err != nil {
		fatal("baca articles: %v", err)
	}
	var arts []art
	for rows.Next() {
		var a art
		if err := rows.Scan(&a.id, &a.licenseURL, &a.provenance, &a.oa, &a.licType); err != nil {
			rows.Close()
			fatal("scan: %v", err)
		}
		arts = append(arts, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		fatal("iterasi: %v", err)
	}

	// ==== Perencanaan (deterministik, 0 GET) ====
	nLIC, nOA, nSkipLIC, nSkipOA := 0, 0, 0, 0
	for i := range arts {
		a := &arts[i]
		// license_type ← URL CC (hanya bila belum terisi — tak menimpa D4)
		if a.licType == storage.LicenseTypeUnknown {
			if t := corpus.LicenseTypeFromURL(a.licenseURL); t != "" {
				if !storage.ValidLicenseType(t) {
					fatal("id %d: hasil parser %q lolos whitelist?!", a.id, t)
				}
				a.licTypeTgt, nLIC = t, nLIC+1
			}
		} else {
			nSkipLIC++
		}
		// oa_status ← bukti embargo di provenance (JSON — bukan LIKE)
		if a.oa == storage.OAUnknown {
			if embargo(a.provenance) {
				a.oaTarget, nOA = storage.OARestricted, nOA+1
			}
		} else {
			nSkipOA++
		}
	}

	mode := "DRY-RUN (0 tulis)"
	if *apply {
		mode = "APPLY (tulis)"
	}
	fmt.Printf("=== attrfix phase=%s [%s] — %d baris dibaca ===\n", *phase, mode, len(arts))
	fmt.Printf("rencana UPDATE: license_type +%d (URL CC → tipe kanonik) · oa_status +%d (embargo → restricted)\n", nLIC, nOA)
	fmt.Printf("dilewati (sudah terisi): license_type %d · oa_status %d\n", nSkipLIC, nSkipOA)

	// Contoh baris terencana (bahan review user) — per kategori.
	showSamples := func(judul string, want func(a art) (string, bool)) {
		fmt.Println(judul)
		w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		n := 0
		for _, a := range arts {
			s, ok := want(a)
			if !ok {
				continue
			}
			fmt.Fprintf(w, "  %d\t%s\n", a.id, s)
			n++
			if n >= 5 {
				break
			}
		}
		_ = w.Flush()
	}
	showSamples("contoh license_type (LIC-URL → TYPE):", func(a art) (string, bool) {
		if a.licTypeTgt == "" {
			return "", false
		}
		return trim(a.licenseURL, 50) + " → " + a.licTypeTgt, true
	})
	showSamples("contoh oa_status (BUKTI → OA):", func(a art) (string, bool) {
		if a.oaTarget == "" {
			return "", false
		}
		return `provenance embargo:"embargo"` + " → " + a.oaTarget, true
	})

	if !*apply {
		fmt.Println("→ dry-run: jalankan lagi dgn -apply utk menulis (backup wajib).")
		return
	}

	// ==== Tulis (idempoten: WHERE target masih 'unknown') ====
	tx, err := db.Begin()
	if err != nil {
		fatal("begin: %v", err)
	}
	wLIC, wOA := 0, 0
	for _, a := range arts {
		if a.licTypeTgt != "" {
			res, err := tx.Exec(`UPDATE articles SET license_type = ? WHERE id = ? AND license_type = ?`,
				a.licTypeTgt, a.id, storage.LicenseTypeUnknown)
			if err != nil {
				fatal("update lic id %d: %v", a.id, err)
			}
			if n, _ := res.RowsAffected(); n == 1 {
				wLIC++
			}
		}
		if a.oaTarget != "" {
			res, err := tx.Exec(`UPDATE articles SET oa_status = ? WHERE id = ? AND oa_status = ?`,
				a.oaTarget, a.id, storage.OAUnknown)
			if err != nil {
				fatal("update oa id %d: %v", a.id, err)
			}
			if n, _ := res.RowsAffected(); n == 1 {
				wOA++
			}
		}
	}
	if err := tx.Commit(); err != nil {
		fatal("commit: %v", err)
	}
	fmt.Printf("\nTERTULIS: license_type +%d · oa_status +%d\n", wLIC, wOA)

	// ==== Verifikasi pasca-tulis ====
	distrib(db, "license_type")
	distrib(db, "oa_status")

	// Whitelist: 0 baris di luar nilai sah.
	var viol int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM articles
		WHERE oa_status NOT IN ('free','paid','restricted','unknown')
		   OR (license_type NOT IN ('unknown','all_rights_reserved')
		       AND license_type NOT LIKE 'CC BY%'
		       AND license_type <> 'CC0')`).Scan(&viol); err != nil {
		fatal("cek whitelist: %v", err)
	}
	fmt.Printf("whitelist violation = %d (mau 0)\n", viol)
	if viol != 0 {
		os.Exit(1)
	}

	// Idempoten: hitung ulang sisa bukti yg belum terisi — harus 0/0
	// (rerun dengan -apply = no-op karena WHERE 'unknown' tak match lagi).
	var relic, reoa int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM articles
		WHERE license_type = 'unknown' AND license_url LIKE '%creativecommons.org%'`,
	).Scan(&relic); err != nil {
		fatal("cek idempoten lic: %v", err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM articles
		WHERE oa_status = 'unknown' AND provenance LIKE '%"embargo":"embargo"%'`,
	).Scan(&reoa); err != nil {
		fatal("cek idempoten oa: %v", err)
	}
	fmt.Printf("sisa idempoten (mau 0/0): URL-CC masih unknown=%d · embargo masih unknown=%d\n", relic, reoa)
	if relic != 0 || reoa != 0 {
		os.Exit(1)
	}
}

// embargo = provenance JSON memuat nilai "embargo":"embargo" (bukti Crossref
// license start > now — bukan sekadar ada kuncinya).
func embargo(prov string) bool {
	var m map[string]any
	if err := json.Unmarshal([]byte(prov), &m); err != nil {
		return false // provenance rusak → jangan jadi dasar (jujur)
	}
	s, _ := m["embargo"].(string)
	return s == "embargo"
}

// distrib = ringkasan sebaran nilai suatu kolom (utk laporan/verifikasi).
func distrib(db *sql.DB, col string) {
	rows, err := db.Query(`SELECT ` + col + `, COUNT(*) FROM articles GROUP BY ` + col + ` ORDER BY COUNT(*) DESC`)
	if err != nil {
		fatal("distrib %s: %v", col, err)
	}
	defer rows.Close()
	fmt.Printf("sebaran %s:\n", col)
	for rows.Next() {
		var v string
		var n int
		if err := rows.Scan(&v, &n); err != nil {
			fatal("scan: %v", err)
		}
		fmt.Printf("  %-20s %6d\n", v, n)
	}
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "attrfix: "+format+"\n", args...)
	os.Exit(1)
}
