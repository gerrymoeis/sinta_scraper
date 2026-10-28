// Program oaenrich — P1 doc 44 §5 (approve user 28 Okt 2026): enrich
// oa_status + lisensi dari DOAJ API by ISSN utk jurnal dgn ≥1 artikel
// oa_status='unknown' — menutup gap G-a/G-e (jalur Crossref menang → DOAJ
// tak dicek → oa unknown by-design §5.5).
//
// FAKTA JUJUR: DOAJ hanya mengindeks jurnal OA → jurnal non-OA/tertutup
// tetap 'unknown' BY-DESIGN (status riil mereka — bukan kegagalan scraper).
//
// DRY-RUN default (0 tulis DB): hitungan per jurnal → REVIEW → -apply.
// Idempoten: UPDATE HANYA bila kolom masih 'unknown' (tak pernah menimpa
// data OAI asli). Provenance artikel di-merge jujur: "oa_src":"doaj_api"
// (+ "lic_src":"doaj_api" bila lisensi ikut terisi) — pola D4 provMergeArt.
//
// Match artikel DOAJ ↔ articles by DOI (case-insensitive, pola PickDOI
// korpus: identifier[] type=doi / field doi). Tanpa DOI = lewat (jujur —
// tanpa kunci join yang andal jangan ditebak).
//
// Pagination: pageSize=100 · sort=last_updated:desc · cap -doaj-max-pages
// (default 10 = 1.000 artikel DOAJ terbaru/jurnal) — halaman terpotong
// dilaporkan TRUNCATED (jujur; artikel tua melebihi cap tetap unknown).
//
// Keputusan lisensi DOAJ (konsisten whitelist D5: CC BY* / CC0 / ARR /
// unknown): license_type = type DOAJ APA ADANYA (mis. "CC BY") bila masih
// 'unknown' · license_status='open' bila masih 'unknown' (kriteria DOAJ =
// jurnal+artikel open access) · oa_status='free' selalu utk match DOAJ.
//
// Pakai (POLA LICFIX — backup manual sebelum -apply):
//
//	go run ./cmd/oaenrich -db data/stage2/sinta-r2-enrich.db            (dry-run)
//	go run ./cmd/oaenrich -db ... -journal 688,696                      (smoke subset)
//	go run ./cmd/oaenrich -db ... -apply                                (tulis)
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"

	_ "modernc.org/sqlite"
)

// doajEnv = envelope DOAJ search API (artikel) — cukup utk kunci join
// (DOI) + atribut lisensi (bibjson.license type/url). Parser mini mandiri
// (cmd terpisah — NOL sentuh pipeline corpus; pola licfix).
type doajEnv struct {
	Total   int `json:"total"`
	Results []struct {
		Bibjson struct {
			DOI        string `json:"doi"`
			Identifier []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"identifier"`
			License struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"license"`
		} `json:"bibjson"`
	} `json:"results"`
}

// jrow = target utk 1 jurnal: ISSN (utk query DOAJ) + hitungan.
type jrow struct {
	id   int64
	name string
	issn string // ISSN aktif dipakai utk log/rekap (print dulu, fallback e)

	issnP string // print_issn (dash) — kandidat pertama
	issnE string // electronic_issn (dash) — kandidat kedua (fakta 28 Okt:
	// DOAJ bisa menyimpan e-ISSN SAJA — Ulumuna 2775-2453=1 · print=0)

	doajTotal  int // total artikel DOAJ utk ISSN aktif (fakta API)
	pages      int // halaman diambil (cap)
	truncated  bool
	matchDOI   int // artikel DOAJ dgn DOI yg MATCH baris articles unknown
	oacand     int // kandidat UPDATE oa_status='free'
	licCand    int // kandidat UPDATE lisensi (masih unknown semua)
	pagesFails int // halaman GET gagal (jujur)
}

func main() {
	dbPath := flag.String("db", "data/stage2/sinta-r2-enrich.db", "path file SQLite korpus")
	applyFlag := flag.Bool("apply", false, "TULIS ke DB (default = DRY-RUN 0 tulis; backup manual .preOA.bak.db dulu — pola licfix)")
	jflag := flag.String("journal", "", "opsional: subset ID jurnal utk smoke (mis. 688,696)")
	maxPages := flag.Int("doaj-max-pages", 10, "cap halaman DOAJ per jurnal (100 artikel/halaman; log jujur bila truncated)")
	delay := flag.Duration("delay", time.Second, "delay etis antar GET DOAJ API")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("GAGAL buka DB: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("GAGAL ping DB: %v", err)
	}

	subset, err := parseSubset(*jflag)
	if err != nil {
		log.Fatalf("GAGAL -journal: %v", err)
	}

	targets, err := loadTargets(db, subset)
	if err != nil {
		log.Fatalf("GAGAL load target: %v", err)
	}
	log.Printf("[oaenrich] target = %d jurnal dgn ≥1 oa_status='unknown' (apply=%v maxPages=%d)",
		len(targets), *applyFlag, *maxPages)
	if len(targets) == 0 {
		log.Print("[oaenrich] tidak ada target — selesai (jujur)")
		return
	}

	client := garuda.NewClient("oaenrich", "sinta-scraper/1.0 (+https://github.com/gerrymoeis/sinta_scraper; polite OA enrich)", *delay, *delay)

	var grand jrow
	for i := range targets {
		j := &targets[i]
		if err := enrichJurnal(client, db, j, *maxPages, *applyFlag); err != nil {
			// Jujur by-design: jurnal gagal dicatat, run LANJUT (resume aman).
			log.Printf("[oaenrich] j%d %q: GAGAL — %v (lanjut)", j.id, j.name, err)
		}
		grand.doajTotal += j.doajTotal
		grand.matchDOI += j.matchDOI
		grand.oacand += j.oacand
		grand.licCand += j.licCand
	}

	printRekap(targets, grand, *applyFlag)
	if *applyFlag {
		log.Print("[oaenrich] APPLY selesai — jalankan ulang utk bukti idempoten (harus 0 kandidat)")
	} else {
		log.Print("[oaenrich] DRY-RUN — 0 tulis. Review angka di atas, lalu: -apply (backup .preOA.bak.db dulu)")
	}
}

// parseSubset = "688,696" → map id. "" = semua (tanpa filter).
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

// loadTargets = jurnal dgn ≥1 artikel oa_status='unknown' + ISSN utk DOAJ.
// ISSN: print_issn bila ada, fallback electronic_issn (pola corpus).
func loadTargets(db *sql.DB, subset map[int64]bool) ([]jrow, error) {
	rows, err := db.Query(`
		SELECT j.id, j.name, COALESCE(j.print_issn, ''), COALESCE(j.electronic_issn, '')
		FROM journals j
		WHERE j.id IN (SELECT DISTINCT journal_id FROM articles WHERE oa_status = 'unknown')
		ORDER BY j.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []jrow
	for rows.Next() {
		var j jrow
		var issnP, issnE string
		if err := rows.Scan(&j.id, &j.name, &issnP, &issnE); err != nil {
			return nil, err
		}
		if len(subset) > 0 && !subset[j.id] {
			continue
		}
		// DOAJ WAJIB format dash (fakta probe 27 Okt — pola doajArticlesURL).
		// Kedua ISSN disimpan: DOAJ bisa terdaftar dgn e-ISSN saja (fakta
		// 28 Okt: Ulumuna print=0, e-ISSN 2775-2453=1) — coba keduanya.
		p := strings.TrimSpace(issnP)
		e := strings.TrimSpace(issnE)
		if p != "" {
			j.issnP = dashISSN(p)
		}
		if e != "" {
			if d := dashISSN(e); d != j.issnP {
				j.issnE = d
			}
		}
		if j.issnP == "" && j.issnE == "" {
			log.Printf("[oaenrich] j%d %q: TANPA ISSN — lewat (jujur; DOAJ by-ISSN mustahil)", j.id, j.name)
			continue
		}
		j.issn = j.issnP
		if j.issn == "" {
			j.issn = j.issnE
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// dashISSN = "20875886" → "2087-5886" (utk 8 digit; selain itu apa adanya).
func dashISSN(s string) string {
	t := strings.ReplaceAll(s, "-", "")
	if len(t) == 8 {
		return t[:4] + "-" + t[4:]
	}
	return s
}

// enrichJurnal = 1 jurnal: coba ISSN print dulu → bila DOAJ 0 total, retry
// e-ISSN (fakta 28 Okt: Ulumuna terdaftar dgn e-ISSN saja) → match by DOI →
// dry-run hitungan / apply UPDATE idempoten.
func enrichJurnal(client *garuda.Client, db *sql.DB, j *jrow, maxPages int, apply bool) error {
	for _, issn := range []string{j.issnP, j.issnE} {
		if issn == "" {
			continue
		}
		total, err := enrichByISSN(client, db, j, issn, maxPages, apply)
		if err != nil {
			return err
		}
		if total > 0 {
			j.issn = issn // ISSN aktif utk rekap
			return nil
		}
	}
	return nil
}

// enrichByISSN = paginasi DOAJ 1 ISSN → hitung/apply kandidat. Return total
// artikel DOAJ (0 = ISSN ini tak terdaftar — caller boleh coba kandidat lain).
func enrichByISSN(client *garuda.Client, db *sql.DB, j *jrow, issn string, maxPages int, apply bool) (int, error) {
	total := 0
	for page := 1; page <= maxPages; page++ {
		u := fmt.Sprintf("https://doaj.org/api/search/articles/issn:%s?pageSize=100&page=%d&sort=last_updated:desc",
			url.QueryEscape(issn), page)
		body, st, _, err := client.GetPlain(u, "")
		if err != nil || st != 200 {
			j.pagesFails++
			return total, fmt.Errorf("DOAJ GET issn=%s page=%d status=%d err=%v", issn, page, st, err)
		}
		var env doajEnv
		if err := json.Unmarshal(body, &env); err != nil {
			return total, fmt.Errorf("DOAJ issn=%s page=%d bukan JSON valid: %v", issn, page, err)
		}
		if page == 1 {
			total = env.Total
			j.doajTotal = env.Total
			log.Printf("[oaenrich] j%d %q (%s): DOAJ total=%d artikel", j.id, j.name, issn, env.Total)
		}
		j.pages++
		if apply {
			if err := applyDoajPage(db, j, env, &j.matchDOI, &j.oacand, &j.licCand); err != nil {
				return total, err
			}
		} else {
			countDoajPage(db, j, env, &j.matchDOI, &j.oacand, &j.licCand)
		}
		// Cakupan: halaman PER-ISSN (page) — bukan j.pages (kumulatif
		// lintas-ISSN saat retry print→e bisa salah hitung).
		if page*100 >= total {
			return total, nil // semua halaman tercakup
		}
	}
	if total > maxPages*100 {
		j.truncated = true // cap habis — sisa artikel tua = unknown (jujur)
	}
	return total, nil
}

// candRow = baris articles kandidat (masih oa unknown + DOI match).
type candRow struct {
	id         int64
	prov       string
	licStatus  string
	licType    string
	doajLicTyp string
	doajLicURL string
}

// matchCandidates = artikel DOAJ 1 halaman ↔ articles jurnal ini by DOI
// (case-insensitive) YANG MASIH oa_status='unknown' (idempoten).
func matchCandidates(db *sql.DB, j *jrow, env doajEnv) ([]candRow, error) {
	var out []candRow
	seen := map[string]bool{}
	for _, r := range env.Results {
		doi := normDOI(r.Bibjson.DOI)
		if doi == "" {
			for _, idf := range r.Bibjson.Identifier {
				if strings.EqualFold(idf.Type, "doi") {
					doi = normDOI(idf.ID)
					break
				}
			}
		}
		if doi == "" || seen[doi] {
			continue // tanpa DOI = lewat (jujur) · duplikat halaman = skip
		}
		seen[doi] = true
		var c candRow
		err := db.QueryRow(`
			SELECT id, COALESCE(provenance,''), license_status, license_type
			FROM articles
			WHERE journal_id = ? AND LOWER(doi) = ? AND oa_status = 'unknown'`,
			j.id, doi).Scan(&c.id, &c.prov, &c.licStatus, &c.licType)
		if err == sql.ErrNoRows {
			continue // sudah terisi / tak ada baris dgn DOI ini — lewat
		}
		if err != nil {
			return nil, err
		}
		c.doajLicTyp = strings.TrimSpace(r.Bibjson.License.Type)
		c.doajLicURL = strings.TrimSpace(r.Bibjson.License.URL)
		out = append(out, c)
	}
	return out, nil
}

// countDoajPage = dry-run: hitung kandidat tanpa tulis.
func countDoajPage(db *sql.DB, j *jrow, env doajEnv, match, oa, lic *int) {
	cands, err := matchCandidates(db, j, env)
	if err != nil {
		log.Printf("[oaenrich] j%d: hitung gagal — %v", j.id, err)
		return
	}
	*match += len(cands)
	for _, c := range cands {
		*oa++
		if c.licStatus == "unknown" || c.licType == "unknown" {
			*lic++
		}
	}
}

// applyDoajPage = -apply: UPDATE idempoten per kandidat (transaction per
// jurnal-halaman) + provenance merge jujur pola D4 (provMergeArt).
func applyDoajPage(db *sql.DB, j *jrow, env doajEnv, match, oa, lic *int) error {
	cands, err := matchCandidates(db, j, env)
	if err != nil {
		return err
	}
	*match += len(cands)
	for _, c := range cands {
		setLic := c.licStatus == "unknown" || c.licType == "unknown"
		prov, perr := provMerge(c.prov, c)
		if perr != nil {
			return fmt.Errorf("artikel %d: provenance merge: %v", c.id, perr)
		}
		var res sql.Result
		if setLic {
			licTyp := c.licType
			if licTyp == "unknown" && c.doajLicTyp != "" {
				licTyp = c.doajLicTyp // type DOAJ apa adanya (jujur)
			}
			res, err = db.Exec(`
				UPDATE articles
				SET oa_status = 'free',
				    license_status = CASE WHEN license_status = 'unknown' THEN 'open' ELSE license_status END,
				    license_type = ?, provenance = ?
				WHERE id = ? AND oa_status = 'unknown'`,
				licTyp, prov, c.id)
		} else {
			res, err = db.Exec(`
				UPDATE articles SET oa_status = 'free', provenance = ?
				WHERE id = ? AND oa_status = 'unknown'`,
				prov, c.id)
		}
		if err != nil {
			return fmt.Errorf("artikel %d UPDATE: %v", c.id, err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			*oa++
			if setLic {
				*lic++
			}
		}
	}
	return nil
}

// provMerge = merge kunci jujur ke provenance artikel (pola D4 provMergeArt
// licfix — map bebas kunci artikel; format JSON lama dipertahankan).
func provMerge(raw string, c candRow) (string, error) {
	m := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return "", err
		}
	}
	m["oa_src"] = "doaj_api"
	if c.doajLicTyp != "" {
		m["lic_src"] = "doaj_api"
	}
	b, err := json.Marshal(m)
	return string(b), err
}

// normDOI = lowercase + trim; buang prefix "doi:" / URL landing (pola
// korpus — PickDOI menghasilkan polos dari "doi:"-prefix adapter).
func normDOI(s string) string {
	d := strings.ToLower(strings.TrimSpace(s))
	d = strings.TrimPrefix(d, "doi:")
	d = strings.TrimPrefix(d, "https://doi.org/")
	d = strings.TrimPrefix(d, "http://doi.org/")
	return strings.TrimPrefix(d, "dx.doi.org/")
}

// printRekap = tabel per-jurnal + grand total (tabwriter pola licfix).
func printRekap(targets []jrow, grand jrow, apply bool) {
	mode := "DRY-RUN"
	if apply {
		mode = "APPLY"
	}
	fmt.Printf("\n══ oaenrich %s ══\n", mode)
	fmt.Println("jID | ISSN | doaj_total | hal | matchDOI | cand_oa | cand_lic | catatan")
	for _, j := range targets {
		note := ""
		switch {
		case j.truncated:
			note = "TRUNCATED (cap halaman habis — sisa unknown jujur)"
		case j.doajTotal == 0:
			note = "DOAJ 0 (bukan jurnal DOAJ — unknown by-design)"
		case j.pagesFails > 0:
			note = "GET gagal (jujur)"
		}
		fmt.Printf("j%d | %s | %d | %d | %d | %d | %d | %s\n",
			j.id, j.issn, j.doajTotal, j.pages, j.matchDOI, j.oacand, j.licCand, note)
	}
	fmt.Printf("TOTAL: %d jurnal · doaj %d artikel · matchDOI %d · cand_oa +%d · cand_lic +%d\n",
		len(targets), grand.doajTotal, grand.matchDOI, grand.oacand, grand.licCand)
}
