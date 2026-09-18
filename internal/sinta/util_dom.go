package sinta

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// hasClass mengecek keberadaan satu token class secara EXACT (bukan substring).
// Ini penting: markup SINTA punya class seperti "col-md" dan "col-md-4"
// berdampingan -- pengecekan strings.Contains akan salah mencocokkan keduanya.
func hasClass(n *html.Node, class string) bool {
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

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// nodeText mengumpulkan semua text node di dalam subtree n, termasuk anak-anaknya.
// Icon <i></i> tidak punya text node sehingga otomatis terabaikan.
func nodeText(n *html.Node) string {
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
	return sb.String()
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// findFirstByClass mencari node pertama (pre-order) di dalam subtree n yang
// punya class tertentu.
func findFirstByClass(n *html.Node, class string) *html.Node {
	var result *html.Node
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && hasClass(n, class) {
			result = n
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(n)
	return result
}

func findAllByClass(n *html.Node, class string) []*html.Node {
	var results []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && hasClass(n, class) {
			results = append(results, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return results
}

func findFirstTag(n *html.Node, tag string) *html.Node {
	var result *html.Node
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == tag {
			result = n
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(n)
	return result
}

func findAllTag(n *html.Node, tag string) []*html.Node {
	var results []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == tag {
			results = append(results, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return results
}

func nearestAncestorTag(n *html.Node, tag string) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == tag {
			return p
		}
	}
	return nil
}

// nextSiblingByClass mencari sibling setelah n (di parent yang sama) dengan class tertentu.
func nextSiblingByClass(n *html.Node, class string) *html.Node {
	for s := n.NextSibling; s != nil; s = s.NextSibling {
		if s.Type == html.ElementNode && hasClass(s, class) {
			return s
		}
	}
	return nil
}

// parseIndoNumber mem-parsing angka format Indonesia, misal "14.902" (titik =
// pemisah ribuan) atau "104,00" (koma = pemisah desimal). Kedua pola SINTA ini
// bisa ditangani satu fungsi: buang semua titik dulu, baru ganti koma jadi titik.
func parseIndoNumber(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	s = strings.ReplaceAll(s, ".", "")
	s = strings.ReplaceAll(s, ",", ".")
	return strconv.ParseFloat(s, 64)
}
