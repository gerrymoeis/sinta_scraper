package sinta

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// Vokabular pencarian DOM: kombinasi ARAH (findFirst/findAll/nearestAncestor/
// nextSibling) × MATCHER (byClass/byTag). Satu implementasi per arah —
// tidak ada varian per-class/per-tag yang menggandakan algoritma.

// byClass cocok dengan elemen yang punya token class PERSIS, bukan substring.
// Jebakan markup SINTA: "col-md" dan "col-md-4" hidup berdampingan.
func byClass(class string) func(*html.Node) bool {
	return func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return false
		}
		for _, a := range n.Attr {
			if a.Key != "class" {
				continue
			}
			for _, c := range strings.Fields(a.Val) {
				if c == class {
					return true
				}
			}
		}
		return false
	}
}

// byTag cocok dengan elemen tag tertentu. Guard Type wajib: text node juga
// punya Data — teks "a" di halaman bisa salah cocok dengan <a>.
func byTag(tag string) func(*html.Node) bool {
	return func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == tag
	}
}

// findFirst: DFS pre-order ke bawah, berhenti di kecocokan pertama.
func findFirst(n *html.Node, match func(*html.Node) bool) *html.Node {
	if match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := findFirst(c, match); r != nil {
			return r
		}
	}
	return nil
}

// findAll: DFS ke bawah, semua kecocokan (urutan pre-order).
func findAll(n *html.Node, match func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	if match(n) {
		out = append(out, n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out = append(out, findAll(c, match)...)
	}
	return out
}

// nearestAncestor: berjalan ke atas mulai dari parent.
func nearestAncestor(n *html.Node, match func(*html.Node) bool) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if match(p) {
			return p
		}
	}
	return nil
}

// nextSibling: berjalan ke kanan pada parent yang sama.
func nextSibling(n *html.Node, match func(*html.Node) bool) *html.Node {
	for s := n.NextSibling; s != nil; s = s.NextSibling {
		if match(s) {
			return s
		}
	}
	return nil
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// textOf mengumpulkan seluruh teks dalam subtree (icon <i></i> tanpa teks
// otomatis terabaikan) dan merapikan whitespace jadi satu spasi —
// menggantikan pola collapseSpace(nodeText(x)) / TrimSpace(nodeText(x)).
func textOf(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(sb.String()), " ")
}

// parseIndoNumber mem-parsing angka format Indonesia: "1.678" (titik = ribuan),
// "104,00" (koma = desimal) — buang titik dulu, ganti koma jadi titik.
func parseIndoNumber(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	s = strings.ReplaceAll(s, ".", "")
	s = strings.ReplaceAll(s, ",", ".")
	return strconv.ParseFloat(s, 64)
}
