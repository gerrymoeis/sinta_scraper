package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sinta-scraper/internal/storage"
)

// runE7GHW = poin 2 arahan user 8 Okt 2026 ("tulis 23 baris terverifikasi
// saya approve") — TULIS hasil eksperimen E7gh ke journal_urls:
//
//	23 baris = klasifikasi ok dlm e7g-waf-eksperimen.json (6 waf lolos +
//	17 cek-robots lolos) → status NULL/403 → 200 + checked_at baru.
//	URL = ojs_url asli baris (verified di tempat) → journals TIDAK berubah
//	→ diff K1 wajib 0 utuh. Sumber tetap "sinta" (update baris sinta —
//	 Upsert memperbarui hasil verifikasi, tak menimpa source).
//
// Guard: backup .preE7gh.bak.db → snapshot PRE → tulis idempoten (ulang
// wajib 0) → diff K1 = 0 → counts delta (+17 checked, +200=23, sisanya utuh)
// → tulis ringkasan ke json eksperimen (field "tulis"). Nol DDL, nol GET.
const e7ghwJumlahBaris = 23

type e7ghwTulisJSON struct {
	Waktu      string           `json:"waktu"`
	Backup     string           `json:"backup"`
	Baris      int              `json:"baris"`
	Tulis      int              `json:"tulis"`
	Ulang      int              `json:"ulang_idempoten"`
	CountsPRE  map[string]int64 `json:"counts_pre"`
	CountsPOST map[string]int64 `json:"counts_post"`
	Delta      map[string]int64 `json:"delta"`
}

func runE7GHW(dbPath, fixturesDir string) error {
	expPath := filepath.Join(filepath.Dir(fixturesDir), "e7g-waf-eksperimen.json")
	raw, err := os.ReadFile(expPath)
	if err != nil {
		return fmt.Errorf("baca %s: %w", expPath, err)
	}
	var exp e7ghJSON
	if err := json.Unmarshal(raw, &exp); err != nil {
		return fmt.Errorf("parse %s: %w", expPath, err)
	}

	// ---- plan 23 baris (ok dlm eksperimen) ---------------------------------
	var plan []e7gRencanaURL
	for _, k := range exp.Cek {
		if k.Cls != "ok" || (k.Sebab != "waf" && k.Sebab != "robots") {
			continue
		}
		cek := time.Now().UTC().Format(time.RFC3339)
		plan = append(plan, e7gRencanaURL{JID: k.ID, URL: k.URL, Source: "sinta",
			Status: intPtr(200), Final: "", Cek: cek})
	}
	if len(plan) != e7ghwJumlahBaris {
		return fmt.Errorf("plan = %d baris (want %d) — tak menulis", len(plan), e7ghwJumlahBaris)
	}
	fmt.Printf("== E7GHW tulis %d baris terverifikasi E7gh (0 GET) ==\n", len(plan))

	// ---- backup SEBELUM tulis ----------------------------------------------
	bak := strings.TrimSuffix(dbPath, ".db") + ".preE7gh.bak.db"
	if _, err := os.Stat(bak); err != nil {
		if err := salinFile(dbPath, bak); err != nil {
			return fmt.Errorf("backup db: %w", err)
		}
		fmt.Printf("backup db → %s\n", bak)
	} else {
		fmt.Printf("backup sudah ada (dipakai apa adanya): %s\n", bak)
	}
	pre, err := e7gSnapshot(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot PRE: %w", err)
	}
	preC, err := e7fSnapshotCanon(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot PRE canonical: %w", err)
	}
	countsPRE, _, err := e7gCounts(dbPath)
	if err != nil {
		return fmt.Errorf("counts PRE: %w", err)
	}

	store, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("buka db: %w", err)
	}
	defer store.Close()

	// ---- tulis idempoten (loop ke-2 wajib 0) -------------------------------
	proses := func() (int, error) {
		total := 0
		for _, r := range plan {
			n, err := store.UpsertJournalURL(r.JID, r.URL, "ojs", r.Source,
				r.Status, r.Final, r.Cek)
			if err != nil {
				return total, err
			}
			total += int(n)
		}
		return total, nil
	}
	t1, err := proses()
	if err != nil {
		return fmt.Errorf("tulis: %w", err)
	}
	t2, err := proses()
	if err != nil {
		return fmt.Errorf("ulang (idempoten): %w", err)
	}
	if t2 != 0 {
		return fmt.Errorf("IDEMPOTEN GAGAL: ulang mengubah %d baris (harus 0) — cek backup %s", t2, bak)
	}
	fmt.Printf("tulis: %d baris · ulang idempoten %d\n", t1, t2)

	// ---- snapshot POST + diff K1 (journals TAK boleh berubah) --------------
	post, err := e7gSnapshot(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot POST: %w", err)
	}
	postC, err := e7fSnapshotCanon(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot POST canonical: %w", err)
	}
	diff := e7gDiff(pre, post, preC, postC, map[int64]string{}) // ojs_url: nol target
	if len(diff) > 0 {
		return fmt.Errorf("VERIFIKASI GAGAL — perubahan journals di luar target: %v · RESTORE manual dari %s", diff, bak)
	}
	countsPOST, perSumber, err := e7gCounts(dbPath)
	if err != nil {
		return fmt.Errorf("counts POST: %w", err)
	}

	// ---- delta guard (expected from fakta plan) ----------------------------
	delta := map[string]int64{}
	for k := range countsPOST {
		delta[k] = countsPOST[k] - countsPRE[k]
	}
	want := map[string]int64{"journals": 0, "hash_unik": 0, "urls_total": 0,
		"urls_canon": 0, "urls_checked": 17, "urls_200": 23, "ojs_url_ketutup": 0}
	for k, w := range want {
		if delta[k] != w {
			return fmt.Errorf("VERIFIKASI GAGAL delta %s = %d (want %d) — RESTORE manual dari %s",
				k, delta[k], w, bak)
		}
	}
	if countsPOST["journals"] != e7gJumlahBaris || countsPOST["ojs_url_ketutup"] != e7gJumlahBaris {
		return fmt.Errorf("VERIFIKASI GAGAL counts akhir: journals=%d ketutup=%d (want %d) — RESTORE dari %s",
			countsPOST["journals"], countsPOST["ojs_url_ketutup"], e7gJumlahBaris, bak)
	}
	if perSumber["sinta"] != e7gJumlahBaris {
		return fmt.Errorf("VERIFIKASI GAGAL sumber sinta = %d (want %d) — RESTORE dari %s",
			perSumber["sinta"], e7gJumlahBaris, bak)
	}

	// ---- ringkasan ke json eksperimen --------------------------------------
	exp.Tulis = &e7ghwTulisJSON{Waktu: time.Now().UTC().Format(time.RFC3339),
		Backup: bak, Baris: len(plan), Tulis: t1, Ulang: t2,
		CountsPRE: countsPRE, CountsPOST: countsPOST, Delta: delta}
	out, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(expPath, out, 0o644); err != nil {
		return fmt.Errorf("simpan json: %w", err)
	}

	fmt.Printf("VERIFIKASI OK · diff K1=0 · delta: checked%+d 200%+d · sumber sinta=%d\n",
		delta["urls_checked"], delta["urls_200"], perSumber["sinta"])
	fmt.Printf("JSON: %s (field tulis)\n", expPath)
	return nil
}

func intPtr(v int) *int { return &v }
