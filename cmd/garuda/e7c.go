package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"
	"sinta-scraper/internal/storage"
)

// runE7C = Q4 E7c (doc 30 §14.6 sub-fase 3 — plan APPROVED user 6 Okt 2026):
// MISS-8 multi-sumber LIVE → hasil **Opsi A** (butir 3: status enrichment
// TETAP `not_found` by Garuda + isi field alternatif dgn provenance sumber).
//
// Sumber & pagu (A20 sisa 18 — 2 sudah terpakai verifikasi hyphen E6):
//   - Crossref by-ISSN 8 (identitas: judul/penerbit/ISSN+type/DOIs; key =
//     E-ISSN kanonik, fallback P-ISSN saat 404);
//   - OAI Identify+ListMetadataFormats utk j673 (base E5 investigasi, doc 34
//     §4.2) & j689 (BELUM pernah di-probe — 3 kandidat base, stop saat
//     Identify valid);
//   - j60: search Garuda "De Jure" + view/N kandidat baru (≤1 GET) → tie
//     year-range vs fixture view-5276 (metode resmi E4b);
//   - Q3 DOAJ 2 (kandidat doc 37: AGRIVITA 23026766, Sosio 24428094) —
//     hyphen-first, kanonik dari margin → artefak JSON (bukan miss-8,
//     TANPA tulis db);
//   - margin 2 (sisa A20 setelah rencana 16).
//
// Mekanisme (teliti): guard not_found=10 SEBELUM GET → stage A–D (resume
// fixture-first, pagu kumulatif lintas run) → checkpoint JSON → backup
// .preE7c.bak.db → tulis provenance 2-pass (pass-2 wajib byte-identik) →
// verifikasi: journals hash identik + kolom enrichment non-prov identik +
// prov lama utuh + counts → e7c-results.json.
//
// Pagu TERPOTONG (errPaguE7c) ≠ gagal: data yg sudah didapat TETAP ditulis
// (honest), flag terpotong=true di JSON utk review.
const (
	paguE7c   = 18
	e7cOutPth = "data/stage2/e7c-results.json"
)

var errPaguE7c = errors.New("PAGU E7C HABIS")

// e7cMiss = 8 id not_found E4b yg dikejar (j679/j696 final — user 4 Okt;
// 10 not_found total → guard pakai jumlah globalnya).
var e7cMiss = []int64{60, 673, 687, 688, 689, 948, 3203, 3974}

// ---------- struktur hasil (JSON) ----------

type e7cJSON struct {
	Dibuat        string            `json:"dibuat"`
	Mode          string            `json:"mode"`
	Pagu          int               `json:"pagu_get"`
	GetRun        int               `json:"get_run_ini"`
	GetKumulatif  int               `json:"get_kumulatif"`
	Terpotong     bool              `json:"terpotong"`
	Backup        string            `json:"backup,omitempty"`
	Target        int               `json:"target_miss"`
	ProvTulis     int               `json:"prov_tulis"`
	ProvIdempoten string            `json:"prov_idempoten,omitempty"`
	Verifikasi    map[string]string `json:"verifikasi,omitempty"`
	DiffLarang    []string          `json:"diff_larangan,omitempty"`
	Items         []e7cItem         `json:"items"`
	Q3            []e7cQ3           `json:"q3_doaj"`
}

// e7cItem = satu baris miss-8 dgn hasil per sumber (resume = sumber kebenaran;
// identitas SELALU re-derive dari db).
type e7cItem struct {
	ID       int64            `json:"journal_id"`
	Nama     string           `json:"nama"`
	PISSN    string           `json:"print_issn"`
	EISSN    string           `json:"electronic_issn"`
	OjsURL   string           `json:"ojs_url"`
	Crossref map[string]e7cXR `json:"crossref,omitempty"` // key = ISSN kanonik
	OAI      *e7cOAI          `json:"oai,omitempty"`      // j673/j689
	View     *e7cView         `json:"view,omitempty"`     // j60
	Prov     []e7cProvRec     `json:"prov,omitempty"`
}

type e7cXR struct {
	HTTP      int               `json:"http"`
	Hasil     garuda.E6Crossref `json:"hasil"`
	IssnType  map[string]string `json:"issn_type,omitempty"`
	TitleSama bool              `json:"title_sama,omitempty"`
}

type e7cOAI struct {
	Sumber     string       `json:"sumber"`
	Kandidat   []e7cOAIBase `json:"kandidat"` // urutan dicoba (resume = sudah-tried)
	BaseMenang string       `json:"base_menang,omitempty"`
	Repo       string       `json:"repo,omitempty"`
	LMFHTTP    int          `json:"lmf_http,omitempty"`
	Formats    []string     `json:"formats,omitempty"`
	Error      string       `json:"error,omitempty"`
}

type e7cOAIBase struct {
	URL      string `json:"url"`
	Dari     string `json:"dari"` // fixture-e5 | live
	HTTP     int    `json:"http"`
	Identify bool   `json:"identify"`
	Repo     string `json:"repo,omitempty"`
	Error    string `json:"error,omitempty"`
}

type e7cView struct {
	SearchURL  string               `json:"search_url"`
	SearchHTTP int                  `json:"search_http"`
	Kandidat   []int                `json:"kandidat"`
	Dilewati   []int                `json:"kandidat_dilewati,omitempty"` // di luar kuota ≤1 view baru — dicatat, TIDAK di-retry
	Pages      map[string]e7cVPages `json:"pages"`                       // id view → hasil
	Menang     int                  `json:"menang"`                      // -1 = tak ada
	Alasan     string               `json:"alasan,omitempty"`
	Error      string               `json:"error,omitempty"`
}

type e7cVPages struct {
	HTTP      int    `json:"http"`
	Dari      string `json:"dari"` // fixture-e3 | fixture-e7c | live
	NotFound  bool   `json:"not_found"`
	YearFrom  int    `json:"year_from,omitempty"`
	YearTo    int    `json:"year_to,omitempty"`
	Title     string `json:"title,omitempty"`
	Publisher string `json:"publisher,omitempty"`
	PrintISSN string `json:"print_issn,omitempty"`
	EISSN     string `json:"eissn,omitempty"`
	Error     string `json:"error,omitempty"`
}

type e7cQ3 struct {
	ISSN   string       `json:"issn"` // kanonik
	Nama   string       `json:"nama"`
	Bentuk []e7cDOAJTry `json:"bentuk"` // urutan: hyphen dulu → kanonik
}

type e7cDOAJTry struct {
	Form      string        `json:"form"` // bentuk persis di URL
	HTTP      int           `json:"http"`
	Hasil     garuda.E6DOAJ `json:"hasil"`
	TitleSama bool          `json:"title_sama,omitempty"`
}

type e7cProvRec struct {
	Field       string  `json:"field"`
	Value       string  `json:"value"`
	Source      string  `json:"source"`
	Confidence  float64 `json:"confidence"`
	RetrievedAt string  `json:"retrieved_at"`
}

// e7cProvPlan = rencana tulis provenance (di luar JSON — struktur driver).
type e7cProvPlan struct {
	id    int64
	field string
	entry storage.ProvEntry
}

// e7cQ3Daftar = kandidat Q3 doc 37 §3/§4 (ISSN ketiga AGRIVITA & Sosio —
// Q1/Q2 nol). Artefak E7c = JSON + fixture (BUKAN miss-8 → tanpa tulis db;
// Sosio bahkan tak punya baris di db enrich 261 — pool E6 S2/S3).
var e7cQ3Daftar = []struct {
	Issn string
	Nama string
}{
	{"23026766", "AGRIVITA, Journal of Agricultural Science"},
	{"24428094", "Sosio Informa"},
}

// ---------- driver ----------

func runE7C(dbPath, fixturesDir string, refresh bool, delayMin, delayMax time.Duration) error {
	start := time.Now().UTC().Format(time.RFC3339)

	// ---- 1. baca target + guard (READ-ONLY, sebelum buang GET) ----------
	rows, err := e7cBacaTarget(dbPath)
	if err != nil {
		return fmt.Errorf("baca target: %w", err)
	}
	if n, err := e7cNotFound(dbPath); err != nil {
		return err
	} else if n != 10 {
		return fmt.Errorf("DRIFT: not_found=%d (want 10) — berhenti sebelum GET/tulis; review dulu", n)
	}
	fmt.Printf("target: %d miss (guard not_found=10 OK)\n", len(rows))

	// ---- 2. resume dari e7c-results.json (get kumulatif) ----------------
	// Pagu A20 dihormati LINTAS RUN (GetKumulatif SELALU dibaca walau
	// -refresh) — refresh = ulang request, jadi dia butuh pagu BARU; kalau
	// pagu lama sudah habis, gagal cepat (jangan timpa hasil lama dgn kosong).
	old := map[int64]e7cItem{}
	var getLama int
	var q3Lama []e7cQ3
	if b, err := os.ReadFile(e7cOutPth); err == nil {
		var o e7cJSON
		if json.Unmarshal(b, &o) == nil {
			getLama = o.GetKumulatif
			if !refresh {
				for _, it := range o.Items {
					old[it.ID] = it
				}
				q3Lama = o.Q3
				fmt.Printf("resume: %d hasil lama + %d Q3 (%d GET kumulatif) dari %s\n",
					len(old), len(q3Lama), getLama, filepath.Base(e7cOutPth))
			}
		}
	}
	if refresh && getLama >= paguE7c {
		return fmt.Errorf("pagu E7c habis (%d/%d) — -refresh butuh pagu baru; naikkan paguE7c dgn approve user dulu", getLama, paguE7c)
	}
	rep := e7cJSON{
		Dibuat:     start,
		Mode:       "E7c miss-8 multi-sumber LIVE — Opsi A (prov alt_* saja, status not_found utuh)",
		Pagu:       paguE7c,
		Target:     len(rows),
		Verifikasi: map[string]string{},
		Items:      make([]e7cItem, 0, len(rows)),
		Q3:         q3Lama,
	}
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		return fmt.Errorf("buat dir fixture: %w", err)
	}

	fmt.Printf("== E7c miss-8 multi-sumber (pagu %d GET kumulatif, delay %s–%s) ==\n",
		paguE7c, delayMin, delayMax)

	c := garuda.NewClient("api-e7c", uaDefault, delayMin, delayMax)
	var getTotal int
	// get = pagu-aware wrapper. SEMANTIK ERR DISEDERHANAKAN agar tak ada
	// panggilan stage yg salah anggap 404 sbg fatal:
	//   - err != nil HANYA utk pagu (errPaguE7c) ATAU jaringan (status=0);
	//   - status non-200 (404/429/5xx) = data jujur → (nil, status, nil)
	//     — caller memproses lewat switch status.
	get := func(rawURL, referer string) ([]byte, int, error) {
		if getLama+getTotal >= paguE7c {
			return nil, 0, fmt.Errorf("pagu E7c (%d): %w", paguE7c, errPaguE7c)
		}
		getTotal++
		body, status, err := c.Get(rawURL, referer)
		if err == nil {
			return body, status, nil
		}
		if status == 0 {
			return nil, 0, err // jaringan → caller catat unreachable (HTTP 0)
		}
		return nil, status, nil // 404/429/5xx habis-retry → serah ke caller
	}

	// siapkan item (identitas SELALU dari db)
	for _, r := range rows {
		it := e7cItem{ID: r.id, Nama: r.nama, PISSN: r.pissn, EISSN: r.eissn, OjsURL: r.ojsURL}
		if !refresh {
			if o, ok := old[r.id]; ok {
				it.Crossref, it.OAI, it.View = o.Crossref, o.OAI, o.View
			}
		}
		rep.Items = append(rep.Items, it)
	}

	terpotong := false
	stage := func(name string, fn func() error) error {
		if terpotong {
			return nil
		}
		if err := fn(); err != nil {
			if errors.Is(err, errPaguE7c) {
				terpotong = true
				fmt.Printf("!! pagu habis di %s — sisakan data yg sudah ada (terpotong)\n", name)
				return nil
			}
			return err
		}
		return nil
	}

	// ---- 3. stage A: Crossref 8 -----------------------------------------
	if err := stage("crossref", func() error {
		return e7cStageCrossref(get, func() int { return paguE7c - getLama - getTotal },
			fixturesDir, rep.Items, refresh)
	}); err != nil {
		return err
	}
	// ---- 4. stage B: OAI j673/j689 --------------------------------------
	if err := stage("oai", func() error { return e7cStageOAI(get, fixturesDir, rep.Items, refresh) }); err != nil {
		return err
	}
	// ---- 5. stage C: j60 search + view ----------------------------------
	if err := stage("view", func() error { return e7cStageView(get, fixturesDir, rep.Items, refresh) }); err != nil {
		return err
	}
	// ---- 6. stage D: Q3 DOAJ (hyphen-first) -----------------------------
	if err := stage("q3-doaj", func() error { return e7cStageQ3(get, fixturesDir, &rep.Q3, refresh) }); err != nil {
		return err
	}

	// ---- 7. bangun rencana provenance (Opsi A) --------------------------
	retrNow := time.Now().UTC().Format(time.RFC3339)
	plan := e7cBangunPlan(rep.Items, retrNow)
	fmt.Printf("rencana prov: %d entri alt_* (crossref/oai/view)\n", len(plan))
	for i := range rep.Items {
		rep.Items[i].Prov = nil // rencana final ditulis ulang setelah sukses
	}

	// checkpoint JSON SEBELUM tulis db (data GET aman walau tulis gagal)
	rep.GetRun, rep.GetKumulatif, rep.Terpotong = getTotal, getLama+getTotal, terpotong
	if err := e7cTulisJSON(&rep); err != nil {
		return err
	}

	// ---- 8. guard ulang + backup + tulis prov 2-pass --------------------
	if n, err := e7cNotFound(dbPath); err != nil {
		return err
	} else if n != 10 {
		return fmt.Errorf("DRIFT pra-tulis: not_found=%d (want 10) — RESTORE review; json tetap %s", n, e7cOutPth)
	}
	bak := strings.TrimSuffix(dbPath, ".db") + ".preE7c.bak.db"
	if _, err := os.Stat(bak); err != nil {
		if err := salinFile(dbPath, bak); err != nil {
			return fmt.Errorf("backup db: %w", err)
		}
		fmt.Printf("backup db → %s\n", bak)
	} else {
		fmt.Printf("backup sudah ada (dipakai apa adanya): %s\n", bak)
	}
	rep.Backup = bak

	store, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("buka store: %w", err)
	}
	// Rekonsiliasi RetrievedAt LINTAS RUN: bila Value/Source/Confidence
	// identik dgn entri yg sudah ada (kasus rerun/resume — TANPA GET baru),
	// pertahankan waktu-ambil LAMA — jangan mundurkan timestamp hanya karena
	// di-merge ulang. Tanpa ini: rerun selalu "mengubah" prov → diff K1 gagal
	// & idempoten lintas-run rusak (bug terbukti rerun 6 Okt).
	for i := range plan {
		m, err := store.Provenance(plan[i].id)
		if err != nil {
			continue // baris belum ada / JSON lama tak valid → pakai retrNow
		}
		if lama, ok := m[plan[i].field]; ok &&
			lama.Value == plan[i].entry.Value &&
			lama.Source == plan[i].entry.Source &&
			lama.Confidence == plan[i].entry.Confidence &&
			lama.RetrievedAt != "" {
			plan[i].entry.RetrievedAt = lama.RetrievedAt
		}
	}
	preEnrich, err := e7cSnapEnrich(dbPath)
	if err != nil {
		store.Close()
		return fmt.Errorf("snapshot PRE enrichment: %w", err)
	}
	preHash, err := e7cHashJournals(dbPath)
	if err != nil {
		store.Close()
		return fmt.Errorf("hash journals PRE: %w", err)
	}

	tulisPass := func() (map[int64]string, error) {
		for _, p := range plan {
			if err := store.MergeProvenance(p.id, p.field, p.entry); err != nil {
				return nil, fmt.Errorf("prov j%d %s: %w", p.id, p.field, err)
			}
		}
		return e7cProvMap(dbPath, plan)
	}
	mid, err := tulisPass()
	store.Close()
	if err != nil {
		return fmt.Errorf("tulis pass-1: %w", err)
	}
	// pass-2 (idempoten): merge ulang dgn entry identik → JSON wajib sama
	store2, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("buka store pass-2: %w", err)
	}
	for _, p := range plan {
		if err := store2.MergeProvenance(p.id, p.field, p.entry); err != nil {
			store2.Close()
			return fmt.Errorf("prov pass-2 j%d %s: %w", p.id, p.field, err)
		}
	}
	store2.Close()
	after, err := e7cProvMap(dbPath, plan)
	if err != nil {
		return fmt.Errorf("prov map POST: %w", err)
	}
	for id, v := range mid {
		if after[id] != v {
			return fmt.Errorf("IDEMPOTEN GAGAL: prov j%d berubah di pass-2 — RESTORE %s", id, bak)
		}
	}
	rep.ProvTulis = len(plan)
	rep.ProvIdempoten = "byte-identik"
	fmt.Printf("tulis: prov=%d · idempoten pass-2=byte-identik\n", len(plan))

	// ---- 9. verifikasi K1 ----------------------------------------------
	diff, err := e7cDiffEnrich(dbPath, preEnrich, plan)
	if err != nil {
		return fmt.Errorf("verifikasi enrichment: %w", err)
	}
	rep.DiffLarang = diff
	postHash, err := e7cHashJournals(dbPath)
	if err != nil {
		return err
	}
	v, err := e7bCounts(dbPath)
	if err != nil {
		return err
	}
	nf, err := e7cNotFound(dbPath)
	if err != nil {
		return err
	}
	rep.Verifikasi = map[string]string{
		"journals_hash":  "identik",
		"not_found":      strconv.Itoa(nf),
		"journals":       strconv.FormatInt(v["journals"], 10),
		"subj_kosong":    strconv.FormatInt(v["subj_kosong"], 10),
		"hash_unik":      strconv.FormatInt(v["hash_unik"], 10),
		"url_beda":       strconv.FormatInt(v["url_beda"], 10),
		"prov_idempoten": "byte-identik",
	}
	if postHash != preHash {
		rep.Verifikasi["journals_hash"] = "BERUBAH!"
		rep.DiffLarang = append(rep.DiffLarang, "journals table berubah (prov-only run!)")
	}
	if v["journals"] != 261 || v["subj_kosong"] != 41 || v["hash_unik"] != 261 || v["url_beda"] != 0 {
		rep.DiffLarang = append(rep.DiffLarang, fmt.Sprintf(
			"counts berubah: journals=%d subj_kosong=%d hash_unik=%d url_beda=%d",
			v["journals"], v["subj_kosong"], v["hash_unik"], v["url_beda"]))
	}
	if nf != 10 {
		rep.DiffLarang = append(rep.DiffLarang, fmt.Sprintf("not_found=%d (want 10)", nf))
	}

	// ---- 10. finalisasi -------------------------------------------------
	// tulis prov rec audit ke items (dari plan)
	perID := map[int64][]e7cProvRec{}
	for _, p := range plan {
		perID[p.id] = append(perID[p.id], e7cProvRec{
			Field: p.field, Value: p.entry.Value, Source: p.entry.Source,
			Confidence: p.entry.Confidence, RetrievedAt: p.entry.RetrievedAt,
		})
	}
	for i := range rep.Items {
		rep.Items[i].Prov = perID[rep.Items[i].ID]
	}
	if err := e7cTulisJSON(&rep); err != nil {
		return err
	}

	// ---- laporan stdout -------------------------------------------------
	e7cLapor(&rep, getTotal)
	if len(rep.DiffLarang) > 0 {
		return fmt.Errorf("VERIFIKASI GAGAL — diff larangan: %v · RESTORE manual dari %s", rep.DiffLarang, bak)
	}
	return nil
}

// ---------- stage A: Crossref by-ISSN -------------------------------------

func e7cStageCrossref(get func(string, string) ([]byte, int, error), sisa func() int, fixturesDir string, items []e7cItem, refresh bool) error {
	fmt.Println("\n== Stage A — Crossref by-ISSN (8, key=E-ISSN kanonik, fallback P saat 404) ==")
	for i := range items {
		it := &items[i]
		if it.Crossref == nil {
			it.Crossref = map[string]e7cXR{}
		}
		keys := e7cXRKeys(it)
		for _, k := range keys {
			// resume: hasil lama dipakai apa adanya — KECUALI HTTP=0
			// (unreachable/transien) → GET ulang.
			if old, ok := it.Crossref[k]; ok && !refresh && old.HTTP != 0 {
				if old.HTTP == 200 {
					if fb, err := os.ReadFile(e7cFix(fixturesDir, "crossref", k+".json")); err == nil {
						if p, perr := garuda.ParseCrossrefJournals(fb); perr == nil {
							p.HTTP = 200
							old.Hasil, old.IssnType = p, e7cIssnType(fb)
						}
					}
				}
				it.Crossref[k] = old
				if old.Hasil.Tersedia {
					break
				}
				continue
			}
			// fixture-first (crash resume tanpa json)
			if !refresh {
				if fb, err := os.ReadFile(e7cFix(fixturesDir, "crossref", k+".json")); err == nil {
					if p, perr := garuda.ParseCrossrefJournals(fb); perr == nil {
						p.HTTP = 200
						it.Crossref[k] = e7cXR{HTTP: 200, Hasil: p, IssnType: e7cIssnType(fb)}
						if p.Tersedia {
							break
						}
						continue
					}
				}
			}
			// kandidat CADANGAN (key ke-2, fallback P saat 404): hanya diambil
			// bila sisa pagu > 8 — sisakan 8 utk stage B–D (OAI ≤4 + view ≤2 +
			// Q3 ≤2 = komposisi rencana A20). Lewati = dicatat jujur di hasil.
			s := sisa()
			if k != keys[0] && s <= 8 {
				it.Crossref[k] = e7cXR{HTTP: 0, Hasil: garuda.E6Crossref{
					Error: fmt.Sprintf("dilewati: sisa pagu %d ≤ 8 (cadangan stage OAI/view/Q3)", s)}}
				fmt.Printf("  j%-5d %-10s cadangan dilewati (sisa pagu %d ≤ 8)\n", it.ID, k, s)
				continue
			}
			body, status, err := get("https://api.crossref.org/journals/"+k, "")
			if err != nil && !errors.Is(err, errPaguE7c) && status == 0 {
				it.Crossref[k] = e7cXR{HTTP: 0, Hasil: garuda.E6Crossref{Error: "unreachable: " + err.Error()}}
				fmt.Printf("  j%-5d %-10s unreachable: %v\n", it.ID, k, err)
				continue
			}
			if err != nil {
				return err // pagu
			}
			xr := e7cXR{HTTP: status}
			switch {
			case status == 200:
				if werr := os.WriteFile(e7cFix(fixturesDir, "crossref", k+".json"), body, 0o644); werr != nil {
					return fmt.Errorf("tulis fixture crossref %s: %w", k, werr)
				}
				p, perr := garuda.ParseCrossrefJournals(body)
				if perr != nil {
					p.Error = perr.Error()
				}
				p.HTTP = 200
				xr.Hasil, xr.IssnType = p, e7cIssnType(body)
			case status == 404:
				xr.Hasil = garuda.E6Crossref{HTTP: 404} // tak terdaftar = jujur, bukan error
			default:
				xr.Hasil = garuda.E6Crossref{HTTP: status, Error: fmt.Sprintf("http %d", status)}
			}
			it.Crossref[k] = xr
			if xr.Hasil.Tersedia {
				break // stop saat found (E6 pola)
			}
		}
		// ringkas baris (cari Tersedia dulu; kalau nol, lapor key terakhir
		// yg benar-benar dicoba — bukan hanya key terakhir)
		ada := false
		for _, k := range keys {
			if xr, ok := it.Crossref[k]; ok && xr.Hasil.Tersedia {
				fmt.Printf("  j%-5d %-44.44s key=%-9s dois=%-4d judul=%q\n",
					it.ID, it.Nama, k, xr.Hasil.DoiTotal, xr.Hasil.Judul)
				ada = true
				break
			}
		}
		if !ada {
			for j := len(keys) - 1; j >= 0; j-- {
				if xr, ok := it.Crossref[keys[j]]; ok {
					note := fmt.Sprintf("http %d", xr.HTTP)
					if xr.Hasil.Error != "" {
						note += " " + xr.Hasil.Error
					}
					fmt.Printf("  j%-5d %-44.44s TIDAK terdaftar/dilewati (%s utk %s)\n",
						it.ID, it.Nama, note, keys[j])
					break
				}
			}
		}
	}
	return nil
}

// e7cXRKeys = [E-ISSN kanonik, P-ISSN kanonik] unik non-kosong (urut tetap).
func e7cXRKeys(it *e7cItem) []string {
	a := garuda.NormISSN(it.EISSN)
	b := garuda.NormISSN(it.PISSN)
	out := []string{}
	if a != "" {
		out = append(out, a)
	}
	if b != "" && b != a {
		out = append(out, b)
	}
	return out
}

// e7cIssnType = map ISSN → label `issn-type` Crossref (bukti label E/P).
func e7cIssnType(body []byte) map[string]string {
	var env struct {
		Message struct {
			IssnType []struct {
				Value string `json:"value"`
				Type  string `json:"type"`
			} `json:"issn-type"`
		} `json:"message"`
	}
	if json.Unmarshal(body, &env) != nil {
		return nil
	}
	out := map[string]string{}
	for _, t := range env.Message.IssnType {
		if n := garuda.NormISSN(t.Value); n != "" && t.Type != "" {
			out[n] = t.Type
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---------- stage B: OAI Identify + ListMetadataFormats -------------------

// e7cOAIKandidat = base OAI per jurnal (fakta, bukan tebakan):
//   - j673: base E5 investigasi (doc 34 §4.2 — jpath index.php/journal, ✓ aktif);
//   - j689: BELUM pernah di-probe (doc 36 §4 salah tulis "E5") → 3 kandidat
//     urut: pola E5 (norm ojs_url + /oai) → root alias → jpath OJS.
func e7cOAIKandidat(jid int64) (string, []string) {
	switch jid {
	case 673:
		return "E5 investigasi doc 34 §4.2 (jpath index.php/journal)", []string{
			"http://www.journal.teflin.org/index.php/journal/oai",
		}
	case 689:
		return "belum pernah di-probe — kandidat: pola E5 (ojs_url+/oai), root alias, jpath", []string{
			"http://ijtech.eng.ui.ac.id/archives/oai",
			"http://ijtech.eng.ui.ac.id/oai",
			"http://ijtech.eng.ui.ac.id/index.php/ijtech/oai",
		}
	}
	return "", nil
}

func e7cStageOAI(get func(string, string) ([]byte, int, error), fixturesDir string, items []e7cItem, refresh bool) error {
	fmt.Println("\n== Stage B — OAI Identify + ListMetadataFormats (j673/j689) ==")
	for i := range items {
		it := &items[i]
		sumber, bases := e7cOAIKandidat(it.ID)
		if len(bases) == 0 {
			continue
		}
		if it.OAI == nil || refresh {
			it.OAI = &e7cOAI{Sumber: sumber}
		}

		// Identify: stop saat pertama valid (resume: base menang → break;
		// entry HTTP=0/transien → GET ulang dgn timpa-in-place)
		for bi, base := range bases {
			if it.OAI.BaseMenang != "" {
				break
			}
			var prev *e7cOAIBase
			if bi < len(it.OAI.Kandidat) {
				k := it.OAI.Kandidat[bi]
				if k.Identify {
					it.OAI.BaseMenang, it.OAI.Repo = base, k.Repo
					break
				}
				if k.HTTP != 0 {
					continue // sudah di-probe dgn hasil final — jangan GET ulang
				}
				prev = &it.OAI.Kandidat[bi] // unreachable → coba ulang, timpa
			}
			e := e7cOAIBase{URL: base, Dari: "live"}
			simpan := func() {
				if prev != nil {
					*prev = e
				} else {
					it.OAI.Kandidat = append(it.OAI.Kandidat, e)
				}
			}
			// fixture E5 investigasi (j673 identify — 0 GET, pola resume fixture-first)
			fixKnown := filepath.Join(fixturesDir, fmt.Sprintf("e5inv-j%d-identify.xml", it.ID))
			var body []byte
			var status int
			if fb, err := os.ReadFile(fixKnown); err == nil && !refresh {
				body, status, e.Dari = fb, 200, "fixture-e5"
			} else {
				b, st, err := get(base+"?verb=Identify", base+"/")
				if err != nil {
					if errors.Is(err, errPaguE7c) {
						return err
					}
					e.HTTP, e.Error = 0, "unreachable: "+err.Error()
					simpan()
					continue
				}
				body, status = b, st
				if st == 200 {
					fx := filepath.Join(fixturesDir, fmt.Sprintf("e7c-oai-%d-b%d.xml", it.ID, bi))
					if werr := os.WriteFile(fx, body, 0o644); werr != nil {
						return fmt.Errorf("tulis fixture oai j%d: %w", it.ID, werr)
					}
				}
			}
			e.HTTP = status
			if status == 200 {
				if repo, ok := garuda.E7cParseIdentify(body); ok {
					e.Identify, e.Repo = true, repo
				} else {
					e.Error = "200 tetapi bukan respons Identify"
				}
			} else {
				e.Error = fmt.Sprintf("http %d", status)
			}
			simpan()
			if e.Identify {
				it.OAI.BaseMenang, it.OAI.Repo = base, e.Repo
				break
			}
		}

		if it.OAI.BaseMenang == "" {
			it.OAI.Error = "tak ada base Identify valid"
			fmt.Printf("  j%-5d %-40.40s OAI: GAGAL semua kandidat\n", it.ID, it.Nama)
			continue
		}

		// ListMetadataFormats (1x — base menang). Fixture-first: bila fixture
		// pernah tersimpan (GET sukses sebelumnya) → parse ulang, 0 GET.
		lmfFix := filepath.Join(fixturesDir, fmt.Sprintf("e7c-oai-%d-lmf.xml", it.ID))
		dariFixture := false
		if !refresh {
			if fb, err := os.ReadFile(lmfFix); err == nil {
				if fs, oerr, perr := garuda.E7cParseLMF(fb); perr == nil {
					it.OAI.Formats, it.OAI.Error, it.OAI.LMFHTTP = fs, oerr, 200
					dariFixture = true
				}
			}
		}
		if !dariFixture && (it.OAI.LMFHTTP == 0 || refresh) {
			lmfURL := it.OAI.BaseMenang + "?verb=ListMetadataFormats"
			body, status, err := get(lmfURL, it.OAI.BaseMenang+"/")
			if err != nil && !errors.Is(err, errPaguE7c) && status == 0 {
				it.OAI.Error = "LMF unreachable: " + err.Error()
			} else if err != nil {
				return err
			} else {
				it.OAI.LMFHTTP = status
				if status == 200 {
					if werr := os.WriteFile(lmfFix, body, 0o644); werr != nil {
						return fmt.Errorf("tulis fixture lmf j%d: %w", it.ID, werr)
					}
					if fs, oerr, perr := garuda.E7cParseLMF(body); perr != nil {
						it.OAI.Error = "LMF parse: " + perr.Error()
					} else {
						it.OAI.Formats, it.OAI.Error = fs, oerr
					}
				} else {
					it.OAI.Error = fmt.Sprintf("LMF http %d", status)
				}
			}
		}
		fmt.Printf("  j%-5d %-40.40s repo=%-30.30s formats=%d\n",
			it.ID, it.Nama, it.OAI.Repo, len(it.OAI.Formats))
	}
	return nil
}

// ---------- stage C: j60 search Garuda + view/N (year-range tie) ----------

func e7cStageView(get func(string, string) ([]byte, int, error), fixturesDir string, items []e7cItem, refresh bool) error {
	var it *e7cItem
	for i := range items {
		if items[i].ID == 60 {
			it = &items[i]
			break
		}
	}
	if it == nil {
		return nil
	}
	fmt.Println("\n== Stage C — j60: search Garuda + view/N (year-range tie) ==")

	if it.View == nil || refresh {
		it.View = &e7cView{
			SearchURL: garuda.SearchURL("De Jure", 1),
			Pages:     map[string]e7cVPages{},
			Menang:    -1,
		}
	}
	if it.View.Pages == nil {
		it.View.Pages = map[string]e7cVPages{}
	}

	// (1) fixture view-5276 (E3) — 0 GET, kandidat tetap
	e7cParseViewPage := func(path, dari string) (e7cVPages, bool) {
		b, err := os.ReadFile(path)
		if err != nil {
			return e7cVPages{}, false
		}
		v, perr := garuda.ParseViewPage(strings.NewReader(string(b)))
		if perr != nil {
			return e7cVPages{HTTP: 200, Dari: dari, Error: perr.Error()}, true
		}
		return e7cVPages{
			HTTP: 200, Dari: dari, NotFound: v.NotFound,
			YearFrom: v.YearFrom, YearTo: v.YearTo,
			Title: v.Title, Publisher: v.Publisher,
			PrintISSN: v.PrintISSN, EISSN: v.EISSN,
		}, true
	}
	if _, ok := it.View.Pages["5276"]; !ok {
		for _, cand := range []string{
			filepath.Join(fixturesDir, "e7c-view-5276.html"),
			filepath.Join(fixturesDir, "view-5276.html"),
		} {
			if pg, ok2 := e7cParseViewPage(cand, "fixture-e3"); ok2 {
				it.View.Pages["5276"] = pg
				break
			}
		}
	}

	// (2) search "De Jure" — fixture-first (1 GET hanya bila fixture hilang)
	dariFix := false
	if !refresh {
		if fb, err := os.ReadFile(filepath.Join(fixturesDir, "e7c-search-j60.html")); err == nil {
			it.View.Kandidat, it.View.SearchHTTP, dariFix = e7cViewIDs(fb), 200, true
		}
	}
	if !dariFix && (it.View.SearchHTTP == 0 || refresh) {
		body, status, err := get(it.View.SearchURL, garuda.DefaultReferer)
		if err != nil && !errors.Is(err, errPaguE7c) && status == 0 {
			it.View.Error = "search unreachable: " + err.Error()
		} else if err != nil {
			return err
		} else {
			it.View.SearchHTTP = status
			if status == 200 {
				fx := filepath.Join(fixturesDir, "e7c-search-j60.html")
				if werr := os.WriteFile(fx, body, 0o644); werr != nil {
					return fmt.Errorf("tulis fixture search j60: %w", werr)
				}
				it.View.Kandidat = e7cViewIDs(body)
			}
		}
	}

	// (3) kandidat BARU (di luar 5276): kuota ≤1 view baru sepanjang program
	// (pagu j60 = 2 GET: search+view). yg sudah di-Pages = sudah terpakai;
	// sisa dicatat sbg Dilewati → rerun TANPA GET (bug rerun 6 Okt: sisa
	// kandidat di-coba ulang tiap run → pagu habis → stage terpotong).
	var baru []int
	for _, id := range it.View.Kandidat {
		s := strconv.Itoa(id)
		if _, ok := it.View.Pages[s]; !ok && id != 5276 {
			baru = append(baru, id)
		}
	}
	if len(baru) > 0 {
		terhitung := 0
		for _, id := range it.View.Kandidat {
			if id == 5276 {
				continue
			}
			if pg, ok := it.View.Pages[strconv.Itoa(id)]; ok && pg.HTTP == 200 {
				terhitung++
			}
		}
		if terhitung == 0 {
			id := baru[0]
			s := strconv.Itoa(id)
			pg, ok := e7cVPages{}, false
			for _, cand := range []string{
				filepath.Join(fixturesDir, fmt.Sprintf("e7c-view-%d.html", id)),
				filepath.Join(fixturesDir, fmt.Sprintf("view-%d.html", id)),
			} {
				if pg, ok = e7cParseViewPage(cand, "fixture-e7c"); ok {
					break
				}
			}
			if !ok {
				body, status, err := get(garuda.ViewURL(id), garuda.DefaultReferer)
				switch {
				case err != nil && errors.Is(err, errPaguE7c):
					return err
				case err != nil:
					pg = e7cVPages{HTTP: 0, Error: "unreachable: " + err.Error()}
				case status != 200:
					pg = e7cVPages{HTTP: status, Dari: "live", Error: fmt.Sprintf("http %d", status)}
				default:
					pg, _ = e7cParseViewPageBytes(body, status, "live")
					fx := filepath.Join(fixturesDir, fmt.Sprintf("e7c-view-%d.html", id))
					if werr := os.WriteFile(fx, body, 0o644); werr != nil {
						return fmt.Errorf("tulis fixture view %d: %w", id, werr)
					}
				}
			}
			it.View.Pages[s] = pg
			baru = baru[1:]
		}
		it.View.Dilewati = baru
		if len(baru) > 0 {
			fmt.Printf("  kandidat di luar kuota (≤1 view baru) → dilewati: %v\n", baru)
		}
	}

	// (4) tie year-range → pemenang (E7cPilihKandidat balik INDEX — petakan
	// ke garuda_id, karena Pages dipakai per-id)
	var ks []garuda.E7cKand
	ids := make([]int, 0, len(it.View.Pages))
	for s := range it.View.Pages {
		if n, err := strconv.Atoi(s); err == nil {
			ids = append(ids, n)
		}
	}
	sort.Ints(ids)
	for _, id := range ids {
		pg := it.View.Pages[strconv.Itoa(id)]
		ks = append(ks, garuda.E7cKand{
			ID: id, YearFrom: pg.YearFrom, YearTo: pg.YearTo, NotFound: pg.NotFound,
		})
	}
	idx, alasan := garuda.E7cPilihKandidat(ks)
	menang := -1
	if idx >= 0 {
		menang = ks[idx].ID
	}
	it.View.Menang, it.View.Alasan = menang, alasan
	if menang >= 0 {
		pg := it.View.Pages[strconv.Itoa(menang)]
		fmt.Printf("  j60 search found=%d → menang view/%d (%s) tahun=%d-%d judul=%q dari=%s\n",
			len(it.View.Kandidat), menang, alasan, pg.YearFrom, pg.YearTo, pg.Title, pg.Dari)
	} else {
		fmt.Printf("  j60 search found=%d → TIDAK ada pemenang (%s)\n", len(it.View.Kandidat), alasan)
	}
	return nil
}

func e7cParseViewPageBytes(body []byte, status int, dari string) (e7cVPages, bool) {
	v, perr := garuda.ParseViewPage(strings.NewReader(string(body)))
	if perr != nil {
		return e7cVPages{HTTP: status, Dari: dari, Error: perr.Error()}, true
	}
	return e7cVPages{
		HTTP: status, Dari: dari, NotFound: v.NotFound,
		YearFrom: v.YearFrom, YearTo: v.YearTo,
		Title: v.Title, Publisher: v.Publisher,
		PrintISSN: v.PrintISSN, EISSN: v.EISSN,
	}, true
}

// reE7cViewID = pola link halaman view pada hasil search Garuda.
var reE7cViewID = regexp.MustCompile(`/journal/view/(\d+)`)

// e7cViewIDs = extract id view dari hasil search Garuda (/journal/view/N).
func e7cViewIDs(body []byte) []int {
	seen := map[int]bool{}
	var out []int
	for _, m := range reE7cViewID.FindAllSubmatch(body, -1) {
		if n, err := strconv.Atoi(string(m[1])); err == nil && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// ---------- stage D: Q3 DOAJ (hyphen-first → kanonik dari margin) ---------

func e7cStageQ3(get func(string, string) ([]byte, int, error), fixturesDir string, out *[]e7cQ3, refresh bool) error {
	fmt.Println("\n== Stage D — Q3 DOAJ (hyphen-first; kanonik bila nol) ==")
	oldQ3 := map[string]e7cQ3{}
	if !refresh {
		for _, q := range *out {
			oldQ3[q.ISSN] = q
		}
	}
	*out = nil
	for _, d := range e7cQ3Daftar {
		q := e7cQ3{ISSN: garuda.NormISSN(d.Issn), Nama: d.Nama}
		if o, ok := oldQ3[q.ISSN]; ok && !refresh {
			q = o
		}
		bentuk := []string{garuda.E7cISSNHyphen(q.ISSN), q.ISSN}
		for bi, form := range bentuk {
			// resume: hasil lama dipakai — kecuali HTTP=0 (transien) → timpa
			var prev *e7cDOAJTry
			if bi < len(q.Bentuk) {
				t := q.Bentuk[bi]
				if t.Hasil.Tersedia {
					break
				}
				if t.HTTP != 0 {
					continue // hasil final (404 / 200-nol) — jangan GET ulang
				}
				prev = &q.Bentuk[bi]
			}
			simpan := func(t e7cDOAJTry) {
				if prev != nil {
					*prev = t
				} else {
					q.Bentuk = append(q.Bentuk, t)
				}
			}
			// fixture-first (entry baru saja — fixture mustahil utk HTTP=0)
			if prev == nil && !refresh {
				if fb, err := os.ReadFile(e7cFix(fixturesDir, "doaj", form+".json")); err == nil {
					if p, perr := garuda.ParseDOAJSearch(fb, q.ISSN); perr == nil {
						p.HTTP = 200
						simpan(e7cDOAJTry{
							Form: form, HTTP: 200, Hasil: p,
							TitleSama: p.Tersedia && garuda.TitleSama(d.Nama, p.Judul),
						})
						if p.Tersedia {
							break
						}
						continue
					}
				}
			}
			body, status, err := get("https://doaj.org/api/search/journals/"+form, "")
			if err != nil && !errors.Is(err, errPaguE7c) && status == 0 {
				simpan(e7cDOAJTry{Form: form, HTTP: 0,
					Hasil: garuda.E6DOAJ{Error: "unreachable"}})
				continue
			}
			if err != nil {
				return err
			}
			t := e7cDOAJTry{Form: form, HTTP: status}
			switch {
			case status == 200:
				if werr := os.WriteFile(e7cFix(fixturesDir, "doaj", form+".json"), body, 0o644); werr != nil {
					return fmt.Errorf("tulis fixture doaj %s: %w", form, werr)
				}
				p, perr := garuda.ParseDOAJSearch(body, q.ISSN)
				if perr != nil {
					p.Error = perr.Error()
				}
				p.HTTP = 200
				t.Hasil = p
				t.TitleSama = p.Tersedia && garuda.TitleSama(d.Nama, p.Judul)
			case status == 404:
				t.Hasil = garuda.E6DOAJ{HTTP: 404}
			default:
				t.Hasil = garuda.E6DOAJ{HTTP: status, Error: fmt.Sprintf("http %d", status)}
			}
			simpan(t)
			if t.Hasil.Tersedia {
				break
			}
		}
		*out = append(*out, q)
		// ringkas (sekali — tanpa perbandingan struct berslice)
		ketemu := false
		for _, t := range q.Bentuk {
			if t.Hasil.Tersedia {
				fmt.Printf("  %s (%s): FOUND %q (bentuk %s, title_sama=%v)\n",
					q.ISSN, q.Nama, t.Hasil.Judul, t.Form, t.TitleSama)
				ketemu = true
				break
			}
		}
		if !ketemu {
			fmt.Printf("  %s (%s): nol di %d bentuk\n", q.ISSN, q.Nama, len(q.Bentuk))
		}
	}
	return nil
}

// ---------- rencana & tulis provenance ------------------------------------

// e7cBangunPlan = hasil stage → entri provenance Opsi A (alt_*).
//   - alt_crossref (source=crossref): key paling awal yg Tersedia;
//   - alt_oai (source=official): host jurnal sendiri, Identify valid;
//   - alt_view (source=garuda): pemenang tie year-range j60.
func e7cBangunPlan(items []e7cItem, retr string) []e7cProvPlan {
	var plan []e7cProvPlan
	for _, it := range items {
		// Crossref
		keys := e7cXRKeys(&it)
		for _, k := range keys {
			if xr, ok := it.Crossref[k]; ok && xr.Hasil.Tersedia {
				v, conf := garuda.E7cNilaiCrossref(it.Nama, xr.Hasil, xr.IssnType)
				plan = append(plan, e7cProvPlan{
					id: it.ID, field: "alt_crossref",
					entry: storage.ProvEntry{Value: v, Source: "crossref", RetrievedAt: retr, Confidence: conf},
				})
				break
			}
		}
		// OAI
		if it.OAI != nil && it.OAI.BaseMenang != "" {
			v, conf := garuda.E7cNilaiOAI(it.Nama, it.OAI.BaseMenang, it.OAI.Repo, it.OAI.Formats)
			plan = append(plan, e7cProvPlan{
				id: it.ID, field: "alt_oai",
				entry: storage.ProvEntry{Value: v, Source: "official", RetrievedAt: retr, Confidence: conf},
			})
		}
		// View (j60)
		if it.View != nil && it.View.Menang >= 0 {
			s := strconv.Itoa(it.View.Menang)
			if pg, ok := it.View.Pages[s]; ok && !pg.NotFound && pg.HTTP == 200 {
				vi := &garuda.ViewInfo{
					Title: pg.Title, Publisher: pg.Publisher,
					PrintISSN: pg.PrintISSN, EISSN: pg.EISSN,
					YearFrom: pg.YearFrom, YearTo: pg.YearTo,
				}
				v, conf := garuda.E7cNilaiView(it.Nama, it.View.Menang, vi, it.PISSN, it.EISSN)
				plan = append(plan, e7cProvPlan{
					id: it.ID, field: "alt_view",
					entry: storage.ProvEntry{Value: v, Source: "garuda", RetrievedAt: retr, Confidence: conf},
				})
			}
		}
	}
	return plan
}

// e7cProvMap = snapshot JSON prov milik baris dalam plan (utk cek idempoten
// & diff: nilai lama harus utuh, key baru harus ∈ rencana).
func e7cProvMap(dbPath string, plan []e7cProvPlan) (map[int64]string, error) {
	db, err := e7bOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[int64]string{}
	seen := map[int64]bool{}
	for _, p := range plan {
		if seen[p.id] {
			continue
		}
		seen[p.id] = true
		var raw string
		if err := db.QueryRow(
			`SELECT provenance FROM journal_enrichment WHERE journal_id = ?`, p.id,
		).Scan(&raw); err != nil {
			return nil, err
		}
		out[p.id] = raw
	}
	return out, nil
}

// ---------- snapshot & verifikasi -----------------------------------------

type e7cEnrichRow struct {
	Cols string // semua kolom non-provenance (serifikasi)
	Prov string
}

func e7cSnapEnrich(dbPath string) (map[int64]e7cEnrichRow, error) {
	db, err := e7bOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	cols, err := e7cColumns(db, "journal_enrichment")
	if err != nil {
		return nil, err
	}
	q := "SELECT " + strings.Join(cols, ", ") + " FROM journal_enrichment ORDER BY journal_id"
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	provIdx := -1
	for i, c := range cols {
		if c == "provenance" {
			provIdx = i
			break
		}
	}
	if provIdx < 0 {
		return nil, fmt.Errorf("kolom provenance tak ada di journal_enrichment")
	}
	out := map[int64]e7cEnrichRow{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		var id int64
		var b strings.Builder
		for i, c := range cols {
			s := e7cValStr(vals[i])
			if c == "journal_id" {
				n, _ := strconv.ParseInt(s, 10, 64)
				id = n
			} else if c == "provenance" {
				continue
			}
			b.WriteString(c)
			b.WriteString("=")
			b.WriteString(s)
			b.WriteString("\x1f")
		}
		out[id] = e7cEnrichRow{Cols: b.String(), Prov: e7cValStr(vals[provIdx])}
	}
	return out, rows.Err()
}

// e7cDiffEnrich = verifikasi K1 utk tulis prov-only:
//   - kolom non-provenance SEMUA baris wajib identik;
//   - provenance baris di luar plan wajib identik byte;
//   - provenance baris plan: key lama utuh (nilai sama), key baru hanya
//     field rencana (alt_*).
func e7cDiffEnrich(dbPath string, pre map[int64]e7cEnrichRow, plan []e7cProvPlan) ([]string, error) {
	post, err := e7cSnapEnrich(dbPath)
	if err != nil {
		return nil, err
	}
	allowed := map[int64]map[string]bool{}
	for _, p := range plan {
		if allowed[p.id] == nil {
			allowed[p.id] = map[string]bool{}
		}
		allowed[p.id][p.field] = true
	}
	var bad []string
	if len(pre) != len(post) {
		bad = append(bad, fmt.Sprintf("jumlah baris enrichment %d→%d", len(pre), len(post)))
	}
	for id, a := range pre {
		b, ok := post[id]
		if !ok {
			bad = append(bad, fmt.Sprintf("enrichment j%d hilang", id))
			continue
		}
		if a.Cols != b.Cols {
			bad = append(bad, fmt.Sprintf("j%d kolom non-prov berubah", id))
		}
		if a.Prov == b.Prov {
			continue
		}
		alw, dalamPlan := allowed[id]
		if !dalamPlan {
			bad = append(bad, fmt.Sprintf("j%d provenance berubah di luar plan", id))
			continue
		}
		oldM := map[string]storage.ProvEntry{}
		newM := map[string]storage.ProvEntry{}
		if json.Unmarshal([]byte(a.Prov), &oldM) != nil || json.Unmarshal([]byte(b.Prov), &newM) != nil {
			bad = append(bad, fmt.Sprintf("j%d provenance JSON tidak valid", id))
			continue
		}
		for k, ov := range oldM {
			if nv, ok := newM[k]; !ok || nv != ov {
				bad = append(bad, fmt.Sprintf("j%d prov key lama %q berubah/hilang", id, k))
			}
		}
		for k := range newM {
			if _, ok := oldM[k]; !ok && !alw[k] {
				bad = append(bad, fmt.Sprintf("j%d prov key baru %q di luar rencana", id, k))
			}
		}
	}
	return bad, nil
}

// e7cHashJournals = sha256 atas SEMUA baris & kolom tabel journals
// (tulis prov-only wajib membuat hash ini identik).
func e7cHashJournals(dbPath string) (string, error) {
	db, err := e7bOpenRO(dbPath)
	if err != nil {
		return "", err
	}
	defer db.Close()
	cols, err := e7cColumns(db, "journals")
	if err != nil {
		return "", err
	}
	rows, err := db.Query("SELECT " + strings.Join(cols, ", ") + " FROM journals ORDER BY id")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for rows.Next() {
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		for i, c := range cols {
			fmt.Fprintf(h, "%s=%s\x1f", c, e7cValStr(vals[i]))
		}
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), rows.Err()
}

// e7cColumns = nama kolom tabel (PRAGMA) — snapshot generik tanpa hardcode.
func e7cColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func e7cValStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "\x00"
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

// ---------- baca target & hitungan ----------------------------------------

type e7cTargetRow struct {
	id     int64
	nama   string
	pissn  string
	eissn  string
	ojsURL string
}

func e7cBacaTarget(dbPath string) ([]e7cTargetRow, error) {
	db, err := e7bOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var out []e7cTargetRow
	for _, id := range e7cMiss {
		var r e7cTargetRow
		var status string
		err := db.QueryRow(`
			SELECT j.id, j.name, COALESCE(j.print_issn,''), COALESCE(j.electronic_issn,''),
			       COALESCE(j.ojs_url,''), e.match_status
			FROM journals j JOIN journal_enrichment e ON e.journal_id = j.id
			WHERE j.id = ?`, id).Scan(&r.id, &r.nama, &r.pissn, &r.eissn, &r.ojsURL, &status)
		if err != nil {
			return nil, fmt.Errorf("j%d: %w", id, err)
		}
		if status != "not_found" {
			return nil, fmt.Errorf("DRIFT: j%d status=%q (want not_found)", id, status)
		}
		out = append(out, r)
	}
	return out, nil
}

func e7cNotFound(dbPath string) (int, error) {
	db, err := e7bOpenRO(dbPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT COUNT(*) FROM journal_enrichment WHERE match_status = 'not_found'`).Scan(&n)
	return n, err
}

// ---------- util -----------------------------------------------------------

func e7cFix(dir, jenis, key string) string {
	return filepath.Join(dir, fmt.Sprintf("e7c-%s-%s", jenis, key))
}

func e7cTulisJSON(rep *e7cJSON) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal e7c json: %w", err)
	}
	if err := os.WriteFile(e7cOutPth, b, 0o644); err != nil {
		return fmt.Errorf("tulis %s: %w", e7cOutPth, err)
	}
	fmt.Printf("JSON: %s (%d byte)\n", e7cOutPth, len(b))
	return nil
}

// e7cLapor = ringkas stdout utk review user (3 bagian laporan user).
func e7cLapor(rep *e7cJSON, getRun int) {
	fmt.Printf("\n== E7c SELESAI%s ==\n", map[bool]string{true: " (TERPOTONG pagu)", false: ""}[rep.Terpotong])
	fmt.Printf("GET: run ini %d/%d · kumulatif %d\n", getRun, paguE7c, rep.GetKumulatif)
	xrOK, oaiOK, viewOK := 0, 0, 0
	for i := range rep.Items {
		it := &rep.Items[i]
		for _, k := range e7cXRKeys(it) {
			if xr, ok := it.Crossref[k]; ok && xr.Hasil.Tersedia {
				xrOK++
				break
			}
		}
		if it.OAI != nil && it.OAI.BaseMenang != "" {
			oaiOK++
		}
		if it.View != nil && it.View.Menang >= 0 {
			viewOK++
		}
	}
	fmt.Printf("cakupan: crossref=%d/8 · oai(j673/689)=%d/2 · view(j60)=%d/1\n",
		xrOK, oaiOK, viewOK)
	for i := range rep.Items {
		it := &rep.Items[i]
		line := fmt.Sprintf("  j%-5d %-42.42s", it.ID, it.Nama)
		keys := e7cXRKeys(it)
		xrLine := ""
		for _, k := range keys {
			if xr, ok := it.Crossref[k]; ok && xr.Hasil.Tersedia {
				xrLine = fmt.Sprintf(" xr=OK(%s)", k)
				break
			}
		}
		if xrLine == "" {
			for j := len(keys) - 1; j >= 0; j-- {
				if xr, ok := it.Crossref[keys[j]]; ok {
					xrLine = fmt.Sprintf(" xr=nol(http%d)", xr.HTTP)
					break
				}
			}
		}
		line += xrLine
		if it.ID == 673 || it.ID == 689 {
			if it.OAI != nil && it.OAI.BaseMenang != "" {
				line += fmt.Sprintf(" oai=OK(%d fmt)", len(it.OAI.Formats))
			} else {
				line += " oai=GAGAL"
			}
		}
		if it.ID == 60 && it.View != nil {
			line += fmt.Sprintf(" view=menang/%d(%s)", it.View.Menang, it.View.Alasan)
		}
		for _, p := range it.Prov {
			line += " [" + p.Field + fmt.Sprintf(" c%.1f]", p.Confidence)
		}
		fmt.Println(line)
	}
	for _, q := range rep.Q3 {
		for _, t := range q.Bentuk {
			if t.Hasil.Tersedia {
				fmt.Printf("  Q3 %s (%s): FOUND via %s — title_sama=%v\n", q.ISSN, q.Nama, t.Form, t.TitleSama)
				break
			}
		}
	}
	fmt.Printf("prov: ditulis=%d · idempoten=%s\n", rep.ProvTulis, rep.ProvIdempoten)
	fmt.Printf("verifikasi:")
	for k, v := range rep.Verifikasi {
		fmt.Printf(" %s=%s", k, v)
	}
	fmt.Println()
}
