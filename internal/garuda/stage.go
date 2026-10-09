package garuda

import (
	"bytes"
	"fmt"
	"time"

	"sinta-scraper/internal/storage"
)

// Stage produksi -stages=garuda (doc 40 — approve user 9 Okt 2026; Q4 hybrid
// §9.1: seluruh logika di package, driver cmd/garuda tetap eksperimen).
// Pipeline per scope -rank, semua sub-fase resume/idempoten:
//
//  1. search + match exact E-ISSN (resume phase2) → capture + prov subject;
//  2. build subject_map + harmonisasi → subject_area_canonical
//     (GET /area on-demand bila label baru);
//  3. sync fill-if-absent → journals (subject canonical; garuda_url capture;
//     nilai beda TIDAK ditimpa — dicatat utk review K5/K6);
//  4. view (opsi B — approve 9 Okt): matched → GET view/N → link home/OAI
//     + fallback subject/print fill-if-absent, flag VIEW_DONE terminal;
//  5. verifikasi jujur (match_status terisi utk tiap target + nol error).
//
// Etika: satu client Garuda dgn limiter sendiri (delay -min/-max-delay),
// metrics per-stage "garuda" (NewClient), UA riset sama dgn Tahap 1.

// FlagViewDone = flag phase2_progress: halaman view sudah di-fetch dan
// berstatus terminal (found / record-not-found / tanpa link) — resume view.
const FlagViewDone = "VIEW_DONE"

// StageConfig = konfigurasi stage garuda (diisi main.go).
type StageConfig struct {
	Ranks       []int // scope sinta_rank (ParseRankSet; nil = semua)
	Refresh     bool  // abaikan resume (dipetakan dari -refresh global)
	UserAgent   string
	MinDelay    time.Duration
	MaxDelay    time.Duration
	FixturesDir string // fixture search/view/area per run (log run)
	Logf        func(format string, args ...any)
}

// StageReport = ringkasan jujur seluruh sub-fase (utk log + verifikasi).
type StageReport struct {
	Target  int
	Harvest *HarvestReport
	Map     *MapReport

	SyncSubject int // fill journals.subject_area dari canonical
	SyncURL     int // fill journals.garuda_url dari capture
	SyncURLBeda int // garuda_url beda — TIDAK ditimpa (review K5/K6)

	ViewProses   int // view di-fetch pada run ini
	ViewSkip     int // resume (flag VIEW_DONE / link sudah ada)
	ViewNotFound int // halaman view record-not-found (jujur, tak ditulis)
	ViewLink     int // home/OAI terisi
	ViewSubj     int // subject fallback terisi (fill-if-absent)
	ViewPrint    int // print_issn fallback terisi (fill-if-absent)
	ViewErr      int // GET/parse gagal (resume run berikut)

	Verified  bool
	VerifyMsg string
}

// RunGarudaStage = orkestrator stage (sub-fase 1–5 di atas). Mengembalikan
// error hanya utk kegagalan fatal (db/query/target kosong); kegagalan per
// baris masuk ke report (ViewErr/Harvest.Errors) → Verified=false → exit
// code jujur dari main, resume mengulang hanya yang gagal.
func RunGarudaStage(store *storage.Store, cfg StageConfig) (*StageReport, error) {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	rep := &StageReport{}

	// ---- 0. target (scope -rank) ---------------------------------------
	targets, err := store.HarvestTargetsRank(cfg.Ranks)
	if err != nil {
		return nil, fmt.Errorf("baca target: %w", err)
	}
	rep.Target = len(targets)
	if len(targets) == 0 {
		return nil, fmt.Errorf("target kosong — tidak ada baris journals utk scope -rank ini (cek -rank & isi db)")
	}
	logf("[garuda] target = %d baris (refresh=%v)", len(targets), cfg.Refresh)

	c := NewClient("garuda", cfg.UserAgent, cfg.MinDelay, cfg.MaxDelay)

	// ---- 1. search + match (resume phase2) ------------------------------
	logf("[garuda] sub-fase 1: search + match exact E-ISSN (resume)")
	hrep, err := HarvestSubjects(store, c, targets, HarvestOptions{
		FixturesDir: cfg.FixturesDir,
		Refresh:     cfg.Refresh,
		OnProgress: func(done, total int, t storage.HarvestTarget, status string) {
			if status == "skip" {
				return
			}
			logf("[garuda] [%d/%d] %-11s EISSN=%s %s", done, total, status, t.EISSN, t.Name)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("harvest: %w", err)
	}
	rep.Harvest = hrep
	logf("[garuda] harvest: matched=%d not_found=%d ambiguous=%d skip(resume)=%d error=%d",
		hrep.Matched, hrep.NotFound, hrep.Ambiguous, hrep.Skipped, hrep.Errors)

	// ---- 2. subject_map + harmonisasi ----------------------------------
	logf("[garuda] sub-fase 2: build subject_map + harmonisasi")
	mrep, err := BuildDanHarmonisasi(store, c, cfg.FixturesDir)
	if err != nil {
		return nil, fmt.Errorf("subject_map: %w", err)
	}
	rep.Map = mrep
	logf("[garuda] canonical: terisi=%d NO_SUBJECT=%d LOW_EVIDENCE=%d | GET /area=%v | unknown=%v",
		mrep.Filled, mrep.NoSubject, mrep.LowEvidence, mrep.FetchedArea, mrep.UnknownLabels)

	// ---- 3. sync fill-if-absent → journals ------------------------------
	logf("[garuda] sub-fase 3: sinkronisasi fill-if-absent ke journals")
	if err := stageSync(store, cfg.Ranks, rep, logf); err != nil {
		return nil, err
	}

	// ---- 4. view (opsi B) -----------------------------------------------
	logf("[garuda] sub-fase 4: view (link home/OAI + fallback subject/print)")
	if err := stageView(store, c, cfg, rep, logf); err != nil {
		return nil, err
	}

	// ---- 5. verifikasi jujur --------------------------------------------
	if err := stageVerify(store, cfg.Ranks, rep, logf); err != nil {
		return nil, err
	}
	return rep, nil
}

// stageSync = sub-fase 3: subject canonical & garuda_url capture → journals
// (fill-if-absent; beda ≠ dicatat jujur, TIDAK ditimpa — K5/K6 review).
func stageSync(store *storage.Store, ranks []int, rep *StageReport, logf func(string, ...any)) error {
	rows, err := store.SyncTargets(ranks)
	if err != nil {
		return fmt.Errorf("sync: baca target: %w", err)
	}
	for _, r := range rows {
		if r.Canonical != "" && r.SubjNow == "" {
			n, err := store.UpdateJournalsE7(r.ID, "subject_area", r.Canonical)
			if err != nil {
				return fmt.Errorf("sync: fill subject j%d: %w", r.ID, err)
			}
			if n == 1 {
				rep.SyncSubject++
				if err := store.MergeProvenance(r.ID, "journals_subject_area", storage.ProvEntry{
					Value: r.Canonical, Source: "garuda", Confidence: 1.0,
				}); err != nil {
					return fmt.Errorf("sync: prov subject j%d: %w", r.ID, err)
				}
			}
		}
		if r.URLCap == "" {
			continue
		}
		switch {
		case r.URLNow == "":
			n, err := store.UpdateJournalsE7(r.ID, "garuda_url", r.URLCap)
			if err != nil {
				return fmt.Errorf("sync: fill garuda_url j%d: %w", r.ID, err)
			}
			if n == 1 {
				rep.SyncURL++
				if err := store.MergeProvenance(r.ID, "journals_garuda_url", storage.ProvEntry{
					Value: r.URLCap, Source: "garuda", Confidence: 1.0,
				}); err != nil {
					return fmt.Errorf("sync: prov garuda_url j%d: %w", r.ID, err)
				}
			}
		case r.URLNow != r.URLCap:
			rep.SyncURLBeda++
			logf("[garuda] j%d garuda_url BEDA: journals=%q capture=%q — TIDAK ditimpa (review K5/K6)",
				r.ID, r.URLNow, r.URLCap)
		}
	}
	logf("[garuda] sync: subject_terisi=%d url_terisi=%d url_beda(review)=%d (dari %d baris)",
		rep.SyncSubject, rep.SyncURL, rep.SyncURLBeda, len(rows))
	return nil
}

// stageView = sub-fase 4 (opsi B): fetch halaman view utk tiap matched →
// link home/OAI (E3 Opsi A) + fallback subject/print fill-if-absent (E8b).
// Terminal ditandai flag VIEW_DONE; error GET/parse TIDAK ditandai → rerun
// mengulang (jujur, bukan basi).
func stageView(store *storage.Store, c *Client, cfg StageConfig, rep *StageReport, logf func(string, ...any)) error {
	targets, err := store.ViewTargets(cfg.Ranks)
	if err != nil {
		return fmt.Errorf("view: baca target: %w", err)
	}
	for _, t := range targets {
		if !cfg.Refresh && (t.ViewDone || t.LinkNow) {
			rep.ViewSkip++
			continue
		}
		body, _, err := c.Get(ViewURL(int(t.GarudaID)), DefaultReferer)
		if err != nil {
			rep.ViewErr++
			logf("[garuda] j%d view/%d GET gagal: %v (tanpa flag — rerun mengulang)", t.ID, t.GarudaID, err)
			continue
		}
		v, perr := ParseViewPage(bytes.NewReader(body))
		if perr != nil {
			rep.ViewErr++
			logf("[garuda] j%d view/%d parse gagal: %v (tanpa flag — rerun mengulang)", t.ID, t.GarudaID, perr)
			continue
		}
		rep.ViewProses++
		markDone := func() error {
			return store.SetPhase2(t.ID, "", []string{FlagViewDone}, nil, "")
		}
		if v.NotFound {
			rep.ViewNotFound++
			if err := markDone(); err != nil {
				return fmt.Errorf("view: flag j%d: %w", t.ID, err)
			}
			logf("[garuda] j%d view/%d: record-not-found (ditatap, tak ditulis)", t.ID, t.GarudaID)
			continue
		}
		subjTerisi, printTerisi := false, false
		if v.HomeURL != "" || v.OAIURL != "" {
			if err := store.UpdateViewLinks(t.ID, v.HomeURL, v.OAIURL); err != nil {
				return fmt.Errorf("view: tulis link j%d: %w", t.ID, err)
			}
			rep.ViewLink++
		}
		if t.SubjNow == "" && len(v.Areas) > 0 {
			val := MergeSubjectArea("", v.Areas)
			n, err := store.UpdateJournalsE7(t.ID, "subject_area", val)
			if err != nil {
				return fmt.Errorf("view: fill subject j%d: %w", t.ID, err)
			}
			if n == 1 {
				rep.ViewSubj++
				subjTerisi = true
				if err := store.MergeProvenance(t.ID, "journals_subject_area", storage.ProvEntry{
					Value: val, Source: "garuda", Confidence: 1.0,
				}); err != nil {
					return fmt.Errorf("view: prov subject j%d: %w", t.ID, err)
				}
			}
		}
		if t.PrintNow == "" {
			// CanonicalISSN("-")/tak valid → "" → TIDAK ditulis (K1 jujur;
			// fakta full-run 9 Okt: 15 view hanya punya placeholder print).
			if p := CanonicalISSN(v.PrintISSN); p != "" {
				n, err := store.UpdateJournalsE7(t.ID, "print_issn", p)
				if err != nil {
					return fmt.Errorf("view: fill print j%d: %w", t.ID, err)
				}
				if n == 1 {
					rep.ViewPrint++
					printTerisi = true
					if err := store.MergeProvenance(t.ID, "journals_print_issn", storage.ProvEntry{
						Value: p, Source: "garuda", Confidence: 1.0,
					}); err != nil {
						return fmt.Errorf("view: prov print j%d: %w", t.ID, err)
					}
				}
			}
		}
		if err := markDone(); err != nil {
			return fmt.Errorf("view: flag j%d: %w", t.ID, err)
		}
		logf("[garuda] j%d view/%d: link home=%q oai=%q | subj_terisi=%v print_terisi=%v | areas=%d",
			t.ID, t.GarudaID, v.HomeURL, v.OAIURL, subjTerisi, printTerisi, len(v.Areas))
	}
	logf("[garuda] view: proses=%d skip(resume)=%d not_found=%d link=%d subject=%d print=%d error=%d (target=%d)",
		rep.ViewProses, rep.ViewSkip, rep.ViewNotFound, rep.ViewLink, rep.ViewSubj, rep.ViewPrint, rep.ViewErr, len(targets))
	return nil
}

// stageVerify = sub-fase 5: verifikasi jujur utk exit code.
// Syarat Verified: (a) tiap target punya match_status (target − harvest
// error), (b) nol harvest error, (c) nol view error.
func stageVerify(store *storage.Store, ranks []int, rep *StageReport, logf func(string, ...any)) error {
	terisi, err := store.CountMatchStatus(ranks)
	if err != nil {
		return fmt.Errorf("verifikasi: %w", err)
	}
	want := rep.Target - rep.Harvest.Errors
	var masalah []string
	if terisi != want {
		masalah = append(masalah, fmt.Sprintf("match_status terisi %d, harusnya %d (target %d − error %d)",
			terisi, want, rep.Target, rep.Harvest.Errors))
	}
	if rep.Harvest.Errors > 0 {
		masalah = append(masalah, fmt.Sprintf("%d error harvest (belum ditandai — rerun mengulang)", rep.Harvest.Errors))
	}
	if rep.ViewErr > 0 {
		masalah = append(masalah, fmt.Sprintf("%d error view (belum ditandai — rerun mengulang)", rep.ViewErr))
	}
	if len(masalah) == 0 {
		rep.Verified = true
		rep.VerifyMsg = fmt.Sprintf("match_status %d/%d | view error 0", terisi, rep.Target)
	} else {
		rep.Verified = false
		rep.VerifyMsg = fmt.Sprintf("%d masalah: %v", len(masalah), masalah)
	}
	logf("[garuda] verifikasi: %s → VERIFIED=%v", rep.VerifyMsg, rep.Verified)
	return nil
}
