package garuda

// Q4 E7f (doc 30 §14.6 butir 6 — approve user 6 Okt 2026): fill subject_area,
// print_issn, doaj_url utk 41 baris dobel-kosong (subjek & canonical kosong).
// Pagu 41 GET (hyphen-first, DOAJ search) + 1 spot-check TOC; nol DDL (ketiga
// kolom sudah ada). Logika murni di sini (parse = ParseDOAJSearch reuse E6);
// driver + tulis db di cmd/garuda/e7f.go. Opsi ditolak utk dedup subject =
// contain/prefix (ambigu) & fuzzy (asumsi) — FoldSubject saja (doc 38 §13).

// E7fBaris = identitas satu target query (baca db, read-only — driver).
type E7fBaris struct {
	ID   int64
	Nama string
	Key  string // ISSN kanonik tanpa strip (E-ISSN utama, fallback P-ISSN)
}

// E7fHasil = hasil query 1 baris (fixture | GET | resume — sama seperti E7d).
type E7fHasil struct {
	ID     int64  `json:"journal_id"`
	Nama   string `json:"nama"`
	Key    string `json:"issn_kanonik"`
	Query  string `json:"query"` // bentuk hyphen utk URL
	Doaj   E6DOAJ `json:"doaj"`
	Sumber string `json:"sumber"` // fixture | GET | resume
}

// E7fRingkas = ringkasan run E7f (jujur utk laporan 3 bagian).
type E7fRingkas struct {
	Total        int `json:"total"`
	Found        int `json:"found"`
	Nol          int `json:"nol"`                 // tak terdaftar (total=0 / ISSN mismatch)
	DenganSubjek int `json:"found_dengan_subjek"` // found dgn bibjson.subject ≥1
	KendalaHTTP  int `json:"kendala_http"`        // http di luar 200/404 (unreachable/dll)
}

// HitungE7f = ringkasan deterministik dari hasil query.
func HitungE7f(items []E7fHasil) E7fRingkas {
	var r E7fRingkas
	r.Total = len(items)
	for _, h := range items {
		switch {
		case h.Doaj.Tersedia:
			r.Found++
			if len(h.Doaj.Subjek) > 0 {
				r.DenganSubjek++
			}
		case h.Doaj.HTTP == 404 || h.Doaj.HTTP == 200:
			r.Nol++
		default:
			// http 0 (unreachable) / http error lain = kendala, bukan "nol" yakin.
			r.KendalaHTTP++
		}
	}
	return r
}

// DoajTocURL = URL halaman TOC DOAJ utk ISSN kanonik
// ("23562641" → "https://doaj.org/toc/2356-2641"). "" bila key tak valid.
func DoajTocURL(kanonik string) string {
	h := HyphenISSN(kanonik)
	if h == "" {
		return ""
	}
	return "https://doaj.org/toc/" + h
}
