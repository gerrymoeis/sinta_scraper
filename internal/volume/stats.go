package volume

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Input = bahan Compute utk SATU jurnal (semua fungsi murni, tanpa HTTP).
type Input struct {
	Archive  []ArchiveIssue // semua tahun (raw); urutan apa pun
	Crossref CrossrefData   // utk OJS dgn DOI; kosong utk DOI-tipis (gap-B)
	TOC      map[string]int // issue_url→count — D1: kunci URL OTENTIK (bukan vol/no — archive↔TOC volno tak pernah diuji silang)
}

// Result = output Compute — dikirim driver ke storage.UpsertVolumeStats.
type Result struct {
	AnchorYear     int64
	AnchorSource   string // "crossref"|"archive"|"crossref+archive" — audit gap-A
	WindowStart    int64
	WindowEnd      int64
	VolPerYear     float64 // D1
	ArtPerVol      float64 // D2 — dari TOC (primer)
	ReleaseMonths  string  // D3 "2,6,10" — min(bulan artikel thn MaxYear) per issue (refine-D/G3)
	IssuesJSON     string  // raw SEMUA tahun, nullable field jujur — default "[]" (G4)
	IsComplete     bool    // driver boleh override bila archive paginasi parsial
	WindowIssues   int     // jumlah issue dlm window (audit) — baris archive Vol=0 TIDAK dihitung (G2)
	WindowTOCable  int     // dlm window dgn issue_url (bisa di-TOC driver) — H18: syarat complete
	WindowCounted  int     // dgn TOC count > 0 (audit; < WindowIssues = parsial)
	ArchiveParsed  int     // baris archive dgn Year ter-parse (log iterasi #1 gap-A)
	UnparsedVI     int     // baris archive Vol=0 (G2: year utk anchor saja, tak dihitung D1)
	CrossrefIssues int     // pasang (vol,no) Crossref (audit)
	Notes          []string
}

// Compute menghitung statistik volume (doc 41 §13.7 K6 REVISED) dari archive
// + Crossref + TOC. Gap-A: anchor = max(Crossref max-year, max archive-year) —
// window TIDAK geser mundur bila issue terbaru belum dideposit (j185-case).
// Gap-B: Crossref kosong (j679-case) → anchor dari archive-year; keduanya 0 →
// anchor null + is_complete=false (jujur, bukan asumsi).
func Compute(in Input) Result {
	var r Result
	r.IssuesJSON = "[]" // G4: default valid — early-return tetap tulis audit

	// 1. Anchor chain (gap-A/B + G1). archiveYear = max tahun baris archive.
	archiveYear := int64(0)
	archiveVI := map[[2]int64]bool{}
	for _, iss := range in.Archive {
		if iss.Year > archiveYear {
			archiveYear = iss.Year
		}
		if iss.Year != 0 {
			r.ArchiveParsed++
		}
		if iss.Vol != 0 {
			archiveVI[[2]int64{iss.Vol, iss.No}] = true
		}
	}
	// G1: anchorCrossref = max MaxYear dari Crossref VI yang MATCH archive —
	// early-view/online-first (issue belum di archive) TIDAK menarik anchor.
	// Fallback global MaxYear hanya bila archive punya TANPA VI valid sama
	// sekali (vol-no parse gagal semua / archive kosong) — jujur utk kasus
	// archive tak terbaca.
	var crossMatched int64
	for _, k := range sortedVI(in.Crossref.Issues) {
		if archiveVI[k] {
			if ci := in.Crossref.Issues[k]; ci.MaxYear > crossMatched {
				crossMatched = ci.MaxYear
			}
		}
	}
	crossYear := crossMatched
	if crossYear == 0 && len(archiveVI) == 0 && in.Crossref.MaxYear > 0 {
		crossYear = in.Crossref.MaxYear
		r.Notes = append(r.Notes, "anchor_crossref=global_max (archive tanpa VI valid)")
	}
	switch {
	case crossYear > 0 && archiveYear > 0:
		r.AnchorYear = max64(crossYear, archiveYear)
		if r.AnchorYear == crossYear && r.AnchorYear == archiveYear {
			r.AnchorSource = "crossref+archive"
		} else if r.AnchorYear == archiveYear {
			r.AnchorSource = "archive"
		} else {
			r.AnchorSource = "crossref"
		}
	case crossYear > 0:
		r.AnchorYear, r.AnchorSource = crossYear, "crossref"
	case archiveYear > 0:
		r.AnchorYear, r.AnchorSource = archiveYear, "archive"
	default:
		r.Notes = append(r.Notes, "anchor_gagal_total (Crossref+archive tanpa tahun)")
		return r
	}
	r.WindowStart = r.AnchorYear - 2
	r.WindowEnd = r.AnchorYear
	r.CrossrefIssues = len(in.Crossref.Issues)

	// 2. Issues JSON: raw SEMUA tahun (user konfirmasi 14 Okt); year/month
	// ambil dari Crossref dulu (93-100%), fallback archive-baris; count = TOC.
	type issueOut struct {
		Vol        int64  `json:"vol"`
		No         int64  `json:"no"`
		Year       int64  `json:"year"`
		Month      int64  `json:"month"`
		ArticleCt  int64  `json:"article_count"`
		IssueURL   string `json:"issue_url"`
		YearSource string `json:"year_source"`
		TOCVisited bool   `json:"-"` // driver SUDAH GET TOC utk URL ini (bisa 0 artikel) — H18 anti-resume-loop
	}
	out := make([]issueOut, 0, len(in.Archive))
	seenVI := map[[2]int64]bool{}
	for _, iss := range in.Archive {
		key := [2]int64{iss.Vol, iss.No}
		oi := issueOut{Vol: iss.Vol, No: iss.No, Year: iss.Year,
			Month: iss.Month, IssueURL: iss.IssueURL, YearSource: "archive"}
		if iss.Vol != 0 {
			if ci, ok := in.Crossref.Issues[key]; ok {
				seenVI[key] = true
				if ci.MaxYear != 0 {
					oi.Year, oi.YearSource = ci.MaxYear, "crossref"
				}
				if ci.MinMonth != 0 {
					oi.Month = ci.MinMonth // refine-D
				}
			}
		}
		if oi.Year == 0 {
			oi.YearSource = ""
		}
		// D1: match TOC by issue_url OTENTIK (hasil ParseTOC driver di-attach
		// ke baris dgn URL sama) — tanpa asumsi volno archive == volno TOC.
		// TOCVisited: key ADA dlm map = driver SUDAH GET (bila count=0 =
		// issue valid 0 artikel / template beda — tetap "sudah dicek" utk H18;
		// TANPA key = gagal/absen → parsial utk retry).
		if oi.IssueURL != "" {
			if c, ok := in.TOC[oi.IssueURL]; ok {
				oi.TOCVisited = true
				if c > 0 {
					oi.ArticleCt = int64(c)
				} else {
					r.Notes = append(r.Notes, fmt.Sprintf(
						"toc_kosong vol%dno%d (0 artikel / permanen-tak-tertersedia / template beda) — D2 exclude, completeness terpenuhi (sudah dicek)",
						oi.Vol, oi.No))
				}
			}
		}
		// G2: baris archive Vol=0 — year tetap utk anchor (loop 1), tapi
		// TIDAK dihitung sbg WindowIssues (anti double-count dgn Crossref-only
		// VI; D1 jujur turun utk 14% theme gagal-parse volno).
		if iss.Vol == 0 {
			r.UnparsedVI++
		} else if oi.Year >= r.WindowStart && oi.Year <= r.WindowEnd && oi.Year != 0 {
			r.WindowIssues++
			if oi.IssueURL != "" {
				r.WindowTOCable++ // archive VI valid selalu punya URL
			}
			if oi.TOCVisited {
				r.WindowCounted++
			}
		}
		out = append(out, oi)
	}
	// Crossref VI tak ada di archive list (deposit/arsip beda) — tetap masuk
	// raw issues dgn issue_url kosong (jujur; year_source=crossref).
	for _, k := range sortedVI(in.Crossref.Issues) {
		if seenVI[k] {
			continue
		}
		ci := in.Crossref.Issues[k]
		oi := issueOut{Vol: k[0], No: k[1], Year: ci.MaxYear,
			Month: ci.MinMonth, YearSource: "crossref"}
		// D1: crossref-only tanpa issue_url — TIDAK masuk TOC map (konsisten
		// H18: tak bisa di-TOC driver); count=0 jujur.
		if oi.Year >= r.WindowStart && oi.Year <= r.WindowEnd {
			r.WindowIssues++
			if oi.ArticleCt > 0 {
				r.WindowCounted++
			}
		}
		out = append(out, oi)
	}
	blob, err := json.Marshal(out)
	if err != nil {
		r.Notes = append(r.Notes, "issues_json_gagal: "+err.Error())
	}
	r.IssuesJSON = string(blob)

	// 3. Agregat dlm jendela V3 (approve b′): D1 issue/tahun · D2 TOC · D3 bulan.
	var sumCt, nCt int64
	months := map[int64]bool{}
	for _, oi := range out {
		if oi.Year == 0 || oi.Year < r.WindowStart || oi.Year > r.WindowEnd {
			continue
		}
		if oi.ArticleCt > 0 {
			sumCt += oi.ArticleCt
			nCt++
		}
		if oi.Month > 0 {
			months[oi.Month] = true
		}
	}
	if r.WindowIssues > 0 {
		r.VolPerYear = float64(r.WindowIssues) / 3.0
	}
	if nCt > 0 {
		r.ArtPerVol = float64(sumCt) / float64(nCt)
	}
	if len(months) > 0 {
		ms := make([]int, 0, len(months))
		for m := range months {
			ms = append(ms, int(m))
		}
		sort.Ints(ms)
		ss := make([]string, len(ms))
		for i, m := range ms {
			ss[i] = strconv.Itoa(m)
		}
		r.ReleaseMonths = strings.Join(ss, ",")
	}

	// 4. IsComplete (refine-C + H18): syarat = counted == TOCable (issue yang
	// BISA di-TOC — issue_url≠""; driver tak bisa GET utk crossref-only).
	// Crossref-only tetap dihitung D1 tapi TIDAK menuntut TOC — anti
	// infinite-retry utk 14% theme archive volno gagal. Bila TOCable=0
	// (semua crossref-only) → retry tak berdaya → complete=true + D2=0 jujur.
	if r.WindowTOCable > 0 {
		r.IsComplete = r.WindowCounted == r.WindowTOCable
	} else if r.WindowIssues > 0 {
		r.IsComplete = true
		r.Notes = append(r.Notes, fmt.Sprintf(
			"window_semua_crossref_only (%d issue tanpa URL archive) — retry tak berdaya",
			r.WindowIssues))
	}
	if r.WindowCounted == 0 && r.WindowTOCable > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf(
			"tanpa_toc_count utk window (%d TOCable, 0 terhitung) — D2=0 jujur",
			r.WindowTOCable))
	}
	if r.ArchiveParsed == 0 && len(in.Archive) > 0 {
		r.Notes = append(r.Notes, "archive_tanpa_tahun (Year=0 semua baris)")
	}
	if r.UnparsedVI > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf(
			"archive_vol0=%d baris (year utk anchor saja; tak dihitung D1 — G2)",
			r.UnparsedVI))
	}
	return r
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
