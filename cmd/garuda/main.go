// Program cmd/garuda: driver eksperimen Tahap 2 Garuda (doc 30 Bagian 13 §13.4).
//
// Mode:
//
//	-probe : probe 10 E-ISSN campuran (pembuka E1) — read-only, tanpa tulis DB,
//	         HTML disimpan sbg fixture (§13.5). Q3 langkah 3.
//	-subjects : harvest subject 261 → build subject_map + harmonisasi (Q3 langkah 4–5).
//	-view : probe halaman view/N campuran (Q4 E3) — fixture view-{id}.html, read-only.
//	-view-fill : isi garuda_home_url/garuda_oai_url dari fixture view (E3 Opsi A).
//	-hitrate : E4a — hit-rate & ladder MATCH dari fixture (nol request), read-only.
//	-e4b : E4b — year-range check duplikat + search alt + view resolve + TULIS (approve 4 Okt).
//	-e5 : E5 — probe OAI Identify + platform home 20 jurnal external (pagu 40 GET),
//	      tanpa tulis db → e5-results.json (approve 4 Okt).
//	-e6 : E6 — coverage Crossref & DOAJ 24 ISSN stratified (18 S1 + 3 S2 + 3 S3),
//	      pagu 50 GET, tanpa tulis db → e6-results.json (opsi A approve 4 Okt).
//	-e7a : E7a — dry-run aturan merge 4 lapis 10 jurnal (doc 30 §14.6),
//	      read-only (nol GET, nol tulis db) → e7a-dryrun.json utk review aturan.
//	-e7b : E7b — sinkronisasi journals: fill subject_area dari canonical +
//	      overwrite garuda_url (backup .preE7 + provenance + verifikasi K1)
//	      → e7b-results.json (approve 6 Okt).
//	-wafsmoke : validasi integrasi hybrid (doc 35) — 1 GET penuh via Client.Get.
//	-otismoke : probe A solver murni-Go (approve 4 Okt) — solve challenge via
//	      browser bawaan-OS (chromedp) + 1 GET konfirmasi tls-client.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"
	"sinta-scraper/internal/storage"

	_ "modernc.org/sqlite"
)

const uaDefault = "sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)"

func main() {
	dbPath := flag.String("db", "data/stage2/sinta-r2-enrich.db", "path db kerja Tahap 2 (db sumber baca = sinta-r1-source.db, di luar proses)")
	fixtures := flag.String("fixtures", "data/stage2/fixtures", "dir simpan HTML mentah")
	probe := flag.Bool("probe", false, "probe 10 E-ISSN campuran (E1-subset), read-only")
	subjects := flag.Bool("subjects", false, "harvest subject + build subject_map + harmonisasi")
	view := flag.Bool("view", false, "probe halaman view/N campuran (E3) — simpan fixture, read-only")
	viewFill := flag.Bool("view-fill", false, "isi garuda_home_url/garuda_oai_url dari fixture view (E3 Opsi A) — tulis 2 kolom saja")
	hitrate := flag.Bool("hitrate", false, "E4a: hit-rate & ladder Match dari fixture (nol request) — read-only, laporan + JSON")
	e4b := flag.Bool("e4b", false, "E4b: year-range check duplikat + search alt + view resolve + tulis (pagu 90 GET, approve 4 Okt)")
	e5 := flag.Bool("e5", false, "E5: probe OAI Identify + platform home 20 jurnal external (pagu 40 GET) — tanpa tulis db")
	e6 := flag.Bool("e6", false, "E6: coverage Crossref & DOAJ 24 ISSN (18 S1 + 3 S2 + 3 S3, opsi A) — pagu 50 GET, tanpa tulis db (approve 4 Okt)")
	e6DB2 := flag.String("e6-db2", "data/stage1/vdac-l1-r23-kumulatif.db", "db sumber sampel silang-rank S2/S3 utk E6 (read-only)")
	wafsmoke := flag.String("wafsmoke", "", "validasi integrasi hybrid (doc 35): GET 1 URL penuh lewat Client.Get — jalur std + fallback 403")
	otismoke := flag.String("otismoke", "", "probe A solver murni-Go: solve challenge via browser bawaan-OS + 1 GET konfirmasi tls-client")
	e7a := flag.Bool("e7a", false, "E7a: dry-run aturan merge 4 lapis 10 jurnal (doc 30 §14.6) - read-only, nol GET, nol tulis db")
	e7b := flag.Bool("e7b", false, "E7b: sinkronisasi journals (fill subject 129 + overwrite garuda_url 9, backup+provenance+verifikasi) - TULIS, nol GET")
	refresh := flag.Bool("refresh", false, "abaikan resume — ulang semua request")
	delayMin := flag.Duration("delay-min", time.Second, "jeda acak minimum antar request")
	delayMax := flag.Duration("delay-max", 2*time.Second, "jeda acak maksimum antar request")
	flag.Parse()

	if *probe {
		if err := runProbe(*dbPath, *fixtures, *delayMin, *delayMax); err != nil {
			fmt.Fprintf(os.Stderr, "probe GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *subjects {
		if err := runSubjects(*dbPath, *fixtures, *refresh, *delayMin, *delayMax); err != nil {
			fmt.Fprintf(os.Stderr, "subjects GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *view {
		if err := runView(*dbPath, *fixtures, *delayMin, *delayMax); err != nil {
			fmt.Fprintf(os.Stderr, "view GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *viewFill {
		if err := runViewFill(*dbPath, *fixtures); err != nil {
			fmt.Fprintf(os.Stderr, "view-fill GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *hitrate {
		if err := runHitrate(*dbPath, *fixtures); err != nil {
			fmt.Fprintf(os.Stderr, "hitrate GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *e4b {
		if err := runE4b(*dbPath, *fixtures, *refresh, *delayMin, *delayMax); err != nil {
			fmt.Fprintf(os.Stderr, "e4b GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *e5 {
		if err := runE5(*dbPath, *fixtures, *refresh, *delayMin, *delayMax); err != nil {
			fmt.Fprintf(os.Stderr, "e5 GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *e6 {
		if err := runE6(*dbPath, *e6DB2, *fixtures, *refresh, *delayMin, *delayMax); err != nil {
			fmt.Fprintf(os.Stderr, "e6 GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *e7a {
		if err := runE7A(*dbPath, "data/stage2/e6-results.json", "data/stage2/e7a-dryrun.json"); err != nil {
			fmt.Fprintf(os.Stderr, "e7a GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *e7b {
		if err := runE7B(*dbPath, "data/stage2/e7b-results.json"); err != nil {
			fmt.Fprintf(os.Stderr, "e7b GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *wafsmoke != "" {
		if err := runWAFSmoke(*wafsmoke, *delayMin, *delayMax); err != nil {
			fmt.Fprintf(os.Stderr, "wafsmoke GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *otismoke != "" {
		if err := runOTISMoke(*otismoke); err != nil {
			fmt.Fprintf(os.Stderr, "otismoke GAGAL: %v\n", err)
			os.Exit(1)
		}
		return
	}
	flag.Usage()
}

// runWAFSmoke = validasi integrasi hybrid (doc 35): satu GET penuh lewat
// Client.Get — jalur std (UA riset); bila 403 → fallback tls-client/solver
// otomatis. End-to-end tanpa menyentuh db.
func runWAFSmoke(rawURL string, delayMin, delayMax time.Duration) error {
	c := garuda.NewClient("wafsmoke", uaDefault, delayMin, delayMax)
	start := time.Now()
	body, status, err := c.Get(rawURL, garuda.DefaultReferer)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return fmt.Errorf("GET %s: status=%d err=%w", rawURL, status, err)
	}
	fmt.Printf("wafsmoke OK | %s | http=%d | %d B | %d ms | solves=%d\n",
		rawURL, status, len(body), elapsed, c.Solves())
	return nil
}

// runOTISMoke = probe A (approve 4 Okt 2026): BrowserSolve (chromedp + browser
// bawaan-OS, flag mirror nodriver) → bila solve OK, 1 GET konfirmasi
// tls-client dgn cookie+UA (bukti == doc 34 §6b baris 8/9). GET dipakai:
// 1 navigasi (reload pasca-solve ikut) + 1 konfirmasi = ≤3.
func runOTISMoke(rawURL string) error {
	exe, err := garuda.FindBrowser()
	if err != nil {
		return err
	}
	fmt.Printf("otismoke browser | %s\n", exe)
	t0 := time.Now()
	cookie, ua, err := garuda.BrowserSolve(rawURL)
	solveMS := time.Since(t0).Milliseconds()
	if err != nil {
		return fmt.Errorf("solve: %w", err)
	}
	fmt.Printf("otismoke solve OK | %d ms | cookie=%d B | ua=%.90s\n", solveMS, len(cookie), ua)
	t1 := time.Now()
	status, body, err := garuda.WAFConfirm(rawURL, cookie, ua)
	if err != nil {
		return fmt.Errorf("konfirmasi: %w", err)
	}
	fmt.Printf("otismoke confirm | http=%d | %d B | %d ms\n", status, len(body), time.Since(t1).Milliseconds())
	if status != 200 {
		return fmt.Errorf("konfirmasi http=%d (bukan 200)", status)
	}
	return nil
}

// runSubjects = Q3 langkah 4–5: harvest 261 → build subject_map → harmonisasi.
func runSubjects(dbPath, fixturesDir string, refresh bool, delayMin, delayMax time.Duration) error {
	store, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	targets, err := store.HarvestTargets()
	if err != nil {
		return err
	}
	c := garuda.NewClient("garuda", uaDefault, delayMin, delayMax)

	fmt.Printf("== Harvest search (target: %d jurnal, resume=%v) ==\n", len(targets), !refresh)
	hrep, err := garuda.HarvestSubjects(store, c, targets, garuda.HarvestOptions{
		FixturesDir: fixturesDir,
		Refresh:     refresh,
		OnProgress: func(done, total int, t storage.HarvestTarget, status string) {
			if status == "skip" {
				return
			}
			fmt.Printf("[%d/%d] %-12s EISSN=%s %s\n", done, total, status, t.EISSN, t.Name)
		},
	})
	if err != nil {
		return err
	}
	fmt.Printf("harvest: matched=%d not_found=%d ambiguous=%d skip(resume)=%d error=%d\n",
		hrep.Matched, hrep.NotFound, hrep.Ambiguous, hrep.Skipped, hrep.Errors)

	fmt.Println("\n== Build subject_map + harmonisasi ==")
	mrep, err := garuda.BuildDanHarmonisasi(store, c, fixturesDir)
	if err != nil {
		return err
	}
	fmt.Printf("vocab: sinta=%d token, garuda=%d label (GET /area=%v)\n",
		mrep.SintaVocab, mrep.GarudaVocab, mrep.FetchedArea)
	if len(mrep.UnknownLabels) > 0 {
		fmt.Printf("label capture di luar /area (dipakai + dicatat): %v\n", mrep.UnknownLabels)
	}
	fmt.Printf("subject_map: %d baris\n", mrep.MapRows)
	fmt.Printf("canonical: terisi=%d NO_SUBJECT=%d LOW_EVIDENCE=%d\n",
		mrep.Filled, mrep.NoSubject, mrep.LowEvidence)

	// distribusi method + merge tanpa bukti (audit K2)
	rows, err := store.SubjectMapSnapshot()
	if err != nil {
		return err
	}
	dist := map[string]int{}
	var support0 int
	for _, r := range rows {
		dist[r.Method]++
		if r.Method != "identity" && r.Support == 0 {
			support0++
		}
	}
	fmt.Printf("method: exact=%d contain=%d prefix=%d identity=%d | merge support=0: %d\n",
		dist["exact"], dist["contain"], dist["prefix"], dist["identity"], support0)
	return nil
}

// ---------- Q4 E3 (doc 30 Bagian 14 §14.2) ----------

// viewKasus = satu URL halaman view/N utk probe E3.
type viewKasus struct {
	label string
	url   string
}

var reViewPath = regexp.MustCompile(`/journal/view/(\d+)`)

// reFixtureView mencocokkan nama file fixture view-{id}.html (bukan URL).
var reFixtureView = regexp.MustCompile(`^view-(\d+)\.html$`)

// idDariURL mengekstrak garuda_id dari URL /journal/view/N.
func idDariURL(u string) (int, error) {
	m := reViewPath.FindStringSubmatch(u)
	if m == nil {
		return 0, fmt.Errorf("bukan URL view/N: %s", u)
	}
	return strconv.Atoi(m[1])
}

// kumpulView menjalankan satu query sampling (URL kosong di-skip) utk daftar E3.
func kumpulView(db *sql.DB, query, label string, daftar *[]viewKasus) error {
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("query %s: %w", label, err)
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return fmt.Errorf("scan %s: %w", label, err)
		}
		if u != "" {
			*daftar = append(*daftar, viewKasus{label: label, url: u})
		}
	}
	return rows.Err()
}

// idDariFixtureSearch mengambil garuda_id kandidat PERTAMA dari fixture
// search jurnal (ambiguous tak menyimpan garuda_id di DB — kandidatnya tetap
// ada offline di fixture hasil Q3, nol request).
func idDariFixtureSearch(fixturesDir string, journalID int64) (int, error) {
	matches, err := filepath.Glob(filepath.Join(fixturesDir, fmt.Sprintf("search-j%d-*.html", journalID)))
	if err != nil || len(matches) == 0 {
		return 0, fmt.Errorf("fixture search j%d tak ada", journalID)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		return 0, err
	}
	page, err := garuda.ParseSearchPage(strings.NewReader(string(b)))
	if err != nil {
		return 0, fmt.Errorf("parse fixture j%d: %w", journalID, err)
	}
	if len(page.Rows) == 0 {
		return 0, fmt.Errorf("fixture j%d tanpa baris", journalID)
	}
	return int(page.Rows[0].GarudaID), nil
}

// runView = Q4 E3: probe halaman /journal/view/N pada sampel campuran
// (matched 6 + ambiguous 3 + not_found dgn garuda_url Tahap 1 5 + 4 URL dari
// 2 kasus garuda_url beda) → fixture view-{id}.html utk pemetaan field
// secara offline. Read-only terhadap db (mode=ro); resume bila file ada.
func runView(dbPath, fixturesDir string, delayMin, delayMax time.Duration) error {
	uri := "file:" + filepath.ToSlash(dbPath) + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return fmt.Errorf("buka db: %w", err)
	}
	defer db.Close()

	var daftar []viewKasus
	queries := []struct{ label, q string }{
		{"matched", `SELECT IFNULL(garuda_url,'') FROM journal_enrichment
			WHERE match_status='matched' AND garuda_id IS NOT NULL AND COALESCE(garuda_url,'')<>''
			ORDER BY journal_id LIMIT 6`},
		{"notfound", `SELECT IFNULL(j.garuda_url,'') FROM journals j
			JOIN journal_enrichment e ON e.journal_id=j.id
			WHERE e.match_status='not_found' AND j.garuda_url LIKE '%/journal/view/%'
			ORDER BY j.id LIMIT 5`},
		{"beda-lama", `SELECT IFNULL(j.garuda_url,'') FROM journals j
			JOIN journal_enrichment e ON e.journal_id=j.id
			WHERE e.match_status='matched' AND TRIM(IFNULL(j.garuda_url,''))<>''
			AND TRIM(IFNULL(j.garuda_url,''))<>TRIM(IFNULL(e.garuda_url,''))
			ORDER BY j.id`},
		{"beda-baru", `SELECT IFNULL(e.garuda_url,'') FROM journals j
			JOIN journal_enrichment e ON e.journal_id=j.id
			WHERE e.match_status='matched' AND TRIM(IFNULL(j.garuda_url,''))<>''
			AND TRIM(IFNULL(j.garuda_url,''))<>TRIM(IFNULL(e.garuda_url,''))
			ORDER BY e.journal_id`},
	}
	for _, qq := range queries {
		if err := kumpulView(db, qq.q, qq.label, &daftar); err != nil {
			return err
		}
	}

	// ambiguous: garuda_id/garuda_url NULL di DB (jujur — tak milih salah satu),
	// ambil kandidat pertama dari fixture search (offline, nol request).
	ambRows, err := db.Query(`SELECT journal_id FROM journal_enrichment
		WHERE match_status='ambiguous' ORDER BY journal_id LIMIT 3`)
	if err != nil {
		return fmt.Errorf("query ambiguous: %w", err)
	}
	for ambRows.Next() {
		var jid int64
		if err := ambRows.Scan(&jid); err != nil {
			ambRows.Close()
			return err
		}
		id, err := idDariFixtureSearch(fixturesDir, jid)
		if err != nil {
			fmt.Printf("ambiguous j%d dilewati: %v\n", jid, err)
			continue
		}
		daftar = append(daftar, viewKasus{label: "ambiguous", url: garuda.ViewURL(id)})
	}
	if err := ambRows.Err(); err != nil {
		ambRows.Close()
		return err
	}
	ambRows.Close()

	// dedup per garuda_id (label pertama menang)
	unik := map[int]string{}
	var urut []int
	for _, k := range daftar {
		id, err := idDariURL(k.url)
		if err != nil {
			fmt.Printf("lewati URL aneh (%s): %v\n", k.label, err)
			continue
		}
		if _, ada := unik[id]; !ada {
			unik[id] = k.label
			urut = append(urut, id)
		}
	}
	if len(urut) == 0 {
		return fmt.Errorf("sampel kosong — cek db %s", dbPath)
	}
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		return fmt.Errorf("buat dir fixture: %w", err)
	}

	fmt.Printf("== E3 probe view/N (sampel: %d URL unik, resume bila file ada) ==\n", len(urut))
	c := garuda.NewClient("garuda-view", uaDefault, delayMin, delayMax)
	var sukses, skip, gagal int
	for i, id := range urut {
		label := unik[id]
		path := filepath.Join(fixturesDir, fmt.Sprintf("view-%d.html", id))
		if _, err := os.Stat(path); err == nil {
			skip++
			fmt.Printf("[%d/%d] view-%d %-12s SKIP (sudah ada)\n", i+1, len(urut), id, label)
			continue
		}
		t0 := time.Now()
		body, status, err := c.Get(garuda.ViewURL(id), garuda.DefaultReferer)
		ms := time.Since(t0).Milliseconds()
		if err != nil || body == nil {
			gagal++
			fmt.Printf("[%d/%d] view-%d %-12s GAGAL http=%d %v\n", i+1, len(urut), id, label, status, err)
			continue
		}
		if werr := os.WriteFile(path, body, 0o644); werr != nil {
			return fmt.Errorf("tulis fixture view-%d: %w", id, werr)
		}
		sukses++
		fmt.Printf("[%d/%d] view-%d %-12s http=%d %dB %dms\n", i+1, len(urut), id, label, status, len(body), ms)
	}
	fmt.Printf("E3 selesai: sukses=%d skip=%d gagal=%d → fixture di %s\n", sukses, skip, gagal, fixturesDir)
	if sukses == 0 && skip == 0 {
		return fmt.Errorf("tidak ada halaman view yang berhasil diunduh")
	}
	return nil
}

// runViewFill = E3 Opsi A (doc 31 §4, disetujui 4 Okt 2026): parse semua
// fixture view-*.html → isi garuda_home_url & garuda_oai_url di
// journal_enrichment. Resolve garuda_id → journal_id offline (nol request):
// (1) garuda_url enrichment (matched — id Q3 utk kasus beda menang),
// (2) garuda_url journals utk not_found, (3) kandidat pertama fixture search
// utk ambiguous. Hanya 2 kolom view yang ditulis; id Record-Not-Found / tanpa
// link dilewati tanpa tulis.
func runViewFill(dbPath, fixturesDir string) error {
	store, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	refs, err := store.ViewLinkRefs()
	if err != nil {
		return err
	}
	byID := map[int]int64{}
	for _, r := range refs {
		if _, ada := byID[r.GarudaID]; !ada {
			byID[r.GarudaID] = r.JournalID
		}
	}
	ambIDs, err := store.AmbiguousJournalIDs()
	if err != nil {
		return err
	}
	for _, jid := range ambIDs {
		id, err := idDariFixtureSearch(fixturesDir, jid)
		if err != nil {
			fmt.Printf("ambiguous j%d dilewati: %v\n", jid, err)
			continue
		}
		if _, ada := byID[id]; !ada {
			byID[id] = jid
		}
	}

	matches, err := filepath.Glob(filepath.Join(fixturesDir, "view-*.html"))
	if err != nil {
		return fmt.Errorf("glob fixture view: %w", err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("tidak ada fixture view-*.html di %s", fixturesDir)
	}

	fmt.Printf("== E3 view-fill: %d fixture, %d resolve id → journal ==\n", len(matches), len(byID))
	var isi, tanpaResolve, tanpaLink, errCount int
	for _, path := range matches {
		m := reFixtureView.FindStringSubmatch(filepath.Base(path))
		if m == nil {
			continue
		}
		id, _ := strconv.Atoi(m[1])
		jid, ada := byID[id]
		if !ada {
			tanpaResolve++
			fmt.Printf("view-%d: TANPA RESOLVE (id lama ≠ pilihan Q3 — dilewati)\n", id)
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			errCount++
			fmt.Printf("view-%d: baca GAGAL: %v\n", id, err)
			continue
		}
		info, err := garuda.ParseViewPage(bytes.NewReader(b))
		if err != nil {
			errCount++
			fmt.Printf("view-%d: parse GAGAL: %v\n", id, err)
			continue
		}
		if info.NotFound || (info.HomeURL == "" && info.OAIURL == "") {
			tanpaLink++
			fmt.Printf("view-%d → j%d: TANPA LINK (record-not-found/placeholder) — dilewati\n", id, jid)
			continue
		}
		if err := store.UpdateViewLinks(jid, info.HomeURL, info.OAIURL); err != nil {
			errCount++
			fmt.Printf("view-%d → j%d: tulis GAGAL: %v\n", id, jid, err)
			continue
		}
		isi++
		fmt.Printf("view-%d → j%d: home=%s oai=%s\n", id, jid, info.HomeURL, info.OAIURL)
	}
	fmt.Printf("view-fill selesai: isi=%d tanpa-resolve=%d tanpa-link=%d error=%d\n",
		isi, tanpaResolve, tanpaLink, errCount)

	// verifikasi akhir: jumlah baris terisi di db
	terisi, err := store.CountViewLinks()
	if err != nil {
		return fmt.Errorf("verifikasi: %w", err)
	}
	fmt.Printf("verifikasi: baris dgn link terisi = %d\n", terisi)
	if isi == 0 {
		return fmt.Errorf("tidak ada baris yang terisi — cek resolve/fixture")
	}
	return nil
}

// hitrateBaris = satu baris laporan E4a (utk JSON detail).
type hitrateBaris struct {
	JournalID int64   `json:"journal_id"`
	Nama      string  `json:"nama"`
	Baseline  string  `json:"baseline"` // match_status db (Q3)
	NKandidat int     `json:"kandidat"` // baris fixture utk jurnal ini
	Status    string  `json:"status"`   // hasil Match (re-ladder)
	By        string  `json:"by,omitempty"`
	Conf      float64 `json:"confidence,omitempty"`
	Auto      bool    `json:"auto_accept,omitempty"`
	Notes     string  `json:"notes,omitempty"`
}

// hitrateJSON = laporan E4a utuh (data/stage2/hitrate-e4a.json).
type hitrateJSON struct {
	Dibuat       string         `json:"dibuat"`
	Total        int            `json:"total"`
	Baseline     map[string]int `json:"baseline"`      // match_status db
	MatchedByDB  map[string]int `json:"matched_by_db"` // tier baseline (Q3)
	Reladder     map[string]int `json:"reladder_status"`
	ReladderBy   map[string]int `json:"reladder_matched_by"`
	Confidence   map[string]int `json:"reladder_confidence"` // skor diskrit 100/85/60
	AutoAccept   int            `json:"reladder_auto_accept"`
	DeltaNaik    int            `json:"delta_naik"`  // miss -> matched
	DeltaTurun   int            `json:"delta_turun"` // baseline matched -> bukan matched
	TetapMatched int            `json:"tetap_matched"`
	TetapMiss    int            `json:"tetap_miss"`
	Miss52       []hitrateBaris `json:"miss_52_detail"`
	Turun        []hitrateBaris `json:"baseline_turun_detail"`
	ButuhLive    []int64        `json:"butuh_live"` // kandidat 0 (found=0)
}

// runHitrate = E4a (doc 30 §14.3 langkah 1): ladder Match (Q2) atas 261
// jurnal memakai fixture search Q3 — READ-ONLY (db mode=ro), NOL request,
// TANPA tulis hasil (tulis = E4b setelah approve K6). Output: laporan
// ringkas + JSON detail utk kalibrasi ambang §5.2.
func runHitrate(dbPath, fixturesDir string) error {
	uri := "file:" + filepath.ToSlash(dbPath) + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return fmt.Errorf("buka db: %w", err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT j.id, j.name, COALESCE(j.print_issn,''), COALESCE(j.electronic_issn,''),
		       COALESCE(j.affiliation_name,''),
		       e.match_status, COALESCE(e.matched_by,''), COALESCE(e.match_confidence,0)
		FROM journals j JOIN journal_enrichment e ON e.journal_id = j.id
		ORDER BY j.id`)
	if err != nil {
		return fmt.Errorf("query jurnal: %w", err)
	}

	rep := hitrateJSON{
		Dibuat:      time.Now().UTC().Format(time.RFC3339),
		Baseline:    map[string]int{},
		MatchedByDB: map[string]int{},
		Reladder:    map[string]int{},
		ReladderBy:  map[string]int{},
		Confidence:  map[string]int{},
	}

	for rows.Next() {
		var id int64
		var name, pissn, eissn, pub, status, byDB string
		var confDB float64
		if err := rows.Scan(&id, &name, &pissn, &eissn, &pub, &status, &byDB, &confDB); err != nil {
			rows.Close()
			return err
		}
		rep.Total++
		rep.Baseline[status]++
		if status == "matched" {
			rep.MatchedByDB[byDB]++
		}

		cands, nRows, err := garuda.KandidatDariFixture(fixturesDir, id)
		if err != nil {
			rows.Close()
			return fmt.Errorf("kandidat j%d: %w", id, err)
		}
		res := garuda.Match(garuda.Input{Name: name, PISSN: pissn, EISSN: eissn, Publisher: pub}, cands)
		rep.Reladder[string(res.Status)]++
		if res.Status == garuda.StatusMatched {
			rep.ReladderBy[res.MatchedBy]++
			rep.Confidence[strconv.FormatFloat(res.Confidence, 'f', -1, 64)]++
			if res.AutoAccept {
				rep.AutoAccept++
			}
		}

		b := hitrateBaris{
			JournalID: id, Nama: name, Baseline: status, NKandidat: nRows,
			Status: string(res.Status), By: res.MatchedBy, Conf: res.Confidence,
			Auto: res.AutoAccept, Notes: res.Notes,
		}
		// klasifikasi delta baseline vs re-ladder
		baseMatched := status == "matched"
		resMatched := res.Status == garuda.StatusMatched
		switch {
		case !baseMatched && resMatched:
			rep.DeltaNaik++
		case baseMatched && !resMatched:
			rep.DeltaTurun++
			rep.Turun = append(rep.Turun, b)
		case baseMatched:
			rep.TetapMatched++
		default:
			rep.TetapMiss++
		}
		// fokus E4: 52 miss (not_found + ambiguous) di-detailkan
		if !baseMatched {
			rep.Miss52 = append(rep.Miss52, b)
		}
		// ladder mustahil tanpa kandidat → daftar live (hanya utk miss)
		if !baseMatched && nRows == 0 {
			rep.ButuhLive = append(rep.ButuhLive, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	// ---- laporan ringkas ----
	fmt.Println("== E4a hit-rate & ladder Match (READ-ONLY — nol request, tanpa tulis) ==")
	fmt.Printf("total=%d · baseline: matched=%d not_found=%d ambiguous=%d\n",
		rep.Total, rep.Baseline["matched"], rep.Baseline["not_found"], rep.Baseline["ambiguous"])
	fmt.Print("A. baseline matched_by: ")
	for k, v := range rep.MatchedByDB {
		fmt.Printf("%s=%d ", k, v)
	}
	fmt.Println()

	fmt.Printf("B. re-ladder 261 (informatif): matched=%d not_found=%d ambiguous=%d\n",
		rep.Reladder["matched"], rep.Reladder["not_found"], rep.Reladder["ambiguous"])
	fmt.Print("   tier: ")
	for _, k := range []string{"eissn", "pissn", "title+publisher", "title"} {
		fmt.Printf("%s=%d ", k, rep.ReladderBy[k])
	}
	fmt.Printf("| confidence: 100=%d 85=%d 60=%d | auto-accept(>=85)=%d\n",
		rep.Confidence["100"], rep.Confidence["85"], rep.Confidence["60"], rep.AutoAccept)
	fmt.Printf("   delta vs baseline: naik=%d turun=%d tetap-matched=%d tetap-miss=%d\n",
		rep.DeltaNaik, rep.DeltaTurun, rep.TetapMatched, rep.TetapMiss)
	if len(rep.Turun) > 0 {
		fmt.Println("   PERHATIAN — baseline matched yg TURUN saat re-ladder (cross-check/selisih):")
		for _, b := range rep.Turun {
			fmt.Printf("     j%-4d %-45.45s %s\n", b.JournalID, b.Nama, b.Notes)
		}
	}

	// ---- 52 miss ----
	var mMatch, mAmb, mNF int
	ambCross, ambSelisih := 0, 0
	tier := map[string]int{}
	for _, b := range rep.Miss52 {
		switch b.Status {
		case "matched":
			mMatch++
			tier[b.By]++
		case "ambiguous":
			mAmb++
			if strings.Contains(b.Notes, "kemiripan title") {
				ambCross++
			} else if strings.Contains(b.Notes, "selisih") {
				ambSelisih++
			}
		default:
			mNF++
		}
	}
	fmt.Printf("C. ladder 52 miss (fokus E4): matched=%d ambiguous=%d not_found=%d\n", mMatch, mAmb, mNF)
	fmt.Print("   naik via tier: ")
	for _, k := range []string{"pissn", "title+publisher", "title"} {
		fmt.Printf("%s=%d ", k, tier[k])
	}
	fmt.Printf("| ambiguous: cross-check=%d selisih-top2=%d\n", ambCross, ambSelisih)

	fmt.Printf("D. butuh live (kandidat 0/found=0): %d jurnal → ", len(rep.ButuhLive))
	fmt.Println(rep.ButuhLive)

	// ---- JSON detail utk kalibrasi ----
	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	jsonPath := filepath.Join(filepath.Dir(fixturesDir), "hitrate-e4a.json")
	if err := os.WriteFile(jsonPath, out, 0o644); err != nil {
		return fmt.Errorf("tulis json: %w", err)
	}
	fmt.Printf("JSON detail → %s\n", jsonPath)
	return nil
}

// ---------- probe (Q3 langkah 3 / pembuka E1) ----------

type kasus struct {
	label string
	q     string
}

func runProbe(dbPath, fixturesDir string, delayMin, delayMax time.Duration) error {
	eissn, pissn, err := seedISSN(dbPath)
	if err != nil {
		return fmt.Errorf("baca seed ISSN dari db: %w", err)
	}
	if len(eissn) == 0 || pissn == "" {
		return fmt.Errorf("seed ISSN tak lengkap (eissn=%d, pissn=%d)", len(eissn), len(pissn))
	}

	kasus := []kasus{
		{"eissn-1", eissn[0]},
		{"eissn-2", eissn[1]},
		{"eissn-3", eissn[2]},
		{"eissn-4", eissn[3]},
		{"eissn-hyphen", eissn[0][:4] + "-" + eissn[0][4:]}, // varian hyphen
		{"pissn", pissn},
		{"nol", "00000000"},
		{"huruf", "xyz"},
		{"pendek", "ab"},    // di bawah minlength=3 (form) — uji server
		{"multi", "jurnal"}, // multi-hit + paginasi (lanjut page=2)
	}
	if len(eissn) < 4 {
		return fmt.Errorf("eissn valid di db hanya %d (butuh 4)", len(eissn))
	}

	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		return fmt.Errorf("buat dir fixture: %w", err)
	}
	c := garuda.NewClient("garuda-probe", uaDefault, delayMin, delayMax)

	fmt.Printf("%-13s %-10s %-4s %5s %7s %6s %5s %6s %6s %s\n",
		"kasus", "q", "http", "ms", "found", "total", "rows", "label", "exact", "catatan")
	for i, k := range kasus {
		t0 := time.Now()
		body, status, err := c.Get(garuda.SearchURL(k.q, 1), garuda.DefaultReferer)
		ms := time.Since(t0).Milliseconds()
		if err != nil && body == nil {
			fmt.Printf("%-13s %-10s %-4d %5d %s\n", k.label, k.q, status, ms, "ERR "+err.Error())
			continue
		}
		fixture := filepath.Join(fixturesDir, fmt.Sprintf("probe-%02d-%s.html", i+1, slug(k.label)))
		if werr := os.WriteFile(fixture, body, 0o644); werr != nil {
			return fmt.Errorf("tulis fixture: %w", werr)
		}
		page, perr := garuda.ParseSearchPage(strings.NewReader(string(body)))
		if perr != nil {
			fmt.Printf("%-13s %-10s %-4d parse ERR %v\n", k.label, k.q, status, perr)
			continue
		}
		var label int
		exact := "-"
		if key := garuda.CanonicalISSN(k.q); key != "" {
			n := 0
			for _, r := range page.Rows {
				if garuda.CanonicalISSN(r.EISSN) == key {
					n++
				}
			}
			exact = fmt.Sprintf("%d/%d", n, len(page.Rows))
		}
		for _, r := range page.Rows {
			label += len(r.Areas)
		}
		note := ""
		if k.label == "multi" {
			// lanjut halaman 2 — uji paginasi utk E1
			body2, st2, err2 := c.Get(garuda.SearchURL(k.q, 2), garuda.DefaultReferer)
			if err2 != nil && body2 == nil {
				note = fmt.Sprintf("p2 ERR status=%d %v", st2, err2)
			} else {
				f2 := filepath.Join(fixturesDir, "probe-10-multi-p2.html")
				_ = os.WriteFile(f2, body2, 0o644)
				if page2, e2 := garuda.ParseSearchPage(strings.NewReader(string(body2))); e2 == nil {
					note = fmt.Sprintf("p2 rows=%d page=%d/%d", len(page2.Rows), page2.Page, page2.OfPages)
				} else {
					note = "p2 parse ERR"
				}
			}
		}
		if page.Found == -1 && k.q != "ab" {
			note = strings.TrimSpace(note + " tanpa blok found")
		}
		fmt.Printf("%-13s %-10s %-4d %5d %7d %6d %5d %6d %6s %s\n",
			k.label, k.q, status, ms, page.Found, page.Total, len(page.Rows), label, exact, note)
	}

	fmt.Printf("\nfixture tersimpan di: %s\n", fixturesDir)
	fmt.Println("temuan probe dicatat di doc 30 Bagian 13 (§13.4 langkah 3) saat keep-track.")
	return nil
}

// seedISSN membaca seed campuran utk probe: 4 E-ISSN + 1 P-ISSN valid dari
// rank S1 (read-only — db sumber TIDAK dimutasi schema oleh probe).
func seedISSN(path string) ([]string, string, error) {
	uri := "file:" + filepath.ToSlash(path) + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, "", err
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT electronic_issn FROM journals
		WHERE sinta_rank = 1 AND electronic_issn <> ''
		ORDER BY id LIMIT 4`)
	if err != nil {
		return nil, "", err
	}
	var e []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return nil, "", err
		}
		e = append(e, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, "", err
	}
	rows.Close()

	var p string
	if err := db.QueryRow(`
		SELECT print_issn FROM journals
		WHERE sinta_rank = 1 AND print_issn <> '' ORDER BY id LIMIT 1`).Scan(&p); err != nil {
		return nil, "", err
	}
	return e, p, nil
}

var reNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	out := reNonAlnum.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(out, "-")
}
