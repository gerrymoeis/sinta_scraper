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

// runE7GW = poin 1 APPROVE user 8 Okt 2026 — TULIS hasil E7gm utk 4 baris
// manual yg terverifikasi 200 (E7gm: https-prefer + timeout 120s):
//
//	journals.ojs_url: j390 & j6008 **http→https** (URL db lama mati/jauh
//	lebih lambat; https = verified 200) + provenance journals_ojs_url
//	(source "e7gm", confidence 1).
//	journal_urls: j915 & j5671 (ojs_url sudah https) status NULL(timeout)
//	→ 200 + checked baru; j390 & j6008 INSERT row url=https (source sinta,
//	200) — **row http lama DIBIARKAN** (jejak 404/timeout E7g — jujur).
//	3 baris mati (j1623 404-parkir, j3995 404, j4040 dns) TAK disentuh
//	(user: "catat saja").
//
// Guard (pola E7g): backup .preE7gw.bak.db → snapshot PRE → tulis idempoten
// (ulang wajib 0) → diff K1 HANYA utk ojsWant (j390/j6008, nilai https) →
// counts delta (+2 urls_total, +2 checked, +4 200, sisanya utuh) → json.
// 0 GET, nol DDL.
type e7gwTulisJSON struct {
	Waktu      string           `json:"waktu"`
	Backup     string           `json:"backup"`
	OjsFix     int              `json:"ojs_fix"`    // http→https (j390, j6008)
	OjsUlang   int              `json:"ojs_ulang"`  // idempoten
	UrlsTulis  int              `json:"urls_tulis"` // 2 update + 2 insert
	UrlsUlang  int              `json:"urls_ulang"` // idempoten
	CountsPRE  map[string]int64 `json:"counts_pre"`
	CountsPOST map[string]int64 `json:"counts_post"`
	Delta      map[string]int64 `json:"delta"`
}

func runE7GW(dbPath, fixturesDir string) error {
	srcPath := filepath.Join(filepath.Dir(fixturesDir), "e7g-manual-retry.json")
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("baca %s: %w", srcPath, err)
	}
	var src e7gmJSON
	if err := json.Unmarshal(raw, &src); err != nil {
		return fmt.Errorf("parse %s: %w", srcPath, err)
	}

	// ---- plan dari hasil E7gm (4 ok) ---------------------------------------
	var urls []e7gRencanaURL
	ojsWant := map[int64]string{} // id → https baru (dgn perbedaan URL db)
	var idOK []int64
	for _, h := range src.Hasil {
		if h.Cls != "ok" {
			continue
		}
		idOK = append(idOK, h.ID)
		now := time.Now().UTC().Format(time.RFC3339)
		urls = append(urls, e7gRencanaURL{JID: h.ID, URL: h.URLDiuji, Source: "sinta",
			Status: intPtr(200), Final: "", Cek: now})
		if h.URLDB != h.URLDiuji {
			ojsWant[h.ID] = h.URLDiuji // http → https
		}
	}
	if len(idOK) != 4 || len(ojsWant) != 2 {
		return fmt.Errorf("plan tak sesuai: ok=%d (want 4) ojs_fix=%d (want 2) — tak menulis",
			len(idOK), len(ojsWant))
	}
	fmt.Printf("== E7GW tulis 4 baris manual E7gm (0 GET) ==\n")
	fmt.Printf("journal_urls=%d · ojs_url http→https=%d (%v)\n",
		len(urls), len(ojsWant), ojsWant)

	// ---- backup + snapshot PRE ---------------------------------------------
	bak := strings.TrimSuffix(dbPath, ".db") + ".preE7gw.bak.db"
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

	// ---- tulis idempoten ----------------------------------------------------
	proses := func() (int, int, error) {
		un, on := 0, 0
		for _, r := range urls {
			n, err := store.UpsertJournalURL(r.JID, r.URL, "ojs", r.Source,
				r.Status, r.Final, r.Cek)
			if err != nil {
				return un, on, err
			}
			un += int(n)
		}
		for id, u := range ojsWant {
			n, err := store.UpdateJournalsE7(id, "ojs_url", u)
			if err != nil {
				return un, on, fmt.Errorf("ojs_url j%d: %w", id, err)
			}
			if n > 0 {
				if err := store.MergeProvenance(id, "journals_ojs_url", storage.ProvEntry{
					Value: u, Source: "e7gm", RetrievedAt: time.Now().UTC().Format(time.RFC3339),
					Confidence: 1,
				}); err != nil {
					return un, on, fmt.Errorf("prov ojs_url j%d: %w", id, err)
				}
				on++
			}
		}
		return un, on, nil
	}
	u1, o1, err := proses()
	if err != nil {
		return fmt.Errorf("tulis: %w", err)
	}
	u2, o2, err := proses()
	if err != nil {
		return fmt.Errorf("ulang (idempoten): %w", err)
	}
	if u2 != 0 || o2 != 0 {
		return fmt.Errorf("IDEMPOTEN GAGAL: ulang mengubah urls=%d ojs=%d (harus 0/0) — cek backup %s",
			u2, o2, bak)
	}
	fmt.Printf("tulis: journal_urls=%d · ojs_url=%d · prov=%d · ulang=%d/%d\n",
		u1, o1, o1, u2, o2)

	// ---- diff K1 (HANYA ojsWant boleh berubah) + counts ---------------------
	post, err := e7gSnapshot(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot POST: %w", err)
	}
	postC, err := e7fSnapshotCanon(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot POST canonical: %w", err)
	}
	diff := e7gDiff(pre, post, preC, postC, ojsWant)
	if len(diff) > 0 {
		return fmt.Errorf("VERIFIKASI GAGAL — perubahan di luar target: %v · RESTORE manual dari %s", diff, bak)
	}
	countsPOST, perSumber, err := e7gCounts(dbPath)
	if err != nil {
		return fmt.Errorf("counts POST: %w", err)
	}
	delta := map[string]int64{}
	for k := range countsPOST {
		delta[k] = countsPOST[k] - countsPRE[k]
	}
	want := map[string]int64{"journals": 0, "hash_unik": 0, "urls_total": 2,
		"urls_canon": 0, "urls_checked": 2, "urls_200": 4, "ojs_url_ketutup": 0}
	for k, w := range want {
		if delta[k] != w {
			return fmt.Errorf("VERIFIKASI GAGAL delta %s = %d (want %d) — RESTORE dari %s",
				k, delta[k], w, bak)
		}
	}
	if countsPOST["journals"] != e7gJumlahBaris || countsPOST["ojs_url_ketutup"] != e7gJumlahBaris {
		return fmt.Errorf("VERIFIKASI GAGAL counts akhir: journals=%d ketutup=%d (want %d) — RESTORE dari %s",
			countsPOST["journals"], countsPOST["ojs_url_ketutup"], e7gJumlahBaris, bak)
	}
	if perSumber["sinta"] != e7gJumlahBaris+2 {
		return fmt.Errorf("VERIFIKASI GAGAL sumber sinta = %d (want %d) — RESTORE dari %s",
			perSumber["sinta"], e7gJumlahBaris+2, bak)
	}

	// ---- simpan ringkasan di json E7gm --------------------------------------
	src.Tulis = &e7gwTulisJSON{Waktu: time.Now().UTC().Format(time.RFC3339),
		Backup: bak, OjsFix: len(ojsWant), OjsUlang: o2, UrlsTulis: u1, UrlsUlang: u2,
		CountsPRE: countsPRE, CountsPOST: countsPOST, Delta: delta}
	out, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(srcPath, out, 0o644); err != nil {
		return fmt.Errorf("simpan json: %w", err)
	}
	fmt.Printf("VERIFIKASI OK · diff K1 hanya %v · delta: urls%+d checked%+d 200%+d · sinta=%d\n",
		ojsWant, delta["urls_total"], delta["urls_checked"], delta["urls_200"], perSumber["sinta"])
	fmt.Printf("JSON: %s (field tulis)\n", srcPath)
	return nil
}
