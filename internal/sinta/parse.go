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
	reProfileID    = regexp.MustCompile(`/profile/(\d+)`)
	rePISSN        = regexp.MustCompile(`P-ISSN\s*:\s*([0-9Xx]*)`)
	reEISSN        = regexp.MustCompile(`E-ISSN\s*:\s*([0-9Xx]*)`)
	reSubject      = regexp.MustCompile(`Subject Area\s*:\s*(.+)$`)
	rePagination   = regexp.MustCompile(`Page\s+(\d+)\s+of\s+([\d.]+)\s*\|\s*Total Records\s+([\d.]+)`)
)

// ParsePage mem-parsing satu halaman HTML listing jurnal SINTA menjadi
// kumpulan Journal yang terstruktur. pageNum dicatat di tiap record sebagai
// jejak audit (SourcePage), bukan dipakai untuk logika parsing.
func ParsePage(r io.Reader, pageNum int) (*PageResult, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("gagal parse HTML halaman %d: %w", pageNum, err)
	}

	cards := findJournalCards(doc)
	journals := make([]Journal, 0, len(cards))
	for _, card := range cards {
		j, err := parseCard(card)
		if err != nil {
			// Satu kartu gagal di-parse tidak boleh menggagalkan seluruh halaman.
			// TODO: ganti jadi structured logging kalau butuh audit lebih detail.
			continue
		}
		j.SourcePage = pageNum
		journals = append(journals, j)
	}

	return &PageResult{
		Journals:     journals,
		CurrentPage:  pageNum,
		TotalPages:   parsePaginationTotalPages(doc),
		TotalRecords: parsePaginationTotalRecords(doc),
	}, nil
}

// findJournalCards mencari semua div.col-md yang juga punya descendant
// div.affil-name. Kombinasi dua class ini spesifik untuk kartu jurnal dan
// menghindari false positive dari div.col-md lain di halaman (grid layout,
// panel filter, dll -- lihat catatan di README soal ini).
func findJournalCards(doc *html.Node) []*html.Node {
	var cards []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "col-md") {
			if findFirstByClass(n, "affil-name") != nil {
				cards = append(cards, n)
				return // jangan turun lebih dalam ke subtree kartu yang sudah ketemu
			}
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

	if nameDiv := findFirstByClass(card, "affil-name"); nameDiv != nil {
		if a := findFirstTag(nameDiv, "a"); a != nil {
			j.ProfileURL = getAttr(a, "href")
			j.Name = strings.TrimSpace(nodeText(a))
			if m := reProfileID.FindStringSubmatch(j.ProfileURL); m != nil {
				if id, err := strconv.Atoi(m[1]); err == nil {
					j.ID = id
				}
			}
		}
	}
	if j.ID == 0 {
		return j, fmt.Errorf("tidak menemukan journal ID pada kartu (mungkin bukan kartu jurnal, atau markup berubah)")
	}

	if abbrevDiv := findFirstByClass(card, "affil-abbrev"); abbrevDiv != nil {
		for _, a := range findAllTag(abbrevDiv, "a") {
			href := getAttr(a, "href")
			text := strings.ToLower(nodeText(a))
			switch {
			case strings.Contains(text, "google scholar"):
				j.GoogleScholarURL = href
			case strings.Contains(text, "editor url"):
				j.EditorURL = href
			case strings.Contains(text, "website"):
				j.WebsiteURL = href
			}
		}
	}

	if locDiv := findFirstByClass(card, "affil-loc"); locDiv != nil {
		if a := findFirstTag(locDiv, "a"); a != nil {
			j.AffiliationURL = getAttr(a, "href")
			j.Affiliation = strings.TrimSpace(nodeText(a))
		}
	}

	if idDiv := findFirstByClass(card, "profile-id"); idDiv != nil {
		text := collapseSpace(nodeText(idDiv))
		if m := rePISSN.FindStringSubmatch(text); m != nil {
			j.ISSNPrint = m[1]
		}
		if m := reEISSN.FindStringSubmatch(text); m != nil {
			j.ISSNElectronic = m[1]
		}
		if m := reSubject.FindStringSubmatch(text); m != nil {
			j.SubjectArea = strings.TrimSpace(m[1])
		}
	}

	if statPrev := findFirstByClass(card, "stat-prev"); statPrev != nil {
		if rankEl := findFirstByClass(statPrev, "accredited"); rankEl != nil {
			j.SintaRank = collapseSpace(nodeText(rankEl))
		}
		j.IsScopus = findFirstByClass(statPrev, "scopus-indexed") != nil
		if garudaEl := findFirstByClass(statPrev, "garuda-indexed"); garudaEl != nil {
			j.IsGaruda = true
			if a := nearestAncestorTag(garudaEl, "a"); a != nil {
				j.GarudaURL = getAttr(a, "href")
			}
		}
	}

	if statBlock := findFirstByClass(card, "journal-list-stat"); statBlock != nil {
		for _, numEl := range findAllByClass(statBlock, "pr-num") {
			label := ""
			if lbl := nextSiblingByClass(numEl, "pr-txt"); lbl != nil {
				label = strings.ToLower(collapseSpace(nodeText(lbl)))
			}
			val, err := parseIndoNumber(collapseSpace(nodeText(numEl)))
			if err != nil {
				continue
			}
			switch label {
			case "impact":
				j.Impact = val
			case "h5-index":
				j.H5Index = int64(val)
			case "citations 5yr":
				j.Citations5yr = int64(val)
			case "citations":
				j.CitationsTotal = int64(val)
			}
		}
	}

	return j, nil
}

// parsePaginationTotalPages mengambil jumlah halaman total dari teks
// "Page 1 of N | Total Records X" di div.pagination-text.
func parsePaginationTotalPages(doc *html.Node) int {
	text := findPaginationText(doc)
	if text == "" {
		return 0
	}
	m := rePagination.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(strings.ReplaceAll(m[2], ".", ""))
	if err != nil {
		return 0
	}
	return n
}

// parsePaginationTotalRecords mengambil total records dari teks
// "Page 1 of N | Total Records X" di div.pagination-text.
func parsePaginationTotalRecords(doc *html.Node) int {
	text := findPaginationText(doc)
	if text == "" {
		return 0
	}
	m := rePagination.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(strings.ReplaceAll(m[3], ".", ""))
	if err != nil {
		return 0
	}
	return n
}

func findPaginationText(doc *html.Node) string {
	if el := findFirstByClass(doc, "pagination-text"); el != nil {
		return collapseSpace(nodeText(el))
	}
	return ""
}
