package main

// ==== D4 / C7-b — EKSEKUSI UPDATE (doc 43 §6.x.8 — approve user 27 Okt) ====
//
// Dua pintu tulis, keduanya DRY-RUN default → -apply (wajib backup .preC7) ·
// whitelist diverifikasi SEBELUM tulis · idempoten (WHERE kolom='unknown') ·
// SATU transaksi per tahap · verifikasi pasca-tulis (distribusi + whitelist
// + provenance JSON valid).
//
//	-phase lic  -apply  → D4a legacy: license_status + license_url +
//	                      provenance.license_src/license_page — dari
//	                      data/licfix-legacy.json (hasil dry-run).
//	-phase attrs -apply → D4b kolom baru 41 jurnal: oa_status + license_type
//	                      + license_url (CC) + provenance oa_src/license_src/
//	                      license_page — dari data/licfix-attrs.json PERSIS
//	                      artefak yg di-review user.
//
// Prinsip #1: -apply TIDAK menjalankan probe ulang — hanya menulis persis
// isi artefak yang di-approve (situs bisa berubah; keputusan tidak).

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"sinta-scraper/internal/storage"
)

// legacyEnt = entri artefak keputusan legacy per jurnal (D4a).
type legacyEnt struct {
	JournalID  int64  `json:"journal_id"`
	Name       string `json:"name"`
	Rows       int    `json:"rows"` // baris license_status='unknown' saat dry-run
	Status     string `json:"license_status"`
	Source     string `json:"license_src"`
	Page       string `json:"license_page"`
	LicenseURL string `json:"license_url"`
	Doaj       string `json:"doaj"`
}

// ==== artefak legacy (ditulis dry-run, dibaca -apply) ====

// writeLegacyArtefak = simpan hasil probe fase lic — -apply MENULIS PERSIS
// file ini (bahan review = bahan tulis, satu sumber kebenaran).
func writeLegacyArtefak(results []result, path string) {
	out := make([]legacyEnt, 0, len(results))
	for _, r := range results {
		out = append(out, legacyEnt{
			JournalID: r.jid, Name: r.name, Rows: r.unk, Status: r.status,
			Source: r.source, Page: r.url, LicenseURL: r.licURL, Doaj: r.doajChk,
		})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fatal("marshal artefak legacy: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fatal("tulis artefak legacy %s: %v", path, err)
	}
	nRes := 0
	for _, e := range out {
		if resolvedLegacy(e.Status) {
			nRes++
		}
	}
	fmt.Printf("\nartefak legacy: %s (%d entri · %d resolved) → -apply utk eksekusi D4a\n",
		path, len(out), nRes)
}

// loadLegacyArtefak = baca + validasi. Status diizinkan: 4 kelas whitelist
// legacy + "unresolved"/"PAGU" (jujur — tapi TIDAK akan ditulis: lihat
// resolvedLegacy). journal_id wajib > 0.
func loadLegacyArtefak(path string) []legacyEnt {
	b, err := os.ReadFile(path)
	if err != nil {
		fatal("baca artefak legacy %s: %v (jalankan dry-run dulu: -phase lic)", path, err)
	}
	var out []legacyEnt
	if err := json.Unmarshal(b, &out); err != nil {
		fatal("artefak legacy %s bukan JSON valid: %v", path, err)
	}
	for _, e := range out {
		if e.JournalID <= 0 {
			fatal("artefak legacy: journal_id %d tak sah", e.JournalID)
		}
		switch e.Status {
		case storage.LicenseOpen, storage.LicenseFreeNotOA, storage.LicensePaid,
			storage.LicenseUnknown, "unresolved", "PAGU":
		default:
			fatal("artefak legacy jurnal %d: status %q di luar whitelist", e.JournalID, e.Status)
		}
	}
	return out
}

// resolvedLegacy = status yg BOLEH ditulis ke kolom legacy (whitelist 4 kelas,
// tanpa "unresolved"/"PAGU" — dua itu memang harus tetap unknown, jujur).
func resolvedLegacy(s string) bool {
	return s == storage.LicenseOpen || s == storage.LicenseFreeNotOA || s == storage.LicensePaid
}

// legacyUpd = 1 baris rencana UPDATE D4a.
type legacyUpd struct {
	id     int64
	status string
	prov   string
	licURL string
}

// attrUpd = 1 baris rencana UPDATE D4b.
type attrUpd struct {
	id      int64
	needOA  bool
	oa      string
	needLIC bool
	lic     string
	licURL  string
	prov    string
}

// ==== D4a: legacy 812 baris ====

func applyLegacy(db *sql.DB, path string, apply bool, bpath string) {
	ents := loadLegacyArtefak(path)
	var plan []legacyUpd
	byStatus, nSkip := map[string]int{}, 0
	for _, e := range ents {
		if !resolvedLegacy(e.Status) {
			nSkip++
			continue
		}
		rows, err := db.Query(
			`SELECT id, provenance FROM articles WHERE journal_id = ? AND license_status = 'unknown'`,
			e.JournalID)
		if err != nil {
			fatal("baca baris jurnal %d: %v", e.JournalID, err)
		}
		for rows.Next() {
			var id int64
			var prov string
			if err := rows.Scan(&id, &prov); err != nil {
				rows.Close()
				fatal("scan baris: %v", err)
			}
			merged, err := provMergeArt(prov, map[string]string{
				"license_src": e.Source, "license_page": e.Page,
			})
			if err != nil {
				rows.Close()
				fatal("id %d: merge provenance: %v", id, err)
			}
			plan = append(plan, legacyUpd{id, e.Status, merged, e.LicenseURL})
			byStatus[e.Status]++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			fatal("iterasi jurnal %d: %v", e.JournalID, err)
		}
		rows.Close()
	}

	mode := "DRY-RUN (0 tulis)"
	if apply {
		mode = "APPLY (tulis)"
	}
	fmt.Printf("=== D4a legacy [%s] — %d entri artefak · %d jurnal resolved-dilewati=%d ===\n",
		mode, len(ents), len(ents)-nSkip, nSkip)
	for _, s := range []string{storage.LicenseOpen, storage.LicenseFreeNotOA, storage.LicensePaid} {
		if byStatus[s] > 0 {
			fmt.Printf("  license_status %-14s %4d baris\n", s, byStatus[s])
		}
	}
	fmt.Printf("rencana UPDATE: %d baris · WHERE license_status='unknown' (idempoten)\n", len(plan))
	showRows(planSample(plan))

	if !apply {
		fmt.Println("→ dry-run: jalankan lagi dgn -apply utk menulis (backup wajib, artefak dipakai apa adanya).")
		return
	}
	requireBackup(bpath)

	tx, err := db.Begin()
	if err != nil {
		fatal("begin: %v", err)
	}
	written := 0
	for _, u := range plan {
		var res sql.Result
		var err error
		if u.licURL != "" {
			res, err = tx.Exec(`UPDATE articles SET license_status = ?, license_url = ?, provenance = ?
				WHERE id = ? AND license_status = 'unknown'`,
				u.status, u.licURL, u.prov, u.id)
		} else {
			res, err = tx.Exec(`UPDATE articles SET license_status = ?, provenance = ?
				WHERE id = ? AND license_status = 'unknown'`,
				u.status, u.prov, u.id)
		}
		if err != nil {
			fatal("update id %d: %v", u.id, err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			written++
		}
	}
	if err := tx.Commit(); err != nil {
		fatal("commit: %v", err)
	}
	fmt.Printf("\nTERTULIS legacy: %d / %d rencana\n", written, len(plan))

	distrib(db, "license_status")
	verifyPostWrite(db, "license_status")
}

// ==== D4b: kolom baru 41 jurnal (artefak persis — TANPA re-probe) ====

func applyAttrs(db *sql.DB, path string, apply bool, bpath string) {
	b, err := os.ReadFile(path)
	if err != nil {
		fatal("baca artefak %s: %v (jalankan dry-run dulu: -phase attrs)", path, err)
	}
	var ents []attrRes
	if err := json.Unmarshal(b, &ents); err != nil {
		fatal("artefak %s bukan JSON valid: %v", path, err)
	}
	for _, e := range ents {
		if e.JournalID <= 0 {
			fatal("artefak: journal_id %d tak sah", e.JournalID)
		}
		if !storage.ValidOAStatus(e.OAStatus) {
			fatal("artefak jurnal %d: oa_status %q di luar whitelist", e.JournalID, e.OAStatus)
		}
		if !storage.ValidLicenseType(e.LicenseType) {
			fatal("artefak jurnal %d: license_type %q di luar whitelist", e.JournalID, e.LicenseType)
		}
	}

	var plan []attrUpd
	nOA, nLIC := 0, 0
	for _, e := range ents {
		rows, err := db.Query(`SELECT id, oa_status, license_type, provenance
			FROM articles WHERE journal_id = ? AND (oa_status = 'unknown' OR license_type = 'unknown')`,
			e.JournalID)
		if err != nil {
			fatal("baca baris jurnal %d: %v", e.JournalID, err)
		}
		for rows.Next() {
			var u attrUpd
			var oa, lic string
			if err := rows.Scan(&u.id, &oa, &lic, &u.prov); err != nil {
				rows.Close()
				fatal("scan baris: %v", err)
			}
			u.needOA = oa == storage.OAUnknown && e.OAStatus != storage.OAUnknown
			u.needLIC = lic == storage.LicenseTypeUnknown && e.LicenseType != storage.LicenseTypeUnknown
			if !u.needOA && !u.needLIC {
				continue // target utk dimensi ini = unknown (jujur) → tak disentuh
			}
			u.oa, u.lic, u.licURL = e.OAStatus, e.LicenseType, e.LicenseURL
			set := map[string]string{"license_src": e.LicenseSrc, "license_page": e.Page}
			if u.needOA {
				set["oa_src"] = e.OASrc
			}
			merged, err := provMergeArt(u.prov, set)
			if err != nil {
				rows.Close()
				fatal("id %d: merge provenance: %v", u.id, err)
			}
			u.prov = merged
			plan = append(plan, u)
			if u.needOA {
				nOA++
			}
			if u.needLIC {
				nLIC++
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			fatal("iterasi jurnal %d: %v", e.JournalID, err)
		}
		rows.Close()
	}

	mode := "DRY-RUN (0 tulis)"
	if apply {
		mode = "APPLY (tulis)"
	}
	fmt.Printf("=== D4b kolom baru [%s] — %d entri artefak %s ===\n", mode, len(ents), path)
	fmt.Printf("rencana UPDATE: oa_status +%d · license_type +%d (WHERE kolom='unknown')\n", nOA, nLIC)
	showRows(planSampleAttrs(plan))

	if !apply {
		fmt.Println("→ dry-run: jalankan lagi dgn -apply utk menulis (backup wajib, artefak dipakai apa adanya).")
		return
	}
	requireBackup(bpath)

	tx, err := db.Begin()
	if err != nil {
		fatal("begin: %v", err)
	}
	wOA, wLIC := 0, 0
	for _, u := range plan {
		if u.needOA {
			res, err := tx.Exec(`UPDATE articles SET oa_status = ?, provenance = ?
				WHERE id = ? AND oa_status = 'unknown'`, u.oa, u.prov, u.id)
			if err != nil {
				fatal("update oa id %d: %v", u.id, err)
			}
			if n, _ := res.RowsAffected(); n == 1 {
				wOA++
			}
		}
		if u.needLIC {
			var res sql.Result
			var err error
			if u.licURL != "" {
				res, err = tx.Exec(`UPDATE articles SET license_type = ?, license_url = ?, provenance = ?
					WHERE id = ? AND license_type = 'unknown'`, u.lic, u.licURL, u.prov, u.id)
			} else {
				res, err = tx.Exec(`UPDATE articles SET license_type = ?, provenance = ?
					WHERE id = ? AND license_type = 'unknown'`, u.lic, u.prov, u.id)
			}
			if err != nil {
				fatal("update lic id %d: %v", u.id, err)
			}
			if n, _ := res.RowsAffected(); n == 1 {
				wLIC++
			}
		}
	}
	if err := tx.Commit(); err != nil {
		fatal("commit: %v", err)
	}
	fmt.Printf("\nTERTULIS kolom baru: oa_status +%d · license_type +%d\n", wOA, wLIC)

	distrib(db, "oa_status")
	distrib(db, "license_type")
	verifyPostWrite(db, "oa_status")
}

// ==== bersama ====

// provMergeArt = tambah kunci provenance TANPA menimpa bukti nyata. Nilai
// lama dianggap "belum terisi" bila "" ATAU "unknown" (placeholder D1 —
// fakta pre-C7: license_src = crossref/oai utk baris berbukti, "unknown"
// utk 888 baris target D4 → "unknown" BUKAN bukti, wajib diganti jejak
// keputusan baru). JSON rusak → error (jangan menulis turunan data tak
// terbaca).
func provMergeArt(raw string, set map[string]string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return "", fmt.Errorf("provenance bukan JSON valid: %w", err)
	}
	for k, v := range set {
		if v == "" || v == storage.LicenseUnknown {
			continue // kosong/placeholder → tak layak jadi jejak
		}
		if cur, ok := m[k].(string); ok && cur != "" && cur != storage.LicenseUnknown {
			continue // bukti nyata (crossref/oai/policy dst) → menang
		}
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// requireBackup = guard -apply: file backup pra-C7 harus sudah ada.
func requireBackup(bpath string) {
	if _, err := os.Stat(bpath); err != nil {
		fatal("-apply butuh backup %q (buat dulu: sqlite3 <db> \".backup '%s'\")", bpath, bpath)
	}
}

// distrib = ringkasan sebaran kolom (verifikasi pasca-tulis).
func distrib(db *sql.DB, col string) {
	rows, err := db.Query(`SELECT ` + col + `, COUNT(*) FROM articles GROUP BY ` + col + ` ORDER BY COUNT(*) DESC`)
	if err != nil {
		fatal("distrib %s: %v", col, err)
	}
	defer rows.Close()
	fmt.Printf("sebaran %s:\n", col)
	for rows.Next() {
		var v string
		var n int
		if err := rows.Scan(&v, &n); err != nil {
			fatal("scan: %v", err)
		}
		fmt.Printf("  %-20s %6d\n", v, n)
	}
}

// verifyPostWrite = 3 cek wajib: whitelist 0 pelanggaran · provenance semua
// JSON valid · is_complete utuh 8.872. Gagal → exit 1.
func verifyPostWrite(db *sql.DB, kind string) {
	var viol int
	switch kind {
	case "license_status":
		err := db.QueryRow(`SELECT COUNT(*) FROM articles
			WHERE license_status NOT IN ('open','free_not_open','paid','unknown')`).Scan(&viol)
		if err != nil {
			fatal("cek whitelist legacy: %v", err)
		}
	case "oa_status":
		err := db.QueryRow(`SELECT COUNT(*) FROM articles
			WHERE oa_status NOT IN ('free','paid','restricted','unknown')
			   OR (license_type NOT IN ('unknown','all_rights_reserved')
			       AND license_type NOT LIKE 'CC BY%' AND license_type <> 'CC0')`).Scan(&viol)
		if err != nil {
			fatal("cek whitelist atribut: %v", err)
		}
	}
	var badJSON, complete int
	if err := db.QueryRow(`SELECT COUNT(*) FROM articles WHERE json_valid(provenance) = 0`).Scan(&badJSON); err != nil {
		fatal("cek provenance: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM articles WHERE is_complete = 1`).Scan(&complete); err != nil {
		fatal("cek complete: %v", err)
	}
	fmt.Printf("verifikasi: whitelist violation=%d (mau 0) · provenance rusak=%d (mau 0) · complete=%d (mau 8872)\n",
		viol, badJSON, complete)
	if viol != 0 || badJSON != 0 || complete != 8872 {
		os.Exit(1)
	}
}

// ==== cetak contoh rencana (bahan review) ====

type rowSample struct{ id, note string }

func planSample(plan []legacyUpd) []rowSample {
	out := []rowSample{}
	for i, u := range plan {
		if i >= 6 {
			break
		}
		n := "unknown → " + u.status
		if u.licURL != "" {
			n += " · url=" + u.licURL
		}
		out = append(out, rowSample{fmt.Sprintf("%d", u.id), n})
	}
	return out
}

func planSampleAttrs(plan []attrUpd) []rowSample {
	out := []rowSample{}
	for i, u := range plan {
		if i >= 6 {
			break
		}
		n := ""
		if u.needOA {
			n += "oa→" + u.oa + " "
		}
		if u.needLIC {
			n += "lic→" + u.lic
			if u.licURL != "" {
				n += " · url=" + u.licURL
			}
		}
		out = append(out, rowSample{fmt.Sprintf("%d", u.id), strings.TrimSpace(n)})
	}
	return out
}

func showRows(samples []rowSample) {
	if len(samples) == 0 {
		fmt.Println("contoh baris: (tidak ada rencana — sudah semua)")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tRENCANA")
	for _, s := range samples {
		fmt.Fprintf(w, "%s\t%s\n", s.id, s.note)
	}
	_ = w.Flush()
}
