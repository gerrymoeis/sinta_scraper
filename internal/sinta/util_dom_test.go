package sinta

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func mustParse(t *testing.T, s string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("parse HTML: %v", err)
	}
	return doc
}

func TestByClassExactToken(t *testing.T) {
	doc := mustParse(t, `<div id="a" class="col-md-4"></div><div id="b" class="foo col-md bar"></div>`)

	a := findFirst(doc, byClass("col-md-4"))
	b := findFirst(doc, byClass("col-md"))
	switch {
	case a == nil:
		t.Fatal("elemen col-md-4 tidak ditemukan")
	case getAttr(a, "id") != "a":
		t.Errorf("findFirst(col-md-4) = id %q, want a", getAttr(a, "id"))
	case b == nil:
		t.Fatal("elemen col-md tidak ditemukan")
	case getAttr(b, "id") != "b":
		t.Errorf("byClass(%q) salah ambil id %q — substring match aktif?", "col-md", getAttr(b, "id"))
	}
}

func TestTraversalDanText(t *testing.T) {
	doc := mustParse(t, `
		<div id="root" class="card">
			<a id="inner" href="/u1"> Satu <i></i> Dua </a>
			<span class="pr-txt">next</span>
		</div>`)
	card := findFirst(doc, byClass("card"))
	if card == nil {
		t.Fatal("div.card tidak ditemukan")
	}

	a := findFirst(card, byTag("a"))
	if a == nil || getAttr(a, "id") != "inner" {
		t.Fatalf("findFirst(byTag a) salah sasaran: %v", a)
	}
	if href := getAttr(a, "href"); href != "/u1" {
		t.Errorf("href = %q, want /u1", href)
	}
	if got := textOf(a); got != "Satu Dua" {
		t.Errorf("textOf = %q, want %q", got, "Satu Dua")
	}
	if links := findAll(card, byTag("a")); len(links) != 1 {
		t.Errorf("findAll(a) = %d, want 1", len(links))
	}
	if s := nextSibling(a, byClass("pr-txt")); s == nil {
		t.Error("nextSibling(.pr-txt) tidak ditemukan")
	}
	if s := nextSibling(a, byClass("nope")); s != nil {
		t.Error("nextSibling(.nope) seharusnya nil")
	}
	if p := nearestAncestor(a, byClass("card")); p == nil || getAttr(p, "id") != "root" {
		t.Errorf("nearestAncestor(.card) salah sasaran: %v", p)
	}
}

func TestParseIndoNumber(t *testing.T) {
	cases := map[string]float64{
		"1.678": 1678, "16.772": 16772, "104,00": 104, "0,85": 0.85, "": 0,
	}
	for in, want := range cases {
		got, err := parseIndoNumber(in)
		if err != nil {
			t.Errorf("parseIndoNumber(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseIndoNumber(%q) = %v, want %v", in, got, want)
		}
	}
}
