package main

// ==== apply.go — tulis hasil artefak ke DB (pola licfix D4: TANPA re-probe) ====
//
// Idempoten berlapis (validitas #1 — tak pernah menimpa data lain):
//
//	UPDATE … WHERE id = ? AND oa_status = 'unknown'  ← baris lompat bila
//	sudah berubah (rerun/intervensi lain) — dilaporkan sbg SKIP jujur.
//
// Provenance di-merge (map kunci bebas artikel — pola provMergeArt licfix):
//	"oa_src": "access_probe"  ← asal bukti (keputusan probe akses 29 Okt)

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
)

// runApply = baca artefak JSON → UPDATE idempoten per baris non-unknown.
func runApply(db *sql.DB, outPath string) error {
	raw, err := os.ReadFile(outPath)
	if err != nil {
		return fmt.Errorf("baca artefak: %w", err)
	}
	var results []res
	if err := json.Unmarshal(raw, &results); err != nil {
		return fmt.Errorf("artefak bukan JSON valid: %w", err)
	}

	applied := map[string]int{}
	skipped, kept := 0, 0
	for _, r := range results {
		if r.Decided == "" || r.Decided == "unknown" {
			kept++ // unknown = tak disentuh (status memang tetap unknown)
			continue
		}
		var prov string
		if err := db.QueryRow(`SELECT COALESCE(provenance,'') FROM articles WHERE id = ?`, r.ArticleID).
			Scan(&prov); err != nil {
			return fmt.Errorf("artikel %d: baca provenance: %w", r.ArticleID, err)
		}
		merged, err := provMerge(prov)
		if err != nil {
			return fmt.Errorf("artikel %d: provenance merge: %w", r.ArticleID, err)
		}
		upd, err := db.Exec(`
			UPDATE articles SET oa_status = ?, provenance = ?
			WHERE id = ? AND oa_status = 'unknown'`,
			r.Decided, merged, r.ArticleID)
		if err != nil {
			return fmt.Errorf("artikel %d UPDATE: %w", r.ArticleID, err)
		}
		if n, _ := upd.RowsAffected(); n == 1 {
			applied[r.Decided]++
		} else {
			skipped++ // sudah berubah sejak dry-run (idempoten — jujur)
		}
	}

	log.Printf("[oaprobe] APPLY: free+%d paid+%d restricted+%d · SKIP (sudah berubah)=%d · tetap-unknown=%d · total baris artefak=%d",
		applied["free"], applied["paid"], applied["restricted"], skipped, kept, len(results))
	log.Print("[oaprobe] rerun dry-run utk bukti idempoten (target harus menyusut drastis)")
	return nil
}

// provMerge = tambah "oa_src":"access_probe" ke provenance artikel (format
// JSON lama dipertahankan — pola D4 provMergeArt licfix).
func provMerge(raw string) (string, error) {
	m := map[string]any{}
	if strings.TrimSpace(raw) != "" && strings.TrimSpace(raw) != "{}" {
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return "", err
		}
	}
	m["oa_src"] = "access_probe"
	b, err := json.Marshal(m)
	return string(b), err
}
