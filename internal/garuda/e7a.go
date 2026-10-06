package garuda

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ==== E7a — simulasi aturan merge 4 lapis K5 (doc 30 §6.2) ====
//
// File ini PURE LOGIC tanpa IO (db/HTTP) supaya gampang diuji — driver
// read-only (nol GET, nol tulis) ada di cmd/garuda/e7a.go. Tugasnya
// MENGAMBARKAN kan apa yang akan dilakukan aturan 4 lapis pada 10 jurnal
// sampel → user review aturan (E7a) sebelum coding merge produksi (E7b).

// RankSumber = hierarki sumber utk Journal metadata (doc 30 §6.2 baris 272):
// OFFICIAL (OJS/web) > GARUDA > SINTA > DOAJ/Crossref/other.
// Tambahan di luar baris itu (usulan utk review E7a):
//   - "manual" (cek manual user, doc 37) diusul DI ATAS official;
//   - "canonical" = turunan union SINTA+Garuda (Q3 harmonisasi) — rank
//     setara GARUDA karena nilai hasil akhirnya datang dari Garuda.
//
// Sumber di luar map = rank 0 (tak sah utk menimpa).
var rankSumber = map[string]int{
	"manual":    5,
	"official":  4,
	"garuda":    3,
	"canonical": 3,
	"sinta":     2,
	"doaj":      1,
	"crossref":  1,
}

// RankSumber mengembalikan rank hierarki sebuah sumber (0 = tak dikenal).
func RankSumber(sumber string) int { return rankSumber[sumber] }

// placeholderL2 = nilai tak-bernilai menurut lapis-2 §6.2 (validitas format).
var placeholderL2 = map[string]bool{
	"0": true, "-": true, "": true, "unknown": true, "n/a": true,
	"null": true, "tidak tersedia": true,
}

// E7Kandidat = satu nilai kandidat utk sebuah field target.
type E7Kandidat struct {
	Sumber     string  `json:"sumber"`       // kunci rankSumber
	Value      string  `json:"value"`        // nilai kandidat
	Retrieved  string  `json:"retrieved_at"` // RFC3339; "" = tak diketahui → L3 gagal
	Confidence float64 `json:"confidence"`   // 0..1 bila ada
	Capture    string  `json:"capture"`      // asal run/snapshot (L4 — dicatat utk audit)
	Evidence   string  `json:"evidence"`     // bukti "lebih baru" utk lapis-3 (opsional)
}

// E7Eval = hasil simulasi 4 lapis untuk SATU field pada SATU jurnal.
//
// Aksi:
//   - FILL      : field kosong → diisi kandidat valid rank tertinggi
//   - NOOP      : semua kandidat sama dengan nilai sekarang / tak ada yang lolos
//   - OVERWRITE : kandidat baru lolos SEMUA lapis → boleh menimpa
//   - TOLAK_L1  : kandidat kalah hierarki (rank < rank sekarang)
//   - TOLAK_L2  : kandidat gagal validasi format / placeholder
//   - TOLAK_L3  : kandidat lebih lama (retrieved_at < sekarang)
//   - CONFLICT  : sama-sama segar (atau kesegaran tak terbukti) tapi beda nilai
//   - REVIEW    : kandidat teknis menang TAPI butuh kebijakan user
//   - EMPTY     : field kosong & tak ada kandidat sama sekali
type E7Eval struct {
	Field     string       `json:"field"`
	Sekarang  string       `json:"sekarang"` // nilai live saat ini ("" = kosong)
	RankNow   int          `json:"rank_now"` // rank sumber nilai sekarang
	Identitas bool         `json:"identitas"`
	Kandidat  []E7Kandidat `json:"kandidat"`
	Aksi      string       `json:"aksi"`
	Alasan    string       `json:"alasan"`
	Terpilih  string       `json:"terpilih,omitempty"`
}

// EvalMerge4Lapis = simulasi K5 §6.2 utk satu field.
//
// nowSumber = sumber nilai sekarang (mis. "sinta"), now = nilai live,
// nowRet = retrieved_at nilai sekarang ("" bila tak diketahui → lapis-3
// dianggap TAK TERBUKTI → kandidat tak boleh menang diam-diam, dipaksa
// CONFLICT utk review). identitas = true utk field identitas (nama jurnal)
// yang tak boleh ditimpa mesin tanpa kebijakan user.
func EvalMerge4Lapis(field, nowSumber, now, nowRet string, kands []E7Kandidat, identitas bool) E7Eval {
	nowRank := RankSumber(nowSumber)
	eval := E7Eval{
		Field: field, Sekarang: now, RankNow: nowRank,
		Identitas: identitas, Kandidat: kands,
		Aksi:   "NOOP",
		Alasan: "tak ada kandidat baru (semua sama dengan nilai sekarang)",
	}

	// Urut: rank tertinggi dulu, retrieved terbaru dulu (stabil → deterministik).
	urut := make([]E7Kandidat, len(kands))
	copy(urut, kands)
	sort.SliceStable(urut, func(i, j int) bool {
		ri, rj := RankSumber(urut[i].Sumber), RankSumber(urut[j].Sumber)
		if ri != rj {
			return ri > rj
		}
		return urut[i].Retrieved > urut[j].Retrieved
	})

	// ---- Field kosong = belum ada klaim → fill-if-absent (boleh dari
	// sumber terendah §6.2; pilih rank tertinggi utk kualitas). ------------
	if strings.TrimSpace(now) == "" {
		for _, k := range urut {
			if ok, alasan := lolosL2(field, k.Value); !ok {
				eval.Aksi, eval.Alasan = "TOLAK_L2", fmt.Sprintf("%s: %s", k.Sumber, alasan)
				continue
			}
			eval.Aksi, eval.Terpilih = "FILL", k.Value
			eval.Alasan = fmt.Sprintf(
				"field kosong → fill-if-absent dari %s (rank %d); L2 valid; L4 capture=%s",
				k.Sumber, RankSumber(k.Sumber), k.Capture)
			return eval
		}
		if eval.Aksi == "TOLAK_L2" {
			return eval
		}
		eval.Aksi, eval.Alasan = "EMPTY",
			"field kosong & tak ada kandidat (sumber tak punya nilai / gagal L2)"
		return eval
	}

	// ---- Field terisi → siklus 4 lapis per kandidat (rank desc). ---------
	// Normalisasi: retrieved sekarang harus berupa RFC3339-Z valid —
	// seluruh timestamp db/provenance memang RFC3339 UTC ("...Z"), jadi
	// perbandingan leksikografis setelah parse = kronologis.
	if nowRet != "" {
		if _, ok := parseRFC3339(nowRet); !ok {
			nowRet = "" // format tak dikenal → dianggap tak terbukti
		}
	}
	for _, k := range urut {
		if strings.TrimSpace(k.Value) == strings.TrimSpace(now) {
			continue // kandidat identik → noop utk kandidat ini
		}
		if ok, alasan := lolosL2(field, k.Value); !ok {
			if eval.Aksi == "NOOP" {
				eval.Aksi, eval.Alasan = "TOLAK_L2", fmt.Sprintf("%s: %s", k.Sumber, alasan)
			}
			continue
		}
		if RankSumber(k.Sumber) < nowRank {
			if eval.Aksi == "NOOP" {
				eval.Aksi = "TOLAK_L1"
				eval.Alasan = fmt.Sprintf(
					"%s (rank %d) < %s (rank %d) → hanya boleh fill-if-absent, bukan menimpa",
					k.Sumber, RankSumber(k.Sumber), nowSumber, nowRank)
			}
			continue
		}
		// Lapis-3 kesegaran (kedua arah wajib terbukti).
		switch {
		case nowRet == "":
			// Retrieved nilai sekarang tak diketahui → jangan timpa diam-diam.
			if eval.Aksi == "NOOP" || eval.Aksi == "TOLAK_L1" || eval.Aksi == "TOLAK_L2" {
				eval.Aksi = "CONFLICT"
				eval.Alasan = fmt.Sprintf(
					"%s: retrieved_at nilai sekarang tak diketahui → kesegaran sekarang TAK TERBUKTI; jangan timpa, CONFLICT utk review (§6.2 lapis-3)",
					k.Sumber)
			}
			continue
		case k.Retrieved == "" || !retValid(k.Retrieved):
			if eval.Aksi == "NOOP" || eval.Aksi == "TOLAK_L1" || eval.Aksi == "TOLAK_L2" {
				eval.Aksi = "CONFLICT"
				eval.Alasan = fmt.Sprintf(
					"%s: retrieved_at kandidat tak diketahui (L3 gagal) & beda nilai → CONFLICT utk review",
					k.Sumber)
			}
			continue
		case k.Retrieved < nowRet:
			if eval.Aksi == "NOOP" || eval.Aksi == "TOLAK_L1" || eval.Aksi == "TOLAK_L2" {
				eval.Aksi = "TOLAK_L3"
				eval.Alasan = fmt.Sprintf(
					"%s: retrieved %s < sekarang %s → lebih lama, tak boleh menimpa",
					k.Sumber, k.Retrieved, nowRet)
			}
			continue
		case k.Retrieved == nowRet:
			// §6.2 lapis-3: sama-sama segar tapi beda nilai → jangan timpa
			// diam-diam; kandidat disimpan sbg CONFLICT utk review.
			eval.Aksi = "CONFLICT"
			eval.Terpilih = ""
			eval.Alasan = fmt.Sprintf(
				"%s: sama-sama segar (%s) tapi beda nilai → CONFLICT; kandidat disimpan di provenance utk review (§6.2 lapis-3)",
				k.Sumber, k.Retrieved)
			continue
		}
		// ---- Lulus SEMUA lapis (L4 capture dicatat utk audit). -----------
		if identitas && k.Sumber != "official" && k.Sumber != "manual" {
			eval.Aksi = "REVIEW"
			eval.Terpilih = k.Value
			eval.Alasan = fmt.Sprintf(
				"field IDENTITAS: %s lolos 4 lapis (L1 rank %d≥%d · L2 valid · L3 %s>%s · L4 capture %s→%s dicatat) TAPI identitas jurnal tak boleh ditimpa mesin → REVIEW kebijakan user",
				k.Sumber, RankSumber(k.Sumber), nowRank, k.Retrieved, nowRet, k.Capture, captureNow)
			continue
		}
		eval.Aksi = "OVERWRITE"
		eval.Terpilih = k.Value
		bukti := ""
		if k.Evidence != "" {
			bukti = " · bukti: " + k.Evidence
		}
		eval.Alasan = fmt.Sprintf(
			"lolos 4 lapis: L1 %s(rank %d)≥%s(rank %d) · L2 valid · L3 retrieved %s>%s%s · L4 capture %s→%s (dicatat)",
			k.Sumber, RankSumber(k.Sumber), nowSumber, nowRank,
			k.Retrieved, nowRet, bukti, k.Capture, k.Capture)
	}
	return eval
}

// captureNow = label lapis-4 utk nilai "sekarang" (seluruh kolom journals
// berasal dari scrape Tahap 1, 29 Sep 2026).
const captureNow = "tahap1-sinta"

// lolosL2 = lapis-2 §6.2: validasi format tipe + bukan placeholder.
func lolosL2(field, value string) (bool, string) {
	v := strings.TrimSpace(value)
	if placeholderL2[strings.ToLower(v)] || placeholderL2[v] {
		return false, fmt.Sprintf("L2 gagal: placeholder %q", value)
	}
	if field == "garuda_url" {
		u, err := url.Parse(v)
		if err != nil || u.Host == "" {
			return false, fmt.Sprintf("L2 gagal: URL tak parse-able %q", value)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return false, fmt.Sprintf("L2 gagal: scheme %q bukan http(s)", u.Scheme)
		}
	}
	return true, ""
}

func parseRFC3339(s string) (time.Time, bool) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// retValid = timestamp RFC3339 valid (syarat lapis-3).
func retValid(s string) bool {
	_, ok := parseRFC3339(s)
	return ok
}
