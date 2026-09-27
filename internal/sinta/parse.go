package sinta

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

var (
	reProfileID  = regexp.MustCompile(`/profile/(\d+)`)
	rePISSN      = regexp.MustCompile(`P-ISSN\s*:\s*([0-9Xx]*)`)
	reEISSN      = regexp.MustCompile(`E-ISSN\s*:\s*([0-9Xx]*)`)
	reSubject    = regexp.MustCompile(`Subject Area\s*:\s*(.+)$`)
	reRank       = regexp.MustCompile(`S\s*(\d)`) // badge "S1 Accredited" → 1
	rePagination = regexp.MustCompile(`Page\s+(\d+)\s+of\s+([\d.]+)\s*\|\s*Total Records\s+([\d.]+)`)
)

// ParsePage mem-parsing satu halaman listing SINTA menjadi FilterPageResult.
// pageNum hanya dicatat sebagai jejak audit (SourcePage). Field SintaRank
// diisi oleh pemanggil — parser tidak tahu flag -rank.
//
// Satu kartu gagal parse dilewati tanpa menggagalkan halaman; jaring
// pengamannya adalah verifikasi jumlah record vs server di akhir stage.
func ParsePage(r io.Reader, pageNum int) (*FilterPageResult, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("gagal parse HTML halaman %d: %w", pageNum, err)
	}

	cards := findJournalCards(doc)
	journals := make([]Journal, 0, len(cards))
	for _, card := range cards {
		j, err := parseCard(card)
		if err != nil {
			continue
		}
		j.SourcePage = pageNum
		journals = append(journals, j)
	}

	totalPages, totalJournals := parsePagination(doc)
	return &FilterPageResult{
		Journals:      journals,
		CurrentPage:   pageNum,
		TotalPages:    totalPages,
		TotalJournals: totalJournals,
	}, nil
}

// findJournalCards mencari semua div.col-md yang punya descendant .affil-name —
// kombinasi dua class ini spesifik kartu jurnal dan menghindari div.col-md
// lain (grid layout, panel filter). Berhenti turun begitu kartu ketemu.
func findJournalCards(doc *html.Node) []*html.Node {
	isCard := byClass("col-md")
	hasName := byClass("affil-name")
	var cards []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" && isCard(n) && findFirst(n, hasName) != nil {
			cards = append(cards, n)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return cards
}

func parseCard(card *html.Node) (Journal, error) {
	var j Journal

	// Nama + URL profil + ID (ID = gate: kartu tanpa ID bukan kartu jurnal)
	nameDiv := findFirst(card, byClass("affil-name"))
	var prof *html.Node
	if nameDiv != nil {
		prof = findFirst(nameDiv, byTag("a"))
	}
	if prof == nil {
		return j, fmt.Errorf("kartu tanpa link profil")
	}
	j.SINTAProfileURL = getAttr(prof, "href")
	j.Name = textOf(prof)
	if m := reProfileID.FindStringSubmatch(j.SINTAProfileURL); m != nil {
		if id, err := strconv.Atoi(m[1]); err == nil {
			j.ID = id
		}
	}
	if j.ID == 0 {
		return j, fmt.Errorf("ID profil tidak ditemukan di %q", j.SINTAProfileURL)
	}

	// Link referensi: "Google Scholar" | "Website" (→ OJSURL) | "Editor URL"
	if abbrevDiv := findFirst(card, byClass("affil-abbrev")); abbrevDiv != nil {
		for _, lnk := range findAll(abbrevDiv, byTag("a")) {
			href := getAttr(lnk, "href")
			text := strings.ToLower(textOf(lnk))
			switch {
			case strings.Contains(text, "google scholar"):
				j.GoogleScholarURL = href
			case strings.Contains(text, "editor url"):
				j.EditorURL = href
			case strings.Contains(text, "website"):
				j.OJSURL = href
			}
		}
	}

	// Afiliasi (teks = program + fakultas + universitas)
	if locDiv := findFirst(card, byClass("affil-loc")); locDiv != nil {
		if a := findFirst(locDiv, byTag("a")); a != nil {
			j.AffiliationURL = getAttr(a, "href")
			j.AffiliationName = textOf(a)
		}
	}

	// ISSN + subject area — subject ADA di listing untuk sebagian jurnal,
	// KOSONG untuk lainnya (Open Point #7)
	if idDiv := findFirst(card, byClass("profile-id")); idDiv != nil {
		text := textOf(idDiv)
		if m := rePISSN.FindStringSubmatch(text); m != nil {
			j.PrintISSN = m[1]
		}
		if m := reEISSN.FindStringSubmatch(text); m != nil {
			j.ElectronicISSN = m[1]
		}
		if m := reSubject.FindStringSubmatch(text); m != nil {
			j.SubjectArea = m[1]
		}
	}

	// Badge akreditasi ("S1 Accredited" → 1) + indeks
	if statPrev := findFirst(card, byClass("stat-prev")); statPrev != nil {
		if rankEl := findFirst(statPrev, byClass("accredited")); rankEl != nil {
			if m := reRank.FindStringSubmatch(textOf(rankEl)); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					j.SintaRank = n
				}
			}
		}
		j.IsScopus = findFirst(statPrev, byClass("scopus-indexed")) != nil
		if garudaEl := findFirst(statPrev, byClass("garuda-indexed")); garudaEl != nil {
			j.IsGaruda = true
			if a := nearestAncestor(garudaEl, byTag("a")); a != nil {
				j.GarudaURL = getAttr(a, "href")
			}
		}
	}

	// Statistik — label asli di kartu (div refresh): Impact, H5-index,
	// Citations 5yr, Citations; nilai format Indonesia ("104,00", "14.902")
	if statBlock := findFirst(card, byClass("journal-list-stat")); statBlock != nil {
		for _, numEl := range findAll(statBlock, byClass("pr-num")) {
			label := ""
			if lbl := nextSibling(numEl, byClass("pr-txt")); lbl != nil {
				label = strings.ToLower(textOf(lbl))
			}
			val, err := parseIndoNumber(textOf(numEl))
			if err != nil {
				continue
			}
			switch label {
			case "impact":
				j.Impact = val
			case "h5-index":
				j.H5Index = int(val)
			case "citations 5yr":
				j.CitationsLast5Years = int(val)
			case "citations":
				j.CitationsTotal = int(val)
			}
		}
	}

	return j, nil
}

// parsePagination mengambil "Page 1 of 1.678 | Total Records 16.772" dari
// div.pagination-text (titik = pemisah ribuan → dibuang). Tanpa pagination →
// 0,0 (halaman kosong/error ditangani pemanggil lewat jumlah kartu).
func parsePagination(doc *html.Node) (totalPages, totalJournals int) {
	el := findFirst(doc, byClass("pagination-text"))
	if el == nil {
		return 0, 0
	}
	m := rePagination.FindStringSubmatch(textOf(el))
	if m == nil {
		return 0, 0
	}
	totalPages, _ = strconv.Atoi(strings.ReplaceAll(m[2], ".", ""))
	totalJournals, _ = strconv.Atoi(strings.ReplaceAll(m[3], ".", ""))
	return totalPages, totalJournals
}
