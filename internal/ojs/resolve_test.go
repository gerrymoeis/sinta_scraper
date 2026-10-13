package ojs

import (
	"reflect"
	"testing"
)

// Fixture = kasus nyata P1 (doc 42 §7.1): j681 stub 24B vs garuda 200 ·
// j687 org→com related · j1626 → lww.com offsite · j817 iainkudus→uinsuku ·
// j88 WAF tunggal · http→https same-host normal · not_found simetri.

func TestResolveUpgradeSkemaNormal(t *testing.T) {
	// http→https same-host = normal, bukan stale (butir 3; j16/j19/j26).
	out := Resolve([]Candidate{
		{URL: "http://j26.id/", Source: "sinta", Status: 200,
			FinalURL: "https://j26.id/", BodyLen: 43_000},
	})
	if out.CanonicalURL != "http://j26.id/" {
		t.Errorf("canonical = %q (want url asal)", out.CanonicalURL)
	}
	if len(out.Flags) != 0 {
		t.Errorf("flags = %v (want kosong — upgrade skema bukan stale)", out.Flags)
	}
}

func TestResolveGarudaMenangStubSinta(t *testing.T) {
	// j681: sinta = stub 24B (200 tapi <1KB), garuda beei.org 200/23KB menang.
	out := Resolve([]Candidate{
		{URL: "http://journal.portalgaruda.org/", Source: "sinta", Status: 200,
			FinalURL: "http://journal.portalgaruda.org/", BodyLen: 24},
		{URL: "https://beei.org/", Source: "garuda", Status: 200,
			FinalURL: "https://beei.org/", BodyLen: 23_000},
	})
	if out.CanonicalURL != "https://beei.org/" {
		t.Errorf("canonical = %q, want garuda (200+23KB)", out.CanonicalURL)
	}
	if len(out.Losers) != 1 || out.Losers[0] != "http://journal.portalgaruda.org/" {
		t.Errorf("losers = %v", out.Losers)
	}
	// Stub 200 final=asal: kalah TAPI bukan stale menurut butir 6 harfiah.
	if !reflect.DeepEqual(out.Flags, []string(nil)) && len(out.Flags) != 0 {
		t.Errorf("flags = %v (want kosong)", out.Flags)
	}
}

func TestResolveHostChangeRelated(t *testing.T) {
	// j687 (not_found simetri — P1): sinta .org → 200 final .com NYATA
	// (host-change related via brand) → canonical + HOST_CHANGE; TANPA
	// STALE (pemenang tak pernah stale — butir 3).
	out := Resolve([]Candidate{
		{URL: "http://criticalcareshock.org/", Source: "sinta", Status: 200,
			FinalURL: "https://criticalcareshock.com/", BodyLen: 248_000},
	})
	if out.CanonicalURL != "http://criticalcareshock.org/" {
		t.Errorf("canonical = %q", out.CanonicalURL)
	}
	want := []string{FlagRedirectHostChange}
	if !reflect.DeepEqual(out.Flags, want) {
		t.Errorf("flags = %v, want %v", out.Flags, want)
	}
	if len(out.Losers) != 0 {
		t.Errorf("losers = %v (want kosong — satu kandidat menang)", out.Losers)
	}
}

func TestResolveHostChangeJ817(t *testing.T) {
	// j817: sinta DNS mati (status 0), garuda redirect iainkudus→uinsuku
	// (share label "journal" = related) → canonical + HOST_CHANGE.
	out := Resolve([]Candidate{
		{URL: "http://stainkudus.ac.id/", Source: "sinta", Status: 0},
		{URL: "http://journal.iainkudus.ac.id/", Source: "garuda", Status: 200,
			FinalURL: "https://journal.uinsuku.ac.id/", BodyLen: 111_000},
	})
	if out.CanonicalURL != "http://journal.iainkudus.ac.id/" {
		t.Errorf("canonical = %q", out.CanonicalURL)
	}
	want := []string{FlagRedirectHostChange, FlagStaleURL}
	if !reflect.DeepEqual(out.Flags, want) {
		t.Errorf("flags = %v, want %v", out.Flags, want)
	}
}

func TestResolveOffsiteHijack(t *testing.T) {
	// j1626: sinta → journals.lww.com (piihak ketiga, 403) — REDIRECT_OFFSITE,
	// jangan canonical otomatis; garuda 403 → semua gagal = WAF_BLOCKED.
	out := Resolve([]Candidate{
		{URL: "http://bjoaonline.com/", Source: "sinta", Status: 403,
			FinalURL: "https://journals.lww.com/bjoa", BodyLen: 5900},
		{URL: "http://balijournals.org/", Source: "garuda", Status: 403,
			FinalURL: "http://balijournals.org/", BodyLen: 75_000},
	})
	if out.CanonicalURL != "" {
		t.Errorf("canonical = %q (want kosong — semua gagal)", out.CanonicalURL)
	}
	want := []string{FlagRedirectOffsite, FlagStaleURL, FlagWAFBlocked}
	if !reflect.DeepEqual(out.Flags, want) {
		t.Errorf("flags = %v, want %v", out.Flags, want)
	}
}

func TestResolveFlagJujurSemuaGagal(t *testing.T) {
	// j88 WAF tunggal · down status-0 · 5xx — flag jujur, canonical kosong.
	kasus := []struct {
		nama  string
		cands []Candidate
		want  []string
	}{
		{"WAF tunggal", []Candidate{
			{URL: "https://j88.id/", Status: 403, FinalURL: "https://j88.id/", BodyLen: 5800},
		}, []string{FlagStaleURL, FlagWAFBlocked}},
		{"status 0", []Candidate{
			{URL: "https://j79.id/", Status: 0},
		}, []string{FlagStaleURL, FlagUnreachable}},
		{"5xx", []Candidate{
			{URL: "https://j94.id/", Status: 522, FinalURL: "https://j94.id/", BodyLen: 237_000},
		}, []string{FlagServerDown, FlagStaleURL}},
	}
	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			out := Resolve(k.cands)
			if out.CanonicalURL != "" {
				t.Errorf("canonical = %q (want kosong)", out.CanonicalURL)
			}
			if !reflect.DeepEqual(out.Flags, k.want) {
				t.Errorf("flags = %v, want %v", out.Flags, k.want)
			}
		})
	}
}

func TestResolveSimetriNotFound(t *testing.T) {
	// Q5 rev.3: resolusi jalan utk not_found (tanpa Garuda — kandidat sinta
	// saja; bukti 3/3 hidup P1).
	out := Resolve([]Candidate{
		{URL: "https://j948.id/", Source: "sinta", Status: 200,
			FinalURL: "https://j948.id/index.php/ibj", BodyLen: 90_000},
	})
	if out.CanonicalURL != "https://j948.id/" || len(out.Flags) != 0 {
		t.Errorf("canonical=%q flags=%v (want url tanpa flag)", out.CanonicalURL, out.Flags)
	}
}

func TestResolveTieBreakDeterministik(t *testing.T) {
	// Dua layak sama besar → URL asc (deterministik).
	out := Resolve([]Candidate{
		{URL: "https://b.id/", Status: 200, FinalURL: "https://b.id/", BodyLen: 5000},
		{URL: "https://a.id/", Status: 200, FinalURL: "https://a.id/", BodyLen: 5000},
	})
	if out.CanonicalURL != "https://a.id/" {
		t.Errorf("canonical = %q, want https://a.id/ (asc)", out.CanonicalURL)
	}
}

func TestResolveTanpaKandidat(t *testing.T) {
	out := Resolve(nil)
	if out.CanonicalURL != "" || len(out.Flags) != 0 {
		t.Errorf("hasil = %+v (want kosong)", out)
	}
}

func TestRelatedHost(t *testing.T) {
	kasus := []struct {
		a, b string
		want bool
	}{
		{"journal.iainkudus.ac.id", "journal.uinsuku.ac.id", true}, // share "journal"
		{"criticalcareshock.org", "criticalcareshock.com", true},   // share brand
		{"bjoaonline.com", "journals.lww.com", false},              // pihak ketiga
		{"a.com", "a.com", true},
	}
	for _, k := range kasus {
		if got := relatedHost(k.a, k.b); got != k.want {
			t.Errorf("relatedHost(%q, %q) = %v, want %v", k.a, k.b, got, k.want)
		}
	}
}
