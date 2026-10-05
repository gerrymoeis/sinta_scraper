package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"sinta-scraper/internal/garuda"

	_ "modernc.org/sqlite"
)

// runE5 = Q4 E5 (doc 30 §14.4 — APPROVED user 4 Okt 2026, opsi (a)):
// probe platform & OAI-PMH pada 20 jurnal external (host jurnal sendiri,
// BUKAN Garuda) — live pertama utk kolom ojs_status (semua "pending").
//
// Desain (approve user):
//   - Sampling deterministik 20/261: 3 cross-check (punya garuda_home_url E3),
//     3 suffix ojs_url bermasalah, 14 stride campuran host.
//   - Per baris 2 GET → PAGU KERAS 40: (1) {norm}/oai?verb=Identify,
//     (2) {norm}/ home-page utk deteksi platform berbasis konten.
//   - Normalisasi ojs_url dari FAKTA pola suffix 75 baris db (doc 34 §2):
//     buang /issue/archive, /issue/view/N, /issue, /index, /register,
//     /index.php di akhir — path journal TIDAK disentuh.
//   - Referer per-URL host external (BUKAN DefaultReferer Garuda):
//     OAI GET referer = {norm}/ (dari home), home GET direct (tanpa referer).
//   - Resume: hasil lama dari e5-results.json dipakai ulang (tak GET ulang) —
//     sama pola dgn E4b; -refresh = ulang semua.
//   - TANPA tulis db (E6/E7 yang menulis). Output: e5-results.json + ringkasan.
//
// Client: NewClient("ojs-e5", …) — limiter global (jeda antar SEMUA GET,
// termasuk lintas host = etis), retry internal 429/5xx {2s,5s,15s} TIDAK
// dihitung ke pagu (pagu = panggilan Get, konsisten dgn E4b).
const paguE5 = 40

// ---------- normalisasi ojs_url (fakta pola db, bukan asumsi) ----------

// reSuffixOJS = segmen halaman di ujung URL — hanya pola yang TERBUKTI di 75
// baris db (analisa doc 34 §2). Kasus ambigu /archives (j689) TIDAK dibuang:
// biar live E5 yang menilai.
var reSuffixOJS = regexp.MustCompile(`(?i)(/issue/archive|/issue/view/\d+|/issue|/index|/register|/index\.php)$`)

// normOJS membuang suffix halaman berulang sampai stabil
// (mis. .../jurnal/index → .../jurnal; host/index.php/register → host).
func normOJS(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	for {
		t := reSuffixOJS.ReplaceAllString(u, "")
		if t == u {
			return u
		}
		u = strings.TrimRight(t, "/")
	}
}

// ---------- deteksi platform berbasis konten home-page ----------

var (
	reCitationTitle = regexp.MustCompile(`(?i)name=["']citation_title["']`)
	rePKPPath       = regexp.MustCompile(`(?i)/lib/pkp/`) // asset internal OJS
	reGenWP         = regexp.MustCompile(`(?i)name=["']generator["'][^>]*content=["'][^"']*wordpress`)
	reWPRel         = regexp.MustCompile(`(?i)["']/(wp-content|wp-includes|wp-admin)/`) // path di host YANG SAMA
)

// deteksiPlatform mengklasifikasi platform dari body home-page (200 saja).
// Urutan: OJS (petunjuk paling khas) → WordPress → custom.
// Rules dari BUKTI empiris run1 (doc 34 §5):
//   - OJS 2.4.x custom theme → generator hanya versi polos (" 2.4.8.1") tanpa
//     nama "Open Journal Systems" → perlu 2 penambah: footer "Public Knowledge
//     Project" + path relatif /lib/pkp/ (j691/j1990/j3602).
//   - /wp-content/ di URL ABSOLUT domain lain (link eksternal, kasus j691
//     training.bcrec.id) = BUKAN penanda WP → wajib path relatif "…/wp-content/.
//   - Teks bebas "wordpress" DITOLAK sbg penanda (false positive link eksternal
//     tanpa bukti struktural — rugi #4 utk deteksi lemah).
func deteksiPlatform(body []byte) (string, string) {
	switch {
	case reCitationTitle.Match(body):
		return "ojs", "meta citation_title"
	case bytesContainsFold(body, "open journal systems"):
		return "ojs", "teks 'Open Journal Systems'"
	case bytesContainsFold(body, "public knowledge project"):
		return "ojs", "footer 'Public Knowledge Project'"
	case rePKPPath.Match(body):
		return "ojs", "path relatif /lib/pkp/"
	case reGenWP.Match(body):
		return "wordpress", "generator WordPress"
	case reWPRel.Match(body):
		return "wordpress", "path relatif /wp-content/"
	}
	return "custom", "-"
}

// bytesContainsFold = strings.Contains(strings.ToLower(body), s) tnp alokasi
// lower penuh berulang (body bisa ratusan KB).
func bytesContainsFold(body []byte, s string) bool {
	return bytes.Contains(bytes.ToLower(body), []byte(s))
}

// ---------- deteksi respons OAI-PMH Identify ----------

var (
	reIdentify = regexp.MustCompile(`(?i)<(oai:)?identify[\s>]`)
	reRepoName = regexp.MustCompile(`(?i)<(oai:)?repositoryName>\s*([^<]*?)\s*</`)
)

// ---------- struktur hasil ----------

type e5Baris struct {
	ID      int64
	Nama    string
	PISSN   string
	EISSN   string
	OjsAsli string
	OjsNorm string
	HomeE3  string // garuda_home_url (E3) — penanda pool cross-check
	Pool    string // crosscheck | suffix | lain
}

type e5OAI struct {
	Status   string `json:"status"` // oai_aktif | oai_mati | unreachable
	HTTP     int    `json:"http"`
	RepoName string `json:"repo_name,omitempty"`
	Sumber   string `json:"sumber,omitempty"` // URL sumber (investigasi: garuda_oai_url / url alternatif)
	Error    string `json:"error,omitempty"`
}

type e5Home struct {
	Platform string `json:"platform"` // ojs | wordpress | custom | mati | unreachable
	Evidence string `json:"evidence,omitempty"`
	HTTP     int    `json:"http"`
	Sumber   string `json:"sumber,omitempty"`
	Error    string `json:"error,omitempty"`
}

type e5Hasil struct {
	JournalID     int64  `json:"journal_id"`
	Nama          string `json:"nama"`
	PISSN         string `json:"print_issn"`
	EISSN         string `json:"electronic_issn"`
	OjsAsli       string `json:"ojs_url"`
	OjsNorm       string `json:"ojs_url_norm"`
	Dinormalisasi bool   `json:"dinormalisasi"`
	Pool          string `json:"pool"`
	OAI           e5OAI  `json:"oai"`
	Home          e5Home `json:"home"`
}

// e5Ringkasan = agregat hitungan utk laporan stdout + JSON.
type e5Ringkasan struct {
	OAI      map[string]int `json:"oai"`
	Platform map[string]int `json:"platform"`
}

type e5JSON struct {
	Dibuat    string      `json:"dibuat"`
	Pagu      int         `json:"pagu_get"`
	GetTotal  int         `json:"get_run_ini"`
	Jumlah    int         `json:"jumlah"`
	Ringkasan e5Ringkasan `json:"ringkasan"`
	Items     []e5Hasil   `json:"items"`
}

// ---------- sampling deterministik ----------

// strideAmbil memilih n indeks menyebar (deterministik, tanpa random).
func strideAmbil(pool []e5Baris, n int) []e5Baris {
	if n <= 0 || len(pool) == 0 {
		return nil
	}
	if len(pool) <= n {
		out := make([]e5Baris, len(pool))
		copy(out, pool)
		return out
	}
	step := len(pool) / n
	out := make([]e5Baris, 0, n)
	for i := 0; i < n; i++ {
		idx := i * step
		if idx >= len(pool) {
			idx = len(pool) - 1
		}
		out = append(out, pool[idx])
	}
	return out
}

// pilihSampelE5 = 3 cross-check + 3 suffix + 14 stride campuran → 20 urut id.
func pilihSampelE5(baris []e5Baris) ([]e5Baris, error) {
	var poolA, poolB, poolC []e5Baris
	inA := map[int64]bool{}
	for _, b := range baris {
		if b.HomeE3 != "" {
			poolA = append(poolA, b)
			inA[b.ID] = true
		}
	}
	for _, b := range baris {
		if b.OjsNorm != b.OjsAsli {
			if !inA[b.ID] {
				poolB = append(poolB, b)
			}
		} else if !inA[b.ID] {
			poolC = append(poolC, b)
		}
	}
	sampel := append(strideAmbil(poolA, 3), strideAmbil(poolB, 3)...)
	sampel = append(sampel, strideAmbil(poolC, 14)...)
	sort.Slice(sampel, func(i, j int) bool { return sampel[i].ID < sampel[j].ID })

	seen := map[int64]bool{}
	var unik []e5Baris
	for _, b := range sampel {
		if !seen[b.ID] {
			seen[b.ID] = true
			unik = append(unik, b)
		}
	}
	if len(unik) != 20 {
		return nil, fmt.Errorf("sampel %d ≠ 20 (poolA=%d poolB=%d poolC=%d)",
			len(unik), len(poolA), len(poolB), len(poolC))
	}
	return unik, nil
}

// ---------- driver ----------

// runE5 = E5 utuh (doc 34). Baca db read-only; resume dari e5-results.json;
// body disimpan sbg fixture e5-j{id}-oai.xml / e5-j{id}-home.html utk audit.
func runE5(dbPath, fixturesDir string, refresh bool, delayMin, delayMax time.Duration) error {
	uri := "file:" + filepath.ToSlash(dbPath) + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return fmt.Errorf("buka db: %w", err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT j.id, j.name, COALESCE(j.print_issn,''), COALESCE(j.electronic_issn,''),
		       TRIM(COALESCE(j.ojs_url,'')), COALESCE(e.garuda_home_url,'')
		FROM journals j JOIN journal_enrichment e ON e.journal_id = j.id
		WHERE TRIM(COALESCE(j.ojs_url,'')) <> ''
		ORDER BY j.id`)
	if err != nil {
		return fmt.Errorf("query jurnal: %w", err)
	}
	var baris []e5Baris
	for rows.Next() {
		var b e5Baris
		if err := rows.Scan(&b.ID, &b.Nama, &b.PISSN, &b.EISSN, &b.OjsAsli, &b.HomeE3); err != nil {
			rows.Close()
			return err
		}
		b.OjsNorm = normOJS(b.OjsAsli)
		b.Pool = "lain"
		if b.OjsNorm != b.OjsAsli {
			b.Pool = "suffix"
		}
		if b.HomeE3 != "" {
			b.Pool = "crosscheck"
		}
		baris = append(baris, b)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(baris) < 261 {
		return fmt.Errorf("baris ojs_url kosong/anomali: %d (<261)", len(baris))
	}

	sampel, err := pilihSampelE5(baris)
	if err != nil {
		return err
	}

	// resume: hasil lama dari e5-results.json
	hasilLama := map[int64]e5Hasil{}
	jsonPath := filepath.Join(filepath.Dir(fixturesDir), "e5-results.json")
	var getLama int
	if !refresh {
		if b, err := os.ReadFile(jsonPath); err == nil {
			var old e5JSON
			if json.Unmarshal(b, &old) == nil {
				for _, h := range old.Items {
					hasilLama[h.JournalID] = h
				}
				getLama = old.GetTotal
				fmt.Printf("resume: %d hasil lama (%d GET) dari %s\n",
					len(hasilLama), getLama, filepath.Base(jsonPath))
			}
		}
	}
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		return fmt.Errorf("buat dir fixture: %w", err)
	}

	fmt.Printf("== E5 probe OAI + platform (sampel 20, pagu %d GET, delay %s–%s) ==\n",
		paguE5, delayMin, delayMax)
	c := garuda.NewClient("ojs-e5", uaDefault, delayMin, delayMax)

	var getTotal int
	get := func(rawURL, referer string) ([]byte, int, error) {
		if getTotal >= paguE5 {
			return nil, 0, fmt.Errorf("PAGU E5 (%d) HABIS", paguE5)
		}
		getTotal++
		return c.Get(rawURL, referer)
	}

	rep := e5JSON{
		Dibuat: time.Now().UTC().Format(time.RFC3339),
		Pagu:   paguE5,
		Ringkasan: e5Ringkasan{
			OAI:      map[string]int{},
			Platform: map[string]int{},
		},
	}

	for i, b := range sampel {
		fixOAI := filepath.Join(fixturesDir, fmt.Sprintf("e5-j%d-oai.xml", b.ID))
		fixHome := filepath.Join(fixturesDir, fmt.Sprintf("e5-j%d-home.html", b.ID))
		var h e5Hasil
		if old, ok := hasilLama[b.ID]; ok && !refresh {
			h = old // salin hasil + sumber apa adanya (value copy)
			// identitas SELALU dari db (source of truth — hasil merge historis
			// bisa kehilangan field identitas)
			h.Nama, h.PISSN, h.EISSN = b.Nama, b.PISSN, b.EISSN
			h.OjsAsli, h.OjsNorm = b.OjsAsli, b.OjsNorm
			h.Dinormalisasi, h.Pool = b.OjsNorm != b.OjsAsli, b.Pool
			// re-parse home dari fixture — detector bisa berubah antar run,
			// konten tetap sumber kebenaran (0 GET).
			if b2, err := os.ReadFile(fixHome); err == nil && old.Home.HTTP == 200 {
				h.Home.Platform, h.Home.Evidence = deteksiPlatform(b2)
			}
		} else {
			h = e5Hasil{
				JournalID: b.ID, Nama: b.Nama, PISSN: b.PISSN, EISSN: b.EISSN,
				OjsAsli: b.OjsAsli, OjsNorm: b.OjsNorm,
				Dinormalisasi: b.OjsNorm != b.OjsAsli, Pool: b.Pool,
			}

			// GET 1: OAI Identify (referer = home-nya sendiri)
			body, status, err := get(b.OjsNorm+"/oai?verb=Identify", b.OjsNorm+"/")
			switch {
			case status == 0:
				h.OAI.Status = "unreachable"
				if err != nil {
					h.OAI.Error = err.Error()
				}
			case status == 200:
				if err := os.WriteFile(fixOAI, body, 0o644); err != nil {
					return fmt.Errorf("tulis fixture j%d: %w", b.ID, err)
				}
				if reIdentify.Match(body) {
					h.OAI.Status = "oai_aktif"
					if m := reRepoName.FindSubmatch(body); m != nil {
						h.OAI.RepoName = string(m[2])
					}
				} else {
					h.OAI.Status = "oai_mati"
					h.OAI.Error = "200 tetapi bukan respons OAI Identify"
				}
				h.OAI.HTTP = status
			default: // 4xx/5xx habis retry — bukan kandidat OAI
				h.OAI.Status = "oai_mati"
				h.OAI.HTTP = status
				if err != nil {
					h.OAI.Error = err.Error()
				}
			}

			// GET 2: home-page utk platform (direct, tanpa referer)
			body2, status2, err2 := get(b.OjsNorm, "")
			switch {
			case status2 == 0:
				h.Home.Platform = "unreachable"
				if err2 != nil {
					h.Home.Error = err2.Error()
				}
			case status2 == 200:
				if err := os.WriteFile(fixHome, body2, 0o644); err != nil {
					return fmt.Errorf("tulis fixture j%d: %w", b.ID, err)
				}
				h.Home.Platform, h.Home.Evidence = deteksiPlatform(body2)
				h.Home.HTTP = status2
			default:
				h.Home.Platform = "mati"
				h.Home.HTTP = status2
				if err2 != nil {
					h.Home.Error = err2.Error()
				}
			}
		}

		normTag := ""
		if h.Dinormalisasi {
			normTag = " [norm]"
		}
		fmt.Printf("[%2d/20] j%-5d %-38.38s eissn=%-9s oai=%-10s http=%-3d platform=%-11s%s\n",
			i+1, h.JournalID, h.Nama, h.EISSN, h.OAI.Status, h.OAI.HTTP, h.Home.Platform, normTag)
		rep.Items = append(rep.Items, h)
		rep.Ringkasan.OAI[h.OAI.Status]++
		rep.Ringkasan.Platform[h.Home.Platform]++
	}
	rep.Jumlah = len(rep.Items)
	rep.GetTotal = getTotal + getLama // kumulatif antar run (resume = 0 GET baru)

	// ---- daftar kendala utk verifikasi user (jurnal + ISSN lengkap) ----
	// Kendala = OAI tak aktif ATAU home tak hidup. Platform "custom" BUKAN
	// kendala (halaman hidup, sistem journal mandiri) — hanya tercatat di
	// ringkasan.
	var kendala []e5Hasil
	for _, h := range rep.Items {
		if h.OAI.Status != "oai_aktif" || h.Home.Platform == "mati" || h.Home.Platform == "unreachable" {
			kendala = append(kendala, h)
		}
	}

	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	if err := os.WriteFile(jsonPath, out, 0o644); err != nil {
		return fmt.Errorf("tulis json: %w", err)
	}

	fmt.Printf("\nE5 selesai: get=%d/%d → %s\n", getTotal, paguE5, jsonPath)
	fmt.Printf("OAI    : oai_aktif=%d oai_mati=%d unreachable=%d\n",
		rep.Ringkasan.OAI["oai_aktif"], rep.Ringkasan.OAI["oai_mati"], rep.Ringkasan.OAI["unreachable"])
	fmt.Printf("platform: ojs=%d wordpress=%d custom=%d mati=%d unreachable=%d\n",
		rep.Ringkasan.Platform["ojs"], rep.Ringkasan.Platform["wordpress"],
		rep.Ringkasan.Platform["custom"], rep.Ringkasan.Platform["mati"],
		rep.Ringkasan.Platform["unreachable"])

	fmt.Printf("\n== KENDALA E5 (%d) — utk verifikasi user ==\n", len(kendala))
	if len(kendala) == 0 {
		fmt.Println("tidak ada — semua sampel OAI aktif / platform terdeteksi")
	}
	for _, h := range kendala {
		fmt.Printf("j%-5d %-40.40s eissn=%-9s pissn=%-9s oai=%-10s platform=%-11s %s\n    url: %s → %s\n",
			h.JournalID, h.Nama, h.EISSN, h.PISSN, h.OAI.Status, h.Home.Platform,
			strings.TrimSpace(h.OAI.Error+" "+h.Home.Error), h.OjsAsli, h.OjsNorm)
	}
	return nil
}
