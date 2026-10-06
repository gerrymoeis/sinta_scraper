package garuda

import "testing"

// TestRankSumber = hierarki §6.2 (OFFICIAL > GARUDA > SINTA > DOAJ/Crossref)
// + usulan manual & canonical (E7a).
func TestRankSumber(t *testing.T) {
	if !(RankSumber("manual") > RankSumber("official") &&
		RankSumber("official") > RankSumber("garuda") &&
		RankSumber("garuda") > RankSumber("sinta") &&
		RankSumber("sinta") > RankSumber("doaj") &&
		RankSumber("doaj") == RankSumber("crossref")) {
		t.Fatalf("hierarki tak sesuai: manual=%d official=%d garuda=%d sinta=%d doaj=%d crossref=%d",
			RankSumber("manual"), RankSumber("official"), RankSumber("garuda"),
			RankSumber("sinta"), RankSumber("doaj"), RankSumber("crossref"))
	}
	if RankSumber("canonical") != RankSumber("garuda") {
		t.Fatalf("canonical harus setara garuda, got %d", RankSumber("canonical"))
	}
	if RankSumber("ngawur") != 0 {
		t.Fatalf("sumber tak dikenal harus rank 0")
	}
}

// TestEvalMerge4Lapis = satu kasus per aksi (FILL/NOOP/OVERWRITE/TOLAK_L1/
// TOLAK_L2/TOLAK_L3/CONFLICT/REVIEW/EMPTY) — dokumentasi hidup aturan §6.2.
func TestEvalMerge4Lapis(t *testing.T) {
	const (
		tahap1 = "2026-09-29T23:26:10Z" // last_scraped_at journals (Tahap 1)
		q3     = "2026-10-03T16:22:01Z" // garuda_retrieved_at (Q3)
		e6     = "2026-10-04T00:00:00Z" // run E6 (Crossref/DOAJ)
	)
	k := func(sumber, value, ret string) E7Kandidat {
		return E7Kandidat{Sumber: sumber, Value: value, Retrieved: ret, Capture: "x"}
	}

	cases := []struct {
		nama      string
		field     string
		nowSumber string
		now       string
		nowRet    string
		kands     []E7Kandidat
		identitas bool
		aksi      string
	}{
		{
			nama:  "FILL: field kosong diisi canonical (rank tertinggi)",
			field: "subject_area", nowSumber: "", now: "", nowRet: "",
			kands: []E7Kandidat{
				k("doaj", "Nursing", e6),
				k("canonical", "Engineering | Architecture", q3),
			},
			aksi: "FILL",
		},
		{
			nama:  "EMPTY: kosong & nol kandidat",
			field: "subject_area", nowSumber: "", now: "", nowRet: "",
			kands: nil,
			aksi:  "EMPTY",
		},
		{
			nama:  "NOOP: kandidat identik dgn nilai sekarang",
			field: "garuda_url", nowSumber: "sinta", now: "https://x.id/v/1", nowRet: tahap1,
			kands: []E7Kandidat{k("garuda", "https://x.id/v/1", q3)},
			aksi:  "NOOP",
		},
		{
			nama:  "TOLAK_L1: sinta (rank 2) menimpa garuda (rank 3) dilarang",
			field: "name", nowSumber: "garuda", now: "Jurnal A", nowRet: q3,
			kands: []E7Kandidat{k("sinta", "Jurnal A Baru", tahap1)},
			aksi:  "TOLAK_L1",
		},
		{
			nama:  "TOLAK_L2: URL bukan http(s) / placeholder",
			field: "garuda_url", nowSumber: "sinta", now: "https://x.id/v/1", nowRet: tahap1,
			kands: []E7Kandidat{k("garuda", "ftp://x.id/v/2", q3)},
			aksi:  "TOLAK_L2",
		},
		{
			nama:  "TOLAK_L3: kandidat rank setara tapi lebih lama (L1 lolos, L3 kalah)",
			field: "subject_area", nowSumber: "canonical", now: "Engineering", nowRet: q3,
			kands: []E7Kandidat{k("garuda", "Science", "2026-10-01T00:00:00Z")},
			aksi:  "TOLAK_L3",
		},
		{
			nama:  "CONFLICT: sama-sama segar beda nilai",
			field: "subject_area", nowSumber: "garuda", now: "Engineering", nowRet: q3,
			kands: []E7Kandidat{k("canonical", "Medical", q3)},
			aksi:  "CONFLICT",
		},
		{
			nama:  "CONFLICT: retrieved sekarang tak diketahui → jangan timpa",
			field: "garuda_url", nowSumber: "sinta", now: "https://x.id/v/1", nowRet: "",
			kands: []E7Kandidat{k("garuda", "https://x.id/v/2", q3)},
			aksi:  "CONFLICT",
		},
		{
			nama:  "OVERWRITE: garuda_url lolos 4 lapis (kasus 684/931)",
			field: "garuda_url", nowSumber: "sinta",
			now:    "https://garuda.kemdiktisaintek.go.id/journal/view/4979",
			nowRet: tahap1,
			kands: []E7Kandidat{{
				Sumber: "garuda", Value: "https://garuda.kemdiktisaintek.go.id/journal/view/42863",
				Retrieved: q3, Capture: "q3-search",
				Evidence: "E3 view 4 Okt: 4979 usang, 42863 lengkap",
			}},
			aksi: "OVERWRITE",
		},
		{
			nama:  "REVIEW: identitas ditimpa non-official → kebijakan user",
			field: "name", nowSumber: "sinta", now: "Jurnal A", nowRet: tahap1,
			kands:     []E7Kandidat{k("garuda", "Jurnal A (Garuda)", q3)},
			identitas: true,
			aksi:      "REVIEW",
		},
	}

	for _, c := range cases {
		got := EvalMerge4Lapis(c.field, c.nowSumber, c.now, c.nowRet, c.kands, c.identitas)
		if got.Aksi != c.aksi {
			t.Errorf("%s: aksi = %s, want %s (alasan: %s)",
				c.nama, got.Aksi, c.aksi, got.Alasan)
		}
		if got.Aksi == "FILL" && got.Terpilih == "" {
			t.Errorf("%s: FILL tanpa Terpilih", c.nama)
		}
	}
}

// TestEvalIdempotenNOOP = kandidat = nilai sekarang (diulang berkali) selalu
// NOOP — syarat §6.2 "merge idempoten bila input tak berubah".
func TestEvalIdempotenNOOP(t *testing.T) {
	now := "https://x.id/v/9"
	for i := 0; i < 3; i++ {
		got := EvalMerge4Lapis("garuda_url", "sinta", now, "2026-10-03T16:22:01Z",
			[]E7Kandidat{{Sumber: "garuda", Value: now, Retrieved: "2026-10-03T16:22:01Z"}}, false)
		if got.Aksi != "NOOP" {
			t.Fatalf("iter %d: aksi = %s, want NOOP", i, got.Aksi)
		}
	}
}
