package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"
	"sinta-scraper/internal/storage"
)

// e8b.go — E8b Opsi B SATU RUN gabungan (approve user 8 Okt 2026:
// "langsung eksekusi satu run saja, dan anda bisa tambah pagunya bila
// diperlukan" — doc 39):
//
//	Fase C resolve: 3 not_found yg punya view URL (j60/j679/j696) →
//	  GET view → ParseViewPage → cocokkan ISSN db (E-ISSN/P-ISSN exact,
//	  tanpa dash) → UpsertGarudaMatch matched (confidence 1.0) + fill
//	  subject_area/print_issn bila kosong (garasi view). Tanpa bukti
//	  ISSN cocok = TIDAK menulis (jujur tetap not_found).
//	Fase A subject: 17 matched subj-kosong → GET view → Areas →
//	  fill journals.subject_area + prov (garuda, 1.0). View tanpa area
//	  / tak ketemu = jujur tetap kosong (tanpa tulis).
//	Fase B tutup: 0 GET — daftar jujur 15 print kosong (online-only,
//	  garuda_pissn "-") + 7 not_found tanpa view → dicatat di json.
//
// Pagu ≤30 GET (20 target + margin retry). Tulis: backup
// .preE8b.bak.db + snapshot/diff K1 (e8bDiff: hanya subject_area/
// print_issn utk id target yg boleh berubah, canonical identik) +
// counts delta + json data/stage2/e8b-results.json.
const paguE8B = 30

type e8bTarget struct {
	ID    int64  `json:"id"`
	Nama  string `json:"nama"`
	URL   string `json:"url"`
	PISSN string `json:"print_issn_db,omitempty"`
	EISSN string `json:"eissn_db,omitempty"`
	Subj  string `json:"subject_db,omitempty"` // utk guard fill-if-absent fase resolve
}

type e8bItem struct {
	e8bTarget
	Fase    string   `json:"fase"` // resolve | subject
	Upaya   int      `json:"upaya"`
	HTTP    int      `json:"http"`
	Ms      int64    `json:"ms"`
	Selesai bool     `json:"selesai"`
	Klas    string   `json:"klas"`
	Tulis   string   `json:"tulis,omitempty"`
	Error   string   `json:"error,omitempty"`
	Areas   []string `json:"areas,omitempty"`
	Judul   string   `json:"judul_view,omitempty"`
}

type e8bTutup struct {
	NotFoundTanpaView []int64 `json:"not_found_tanpa_view"`
	PrintKosongMatch  []int64 `json:"print_kosong_matched"`
	SubjKosongMatch   []int64 `json:"subject_kosong_matched"`
}

type e8bJSON struct {
	Dibuat     string           `json:"dibuat"`
	Pagu       int              `json:"pagu_get"`
	Get        int              `json:"get"`
	TargetRlv  []e8bTarget      `json:"target_resolve"`
	TargetSub  []e8bTarget      `json:"target_subject"`
	Items      []e8bItem        `json:"items"`
	SubjWant   map[int64]string `json:"subj_want"`
	PrintWant  map[int64]string `json:"print_want"`
	Tutup      e8bTutup         `json:"tutup_jujur"`
	CountsPre  map[string]int64 `json:"counts_pre"`
	CountsPost map[string]int64 `json:"counts_post"`
	DiffBad    []string         `json:"diff_bad"`
	CountsOK   bool             `json:"counts_ok"`
	Ringkas    map[string]int   `json:"ringkasan"`
}

// viewIDRe = GarudaID dari URL view (/journal/view/N).
var viewIDRe = regexp.MustCompile(`/journal/view/(\d+)`)

// normISSN = normalisasi pembanding ISSN: upper + buang semua non-alnum
// (strip strip/spasi/format) — "2085-1618" == "20851618".
func normISSN(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func targetE8B(dbPath, fase string) ([]e8bTarget, error) {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var rows interface {
		Next() bool
		Scan(...any) error
		Close() error
		Err() error
	}
	if fase == "resolve" {
		r, qerr := db.Query(`
			SELECT j.id, j.name,
			       COALESCE(NULLIF(TRIM(COALESCE(j.garuda_url, '')), ''),
			                TRIM(COALESCE(e.garuda_url, ''))),
			       TRIM(COALESCE(j.print_issn,'')), TRIM(COALESCE(j.electronic_issn,'')),
			       TRIM(COALESCE(j.subject_area,''))
			FROM journals j
			JOIN journal_enrichment e ON e.journal_id = j.id
			WHERE e.match_status = 'not_found'
			  AND COALESCE(NULLIF(TRIM(COALESCE(j.garuda_url, '')), ''),
			               TRIM(COALESCE(e.garuda_url, ''))) LIKE '%/journal/view/%'
			ORDER BY j.id`)
		if qerr != nil {
			return nil, fmt.Errorf("target resolve: %w", qerr)
		}
		rows = r
	} else {
		r, qerr := db.Query(`
			SELECT j.id, j.name,
			       COALESCE(NULLIF(TRIM(COALESCE(j.garuda_url, '')), ''),
			                TRIM(COALESCE(e.garuda_url, ''))),
			       TRIM(COALESCE(j.print_issn,'')), TRIM(COALESCE(j.electronic_issn,'')),
			       TRIM(COALESCE(j.subject_area,''))
			FROM journals j
			JOIN journal_enrichment e ON e.journal_id = j.id
			WHERE e.match_status = 'matched'
			  AND TRIM(COALESCE(j.subject_area,'')) = ''
			  AND COALESCE(NULLIF(TRIM(COALESCE(j.garuda_url, '')), ''),
			               TRIM(COALESCE(e.garuda_url, ''))) LIKE '%/journal/view/%'
			ORDER BY j.id`)
		if qerr != nil {
			return nil, fmt.Errorf("target subject: %w", qerr)
		}
		rows = r
	}
	defer rows.Close()
	var out []e8bTarget
	for rows.Next() {
		var t e8bTarget
		if err := rows.Scan(&t.ID, &t.Nama, &t.URL, &t.PISSN, &t.EISSN, &t.Subj); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func runE8B(dbPath, fixturesDir string, delayMin, delayMax time.Duration) error {
	jsonPath := filepath.Join(filepath.Dir(fixturesDir), "e8b-results.json")
	backupPath := strings.TrimSuffix(dbPath, ".db") + ".preE8b.bak.db"

	st := e8bJSON{Pagu: paguE8B, SubjWant: map[int64]string{}, PrintWant: map[int64]string{}}
	lamaAda := false
	if raw, err := os.ReadFile(jsonPath); err == nil && json.Unmarshal(raw, &st) == nil {
		lamaAda = true
	}
	if st.Dibuat == "" {
		st.Dibuat = time.Now().UTC().Format(time.RFC3339)
	}
	if st.SubjWant == nil {
		st.SubjWant = map[int64]string{}
	}
	if st.PrintWant == nil {
		st.PrintWant = map[int64]string{}
	}

	if !lamaAda {
		// run baru: target di-query db + guard ketat (doc 39 expect 3 + 17)
		rlv, err := targetE8B(dbPath, "resolve")
		if err != nil {
			return err
		}
		if len(rlv) != 3 {
			return fmt.Errorf("target resolve = %d, expect 3 (doc 39) — berhenti utk review", len(rlv))
		}
		sub, err := targetE8B(dbPath, "subject")
		if err != nil {
			return err
		}
		if len(sub) != 17 {
			return fmt.Errorf("target subject = %d, expect 17 (doc 39) — berhenti utk review", len(sub))
		}
		st.TargetRlv, st.TargetSub = rlv, sub
		// backup fresh state SEBELUM tulis apa pun
		if _, err := os.Stat(backupPath); err == nil {
			return fmt.Errorf("backup %s sudah ada tanpa json %s — konflik state, review dulu", backupPath, jsonPath)
		}
		if err := salinFile(dbPath, backupPath); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}

	// gabung target + hasil lama (resume)
	lama := map[string]e8bItem{}
	for _, it := range st.Items {
		lama[it.Fase+"|"+fmt.Sprint(it.ID)] = it
	}
	st.Items = nil
	var semua []e8bItem
	for _, t := range st.TargetRlv {
		if o, ok := lama["resolve|"+fmt.Sprint(t.ID)]; ok {
			semua = append(semua, o)
		} else {
			semua = append(semua, e8bItem{e8bTarget: t, Fase: "resolve"})
		}
	}
	for _, t := range st.TargetSub {
		if o, ok := lama["subject|"+fmt.Sprint(t.ID)]; ok {
			semua = append(semua, o)
		} else {
			semua = append(semua, e8bItem{e8bTarget: t, Fase: "subject"})
		}
	}
	st.Items = semua

	fmt.Printf("== E8B Opsi B satu run (pagu ≤%d GET, target %d resolve + %d subject) ==\n",
		paguE8B, len(st.TargetRlv), len(st.TargetSub))

	simpan := func() error {
		st.Ringkas = map[string]int{}
		for _, it := range st.Items {
			st.Ringkas[it.Klas]++
		}
		w, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(jsonPath, w, 0o644)
	}
	ambil := func() error {
		if st.Get >= paguE8B {
			return fmt.Errorf("pagu E8b habis (%d)", paguE8B)
		}
		st.Get++
		return nil
	}

	store, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	c := garuda.NewClient("garuda-e8b", uaDefault, delayMin, delayMax)
	_ = os.MkdirAll(fixturesDir, 0o755)

	// fetch view: 2 upaya utk transien (err/5xx/429); body utk parse + fixture
	fetch := func(it *e8bItem) []byte {
		for up := 1; up <= 2; up++ {
			if err := ambil(); err != nil {
				fmt.Println("  " + err.Error())
				return nil
			}
			start := time.Now()
			body, stt, gerr := c.Get(it.URL, "")
			it.Upaya, it.HTTP, it.Ms = up, stt, time.Since(start).Milliseconds()
			if gerr != nil {
				it.Error = gerr.Error()
			}
			if gerr == nil && stt == 200 {
				_ = os.WriteFile(filepath.Join(fixturesDir,
					fmt.Sprintf("e8b-view-j%d.html", it.ID)), body, 0o644)
				return body
			}
			if gerr == nil && stt != 0 && stt < 500 && stt != 429 {
				it.Klas = fmt.Sprintf("http_%d", stt)
				it.Selesai = true // final non-transien (403/404 dst — hybrid sudah dicoba)
				return nil
			}
			if gerr != nil {
				it.Klas = "err"
			} else {
				it.Klas = fmt.Sprintf("http_%d", stt)
			}
		}
		return nil // transien habis 2 upaya → Selesai tetap false (rerun bisa)
	}

	isiWant := func(it *e8bItem, val *string, kolom string, want map[int64]string) error {
		n, err := store.UpdateJournalsE7(it.ID, kolom, *val)
		if err != nil {
			return err
		}
		if n == 1 {
			want[it.ID] = *val
			fld := "journals_" + kolom
			return store.MergeProvenance(it.ID, fld, storage.ProvEntry{
				Value: *val, Source: "garuda", Confidence: 1.0,
			})
		}
		return nil
	}

	for i := range st.Items {
		it := &st.Items[i]
		if it.Selesai {
			continue
		}
		body := fetch(it)
		if body == nil {
			_ = simpan()
			if st.Get >= paguE8B {
				break
			}
			continue
		}
		v, perr := garuda.ParseViewPage(bytes.NewReader(body))
		if perr != nil {
			it.Klas, it.Error, it.Selesai = "parse_err", perr.Error(), false
			_ = simpan()
			continue
		}
		it.Judul, it.Areas = v.Title, v.Areas

		switch {
		case v.NotFound:
			it.Klas = "view_notfound"
			it.Selesai = true // halaman basi — tak dikejar lagi; status db tak diubah

		case it.Fase == "resolve":
			mE := normISSN(v.EISSN) != "" && normISSN(v.EISSN) == normISSN(it.EISSN)
			mP := normISSN(v.PrintISSN) != "" && normISSN(v.PrintISSN) == normISSN(it.PISSN)
			if !mE && !mP {
				it.Klas = "tanpa_bukti"
				it.Selesai = true // ISSN view tak cocok db → TIDAK menulis (jujur)
				break
			}
			gid := int64(0)
			if m := viewIDRe.FindStringSubmatch(it.URL); m != nil {
				fmt.Sscan(m[1], &gid)
			}
			if gid == 0 {
				it.Klas, it.Error, it.Selesai = "parse_err", "garuda_id tak terbaca dari URL view", false
				break
			}
			mby := "pissn"
			if mE {
				mby = "eissn"
			}
			if err := store.UpsertGarudaMatch(storage.GarudaMatch{
				JournalID: it.ID, Status: "matched", GarudaID: gid,
				GarudaURL: it.URL, Title: v.Title, Publisher: v.Publisher,
				PISSN: v.PrintISSN, EISSN: v.EISSN,
				Subject:   strings.Join(v.Areas, garuda.GarudaSubjectSep),
				MatchedBy: mby, Confidence: 1.0,
			}); err != nil {
				return err
			}
			tulis := "matched:" + mby
			// fill-if-absent: subject hanya bila db masih kosong
			if it.Subj == "" && len(v.Areas) > 0 {
				val := garuda.MergeSubjectArea("", v.Areas)
				if n, err := store.UpdateJournalsE7(it.ID, "subject_area", val); err != nil {
					return err
				} else if n == 1 {
					st.SubjWant[it.ID] = val
					if err := store.MergeProvenance(it.ID, "journals_subject_area", storage.ProvEntry{
						Value: val, Source: "garuda", Confidence: 1.0,
					}); err != nil {
						return err
					}
					tulis += "+subject"
				}
			}
			// fill-if-absent: print_issn hanya bila db masih kosong
			if it.PISSN == "" {
				if p := normISSN(v.PrintISSN); p != "" {
					if err := isiWant(it, &p, "print_issn", st.PrintWant); err != nil {
						return err
					}
					tulis += "+print"
				}
			}
			it.Tulis, it.Klas, it.Selesai = tulis, "matched", true

		default: // fase subject
			if len(v.Areas) == 0 {
				it.Klas = "view_tanpa_area"
				it.Selesai = true // jujur: view tanpa label area → subject tetap kosong
				break
			}
			val := garuda.MergeSubjectArea("", v.Areas)
			if err := isiWant(it, &val, "subject_area", st.SubjWant); err != nil {
				return err
			}
			it.Tulis, it.Klas, it.Selesai = "subject_terisi", "subject_terisi", true
		}
		if err := simpan(); err != nil {
			return err
		}
		fmt.Printf("j%-6d %-8s %-15s http=%d %dms %s\n",
			it.ID, it.Fase, it.Klas, it.HTTP, it.Ms, it.Nama)
	}
	if err := simpan(); err != nil {
		return err
	}

	// ---- Fase B: tutup jujur (0 GET, baca db) + verifikasi K1 ----------
	if err := tutupDanVerifE8B(dbPath, backupPath, &st); err != nil {
		return err
	}
	if err := simpan(); err != nil {
		return err
	}

	fmt.Printf("\n== E8B SELESAI == GET %d/%d | diff_bad=%d counts_ok=%v\n",
		st.Get, st.Pagu, len(st.DiffBad), st.CountsOK)
	for k, v := range st.Ringkas {
		fmt.Printf("  %-16s %d\n", k, v)
	}
	fmt.Printf("tutup jujur: not_found tanpa view=%d | print kosong matched=%d | subject kosong matched=%d\n",
		len(st.Tutup.NotFoundTanpaView), len(st.Tutup.PrintKosongMatch), len(st.Tutup.SubjKosongMatch))
	fmt.Printf("JSON: %s\n", jsonPath)
	return nil
}

// tutupDanVerifE8B = daftar jujur (0 GET) + verifikasi K1 terhadap backup
// pre-E8b: e8bDiff (hanya subject_area/print_issn utk id target boleh
// berubah, canonical identik) + selisih counts delta.
func tutupDanVerifE8B(dbPath, backupPath string, st *e8bJSON) error {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	ids := func(q string) ([]int64, error) {
		rs, err := db.Query(q)
		if err != nil {
			return nil, err
		}
		defer rs.Close()
		var out []int64
		for rs.Next() {
			var id int64
			if err := rs.Scan(&id); err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, rs.Err()
	}
	var err2 error
	if st.Tutup.NotFoundTanpaView, err2 = ids(`
		SELECT j.id FROM journals j
		JOIN journal_enrichment e ON e.journal_id = j.id
		WHERE e.match_status = 'not_found'
		  AND COALESCE(NULLIF(TRIM(COALESCE(j.garuda_url, '')), ''),
		               TRIM(COALESCE(e.garuda_url, ''))) NOT LIKE '%/journal/view/%'
		ORDER BY j.id`); err2 != nil {
		return err2
	}
	if st.Tutup.PrintKosongMatch, err2 = ids(`
		SELECT j.id FROM journals j
		JOIN journal_enrichment e ON e.journal_id = j.id
		WHERE e.match_status = 'matched'
		  AND TRIM(COALESCE(j.print_issn,'')) = ''
		ORDER BY j.id`); err2 != nil {
		return err2
	}
	if st.Tutup.SubjKosongMatch, err2 = ids(`
		SELECT j.id FROM journals j
		JOIN journal_enrichment e ON e.journal_id = j.id
		WHERE e.match_status = 'matched'
		  AND TRIM(COALESCE(j.subject_area,'')) = ''
		ORDER BY j.id`); err2 != nil {
		return err2
	}

	pre, err := e7gSnapshot(backupPath)
	if err != nil {
		return fmt.Errorf("snapshot pre: %w", err)
	}
	post, err := e7gSnapshot(dbPath)
	if err != nil {
		return fmt.Errorf("snapshot post: %w", err)
	}
	preC, err := e7fSnapshotCanon(backupPath)
	if err != nil {
		return fmt.Errorf("canon pre: %w", err)
	}
	postC, err := e7fSnapshotCanon(dbPath)
	if err != nil {
		return fmt.Errorf("canon post: %w", err)
	}
	st.DiffBad = e8bDiff(pre, post, preC, postC, st.SubjWant, st.PrintWant)

	st.CountsPre, err = e8bCounts(backupPath)
	if err != nil {
		return fmt.Errorf("counts pre: %w", err)
	}
	st.CountsPost, err = e8bCounts(dbPath)
	if err != nil {
		return fmt.Errorf("counts post: %w", err)
	}
	nMatch := 0
	for _, it := range st.Items {
		if it.Fase == "resolve" && it.Klas == "matched" {
			nMatch++
		}
	}
	var bad []string
	cek := func(k string, want int64) {
		if st.CountsPost[k] != want {
			bad = append(bad, fmt.Sprintf("%s: post=%d want=%d", k, st.CountsPost[k], want))
		}
	}
	// invariant utuh (E8b tak menyentuhnya)
	for _, k := range []string{"journals", "hash_unik", "ojs_url_ketutup"} {
		cek(k, st.CountsPre[k])
	}
	// delta sesuai tulisan
	cek("matched", st.CountsPre["matched"]+int64(nMatch))
	cek("not_found", st.CountsPre["not_found"]-int64(nMatch))
	cek("subj_kosong", st.CountsPre["subj_kosong"]-int64(len(st.SubjWant)))
	cek("print_kosong", st.CountsPre["print_kosong"]-int64(len(st.PrintWant)))
	st.CountsOK = len(bad) == 0
	st.DiffBad = append(st.DiffBad, bad...)
	return nil
}

// e8bCounts = hitung kunci verifikasi utk backup & db (pola e7gCounts +
// subj/print kosong + status match).
func e8bCounts(dbPath string) (map[string]int64, error) {
	db, err := e7fOpenRO(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := map[string]int64{}
	q := []struct{ key, sql string }{
		{"journals", `SELECT COUNT(*) FROM journals`},
		{"hash_unik", `SELECT COUNT(DISTINCT content_hash) FROM journals`},
		{"subj_kosong", `SELECT COUNT(*) FROM journals WHERE TRIM(COALESCE(subject_area,'')) = ''`},
		{"print_kosong", `SELECT COUNT(*) FROM journals WHERE TRIM(COALESCE(print_issn,'')) = ''`},
		{"matched", `SELECT COUNT(*) FROM journal_enrichment WHERE match_status = 'matched'`},
		{"not_found", `SELECT COUNT(*) FROM journal_enrichment WHERE match_status = 'not_found'`},
		{"ojs_url_ketutup", `
			SELECT COUNT(*) FROM journals j
			WHERE TRIM(COALESCE(j.ojs_url,'')) <> ''
			  AND EXISTS (SELECT 1 FROM journal_urls u
			              WHERE u.journal_id = j.id AND u.kind = 'ojs'
			                AND u.url = TRIM(j.ojs_url))`},
	}
	for _, x := range q {
		var n int64
		if err := db.QueryRow(x.sql).Scan(&n); err != nil {
			return nil, fmt.Errorf("%s: %w", x.key, err)
		}
		out[x.key] = n
	}
	return out, nil
}

// e8bDiff = K1 E8b: baris journals identik kecuali subject_area/print_issn
// utk id ∈ want (nilainya persis); canonical tak tersentuh sama sekali.
func e8bDiff(pre, post map[int64]e7gSnap, preC, postC map[int64]string,
	subjWant, printWant map[int64]string) []string {
	var bad []string
	if len(pre) != len(post) {
		bad = append(bad, fmt.Sprintf("jumlah baris journals %d→%d", len(pre), len(post)))
	}
	for id, a := range pre {
		b, ok := post[id]
		if !ok {
			bad = append(bad, fmt.Sprintf("j%d hilang", id))
			continue
		}
		if a.Name != b.Name || a.ContentHash != b.ContentHash || a.LastScr != b.LastScr ||
			a.GarudaURL != b.GarudaURL || a.EISSN != b.EISSN || a.Rank != b.Rank ||
			a.OJSStatus != b.OJSStatus || a.DoajURL != b.DoajURL || a.OJSURL != b.OJSURL {
			bad = append(bad, fmt.Sprintf("j%d kolom non-target berubah", id))
		}
		if w, dituju := subjWant[id]; dituju {
			if b.SubjectArea != w {
				bad = append(bad, fmt.Sprintf("j%d subject=%q want %q", id, b.SubjectArea, w))
			}
		} else if a.SubjectArea != b.SubjectArea {
			bad = append(bad, fmt.Sprintf("j%d subject_area berubah di luar target", id))
		}
		if w, dituju := printWant[id]; dituju {
			if b.PrintISSN != w {
				bad = append(bad, fmt.Sprintf("j%d print=%q want %q", id, b.PrintISSN, w))
			}
		} else if a.PrintISSN != b.PrintISSN {
			bad = append(bad, fmt.Sprintf("j%d print_issn berubah di luar target", id))
		}
	}
	for id := range subjWant {
		if _, ada := pre[id]; !ada {
			bad = append(bad, fmt.Sprintf("j%d target subject tapi baris tak ada", id))
		}
	}
	for id := range printWant {
		if _, ada := pre[id]; !ada {
			bad = append(bad, fmt.Sprintf("j%d target print tapi baris tak ada", id))
		}
	}
	for id, ca := range preC {
		if postC[id] != ca {
			bad = append(bad, fmt.Sprintf("j%d canonical berubah", id))
		}
	}
	for id := range postC {
		if _, ada := preC[id]; !ada {
			bad = append(bad, fmt.Sprintf("j%d canonical baris baru", id))
		}
	}
	return bad
}
