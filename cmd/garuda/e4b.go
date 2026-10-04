package main

// runE4b = Q4 E4b (doc 30 §14.3 — DISETUJUI user 4 Okt 2026; pagu ≤90 GET):
//
//	A. year-range check 24 duplikat: GET view tiap kandidat (49 kandidat,
//	   fixture E3 dipakai ulang) → pemenang = tahun akhir terbesar;
//	B. search alternatif 18 found=0: P-ISSN (fallback judul) — 1 GET/jurnal;
//	C. view 21 URL Tahap 1: 15 not_found + 6 kasus turun (verifikasi);
//	D. tulis idempoten (backup db dulu) + laporan + JSON e4b-results.json.
//
// Hukum tulis (keputusan K6 4 Okt 2026):
//   - duplikat → matched pemenang-tahun + flag DUPLICATE_GARUDA + provenance;
//     TANPA tulis bila sim(pemenang) < 60 DAN ISSN pemenang beda dgn baris
//     SINTA (anomali → review); ISSN cocok = identitas terbukti → override sim;
//   - search-alt → hasil ladder apa adanya (conf jujur 60/85/100 → kolom 0..1);
//   - view not_found → matched HANYA bila lolos ladder (§5.2);
//   - 6 kasus turun → VERIFIKASI SAJA, tanpa tulis.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"
	"sinta-scraper/internal/storage"
)

const paguE4b = 91 // pagu keras: 90 (approve 4 Okt) + 1 (approve user utk j744 → id baru 26763)

// idBaru = garuda_id hasil verifikasi manual user (4 Okt 2026) — menggantikan
// garuda_url Tahap 1 yang basi (halaman view record-not-found).
var idBaru = map[int64]int64{
	744: 26763, // Indonesia Law Review — user search E-ISSN 20888430 & P-ISSN ketemu
}

// barisE4b = baris jurnal utk klasifikasi E4b.
type barisE4b struct {
	id        int64
	name      string
	pissn     string
	eissn     string
	pub       string
	statusDB  string // match_status baseline (Q3)
	gidE      int64  // garuda_id enrichment (utk kasus turun)
	gurlJ     string // garuda_url journals (utk 21 notfound)
	res       garuda.Result
	nKandidat int
}

// kandView = satu kandidat duplikat + hasil fetch/parse halaman view.
type kandView struct {
	k    garuda.KandidatTahun
	dari string // "fixture" | "live"
	v    *garuda.ViewInfo
}

// ---- struktur JSON laporan E4b ----

type kandViewJ struct {
	GarudaID int64  `json:"garuda_id"`
	Dari     string `json:"dari"`
	YearFrom int    `json:"year_from,omitempty"`
	YearTo   int    `json:"year_to,omitempty"`
	NotFound bool   `json:"not_found,omitempty"`
}

type dupHasil struct {
	JournalID int64       `json:"journal_id"`
	Nama      string      `json:"nama"`
	Kandidat  []kandViewJ `json:"kandidat"`
	Pemenang  int64       `json:"pemenang,omitempty"`
	Kalah     string      `json:"kalah,omitempty"`
	Alasan    string      `json:"alasan,omitempty"`
	SimMenang float64     `json:"sim_pemenang"`
	Override  bool        `json:"override_issn,omitempty"` // sim<60 tapi ISSN pemenang cocok → tulis via jalur ISSN
	Tulis     bool        `json:"ditulis"`
	Anomali   string      `json:"anomali,omitempty"`
}

type searchHasil struct {
	JournalID int64   `json:"journal_id"`
	Nama      string  `json:"nama"`
	Q         string  `json:"q"`
	Metode    string  `json:"metode"` // pissn|judul
	Kandidat  int     `json:"kandidat"`
	Status    string  `json:"status"`
	By        string  `json:"by,omitempty"`
	Conf      float64 `json:"confidence,omitempty"`
	Tulis     bool    `json:"ditulis"`
	Catatan   string  `json:"catatan,omitempty"`
}

type viewHasil struct {
	JournalID int64   `json:"journal_id"`
	Nama      string  `json:"nama"`
	GarudaID  int64   `json:"garuda_id"`
	Kelompok  string  `json:"kelompok"` // notfound|turun
	Dari      string  `json:"dari"`
	NotFound  bool    `json:"not_found,omitempty"`
	YearTo    int     `json:"year_to,omitempty"`
	Title     string  `json:"title,omitempty"`
	Publisher string  `json:"publisher,omitempty"`
	PISSN     string  `json:"pissn,omitempty"`
	EISSN     string  `json:"eissn,omitempty"`
	Status    string  `json:"status"`
	By        string  `json:"by,omitempty"`
	Conf      float64 `json:"confidence,omitempty"`
	Tulis     bool    `json:"ditulis"`
	Catatan   string  `json:"catatan,omitempty"`
}

type e4bJSON struct {
	Dibuat    string        `json:"dibuat"`
	GETView   int           `json:"get_view"`
	GETSearch int           `json:"get_search"`
	Dup       []dupHasil    `json:"duplikat"`
	Search    []searchHasil `json:"search_alt"`
	View      []viewHasil   `json:"view"`
	Anomali   []string      `json:"anomali"`
	Tulis     struct {
		Matched   int `json:"matched"`
		Ambiguous int `json:"ambiguous"`
		FlagDup   int `json:"flag_duplicate"`
	} `json:"tulis"`
}

// e4bClient = client dgn pagu keras + penghitung GET per jenis.
type e4bClient struct {
	c         *garuda.Client
	fixtures  string
	getView   int
	getSearch int
}

// ambil = baca fixture bila ada (resume D2), selain itu GET → simpan.
// Mengembalikan body + sumber ("fixture"|"live").
func (e *e4bClient) ambil(url, path string, jenis string, refresh bool) ([]byte, string, error) {
	if !refresh {
		if b, err := os.ReadFile(path); err == nil {
			return b, "fixture", nil
		}
	}
	if jenis == "search" {
		if e.getSearch >= paguE4b {
			return nil, "", fmt.Errorf("PAGU %d GET terlampaui (search) — stop", paguE4b)
		}
		e.getSearch++
	} else {
		if e.getView+e.getSearch >= paguE4b {
			return nil, "", fmt.Errorf("PAGU %d GET terlampaui (view) — stop", paguE4b)
		}
		e.getView++
	}
	body, status, err := e.c.Get(url, garuda.DefaultReferer)
	if err != nil || body == nil {
		return nil, "", fmt.Errorf("GET %s: http=%d %v", url, status, err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return nil, "", fmt.Errorf("tulis %s: %w", path, err)
	}
	return body, "live", nil
}

// runE4b = driver E4b: fetch → analisa → backup → tulis → lapor.
func runE4b(dbPath, fixturesDir string, refresh bool, delayMin, delayMax time.Duration) error {
	// ---- backup db SEBELUM proses ----
	bak := strings.TrimSuffix(dbPath, ".db") + ".preE4b.bak.db"
	if _, err := os.Stat(bak); err != nil {
		if err := salinFile(dbPath, bak); err != nil {
			return fmt.Errorf("backup db: %w", err)
		}
		fmt.Printf("backup db → %s\n", bak)
	} else {
		fmt.Printf("backup sudah ada (dipakai apa adanya): %s\n", bak)
	}

	// ---- load baris + recompute ladder dari fixture ----
	baris, err := loadBarisE4b(dbPath, fixturesDir)
	if err != nil {
		return err
	}
	var dup24, live18, turun6 []*barisE4b
	for _, b := range baris {
		baseMatched := b.statusDB == "matched"
		resMatched := b.res.Status == garuda.StatusMatched
		switch {
		case !baseMatched && b.res.Status == garuda.StatusAmbiguous:
			dup24 = append(dup24, b)
		case !baseMatched && b.nKandidat == 0:
			live18 = append(live18, b)
		case baseMatched && !resMatched:
			turun6 = append(turun6, b)
		}
	}
	fmt.Printf("== E4b — klasifikasi: duplikat=%d · found=0=%d · kasus-turun=%d (dari %d) ==\n",
		len(dup24), len(live18), len(turun6), len(baris))

	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		return fmt.Errorf("buat dir fixture: %w", err)
	}
	ec := &e4bClient{c: garuda.NewClient("garuda-e4b", uaDefault, delayMin, delayMax), fixtures: fixturesDir}
	rep := &e4bJSON{Dibuat: time.Now().UTC().Format(time.RFC3339), Anomali: []string{}}

	// ============ FASE A: tahun utk 24 duplikat ============
	fmt.Println("\n== Fase A — year-range check (kandidat tiap jurnal ambiguous) ==")
	var dups []dupHasil
	for _, b := range dup24 {
		cands, _, err := garuda.KandidatDariFixture(fixturesDir, b.id)
		if err != nil || len(cands) == 0 {
			rep.Anomali = append(rep.Anomali, fmt.Sprintf("j%d: kandidat tak bisa dimuat: %v", b.id, err))
			continue
		}
		var kvs []kandView
		for _, c := range cands {
			path := filepath.Join(fixturesDir, fmt.Sprintf("view-%d.html", c.GarudaID))
			var v *garuda.ViewInfo
			dari := "fixture"
			body, sumber, ferr := ec.ambil(garuda.ViewURL(int(c.GarudaID)), path, "view", refresh)
			if ferr != nil {
				rep.Anomali = append(rep.Anomali, fmt.Sprintf("j%d view-%d: %v", b.id, c.GarudaID, ferr))
			} else {
				dari = sumber
				v, _ = garuda.ParseViewPage(bytes.NewReader(body))
			}
			if v == nil {
				v = &garuda.ViewInfo{NotFound: true}
			}
			kvs = append(kvs, kandView{
				k: garuda.KandidatTahun{
					GarudaID: c.GarudaID,
					YearTo:   v.YearTo,
					NotFound: v.NotFound,
					PISSN:    c.PISSN,
				},
				dari: dari,
				v:    v,
			})
		}
		kt := make([]garuda.KandidatTahun, len(kvs))
		for i, kv := range kvs {
			kt[i] = kv.k
		}
		pil := garuda.PilihPemenangTahun(kt)
		dh := dupHasil{JournalID: b.id, Nama: b.name, Alasan: pil.Alasan}
		for _, kv := range kvs {
			dh.Kandidat = append(dh.Kandidat, kandViewJ{
				GarudaID: kv.k.GarudaID, Dari: kv.dari,
				YearFrom: kv.v.YearFrom, YearTo: kv.v.YearTo, NotFound: kv.k.NotFound,
			})
		}
		if pil.Idx < 0 {
			dh.Anomali = "tanpa pemenang"
			rep.Anomali = append(rep.Anomali, fmt.Sprintf("j%d: %s", b.id, dh.Anomali))
			dups = append(dups, dh)
			continue
		}
		menang := kvs[pil.Idx]
		dh.Pemenang = menang.k.GarudaID
		for i, kv := range kvs {
			if i != pil.Idx {
				if dh.Kalah != "" {
					dh.Kalah += ","
				}
				dh.Kalah += strconv.FormatInt(kv.k.GarudaID, 10)
			}
		}
		dh.SimMenang = garuda.TitleSimilarity(b.name, menang.v.Title)
		// Guard "jurnal yang sama": pemenang tahun HANYA ditulis bila ISSN-nya
		// cocok dgn baris SINTA (E-ISSN kanonik / P-ISSN) — tahun terbaru tak
		// menjamin identitas (mogok "salah jurnal"). ISSN cocok = identitas
		// terbukti walau judul beda (rename jurnal, mis. j1811) → override sim.
		issnCocok := issnSama(menang.v.EISSN, b.eissn) || issnSama(menang.v.PrintISSN, b.pissn)
		if dh.SimMenang < garuda.AmbangCrossMin() && !issnCocok {
			dh.Anomali = fmt.Sprintf("pemenang tahun sim title %.0f < 60 DAN ISSN beda dgn baris SINTA — TIDAK ditulis (review)", dh.SimMenang)
			rep.Anomali = append(rep.Anomali, fmt.Sprintf("j%d (%s): %s", b.id, b.name, dh.Anomali))
		} else {
			if dh.SimMenang < garuda.AmbangCrossMin() {
				dh.Override = true
				dh.Alasan += " | ISSN pemenang cocok dgn baris SINTA → override sim<60"
			}
			dh.Tulis = true
		}
		fmt.Printf("j%-5d %-42.42s menang=%-6d tahun=%d sim=%.0f issn=%v %s\n",
			b.id, b.name, menang.k.GarudaID, menang.k.YearTo, dh.SimMenang, issnCocok, dh.Alasan)
		dups = append(dups, dh)
	}
	rep.Dup = dups

	// ============ FASE B: search alternatif utk 18 found=0 ============
	fmt.Printf("\n== Fase B — search alternatif (%d jurnal, 1 GET/jurnal) ==\n", len(live18))
	var sres []searchHasil
	for _, b := range live18 {
		q, metode := b.pissn, "pissn"
		if garuda.CanonicalISSN(q) == "" {
			q, metode = qJudul(b.name), "judul"
		}
		path := filepath.Join(fixturesDir, fmt.Sprintf("search-j%d-alt-%s.html", b.id, slug(q)))
		sh := searchHasil{JournalID: b.id, Nama: b.name, Q: q, Metode: metode}
		if _, _, err := ec.ambil(garuda.SearchURL(q, 1), path, "search", refresh); err != nil {
			rep.Anomali = append(rep.Anomali, fmt.Sprintf("j%d search alt: %v", b.id, err))
			sh.Catatan = "fetch gagal"
			sres = append(sres, sh)
			continue
		}
		cands, _, err := garuda.KandidatDariFixture(fixturesDir, b.id)
		if err != nil {
			rep.Anomali = append(rep.Anomali, fmt.Sprintf("j%d kandidat: %v", b.id, err))
			sres = append(sres, sh)
			continue
		}
		sh.Kandidat = len(cands)
		b.res = garuda.Match(garuda.Input{Name: b.name, PISSN: b.pissn, EISSN: b.eissn, Publisher: b.pub}, cands)
		sh.Status = string(b.res.Status)
		sh.By = b.res.MatchedBy
		sh.Conf = b.res.Confidence
		sh.Tulis = true
		fmt.Printf("j%-5d %-40.40s q=%-14.14s (%s) kand=%d → %s %s conf=%.0f\n",
			b.id, b.name, q, metode, len(cands), b.res.Status, b.res.MatchedBy, b.res.Confidence)
		sres = append(sres, sh)
	}
	rep.Search = sres

	// ============ FASE C: view 21 URL Tahap 1 ============
	nf := notfoundBergurl(baris)
	fmt.Printf("\n== Fase C — view URL Tahap 1 (notfound=%d + turun=%d) ==\n", len(nf), len(turun6))
	type kasusView struct {
		b        *barisE4b
		id       int64
		kelompok string
	}
	var kasus []kasusView
	for _, b := range nf {
		id, err := idDariURL(b.gurlJ)
		if err != nil {
			rep.Anomali = append(rep.Anomali, fmt.Sprintf("j%d gurlJ %q: %v", b.id, b.gurlJ, err))
			continue
		}
		if baru, ok := idBaru[b.id]; ok {
			fmt.Printf("j%d: pakai id baru hasil verifikasi user — %d basi → %d\n", b.id, id, baru)
			id = int(baru)
		}
		kasus = append(kasus, kasusView{b, int64(id), "notfound"})
	}
	for _, b := range turun6 {
		if b.gidE <= 0 {
			rep.Anomali = append(rep.Anomali, fmt.Sprintf("j%d turun tanpa garuda_id", b.id))
			continue
		}
		kasus = append(kasus, kasusView{b, b.gidE, "turun"})
	}
	var vres []viewHasil
	lihat := map[int64]bool{}
	for _, k := range kasus {
		if lihat[k.id] {
			continue
		}
		lihat[k.id] = true
		path := filepath.Join(fixturesDir, fmt.Sprintf("view-%d.html", k.id))
		dari := "fixture"
		var v *garuda.ViewInfo
		body, sumber, err := ec.ambil(garuda.ViewURL(int(k.id)), path, "view", refresh)
		if err != nil {
			rep.Anomali = append(rep.Anomali, fmt.Sprintf("j%d view-%d: %v", k.b.id, k.id, err))
		} else {
			dari = sumber
			v, _ = garuda.ParseViewPage(bytes.NewReader(body))
		}
		if v == nil {
			v = &garuda.ViewInfo{NotFound: true}
		}
		vh := viewHasil{
			JournalID: k.b.id, Nama: k.b.name, GarudaID: k.id,
			Kelompok: k.kelompok, Dari: dari, NotFound: v.NotFound, YearTo: v.YearTo,
			Title: v.Title, Publisher: v.Publisher, PISSN: v.PrintISSN, EISSN: v.EISSN,
		}
		switch {
		case v.NotFound:
			vh.Status, vh.Catatan = "not_found", "halaman view record-not-found"
			rep.Anomali = append(rep.Anomali, fmt.Sprintf("j%d view-%d mati (id basi)", k.b.id, k.id))
		default:
			r := garuda.ResolveView(
				garuda.Input{Name: k.b.name, PISSN: k.b.pissn, EISSN: k.b.eissn, Publisher: k.b.pub},
				garuda.Candidate{GarudaID: k.id, Title: v.Title, Publisher: v.Publisher, PISSN: v.PrintISSN, EISSN: v.EISSN},
			)
			vh.Status, vh.By, vh.Conf = string(r.Status), r.MatchedBy, r.Confidence
			if k.kelompok == "turun" {
				vh.Catatan = "verifikasi saja — tanpa tulis (keputusan iii)"
			} else if r.Status == garuda.StatusMatched {
				vh.Tulis = true
			} else {
				vh.Catatan = "tak lolos ladder → TIDAK ditulis (review)"
			}
		}
		fmt.Printf("j%-5d %-40.40s view-%-6d %-8s → %-9s %-16s conf=%.0f tahun=%d %s\n",
			k.b.id, k.b.name, k.id, k.kelompok, vh.Status, vh.By, vh.Conf, v.YearTo, vh.Catatan)
		vres = append(vres, vh)
	}
	rep.View = vres
	rep.GETView, rep.GETSearch = ec.getView, ec.getSearch

	// ============ FASE D: tulis db ============
	fmt.Println("\n== Fase D — tulis db (idempoten) ==")
	store, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("buka db rw: %w", err)
	}
	defer store.Close()
	if err := tulisE4b(store, rep, dup24, live18, fixturesDir); err != nil {
		return fmt.Errorf("tulis: %w", err)
	}

	// ============ laporan + JSON ============
	fmt.Printf("\n== Ringkasan E4b ==\n")
	fmt.Printf("GET: view=%d search=%d total=%d (pagu %d)\n",
		rep.GETView, rep.GETSearch, rep.GETView+rep.GETSearch, paguE4b)
	fmt.Printf("duplikat ditulis=%d/%d · search-alt ditulis=%d/%d · view ditulis=%d/%d\n",
		hitungDupTulis(rep), len(rep.Dup), hitungSearchTulis(rep), len(rep.Search),
		hitungViewTulis(rep), len(rep.View))
	fmt.Printf("tulis: matched=%d ambiguous=%d flag-duplicate=%d · anomali=%d\n",
		rep.Tulis.Matched, rep.Tulis.Ambiguous, rep.Tulis.FlagDup, len(rep.Anomali))
	for _, a := range rep.Anomali {
		fmt.Printf("  ANOMALI: %s\n", a)
	}
	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	jsonPath := filepath.Join(filepath.Dir(fixturesDir), "e4b-results.json")
	if err := os.WriteFile(jsonPath, out, 0o644); err != nil {
		return fmt.Errorf("tulis json: %w", err)
	}
	fmt.Printf("JSON → %s\n", jsonPath)
	return nil
}

// tulisE4b = fase D: seluruh tulisan idempoten sesuai hukum tulis E4b.
func tulisE4b(store *storage.Store, rep *e4bJSON, dup24, live18 []*barisE4b, fixturesDir string) error {
	byID := map[int64]*barisE4b{}
	for _, b := range dup24 {
		byID[b.id] = b
	}
	for _, b := range live18 {
		byID[b.id] = b
	}

	// ---- 1) duplikat: matched pemenang-tahun + flag + provenance ----
	for _, d := range rep.Dup {
		if !d.Tulis {
			continue
		}
		b := byID[d.JournalID]
		if b == nil {
			return fmt.Errorf("dup j%d: baris tak ditemukan", d.JournalID)
		}
		cands, _, err := garuda.KandidatDariFixture(fixturesDir, b.id)
		if err != nil {
			return fmt.Errorf("dup j%d kandidat: %w", b.id, err)
		}
		var menang *garuda.Candidate
		for i := range cands {
			if cands[i].GarudaID == d.Pemenang {
				menang = &cands[i]
				break
			}
		}
		if menang == nil {
			return fmt.Errorf("dup j%d: pemenang %d tak ada di kandidat", b.id, d.Pemenang)
		}
		// ladder utk kandidat tunggal → by/conf (crossFail sudah ditangani
		// di hukum tulis: sim ≥ 60 ATAU override ISSN-identik).
		r := garuda.Match(garuda.Input{Name: b.name, PISSN: b.pissn, EISSN: b.eissn, Publisher: b.pub},
			[]garuda.Candidate{*menang})
		if r.Status != garuda.StatusMatched {
			// Override K6(i): Fase A lolos via guard identitas (ISSN pemenang
			// = ISSN baris SINTA) → E-ISSN/P-ISSN exact = kunci kanonik Q2
			// (conf 100, konsisten baseline); cross-check title ditahan utk
			// kasus NORMAL — utk duplikat-terbukti-kembar, tahun+ISSN sudah
			// menjawab "salah jurnal". Cek ISSN diulang di sini (defensif).
			if !d.Override {
				return fmt.Errorf("dup j%d: kandidat menang tak lolos ladder (%s)", b.id, r.Notes)
			}
			switch {
			case issnSama(menang.EISSN, b.eissn):
				r = garuda.Result{Status: garuda.StatusMatched, MatchedBy: garuda.MatchedByEISSN,
					Confidence: 100, AutoAccept: true,
					Notes: "duplikat tahun + E-ISSN identik dgn baris SINTA (override cross-check title)"}
			case issnSama(menang.PISSN, b.pissn):
				r = garuda.Result{Status: garuda.StatusMatched, MatchedBy: "pissn",
					Confidence: 100, AutoAccept: true,
					Notes: "duplikat tahun + P-ISSN identik dgn baris SINTA (override cross-check title)"}
			default:
				return fmt.Errorf("dup j%d: override tapi ISSN pemenang (%s/%s) beda dgn baris SINTA (%s/%s)",
					b.id, menang.EISSN, menang.PISSN, b.eissn, b.pissn)
			}
		}
		m := storage.GarudaMatch{
			JournalID:  b.id,
			Status:     string(garuda.StatusMatched),
			GarudaID:   menang.GarudaID,
			GarudaURL:  garuda.ViewURL(int(menang.GarudaID)),
			Title:      menang.Title,
			Publisher:  menang.Publisher,
			PISSN:      menang.PISSN,
			EISSN:      menang.EISSN,
			MatchedBy:  r.MatchedBy,
			Confidence: r.Confidence / 100, // skala 0..1 — konsisten baseline Q3 (1.0)
		}
		if err := store.UpsertGarudaMatch(m); err != nil {
			return fmt.Errorf("dup j%d upsert: %w", b.id, err)
		}
		// flag: tandai DUPLICATE_GARUDA, lepas AMBIGUOUS lama (Q3) — status
		// kini matched (flags & match_status wajib konsisten).
		if err := store.SetPhase2(b.id, "",
			[]string{"DUPLICATE_GARUDA"}, []string{garuda.FlagAmbiguous}, ""); err != nil {
			return fmt.Errorf("dup j%d flag: %w", b.id, err)
		}
		if err := store.MergeProvenance(b.id, "garuda_id", storage.ProvEntry{
			Value: strconv.FormatInt(d.Pemenang, 10), Source: "garuda", Confidence: 1.0,
		}); err != nil {
			return fmt.Errorf("dup j%d prov garuda_id: %w", b.id, err)
		}
		if d.Kalah != "" {
			if err := store.MergeProvenance(b.id, "garuda_duplicate", storage.ProvEntry{
				Value: d.Kalah, Source: "garuda", Confidence: 1.0,
			}); err != nil {
				return fmt.Errorf("dup j%d prov duplicate: %w", b.id, err)
			}
		}
		rep.Tulis.Matched++
		rep.Tulis.FlagDup++
	}

	// ---- 2) search-alt: hasil ladder apa adanya ----
	for _, s := range rep.Search {
		if !s.Tulis {
			continue
		}
		b := byID[s.JournalID]
		if b == nil {
			return fmt.Errorf("search j%d: baris tak ditemukan", s.JournalID)
		}
		m := storage.GarudaMatch{
			JournalID: b.id,
			Status:    s.Status,
			MatchedBy: s.By,
		}
		if s.Status == string(garuda.StatusMatched) {
			if b.res.Candidate == nil {
				return fmt.Errorf("search j%d: matched tanpa kandidat", b.id)
			}
			c := b.res.Candidate
			m.GarudaID = c.GarudaID
			m.GarudaURL = garuda.ViewURL(int(c.GarudaID))
			m.Title, m.Publisher, m.PISSN, m.EISSN = c.Title, c.Publisher, c.PISSN, c.EISSN
			m.Confidence = s.Conf / 100 // 0..1
		}
		if err := store.UpsertGarudaMatch(m); err != nil {
			return fmt.Errorf("search j%d upsert: %w", b.id, err)
		}
		if s.Status == string(garuda.StatusMatched) {
			rep.Tulis.Matched++
		} else {
			rep.Tulis.Ambiguous++
		}
	}

	// ---- 3) view notfound: matched bila lolos ladder ----
	for _, v := range rep.View {
		if !v.Tulis || v.Kelompok != "notfound" {
			continue
		}
		m := storage.GarudaMatch{
			JournalID:  v.JournalID,
			Status:     string(garuda.StatusMatched),
			GarudaID:   v.GarudaID,
			GarudaURL:  garuda.ViewURL(int(v.GarudaID)),
			Title:      v.Title,
			Publisher:  v.Publisher,
			PISSN:      v.PISSN,
			EISSN:      v.EISSN,
			MatchedBy:  v.By,
			Confidence: v.Conf / 100, // 0..1
		}
		if err := store.UpsertGarudaMatch(m); err != nil {
			return fmt.Errorf("view j%d upsert: %w", v.JournalID, err)
		}
		rep.Tulis.Matched++
	}

	// ---- 4) konsistensi flags: matched tak boleh ber-flag AMBIGUOUS ----
	bersih, err := store.AmbigousMatchedIDs()
	if err != nil {
		return fmt.Errorf("baca flag AMBIGUOUS basi: %w", err)
	}
	for _, id := range bersih {
		if err := store.SetPhase2(id, "", nil, []string{garuda.FlagAmbiguous}, ""); err != nil {
			return fmt.Errorf("rapikan flag j%d: %w", id, err)
		}
	}
	if len(bersih) > 0 {
		fmt.Printf("flag AMBIGUOUS basi dilepas dari %d baris matched\n", len(bersih))
	}
	return nil
}

// loadBarisE4b = seluruh baris + recompute Match dari fixture (pola runHitrate).
func loadBarisE4b(dbPath, fixturesDir string) ([]*barisE4b, error) {
	uri := "file:" + filepath.ToSlash(dbPath) + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, fmt.Errorf("buka db ro: %w", err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT j.id, j.name, COALESCE(j.print_issn,''), COALESCE(j.electronic_issn,''),
		       COALESCE(j.affiliation_name,''),
		       e.match_status, COALESCE(e.garuda_id,0), COALESCE(j.garuda_url,'')
		FROM journals j JOIN journal_enrichment e ON e.journal_id = j.id
		ORDER BY j.id`)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []*barisE4b
	for rows.Next() {
		b := &barisE4b{}
		if err := rows.Scan(&b.id, &b.name, &b.pissn, &b.eissn, &b.pub,
			&b.statusDB, &b.gidE, &b.gurlJ); err != nil {
			return nil, err
		}
		cands, n, err := garuda.KandidatDariFixture(fixturesDir, b.id)
		if err != nil {
			return nil, fmt.Errorf("kandidat j%d: %w", b.id, err)
		}
		b.nKandidat = n
		b.res = garuda.Match(garuda.Input{Name: b.name, PISSN: b.pissn, EISSN: b.eissn, Publisher: b.pub}, cands)
		out = append(out, b)
	}
	return out, rows.Err()
}

// notfoundBergurl = baris not_found dgn garuda_url Tahap 1 (21 baris Q3).
func notfoundBergurl(bs []*barisE4b) []*barisE4b {
	var out []*barisE4b
	for _, b := range bs {
		if b.statusDB == "not_found" && strings.Contains(b.gurlJ, "/journal/view/") {
			out = append(out, b)
		}
	}
	return out
}

// issnSama = kedua ISSN sama setelah normalisasi kanonik ("" = tak valid → false).
func issnSama(a, b string) bool {
	ca, cb := garuda.CanonicalISSN(a), garuda.CanonicalISSN(b)
	return ca != "" && ca == cb
}

// qJudul = query search alternatif berbasis judul (fallback bila P-ISSN
// tak valid) — maks 60 char, dipotong di batas spasi.
func qJudul(n string) string {
	n = strings.TrimSpace(n)
	if len(n) <= 60 {
		return n
	}
	n = n[:60]
	if i := strings.LastIndex(n, " "); i > 30 {
		n = n[:i]
	}
	return n
}

func salinFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func hitungDupTulis(r *e4bJSON) int {
	n := 0
	for _, d := range r.Dup {
		if d.Tulis {
			n++
		}
	}
	return n
}

func hitungSearchTulis(r *e4bJSON) int {
	n := 0
	for _, s := range r.Search {
		if s.Tulis {
			n++
		}
	}
	return n
}

func hitungViewTulis(r *e4bJSON) int {
	n := 0
	for _, v := range r.View {
		if v.Tulis {
			n++
		}
	}
	return n
}
