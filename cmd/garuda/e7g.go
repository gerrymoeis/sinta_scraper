package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"
	"sinta-scraper/internal/storage"
)

// runE7G = Q4 E7g (doc 30 §14.6 butir 9 — PAGU APPROVED user 7 Okt 2026):
// verifikasi hidup/matot 261 `ojs_url` + rantai repair kandidat (doc 17 §4.2)
// + evaluasi robots.txt per host (doc 11 Bagian 7) + TULIS journal_urls
// (skema doc 30 §3.2, 0 baris → kini terisi) — nol DDL.
//
// Struktur (satu run, 4 fase):
//
//	A. ROBOTS: GET robots.txt 1× per host unik — KUNCI host (bukan origin):
//	   19 host dual-scheme (http+https) dihitung SATU, 174 total; origin fetch
//	   = https bila host punya baris https (fixture-first — body disimpan utk
//	   evaluasi path berikutnya, rerun 0 GET).
//	B. CEK: GET tiap ojs_url dgn retry 1× transien (pagu 300) — klasifikasi
//	   ok/404/410/waf/server/dns/timeout/unreachable; 403/429 WAF BUKAN mati
//	   (approve 7 Okt) & host dgn robots tolak dilewat TANPA GET (jujur).
//	C. KANDIDAT: utk baris mati (404/410/dns/timeout/unreachable) — rantai
//	   Garuda Home Page (fixture view, offline) → OpenAlex homepage_url →
//	   DOAJ ref.journal (fixture, offline) → manual; tiap kandidat WAJIB
//	   robots-host dulu + verify GET 200 (pagu 300).
//	D. TULIS (0 GET): backup .preE7g.bak.db → journal_urls (semua kandidat,
//	   sumber asal dipertahankan, is_canonical=0 semua) → journals.ojs_url
//	   HANYA bila lama matot DAN kandidat 200 (+ provenance) → diff K1 →
//	   counts.
const (
	paguRobotsE7g  = 175 // 174 host unik + margin 1 (approve 7 Okt)
	paguCekE7g     = 300 // 261 + margin retry (approve 7 Okt)
	paguKandE7g    = 300 // rantai kandidat M ≤ 80 (approve 7 Okt)
	e7gJumlahBaris = 261 // guard drift query (fakta db)
	e7gJumlahHost  = 174 // guard drift host (fakta db, run pertama)
)

var errPaguE7g = errors.New("PAGU E7g HABIS")

// ---- tipe data -------------------------------------------------------------

type e7gBaris struct {
	ID   int64
	Nama string
	Ojs  string // TRIM(ojs_url) — dipakai GET, resume, & journal_urls.url
	Key  string // ISSN kanonik ("" bila tak valid — sumber 2/3 dilewat jujur)
}

type e7gRobotsHasil struct {
	Host   string `json:"host"`   // kunci: host[:port] kecil — 1 GET/host
	Origin string `json:"origin"` // origin yg di-GET (https-prefer utk dual-scheme)
	HTTP   int    `json:"http"`   // 0 = gagal tanpa respons / body gagal
	Dari   string `json:"dari"`   // GET | resume
}

type e7gCekHasil struct {
	ID        int64  `json:"journal_id"`
	Nama      string `json:"nama"`
	URL       string `json:"url"`  // URL yang dicek (sumber baris "sinta")
	HTTP      int    `json:"http"` // 0 = tak ada respons / tak dicek
	Final     string `json:"final_url,omitempty"`
	Cls       string `json:"klasifikasi"` // "" bila tak dicek (robots tolak)
	Err       string `json:"error,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
	Dari      string `json:"dari"` // GET | resume | resume-kandidat | robots-tolak
	Upaya     int    `json:"upaya,omitempty"`
}

type e7gTahap struct {
	Sumber  string `json:"sumber"` // garuda | openalex | doaj
	URL     string `json:"url"`
	HTTP    int    `json:"http"` // 0 = tak ada respons / tak di-GET
	Final   string `json:"final_url,omitempty"`
	Cls     string `json:"klasifikasi,omitempty"`
	Err     string `json:"error,omitempty"`
	Catatan string `json:"catatan,omitempty"` // "robots tolak" / error parse
}

type e7gKandHasil struct {
	ID        int64      `json:"journal_id"`
	Selesai   bool       `json:"selesai"`
	Pagu      bool       `json:"pagu_habis,omitempty"` // berhenti krn pagu (lanjut run berikutnya)
	Tahap     []e7gTahap `json:"tahap,omitempty"`
	Terpilih  string     `json:"terpilih,omitempty"` // kandidat terverifikasi 200
	Sumber    string     `json:"sumber,omitempty"`   // sumber terpilih
	Manual    bool       `json:"butuh_manual,omitempty"`
	CheckedAt string     `json:"checked_at,omitempty"`
}

type e7gGet struct {
	Robots int `json:"robots"`
	Cek    int `json:"cek"`
	Kand   int `json:"kandidat"`
}

func (g e7gGet) total() int { return g.Robots + g.Cek + g.Kand }

type e7gHasilJSON struct {
	Dibuat   string           `json:"dibuat"`
	Pagu     map[string]int   `json:"pagu_get"`
	Get      e7gGet           `json:"get_kumulatif"`
	Jumlah   int              `json:"jumlah"`
	Hosts    int              `json:"hosts"`
	Robots   []e7gRobotsHasil `json:"robots"`
	Cek      []e7gCekHasil    `json:"cek"`
	Kandidat []e7gKandHasil   `json:"kandidat"`
	Ringkas  map[string]int   `json:"ringkasan"`
	Fill     *e7gFillJSON     `json:"fill,omitempty"`
}

type e7gFillJSON struct {
	Backup       string            `json:"backup"`
	Robots200    int               `json:"robots_file_200"`
	RobotsTanpa  int               `json:"robots_tanpa_file"`
	RobotsGagal  int               `json:"robots_gagal"`
	Dilarang     int               `json:"cek_dilarang_robots"`
	KandSelesai  int               `json:"kandidat_selesai"`
	Terpilih     map[string]int    `json:"terpilih_per_sumber"`
	Manual       int               `json:"butuh_manual"`
	OjsPerbaikan int               `json:"ojs_url_perbaikan"`
	OjsUlang     int               `json:"ojs_url_ulang_idempoten"`
	UrlsTulis    int               `json:"journal_urls_baris"`
	UrlsUlang    int               `json:"journal_urls_ulang_idempoten"`
	ProvTulis    int               `json:"provenance_tulis"`
	Verifikasi   map[string]string `json:"verifikasi"`
	DiffLarang   []string          `json:"diff_larangan"` // harus kosong
}

type e7gState struct {
	robots   map[string]e7gRobotsHasil // key: host[:port] (1×/host — approve 7 Okt)
	cek      map[int64]e7gCekHasil     // key: journal id
	kand     map[int64]e7gKandHasil    // key: journal id
	get      e7gGet
	fillLama *e7gFillJSON
}

type e7gGetURL struct {
	Status int
	Final  string
	Cls    string
	Err    string
	Upaya  int
	Body   []byte
}

// e7gRencanaURL = satu baris rencana tulis journal_urls.
type e7gRencanaURL struct {
	JID    int64
	URL    string
	Source string // sinta (url asli) | garuda | openalex | doaj
	Status *int   // nil = belum/tak ada respons HTTP
	Final  string
	Cek    string // checked_at ("" = belum pernah di-GET)
}

// ---- helper baca db & target ------------------------------------------------

// e7gTarget = 261 baris ber-ojs_url (TRIM) + key ISSN kanonik (fallback
// spt E7f: eissn → pissn → garuda_eissn → garuda_pissn; "" = jujur).
func e7gTarget(dbPath string) ([]e7gBaris, error) {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`
		SELECT j.id, j.name, TRIM(COALESCE(j.ojs_url,'')),
		       COALESCE(j.electronic_issn,''), COALESCE(j.print_issn,''),
		       COALESCE(e.garuda_eissn,''), COALESCE(e.garuda_pissn,'')
		FROM journals j JOIN journal_enrichment e ON e.journal_id = j.id
		WHERE TRIM(COALESCE(j.ojs_url,'')) <> ''
		ORDER BY j.id`)
	if err != nil {
		return nil, fmt.Errorf("query target: %w", err)
	}
	defer rows.Close()
	var out []e7gBaris
	for rows.Next() {
		var b e7gBaris
		var eIssn, pIssn, gE, gP string
		if err := rows.Scan(&b.ID, &b.Nama, &b.Ojs, &eIssn, &pIssn, &gE, &gP); err != nil {
			return nil, err
		}
		for _, c := range []string{eIssn, pIssn, gE, gP} {
			if k := garuda.NormISSN(c); k != "" {
				b.Key = k
				break
			}
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// e7gSlugHost = nama file fixture robots per origin (aman utk Windows +
// hash fnv32a cegah tabrakan slug).
func e7gSlugHost(origin string) string {
	s := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://"))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(origin))
	return fmt.Sprintf("%s-%08x", b.String(), h.Sum32())
}

func e7gFixtureRobots(fixturesDir, origin string) string {
	return filepath.Join(fixturesDir, "e7g-robots-"+e7gSlugHost(origin)+".txt")
}

// e7gHost = host[:port] kecil utk kunci robots. BUKAN origin: 19 host
// dual-scheme (http+https) harus dihitung 1× supaya total 174 sesuai pagu
// & fakta db (approve 7 Okt — "robots.txt 1×/host ≤175").
func e7gHost(rawURL string) string {
	o := garuda.RobotsOrigin(rawURL)
	if o == "" {
		return ""
	}
	return o[strings.Index(o, "://")+3:]
}

// e7gOriginPerHost = origin fetch robots per host: https MENANG bila host
// punya minimal satu baris https, selain itu http pertama — deterministik
// (tak tergantung urutan baris) utk idempoten rerun & fixture stabil.
func e7gOriginPerHost(baris []e7gBaris) map[string]string {
	m := map[string]string{}
	httpsAda := map[string]string{}
	for _, b := range baris {
		o := garuda.RobotsOrigin(b.Ojs)
		if o == "" {
			continue
		}
		h := e7gHost(b.Ojs)
		if _, ok := m[h]; !ok {
			m[h] = o
		}
		if strings.HasPrefix(o, "https://") {
			httpsAda[h] = o
		}
	}
	for h, o := range httpsAda {
		m[h] = o
	}
	return m
}

// e7gHomeMap = kandidat Garuda "Home Page" dari fixture view-*.html (offline,
// pola resolve runViewFill: ViewLinkRefs → ambiguous via fixture search).
// Mengembalikan map + jumlah fixture gagal parse/tanpa link (dicatat jujur).
func e7gHomeMap(store *storage.Store, fixturesDir string) (map[int64]string, int, error) {
	refs, err := store.ViewLinkRefs()
	if err != nil {
		return nil, 0, fmt.Errorf("ViewLinkRefs: %w", err)
	}
	byID := map[int]int64{}
	for _, r := range refs {
		if _, ada := byID[r.GarudaID]; !ada {
			byID[r.GarudaID] = r.JournalID
		}
	}
	ambIDs, err := store.AmbiguousJournalIDs()
	if err != nil {
		return nil, 0, fmt.Errorf("AmbiguousJournalIDs: %w", err)
	}
	for _, jid := range ambIDs {
		id, err := idDariFixtureSearch(fixturesDir, jid)
		if err != nil {
			continue
		}
		if _, ada := byID[id]; !ada {
			byID[id] = jid
		}
	}
	matches, err := filepath.Glob(filepath.Join(fixturesDir, "view-*.html"))
	if err != nil {
		return nil, 0, fmt.Errorf("glob view: %w", err)
	}
	out := map[int64]string{}
	tanpaLink := 0
	for _, path := range matches {
		m := reFixtureView.FindStringSubmatch(filepath.Base(path))
		if m == nil {
			continue
		}
		id, _ := strconv.Atoi(m[1])
		jid, ada := byID[id]
		if !ada {
			continue // id lama tak terpilih Q3 — dilewati (pola view-fill)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, fmt.Errorf("baca %s: %w", filepath.Base(path), err)
		}
		info, err := garuda.ParseViewPage(bytes.NewReader(b))
		if err != nil || info.NotFound || info.HomeURL == "" {
			tanpaLink++
			continue
		}
		out[jid] = strings.TrimSpace(info.HomeURL)
	}
	return out, tanpaLink, nil
}

// e7gDoajMap = kandidat DOAJ ref.journal dari fixture (cascade E7f → E7d →
// E6; offline, 0 GET).
func e7gDoajMap(baris []e7gBaris, fixturesDir string) (map[int64]string, int) {
	out := map[int64]string{}
	gagal := 0
	for _, b := range baris {
		if b.Key == "" {
			continue
		}
		for _, pref := range []string{"e7f-doaj", "e7d-doaj", "e6-doaj"} {
			path := filepath.Join(fixturesDir, fmt.Sprintf("%s-%s.json", pref, b.Key))
			body, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			u, err := garuda.DoajRefJournal(body)
			if err != nil {
				gagal++
			} else if u != "" {
				out[b.ID] = u
			}
			break
		}
	}
	return out, gagal
}

// e7gOpenAlexMap = homepage_url dari fixture respons API E7g (rerun 0 GET).
func e7gOpenAlexMap(fixturesDir string) (map[string]string, error) {
	matches, err := filepath.Glob(filepath.Join(fixturesDir, "e7g-openalex-*.json"))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, path := range matches {
		base := filepath.Base(path)
		key := strings.TrimSuffix(strings.TrimPrefix(base, "e7g-openalex-"), ".json")
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		hp, err := garuda.OpenAlexHomepage(body)
		if err != nil {
			continue // fixture rusak → treat tak ada (chain GET ulang)
		}
		if hp != "" {
			out[key] = hp
		}
	}
	return out, nil
}

// ---- snapshot & diff K1 -----------------------------------------------------

type e7gSnap struct {
	Name        string
	SubjectArea string
	GarudaURL   string
	ContentHash string
	LastScr     string
	PrintISSN   string
	EISSN       string
	DoajURL     string
	OJSURL      string // kolom target E7g
	Rank        int
	OJSStatus   string
}

func e7gSnapshot(dbPath string) (map[int64]e7gSnap, error) {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`
		SELECT id, name, COALESCE(subject_area,''), COALESCE(garuda_url,''),
		       content_hash, last_scraped_at, COALESCE(print_issn,''),
		       COALESCE(electronic_issn,''), COALESCE(doaj_url,''),
		       COALESCE(ojs_url,''), sinta_rank, ojs_status
		FROM journals`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]e7gSnap{}
	for rows.Next() {
		var id int64
		var s e7gSnap
		if err := rows.Scan(&id, &s.Name, &s.SubjectArea, &s.GarudaURL,
			&s.ContentHash, &s.LastScr, &s.PrintISSN, &s.EISSN, &s.DoajURL,
			&s.OJSURL, &s.Rank, &s.OJSStatus); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}

// e7gDiff = K1: HANYA ojs_url utk id ∈ ojsWant (nilai persis) yg boleh
// berubah; kolom lain & canonical IDENTIK (E7g tak menyentuhnya).
func e7gDiff(pre, post map[int64]e7gSnap, preC, postC map[int64]string,
	ojsWant map[int64]string) []string {
	var bad []string
	if len(pre) != len(post) {
		bad = append(bad, fmt.Sprintf("jumlah baris journals %d→%d", len(pre), len(post)))
	}
	for id, a := range pre {
		b, ok := post[id]
		if !ok {
			bad = append(bad, fmt.Sprintf("j%d hilang", id))
			continue
		}
		if a.Name != b.Name || a.ContentHash != b.ContentHash || a.LastScr != b.LastScr ||
			a.GarudaURL != b.GarudaURL || a.EISSN != b.EISSN || a.Rank != b.Rank ||
			a.OJSStatus != b.OJSStatus || a.SubjectArea != b.SubjectArea ||
			a.PrintISSN != b.PrintISSN || a.DoajURL != b.DoajURL {
			bad = append(bad, fmt.Sprintf("j%d kolom non-target berubah", id))
		}
		if a.OJSURL != b.OJSURL {
			want, dituju := ojsWant[id]
			if !dituju {
				bad = append(bad, fmt.Sprintf("j%d ojs_url berubah di luar target", id))
			} else if b.OJSURL != want {
				bad = append(bad, fmt.Sprintf("j%d ojs_url=%q, want %q", id, b.OJSURL, want))
			}
		}
	}
	for id := range ojsWant {
		if _, ada := pre[id]; !ada {
			bad = append(bad, fmt.Sprintf("j%d target tapi baris tak ada", id))
		}
	}
	// canonical: E7g tak boleh menyentuh sama sekali
	for id, ca := range preC {
		if postC[id] != ca {
			bad = append(bad, fmt.Sprintf("j%d canonical berubah", id))
		}
	}
	for id := range postC {
		if _, ada := preC[id]; !ada {
			bad = append(bad, fmt.Sprintf("j%d canonical baris baru", id))
		}
	}
	return bad
}

// e7gCounts = verifikasi akhir db (pola e7fCounts + agregat journal_urls).
func e7gCounts(dbPath string) (map[string]int64, map[string]int64, error) {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	agg := map[string]int64{}
	q := []struct{ key, sql string }{
		{"journals", `SELECT COUNT(*) FROM journals`},
		{"hash_unik", `SELECT COUNT(DISTINCT content_hash) FROM journals`},
		{"urls_total", `SELECT COUNT(*) FROM journal_urls`},
		{"urls_canon", `SELECT COUNT(*) FROM journal_urls WHERE is_canonical <> 0`},
		{"urls_checked", `SELECT COUNT(*) FROM journal_urls WHERE checked_at IS NOT NULL`},
		{"urls_200", `SELECT COUNT(*) FROM journal_urls WHERE http_status = 200`},
		// tiap jurnal: TRIM(ojs_url) saat ini pasti punya baris journal_urls
		// (matot belum diperbaiki = baris sinta; sudah diperbaiki = baris
		// kandidat terverifikasi) — kunci konsistensi journals ↔ journal_urls.
		// HITUNG YANG SUDAH TERTUTUP (EXISTS) = 261.
		{"ojs_url_ketutup", `
			SELECT COUNT(*) FROM journals j
			WHERE TRIM(COALESCE(j.ojs_url,'')) <> ''
			  AND EXISTS (SELECT 1 FROM journal_urls u
			              WHERE u.journal_id = j.id AND u.kind = 'ojs'
			                AND u.url = TRIM(j.ojs_url))`},
	}
	for _, x := range q {
		var n int64
		if err := db.QueryRow(x.sql).Scan(&n); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", x.key, err)
		}
		agg[x.key] = n
	}
	perSumber := map[string]int64{}
	rs, err := db.Query(`SELECT source, COUNT(*) FROM journal_urls GROUP BY source`)
	if err != nil {
		return nil, nil, err
	}
	defer rs.Close()
	for rs.Next() {
		var s string
		var n int64
		if err := rs.Scan(&s, &n); err != nil {
			return nil, nil, err
		}
		perSumber[s] = n
	}
	return agg, perSumber, rs.Err()
}

// e7gRingkas = ringkasan hitung utk json & laporan (diulang tiap run —
// stabil karena dihitung dari state resume).
func e7gRingkas(baris []e7gBaris, st *e7gState) map[string]int {
	r := map[string]int{}
	for _, e := range st.robots {
		r["host_robots"]++
		switch e.HTTP {
		case 200:
			r["robots_200"]++
		case 404, 410:
			r["robots_tanpa"]++
		default:
			r["robots_gagal"]++
		}
	}
	for _, b := range baris {
		e, ok := st.cek[b.ID]
		if !ok {
			r["cek_belum"]++
			continue
		}
		if e.Dari == "robots-tolak" {
			r["cek_dilarang"]++
		} else {
			r["cls_"+e.Cls]++
		}
		if garuda.E7gMati(e.Cls) {
			r["mati"]++
		}
	}
	for _, k := range st.kand {
		if k.Selesai {
			r["kand_selesai"]++
		}
		switch {
		case k.Terpilih != "":
			r["terpilih_"+k.Sumber]++
		case k.Selesai && k.Manual:
			r["manual"]++
		}
	}
	return r
}

// ---- driver utama -----------------------------------------------------------

func runE7G(dbPath, fixturesDir string, refresh bool, delayMin, delayMax time.Duration) error {
	start := time.Now().UTC().Format(time.RFC3339)

	// ---- 1. target & guard drift ------------------------------------------
	baris, err := e7gTarget(dbPath)
	if err != nil {
		return err
	}
	if len(baris) != e7gJumlahBaris {
		return fmt.Errorf("DRIFT target: %d baris ber-ojs_url (want %d) — review db dulu",
			len(baris), e7gJumlahBaris)
	}

	store, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("buka store: %w", err)
	}
	defer store.Close()

	// ---- 2. resume json ----------------------------------------------------
	jsonPath := filepath.Join(filepath.Dir(fixturesDir), "e7g-results.json")
	st := e7gState{
		robots: map[string]e7gRobotsHasil{},
		cek:    map[int64]e7gCekHasil{},
		kand:   map[int64]e7gKandHasil{},
	}
	adaResume := false
	if !refresh {
		if b, err := os.ReadFile(jsonPath); err == nil {
			var old e7gHasilJSON
			if json.Unmarshal(b, &old) == nil {
				adaResume = true
				for _, x := range old.Robots {
					key := x.Host
					if key == "" {
						key = e7gHost(x.Origin) // format lama (key=origin)
					}
					st.robots[key] = x
				}
				for _, x := range old.Cek {
					st.cek[x.ID] = x
				}
				for _, x := range old.Kandidat {
					st.kand[x.ID] = x
				}
				st.get = old.Get
				st.fillLama = old.Fill
				fmt.Printf("resume: robots=%d cek=%d kandidat=%d (%d GET) dari %s\n",
					len(st.robots), len(st.cek), len(st.kand),
					st.get.total(), filepath.Base(jsonPath))
			}
		}
	}
	// run pertama / -refresh: journal_urls harus bisa dijelaskan oleh resume
	// json (URL asli Tahap 1 hanya hidup di sana setelah repair) — selain itu
	// = data misteri → review dulu.
	if refresh || !adaResume {
		var n int64
		dbRO, err := e7fOpenRO(dbPath)
		if err == nil {
			_ = dbRO.QueryRow(`SELECT COUNT(*) FROM journal_urls`).Scan(&n)
			dbRO.Close()
		}
		if n > 0 {
			return fmt.Errorf("journal_urls terisi %d baris tanpa resume sah (%s) — -refresh setelah repair belum didukung (jejak URL asli ada di json); review dulu",
				n, map[bool]string{true: "-refresh", false: "run pertama"}[refresh])
		}
	}

	// validasi resume per baris: entry cek harus cocok dgn url db saat ini;
	// url berubah = perbaikan E7g sendiri (kandidat terpilih == url) →
	// dipakai ulang tanpa GET; selain itu → re-cek.
	var perluCek []e7gBaris
	for _, b := range baris {
		e, ada := st.cek[b.ID]
		k, adaK := st.kand[b.ID]
		if ada && (e.URL == b.Ojs || (adaK && k.Terpilih == b.Ojs)) {
			continue
		}
		perluCek = append(perluCek, b)
	}
	hostUrut := []string{}
	seenHost := map[string]bool{}
	for _, b := range perluCek {
		h := e7gHost(b.Ojs)
		if h == "" {
			return fmt.Errorf("j%d: ojs_url tanpa scheme/host: %q", b.ID, b.Ojs)
		}
		if !seenHost[h] {
			seenHost[h] = true
			hostUrut = append(hostUrut, h)
		}
	}
	// origin fetch per host (https-prefer) — utk baris dual-scheme & host
	// kandidat baru di Fase C (host kandidat di luar baris: pakai origin URL).
	originPerHost := e7gOriginPerHost(baris)
	if !adaResume && len(hostUrut) != e7gJumlahHost {
		return fmt.Errorf("DRIFT host: %d unik (want %d) — review db dulu",
			len(hostUrut), e7gJumlahHost)
	}

	// ---- 3. peta kandidat offline -----------------------------------------
	homeMap, homeTanpaLink, err := e7gHomeMap(store, fixturesDir)
	if err != nil {
		return err
	}
	doajMap, doajGagal := e7gDoajMap(baris, fixturesDir)
	oaMap, err := e7gOpenAlexMap(fixturesDir)
	if err != nil {
		return err
	}
	fmt.Printf("kandidat offline: home-view=%d (tanpa-link=%d) · doaj-ref=%d (parse-gagal=%d) · openalex-fixture=%d\n",
		len(homeMap), homeTanpaLink, len(doajMap), doajGagal, len(oaMap))

	// ---- 4. client + pagu + penyimpanan resume -----------------------------
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		return fmt.Errorf("buat dir fixture: %w", err)
	}
	fmt.Printf("== E7g verifikasi & repair URL OJS (pagu: robots %d + cek %d + kandidat %d = %d GET, delay %s-%s) ==\n",
		paguRobotsE7g, paguCekE7g, paguKandE7g,
		paguRobotsE7g+paguCekE7g+paguKandE7g, delayMin, delayMax)
	fmt.Printf("target: %d baris · %d host (perlu cek ulang: %d baris)\n",
		len(baris), len(hostUrut), len(perluCek))

	c := garuda.NewClient("api-e7g", uaDefault, delayMin, delayMax)

	getRobots := func() error {
		if st.get.Robots >= paguRobotsE7g {
			return fmt.Errorf("pagu robots (%d): %w", paguRobotsE7g, errPaguE7g)
		}
		st.get.Robots++
		return nil
	}
	getCek := func() error {
		if st.get.Cek >= paguCekE7g {
			return fmt.Errorf("pagu cek (%d): %w", paguCekE7g, errPaguE7g)
		}
		st.get.Cek++
		return nil
	}
	getKand := func() error {
		if st.get.Kand >= paguKandE7g {
			return fmt.Errorf("pagu kandidat (%d): %w", paguKandE7g, errPaguE7g)
		}
		st.get.Kand++
		return nil
	}

	simpan := func() error {
		rep := e7gHasilJSON{
			Dibuat: start,
			Pagu: map[string]int{
				"robots": paguRobotsE7g, "cek": paguCekE7g,
				"kandidat": paguKandE7g,
				"total":    paguRobotsE7g + paguCekE7g + paguKandE7g,
			},
			Get: st.get, Jumlah: len(baris), Hosts: len(hostUrut),
			Robots:   []e7gRobotsHasil{},
			Cek:      []e7gCekHasil{},
			Kandidat: []e7gKandHasil{},
			Ringkas:  e7gRingkas(baris, &st),
			Fill:     st.fillLama,
		}
		for _, x := range st.robots {
			rep.Robots = append(rep.Robots, x)
		}
		for _, b := range baris {
			if x, ok := st.cek[b.ID]; ok {
				rep.Cek = append(rep.Cek, x)
			}
		}
		for _, b := range baris {
			if x, ok := st.kand[b.ID]; ok {
				rep.Kandidat = append(rep.Kandidat, x)
			}
		}
		sort.Slice(rep.Robots, func(i, j int) bool { return rep.Robots[i].Origin < rep.Robots[j].Origin })
		sort.Slice(rep.Cek, func(i, j int) bool { return rep.Cek[i].ID < rep.Cek[j].ID })
		sort.Slice(rep.Kandidat, func(i, j int) bool { return rep.Kandidat[i].ID < rep.Kandidat[j].ID })
		w, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json: %w", err)
		}
		if err := os.WriteFile(jsonPath, w, 0o644); err != nil {
			return fmt.Errorf("tulis json: %w", err)
		}
		return nil
	}

	// fetchRobots = GET robots.txt utk origin host (1×/host) + simpan body
	// sbg fixture (pagu sudah dipesan pemanggil lewat ambil()).
	fetchRobots := func(host, origin string) error {
		body, status, _, _ := c.GetPlain(origin+"/robots.txt", "")
		hp := status
		if body == nil {
			hp = 0 // status 200 tapi body gagal → tanpa aturan andal → konservatif
		}
		if hp == 200 {
			if err := os.WriteFile(e7gFixtureRobots(fixturesDir, origin), body, 0o644); err != nil {
				return fmt.Errorf("tulis fixture robots %s: %w", origin, err)
			}
		}
		st.robots[host] = e7gRobotsHasil{Host: host, Origin: origin, HTTP: hp, Dari: "GET"}
		return nil
	}

	// ensureRobots = ambil robots utk HOST bila belum siap; ambil() =
	// reservasi pagu FASE PEMANGGIL (A: getRobots, C: getKand).
	ensureRobots := func(host, origin string, ambil func() error) error {
		if e, ada := st.robots[host]; ada {
			if e.HTTP != 200 {
				return nil
			}
			if _, err := os.Stat(e7gFixtureRobots(fixturesDir, e.Origin)); err == nil {
				return nil
			}
			// 200 tapi fixture hilang → fetch ulang
		}
		if err := ambil(); err != nil {
			return err
		}
		return fetchRobots(host, origin)
	}

	// bolehURL = evaluasi robots utk URL (1 robots/host — utk host dual-scheme
	// dipakai origin https-prefer; host baru (kandidat Fase C, di luar baris)
	// pakai origin URL itu sendiri). error hanya = pagu habis (pemanggil
	// memutuskan sendiri).
	bolehURL := func(rawURL string, ambil func() error) (bool, error) {
		host := e7gHost(rawURL)
		if host == "" {
			return false, nil // konservatif (fakta db: tak ada — semua http(s))
		}
		origin := originPerHost[host]
		if origin == "" {
			origin = garuda.RobotsOrigin(rawURL)
		}
		if origin == "" {
			return false, nil
		}
		if err := ensureRobots(host, origin, ambil); err != nil {
			return false, err
		}
		e := st.robots[host]
		switch e.HTTP {
		case 200:
			b, err := os.ReadFile(e7gFixtureRobots(fixturesDir, e.Origin))
			if err != nil {
				return false, nil // fixture hilang pasca-fetch = anomali → konservatif
			}
			return garuda.RobotsBoleh(garuda.ParseRobots(b), garuda.RobotsURI(rawURL), uaDefault), nil
		case 404, 410:
			return true, nil // tak ada file = tanpa pembatasan
		default:
			return false, nil // gagal/403/5xx → konservatif (jujur dicatat)
		}
	}

	// getURL = 1 GET (+1 retry utk transien; tiap percobaan makan pagu).
	getURL := func(ambil func() error, rawURL string) (e7gGetURL, error) {
		var h e7gGetURL
		for attempt := 1; attempt <= 2; attempt++ {
			if err := ambil(); err != nil {
				return h, err
			}
			body, status, final, gerr := c.GetPlain(rawURL, "")
			h.Status, h.Final, h.Upaya, h.Body = status, final, attempt, body
			h.Cls = garuda.KlasifikasiE7g(status, gerr)
			if gerr != nil {
				h.Err = gerr.Error()
			} else {
				h.Err = ""
			}
			if attempt == 1 && garuda.E7gRetry(h.Cls) {
				time.Sleep(2 * time.Second)
				continue
			}
			return h, nil
		}
		return h, nil
	}

	now := func() string { return time.Now().UTC().Format(time.RFC3339) }

	// ---- FASE A: robots.txt per host --------------------------------------
	fmt.Printf("\n-- Fase A: robots.txt (%d host perlu cek) --\n", len(hostUrut))
	for i, h := range hostUrut {
		if _, ada := st.robots[h]; ada {
			continue
		}
		origin := originPerHost[h]
		if origin == "" {
			return fmt.Errorf("Fase A [%d/%d] host %s: tanpa origin (drift)", i+1, len(hostUrut), h)
		}
		if err := ensureRobots(h, origin, getRobots); err != nil {
			_ = simpan()
			return fmt.Errorf("Fase A [%d/%d] %s: %w", i+1, len(hostUrut), h, err)
		}
		e := st.robots[h]
		note := "gagal"
		switch e.HTTP {
		case 200:
			note = "ada aturan"
		case 404, 410:
			note = "tanpa file"
		}
		fmt.Printf("[%d/%d] robots %-45s http=%-3d %s\n", i+1, len(hostUrut), e.Origin, e.HTTP, note)
		if (i+1)%25 == 0 {
			if err := simpan(); err != nil {
				return err
			}
		}
	}
	if err := simpan(); err != nil {
		return err
	}

	// ---- FASE B: cek 261 ojs_url ------------------------------------------
	fmt.Printf("\n-- Fase B: cek ojs_url (%d baris; resume %d) --\n",
		len(baris), len(baris)-len(perluCek))
	for i, b := range baris {
		if _, perlu := perluLookup(perluCek, b.ID); !perlu {
			continue
		}
		boleh, rerr := bolehURL(b.Ojs, getRobots)
		if rerr != nil {
			_ = simpan()
			return fmt.Errorf("Fase B j%d: %w", b.ID, rerr)
		}
		if !boleh {
			st.cek[b.ID] = e7gCekHasil{ID: b.ID, Nama: b.Nama, URL: b.Ojs,
				Dari: "robots-tolak"}
			fmt.Printf("[%3d/%d] j%-6d DILARANG ROBOTS (tanpa GET) %s\n",
				i+1, len(baris), b.ID, b.Nama)
			continue
		}
		h, gerr := getURL(getCek, b.Ojs)
		if gerr != nil {
			_ = simpan()
			return fmt.Errorf("Fase B j%d: %w", b.ID, gerr)
		}
		e := e7gCekHasil{
			ID: b.ID, Nama: b.Nama, URL: b.Ojs,
			HTTP: h.Status, Cls: h.Cls, Err: h.Err,
			CheckedAt: now(), Dari: "GET", Upaya: h.Upaya,
		}
		if h.Final != "" && h.Final != b.Ojs {
			e.Final = h.Final
		}
		st.cek[b.ID] = e
		fmt.Printf("[%3d/%d] j%-6d %-12s http=%-3d final=%-6s %s\n",
			i+1, len(baris), b.ID, h.Cls, h.Status, dipotong(h.Final, 60), b.Nama)
		if (i+1)%20 == 0 {
			if err := simpan(); err != nil {
				return err
			}
		}
	}
	// guard: semua 261 wajib punya keputusan sebelum lanjut
	lengkap := 0
	for _, b := range baris {
		if _, ok := st.cek[b.ID]; ok {
			lengkap++
		}
	}
	if lengkap != len(baris) {
		_ = simpan()
		return fmt.Errorf("Fase B belum lengkap: %d/%d (resume lagi)", lengkap, len(baris))
	}
	if err := simpan(); err != nil {
		return err
	}

	// ---- FASE C: rantai kandidat utk baris mati ----------------------------
	var mati []e7gBaris
	for _, b := range baris {
		e := st.cek[b.ID]
		if !garuda.E7gMati(e.Cls) {
			continue
		}
		if k, ok := st.kand[b.ID]; ok && k.Selesai {
			continue
		}
		mati = append(mati, b)
	}
	fmt.Printf("\n-- Fase C: rantai kandidat (%d baris mati, resume %d) --\n",
		len(mati), countSelesai(st.kand, baris))

	// prosesKandidat = rantai doc 17 §4.2 (home → OpenAlex → DOAJ → manual).
	prosesKandidat := func(b e7gBaris) e7gKandHasil {
		var k e7gKandHasil
		k.ID, k.Selesai = b.ID, true
		tambah := func(sumber, u string, h e7gGetURL, cat string) {
			k.Tahap = append(k.Tahap, e7gTahap{
				Sumber: sumber, URL: u, HTTP: h.Status, Final: h.Final,
				Cls: h.Cls, Err: h.Err, Catatan: cat,
			})
		}
		// coba = verify 1 kandidat (robots host → GET); true = TERPILIH/stop.
		coba := func(sumber, u string) bool {
			if u == "" || u == b.Ojs {
				return false
			}
			for _, t := range k.Tahap {
				if t.URL == u {
					return false
				}
			}
			boleh, rerr := bolehURL(u, getKand)
			if rerr != nil {
				k.Pagu, k.Selesai = true, false
				return true
			}
			if !boleh {
				k.Tahap = append(k.Tahap, e7gTahap{Sumber: sumber, URL: u,
					Catatan: "robots tolak"})
				return false
			}
			h, gerr := getURL(getKand, u)
			if gerr != nil {
				k.Pagu, k.Selesai = true, false
				return true
			}
			tambah(sumber, u, h, "")
			if h.Status == 200 {
				k.Terpilih, k.Sumber = u, sumber
				return true
			}
			return false
		}
		// akhir = satu jalan keluar supaya CheckedAt selalu terisi (dipakai
		// journal_urls.checked_at utk SEMUA kandidat yg kena rantai ini).
		akhir := func() e7gKandHasil {
			k.CheckedAt = now()
			return k
		}

		// 1) Garuda Home Page (fixture view — offline)
		if coba("garuda", homeMap[b.ID]) {
			return akhir()
		}
		// 2) OpenAlex homepage_url (fixture resume → GET API bila belum)
		if !k.Pagu && k.Terpilih == "" && b.Key != "" {
			oa := oaMap[b.Key]
			if oa == "" {
				apiURL := "https://api.openalex.org/sources/issn:" + garuda.HyphenISSN(b.Key)
				boleh, rerr := bolehURL(apiURL, getKand)
				switch {
				case rerr != nil:
					k.Pagu, k.Selesai = true, false
				case !boleh:
					k.Tahap = append(k.Tahap, e7gTahap{Sumber: "openalex",
						URL: apiURL, Catatan: "robots tolak"})
				default:
					h, gerr := getURL(getKand, apiURL)
					if gerr != nil {
						k.Pagu, k.Selesai = true, false
					} else {
						tambah("openalex", apiURL, h, "")
						if h.Status == 200 && h.Body != nil {
							fx := filepath.Join(fixturesDir, "e7g-openalex-"+b.Key+".json")
							if err := os.WriteFile(fx, h.Body, 0o644); err != nil {
								k.Tahap[len(k.Tahap)-1].Catatan = "tulis fixture: " + err.Error()
							} else if hp, perr := garuda.OpenAlexHomepage(h.Body); perr != nil {
								k.Tahap[len(k.Tahap)-1].Catatan = perr.Error()
							} else if hp != "" {
								oa = hp
								oaMap[b.Key] = hp
							}
						}
					}
				}
			}
			if !k.Pagu && k.Terpilih == "" && coba("openalex", oa) {
				return akhir()
			}
		}
		// 3) DOAJ ref.journal (fixture offline)
		if !k.Pagu && k.Terpilih == "" {
			if coba("doaj", doajMap[b.ID]) {
				return akhir()
			}
		}
		// 4) akhir: tanpa kandidat terverifikasi → butuh manual
		if !k.Pagu && k.Terpilih == "" {
			k.Manual = true
		}
		return akhir()
	}

	paguHabis := false
	for i, b := range mati {
		if paguHabis {
			break
		}
		k := prosesKandidat(b)
		st.kand[b.ID] = k
		ganti := "-"
		if k.Terpilih != "" {
			ganti = "TERPILIH " + k.Sumber
		} else if k.Pagu {
			ganti = "PAGU HABIS"
		} else if k.Manual {
			ganti = "MANUAL"
		}
		fmt.Printf("[%d/%d] j%-6d mati %-12s tahap=%d %s\n",
			i+1, len(mati), b.ID, st.cek[b.ID].Cls, len(k.Tahap), ganti)
		if k.Pagu {
			paguHabis = true
			fmt.Printf("     pagu kandidat habis — %d baris sisanya dilanjut run berikutnya\n",
				len(mati)-i-1)
		}
		if (i+1)%20 == 0 {
			if err := simpan(); err != nil {
				return err
			}
		}
	}
	if err := simpan(); err != nil {
		return err
	}

	// ---- 5. bangun rencana TULIS (0 GET) -----------------------------------
	plan, planSumber, nChecked, n200, ojsWant := e7gRencana(baris, st, homeMap, doajMap, oaMap)
	fmt.Printf("\nrencana tulis: journal_urls=%d baris (sumber: %v) · ojs_url diperbaiki=%d\n",
		len(plan), planSumber, len(ojsWant))
	if len(st.cek) < len(baris) {
		return fmt.Errorf("cek belum lengkap (%d/%d) — tak menulis", len(st.cek), len(baris))
	}

	// ---- 6. backup SEBELUM tulis ------------------------------------------
	bak := strings.TrimSuffix(dbPath, ".db") + ".preE7g.bak.db"
	if _, err := os.Stat(bak); err != nil {
		if err := salinFile(dbPath, bak); err != nil {
			return fmt.Errorf("backup db: %w", err)
		}
		fmt.Printf("backup db → %s\n", bak)
	} else {
		fmt.Printf("backup sudah ada (dipakai apa adanya): %s\n", bak)
	}
	pre, err := e7gSnapshot(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot PRE: %w", err)
	}
	preC, err := e7fSnapshotCanon(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot PRE canonical: %w", err)
	}

	// ---- 7. tulis idempoten + provenance (loop ke-2 wajib 0) ---------------
	type tulis struct{ urls, ojs, prov int }
	proses := func() (tulis, error) {
		var t tulis
		for _, r := range plan {
			n, err := store.UpsertJournalURL(r.JID, r.URL, "ojs", r.Source,
				r.Status, r.Final, r.Cek)
			if err != nil {
				return t, err
			}
			t.urls += int(n)
		}
		for id, g := range ojsWant {
			n, err := store.UpdateJournalsE7(id, "ojs_url", g.url)
			if err != nil {
				return t, fmt.Errorf("ojs_url j%d: %w", id, err)
			}
			if n > 0 {
				if err := store.MergeProvenance(id, "journals_ojs_url", storage.ProvEntry{
					Value: g.url, Source: g.sumber, RetrievedAt: start, Confidence: 1,
				}); err != nil {
					return t, fmt.Errorf("prov ojs_url j%d: %w", id, err)
				}
				t.ojs++
				t.prov++
			}
		}
		return t, nil
	}
	t1, err := proses()
	if err != nil {
		return fmt.Errorf("tulis: %w", err)
	}
	t2, err := proses()
	if err != nil {
		return fmt.Errorf("ulang (idempoten): %w", err)
	}
	if t2.urls+t2.ojs+t2.prov != 0 {
		return fmt.Errorf("IDEMPOTEN GAGAL: ulang mengubah urls=%d ojs=%d prov=%d (harus 0) — cek backup %s",
			t2.urls, t2.ojs, t2.prov, bak)
	}
	fmt.Printf("tulis: journal_urls=%d · ojs_url=%d · prov=%d · ulang=%d/%d/%d\n",
		t1.urls, t1.ojs, t1.prov, t2.urls, t2.ojs, t2.prov)

	// ---- 8. snapshot POST + diff K1 + counts -------------------------------
	post, err := e7gSnapshot(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot POST: %w", err)
	}
	postC, err := e7fSnapshotCanon(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot POST canonical: %w", err)
	}
	ojsWantMap := map[int64]string{}
	for id, g := range ojsWant {
		ojsWantMap[id] = g.url
	}
	diff := e7gDiff(pre, post, preC, postC, ojsWantMap)
	if len(diff) > 0 {
		return fmt.Errorf("VERIFIKASI GAGAL — perubahan di luar target: %v · RESTORE manual dari %s", diff, bak)
	}
	agg, perSumber, err := e7gCounts(dbPath)
	if err != nil {
		return fmt.Errorf("counts POST: %w", err)
	}
	if agg["journals"] != e7gJumlahBaris || agg["hash_unik"] != e7gJumlahBaris ||
		agg["urls_total"] != int64(len(plan)) ||
		agg["urls_canon"] != 0 ||
		agg["urls_checked"] != int64(nChecked) ||
		agg["urls_200"] != int64(n200) ||
		agg["ojs_url_ketutup"] != e7gJumlahBaris {
		return fmt.Errorf(
			"VERIFIKASI GAGAL counts: journals=%d hash_unik=%d urls_total=%d (want %d) urls_canon=%d urls_checked=%d (want %d) urls_200=%d (want %d) ojs_url_ketutup=%d (want %d) — RESTORE manual dari %s",
			agg["journals"], agg["hash_unik"], agg["urls_total"], len(plan),
			agg["urls_canon"], agg["urls_checked"], nChecked,
			agg["urls_200"], n200, agg["ojs_url_ketutup"], e7gJumlahBaris, bak)
	}
	for s, want := range planSumber {
		if perSumber[s] != want {
			return fmt.Errorf("VERIFIKASI GAGAL sumber %q: db=%d want=%d — RESTORE manual dari %s",
				s, perSumber[s], want, bak)
		}
	}
	if len(perSumber) != len(planSumber) {
		return fmt.Errorf("VERIFIKASI GAGAL sumber ekstra di db: %v (want %v) — RESTORE manual dari %s",
			perSumber, planSumber, bak)
	}

	// ---- 9. fill json final + ringkasan ------------------------------------
	fill := e7gFillJSON{
		Backup:       bak,
		Terpilih:     map[string]int{},
		OjsPerbaikan: t1.ojs, OjsUlang: t2.ojs,
		UrlsTulis: t1.urls, UrlsUlang: t2.urls,
		ProvTulis:  t1.prov,
		Verifikasi: map[string]string{}, DiffLarang: diff,
	}
	for _, e := range st.robots {
		switch e.HTTP {
		case 200:
			fill.Robots200++
		case 404, 410:
			fill.RobotsTanpa++
		default:
			fill.RobotsGagal++
		}
	}
	for _, b := range baris {
		if e, ok := st.cek[b.ID]; ok && e.Dari == "robots-tolak" {
			fill.Dilarang++
		}
	}
	sisaMati := 0
	for _, b := range baris {
		e, ok := st.cek[b.ID]
		if ok && garuda.E7gMati(e.Cls) {
			if k, okK := st.kand[b.ID]; !okK || !k.Selesai {
				sisaMati++
			}
		}
	}
	for _, k := range st.kand {
		if k.Selesai {
			fill.KandSelesai++
		}
		if k.Terpilih != "" {
			fill.Terpilih[k.Sumber]++
		} else if k.Selesai && k.Manual {
			fill.Manual++
		}
	}
	// jejak tulis kumulatif (pola E7f): rerun plan-flat tak menimpa jejak
	// fill asli — asumsi jujur: baris YANG SAMA tak pernah di-fill dua
	// siklus berbeda (bila db di-restore manual, json ikut dikoreksi).
	if lama := st.fillLama; lama != nil {
		fill.Backup = lama.Backup
		fill.OjsPerbaikan += lama.OjsPerbaikan
		fill.OjsUlang += lama.OjsUlang
		fill.UrlsTulis += lama.UrlsTulis
		fill.UrlsUlang += lama.UrlsUlang
		fill.ProvTulis += lama.ProvTulis
	}
	for k, n := range agg {
		fill.Verifikasi[k] = fmt.Sprintf("%d", n)
	}
	for s, n := range perSumber {
		fill.Verifikasi["sumber_"+s] = fmt.Sprintf("%d", n)
	}
	st.fillLama = &fill
	if err := simpan(); err != nil {
		return err
	}

	// ---- 10. ringkasan utk laporan ----------------------------------------
	r := e7gRingkas(baris, &st)
	fmt.Printf("\n== E7g SELESAI ==\n")
	fmt.Printf("GET: kumulatif robots=%d/%d cek=%d/%d kandidat=%d/%d total=%d/%d\n",
		st.get.Robots, paguRobotsE7g, st.get.Cek, paguCekE7g,
		st.get.Kand, paguKandE7g, st.get.total(),
		paguRobotsE7g+paguCekE7g+paguKandE7g)
	fmt.Printf("robots: 200=%d tanpa-file=%d gagal=%d · cek: %s\n",
		fill.Robots200, fill.RobotsTanpa, fill.RobotsGagal, ringkasCekStr(r))
	fmt.Printf("kandidat: mati=%d selesai=%d terpilih(garuda=%d openalex=%d doaj=%d) manual=%d sisa=%d\n",
		r["mati"], fill.KandSelesai, fill.Terpilih["garuda"], fill.Terpilih["openalex"],
		fill.Terpilih["doaj"], fill.Manual, sisaMati)
	fmt.Printf("tulis: journal_urls→%d · ojs_url diperbaiki→%d · prov→%d · ulang=%d/%d/%d\n",
		agg["urls_total"], t1.ojs, t1.prov, t2.urls, t2.ojs, t2.prov)
	fmt.Printf("VERIFIKASI OK · diff larangan=0 · urls_canon=0 · ojs_url ketutup=%d/%d\n",
		agg["ojs_url_ketutup"], e7gJumlahBaris)
	fmt.Printf("JSON: %s\n", jsonPath)
	if sisaMati > 0 || paguHabis {
		fmt.Printf("CATATAN: %d baris mati BELUM selesai rantainya (pagu) — jalankan -e7g lagi utk melanjut + tulis sisa.\n", sisaMati)
	}
	return nil
}

// ---- helper driver ----------------------------------------------------------

func perluLookup(baris []e7gBaris, id int64) (e7gBaris, bool) {
	for _, b := range baris {
		if b.ID == id {
			return b, true
		}
	}
	return e7gBaris{}, false
}

func countSelesai(kand map[int64]e7gKandHasil, baris []e7gBaris) int {
	n := 0
	for _, b := range baris {
		if k, ok := kand[b.ID]; ok && k.Selesai {
			n++
		}
	}
	return n
}

func dipotong(s string, n int) string {
	if s == "" {
		return "-"
	}
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func ringkasCekStr(r map[string]int) string {
	parts := []string{}
	for _, k := range []string{"cls_ok", "cls_404", "cls_410", "cls_waf",
		"cls_server", "cls_dns", "cls_timeout", "cls_unreachable", "cls_lain"} {
		if r[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", strings.TrimPrefix(k, "cls_"), r[k]))
		}
	}
	if r["cek_dilarang"] > 0 {
		parts = append(parts, fmt.Sprintf("dilarang-robots=%d", r["cek_dilarang"]))
	}
	if r["cek_belum"] > 0 {
		parts = append(parts, fmt.Sprintf("belum=%d", r["cek_belum"]))
	}
	return strings.Join(parts, " ")
}

type e7gGanti struct {
	url    string
	sumber string
}

// e7gRencana = rencana tulis journal_urls + peta repair ojs_url (0 GET).
// Mengembalikan baris rencana, agregat per-sumber, jumlah checked/200, dan
// target perbaikan ojs_url.
func e7gRencana(baris []e7gBaris, st e7gState, homeMap map[int64]string,
	doajMap map[int64]string, oaMap map[string]string) ([]e7gRencanaURL, map[string]int64, int, int, map[int64]e7gGanti) {

	intPtr := func(n int) *int { return &n }
	plan := []e7gRencanaURL{}
	sumber := map[string]int64{}
	nChecked, n200 := 0, 0
	ojsWant := map[int64]e7gGanti{}
	catat := func(s string) {
		sumber[s]++
	}

	for _, b := range baris {
		e := st.cek[b.ID]
		k := st.kand[b.ID] // nol = tanpa rantai (baris tak mati)

		// -- baris "sinta": URL yang dicek (stabil lintas rerun) --
		var status *int
		final := ""
		cek := ""
		if e.Dari != "robots-tolak" && e.Dari != "" {
			if e.HTTP > 0 {
				status = intPtr(e.HTTP)
			}
			cek = e.CheckedAt
			if e.Final != "" && e.Final != e.URL {
				final = e.Final
			}
		}
		plan = append(plan, e7gRencanaURL{
			JID: b.ID, URL: e.URL, Source: "sinta",
			Status: status, Final: final, Cek: cek,
		})
		catat("sinta")

		// -- kandidat (dedup, urutan deterministik) --
		type kandCari struct{ url, source string }
		var daftar []kandCari
		if u := homeMap[b.ID]; u != "" && u != e.URL {
			daftar = append(daftar, kandCari{u, "garuda"})
		}
		if u := doajMap[b.ID]; u != "" && u != e.URL {
			daftar = append(daftar, kandCari{u, "doaj"})
		}
		if b.Key != "" {
			if u := oaMap[b.Key]; u != "" && u != e.URL {
				daftar = append(daftar, kandCari{u, "openalex"})
			}
		}
		if k.Terpilih != "" && k.Terpilih != e.URL {
			daftar = append(daftar, kandCari{k.Terpilih, k.Sumber})
		}
		seen := map[string]bool{e.URL: true}
		for _, kc := range daftar {
			if seen[kc.url] {
				continue
			}
			seen[kc.url] = true

			src := kc.source
			var st_ *int
			fin := ""
			ck := ""
			switch {
			case k.Terpilih != "" && kc.url == k.Terpilih:
				src = k.Sumber
				st_ = intPtr(200)
				ck = k.CheckedAt
				for _, t := range k.Tahap {
					if t.URL == kc.url && t.Final != "" && t.Final != kc.url {
						fin = t.Final
					}
				}
			default:
				for _, t := range k.Tahap {
					if t.URL != kc.url {
						continue
					}
					src = t.Sumber
					if t.Catatan == "robots tolak" {
						// tak pernah di-GET → status & checked_at kosong
					} else if t.HTTP > 0 {
						st_ = intPtr(t.HTTP)
						ck = k.CheckedAt
						if t.Final != "" && t.Final != kc.url {
							fin = t.Final
						}
					} else {
						// pernah di-GET tapi jaringan gagal → checked, tanpa status
						ck = k.CheckedAt
					}
					break
				}
			}
			plan = append(plan, e7gRencanaURL{
				JID: b.ID, URL: kc.url, Source: src,
				Status: st_, Final: fin, Cek: ck,
			})
			catat(src)
		}

		// -- repair ojs_url: matot + kandidat terverifikasi 200 --
		if k.Terpilih != "" && garuda.E7gMati(e.Cls) && k.Terpilih != e.URL {
			ojsWant[b.ID] = e7gGanti{url: k.Terpilih, sumber: k.Sumber}
		}
	}
	for _, p := range plan {
		if p.Cek != "" {
			nChecked++
		}
		if p.Status != nil && *p.Status == 200 {
			n200++
		}
	}
	return plan, sumber, nChecked, n200, ojsWant
}
