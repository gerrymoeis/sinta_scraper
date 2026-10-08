package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"sinta-scraper/internal/garuda"
)

// runE7GH = EKSPERIMEN hybrid E7g (arah user 7 Okt 2026 — "eksperimenkan
// fallback WAF & browser default adaptif kita dulu, catat hasil") — TANPA
// tulis db. Tiga kelompok baris yg tak mampu diverifikasi E7g (GetPlain =
// tanpa fallback):
//
//	W. 11 baris waf (403) + 2 baris server (5xx) → Client.Get penuh:
//	   std → bila 403 → Hybrid (tls-client → marker? BrowserSolve adaptif
//	   Chrome→Edge→Chromium, doc 35); 5xx → retry backoff.
//	R. 31 host robots "fetch-gagal" (403/0/530 → E7g disallow konservatif,
//	   37 baris terblokir) → re-fetch robots.txt via jalur hybrid → bila
//	   kini 200 → evaluasi ATURAN ASLI → baris yg diizinkan → GET cek;
//	   aturan asli melarang → TETAP larang (taat doc 11 — bukan pembobolan);
//	   robots tetap gagal → konservatif, jujur.
//	S. 1 baris genuine-disallow (j1842, robots 200 + Disallow) DILEWATKAN
//	   sengaja — E7g tak punya izin bypass aturan asli.
//
// Pagu panggilan Get ≤ 100 (31 robots + 13 waf/server + ≤37 cek = 81;
// retry internal Client utk 429/5xx menambah beberapa — dicatat jujur).
// Hasil = data/stage2/e7g-waf-eksperimen.json + fixture e7gh-robots-*.txt.
const paguEksperimenE7gh = 100

type e7ghRobots struct {
	Host    string `json:"host"`
	Origin  string `json:"origin"`
	HTTP    int    `json:"http"` // hasil re-fetch hybrid; 0 = masih gagal
	Catatan string `json:"catatan,omitempty"`
}

type e7ghBaris struct {
	ID      int64  `json:"journal_id"`
	Nama    string `json:"nama"`
	Sebab   string `json:"sebab"` // waf | server | robots
	URL     string `json:"url"`
	Boleh   string `json:"boleh,omitempty"` // ya | larang-aturan | tak-terevaluasi
	Catatan string `json:"catatan,omitempty"`
	HTTP    int    `json:"http"`
	Cls     string `json:"klasifikasi,omitempty"`
	Final   string `json:"final_url,omitempty"`
	Err     string `json:"error,omitempty"`
	Solves  int    `json:"solves_delta,omitempty"`
}

type e7ghJSON struct {
	Dibuat  string          `json:"dibuat"`
	Pagu    int             `json:"pagu_get"`
	Get     int             `json:"get"`
	Solves  int             `json:"solves"`
	Robots  []e7ghRobots    `json:"robots"`
	Cek     []e7ghBaris     `json:"cek"`
	Taat    []e7ghBaris     `json:"dilewat_taat_robots"`
	Ringkas map[string]int  `json:"ringkasan"`
	Tulis   *e7ghwTulisJSON `json:"tulis,omitempty"` // hasil poin 2 (approve 8 Okt)
}

func runE7GH(fixturesDir string, delayMin, delayMax time.Duration) error {
	srcPath := filepath.Join(filepath.Dir(fixturesDir), "e7g-results.json")
	outPath := filepath.Join(filepath.Dir(fixturesDir), "e7g-waf-eksperimen.json")

	srcRaw, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("baca %s: %w", srcPath, err)
	}
	var src e7gHasilJSON
	if err := json.Unmarshal(srcRaw, &src); err != nil {
		return fmt.Errorf("parse %s: %w", srcPath, err)
	}
	robAsal := map[string]e7ghRobots{} // resume hasil re-fetch
	st := e7ghJSON{Pagu: paguEksperimenE7gh}
	if raw, err := os.ReadFile(outPath); err == nil {
		var old e7ghJSON
		if json.Unmarshal(raw, &old) == nil {
			for _, r := range old.Robots {
				robAsal[r.Host] = r
			}
			st = old
		}
	}
	adaCek := map[int64]e7ghBaris{} // resume: id → hasil lama (SEBELUM reset)
	for _, b := range st.Cek {
		adaCek[b.ID] = b
	}
	st.Robots = []e7ghRobots{}
	st.Cek = []e7ghBaris{}
	st.Taat = []e7ghBaris{}
	if st.Dibuat == "" {
		st.Dibuat = time.Now().UTC().Format(time.RFC3339)
	}

	// ---- target dari hasil E7g ---------------------------------------------
	robByHost := map[string]e7gRobotsHasil{}
	for _, r := range src.Robots {
		robByHost[r.Host] = r
	}
	var waf, server, dilGagal, taatAsli []e7gCekHasil
	for _, c := range src.Cek {
		switch {
		case c.Cls == "waf":
			waf = append(waf, c)
		case c.Cls == "server":
			server = append(server, c)
		case c.Dari == "robots-tolak":
			h := e7gHost(c.URL)
			r, ok := robByHost[h]
			switch {
			case ok && r.HTTP == 200:
				taatAsli = append(taatAsli, c) // aturan asli — TIDAK diuji
			default:
				dilGagal = append(dilGagal, c)
			}
		}
	}
	hostSet := map[string]bool{}
	for _, c := range dilGagal {
		hostSet[e7gHost(c.URL)] = true
	}
	hostUrut := []string{}
	for h := range hostSet {
		hostUrut = append(hostUrut, h)
	}
	sort.Strings(hostUrut)

	fmt.Printf("== E7GH eksperimen hybrid (pagu ≤%d Get, tanpa tulis db) ==\n", paguEksperimenE7gh)
	fmt.Printf("target: waf=%d server=%d · robots host=%d (baris=%d) · taat-robots=%d\n",
		len(waf), len(server), len(hostUrut), len(dilGagal), len(taatAsli))

	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		return err
	}
	c := garuda.NewClient("api-e7gh", uaDefault, delayMin, delayMax)
	ambil := func() error {
		if st.Get >= paguEksperimenE7gh {
			return fmt.Errorf("pagu eksperimen (%d): %w", paguEksperimenE7gh, errPaguE7g)
		}
		st.Get++
		return nil
	}
	simpan := func() error {
		if s := c.Solves(); s > st.Solves {
			st.Solves = s // resume: client baru tiap run — jangan timpa total
		}
		st.Ringkas = ringkasE7gh(&st)
		w, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(outPath, w, 0o644)
	}

	// ---- Fase R: re-fetch robots.txt host terblokir (jalur hybrid) ---------
	fmt.Printf("\n-- Fase R: robots.txt ulang (%d host) --\n", len(hostUrut))
	bodyByHost := map[string][]byte{}
	for i, h := range hostUrut {
		r, ok := robAsal[h]
		if !ok {
			origin := robByHost[h].Origin
			if origin == "" {
				continue
			}
			if err := ambil(); err != nil {
				_ = simpan()
				return err
			}
			s0 := c.Solves()
			body, stt, gerr := c.Get(origin+"/robots.txt", "")
			r = e7ghRobots{Host: h, Origin: origin, HTTP: stt}
			switch {
			case stt == 200 && body != nil:
				r.Catatan = "200 via jalur hybrid"
				if err := os.WriteFile(filepath.Join(fixturesDir, "e7gh-robots-"+e7gSlugHost(origin)+".txt"), body, 0o644); err != nil {
					r.Catatan += " · tulis fixture: " + err.Error()
				}
				bodyByHost[h] = body
			case stt == 404 || stt == 410:
				r.Catatan = "tanpa file"
			default:
				r.Catatan = fmt.Sprintf("tetap gagal (err=%v)", gerr)
			}
			if d := c.Solves() - s0; d > 0 {
				r.Catatan += fmt.Sprintf(" · solves+%d", d)
			}
			robAsal[h] = r
		} else if r.HTTP == 200 && bodyByHost[h] == nil {
			if b, err := os.ReadFile(filepath.Join(fixturesDir, "e7gh-robots-"+e7gSlugHost(r.Origin)+".txt")); err == nil {
				bodyByHost[h] = b
			}
		}
		st.Robots = append(st.Robots, robAsal[h])
		fmt.Printf("[%d/%d] %-40s http=%-4d %s\n", i+1, len(hostUrut), h, robAsal[h].HTTP, robAsal[h].Catatan)
	}
	if err := simpan(); err != nil {
		return err
	}

	// ---- evaluasi robots → antre cek + klasifikasi taat --------------------
	kiniLarang := []e7ghBaris{}
	antre := []e7gCekHasil{}
	for _, c0 := range dilGagal {
		h := e7gHost(c0.URL)
		r, ok := robAsal[h]
		k := e7ghBaris{ID: c0.ID, Nama: c0.Nama, Sebab: "robots", URL: c0.URL}
		switch {
		case !ok || r.HTTP != 200:
			k.Boleh = "tak-terevaluasi"
			k.Catatan = fmt.Sprintf("robots http=%d — aturan tak terbaca, konservatif", r.HTTP)
		case r.HTTP == 200 && len(bodyByHost[h]) == 0:
			k.Boleh = "tak-terevaluasi"
			k.Catatan = "robots 200 tapi body tak tersedia — tak menilai tanpa bukti"
		case !garuda.RobotsBoleh(garuda.ParseRobots(bodyByHost[h]), garuda.RobotsURI(c0.URL), uaDefault):
			k.Boleh = "larang-aturan"
			k.Catatan = "robots 200 → aturan ASLI melarang path ini (tetap taat)"
		default:
			k.Boleh = "ya"
			k.Catatan = "robots 200 → aturan mengizinkan → GET cek"
			antre = append(antre, c0)
		}
		if k.Boleh != "ya" {
			kiniLarang = append(kiniLarang, k)
		}
	}
	for _, c0 := range taatAsli {
		kiniLarang = append(kiniLarang, e7ghBaris{ID: c0.ID, Nama: c0.Nama, Sebab: "robots",
			URL: c0.URL, Boleh: "larang-aturan", Catatan: "E7g: robots 200 + Disallow asli"})
	}

	// ---- Fase W + C: GET penuh (std → hybrid) ------------------------------
	fmt.Printf("\n-- Fase W/C: GET penuh (waf=%d server=%d cek-robots=%d) --\n",
		len(waf), len(server), len(antre))
	proses := func(c0 e7gCekHasil, sebab string) (e7ghBaris, error) {
		if x, ok := adaCek[c0.ID]; ok {
			st.Cek = append(st.Cek, x) // resume — hasil lama dibawa ke keluaran
			return x, nil
		}
		k := e7ghBaris{ID: c0.ID, Nama: c0.Nama, Sebab: sebab, URL: c0.URL}
		if err := ambil(); err != nil {
			return k, err
		}
		s0 := c.Solves()
		_, stt, gerr := c.Get(c0.URL, "")
		k.HTTP, k.Solves = stt, c.Solves()-s0
		k.Cls = garuda.KlasifikasiE7g(stt, gerr)
		if gerr != nil {
			k.Err = gerr.Error()
		}
		fmt.Printf("j%-6d %-8s http=%-4d cls=%-10s solves+%d %s\n",
			k.ID, sebab, k.HTTP, k.Cls, k.Solves, k.Nama)
		st.Cek = append(st.Cek, k)
		adaCek[k.ID] = k
		return k, nil
	}
	for _, c0 := range waf {
		if _, err := proses(c0, "waf"); err != nil {
			_ = simpan()
			return err
		}
	}
	for _, c0 := range server {
		if _, err := proses(c0, "server"); err != nil {
			_ = simpan()
			return err
		}
	}
	for _, c0 := range antre {
		if _, err := proses(c0, "robots"); err != nil {
			_ = simpan()
			return err
		}
	}
	st.Taat = kiniLarang
	if err := simpan(); err != nil {
		return err
	}

	fmt.Printf("\n== E7GH SELESAI ==\n")
	fmt.Printf("GET: %d/%d · solves=%d\n", st.Get, st.Pagu, st.Solves)
	for _, k := range []string{"robots_200_baru", "robots_tetap_gagal", "waf_lolos", "waf_masih",
		"server_lolos", "cek_ok", "cek_mati", "taat_larang", "tak_terevaluasi"} {
		if v := st.Ringkas[k]; v > 0 {
			fmt.Printf("  %-20s %d\n", k, v)
		}
	}
	fmt.Printf("JSON: %s\n", outPath)
	return nil
}

func ringkasE7gh(st *e7ghJSON) map[string]int {
	r := map[string]int{}
	for _, x := range st.Robots {
		if x.HTTP == 200 {
			r["robots_200_baru"]++
		} else {
			r["robots_tetap_gagal"]++
		}
	}
	for _, k := range st.Cek {
		switch k.Sebab {
		case "waf":
			if k.HTTP == 200 {
				r["waf_lolos"]++
			} else {
				r["waf_masih"]++
			}
		case "server":
			if k.HTTP == 200 {
				r["server_lolos"]++
			}
		case "robots":
			if k.HTTP == 200 {
				r["cek_ok"]++
			} else if garuda.E7gMati(k.Cls) {
				r["cek_mati"]++
			}
		}
	}
	for _, k := range st.Taat {
		if k.Boleh == "larang-aturan" {
			r["taat_larang"]++
		} else {
			r["tak_terevaluasi"]++
		}
	}
	return r
}
