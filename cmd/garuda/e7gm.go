package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"sinta-scraper/internal/garuda"
	"sinta-scraper/internal/sinta"
)

// runE7GM = poin 1 arahan user 8 Okt 2026 — REVIEW + RETRY 7 baris MANUAL
// E7g yg gagal, lalu uji ulang & catat:
//
//	Diagnosa (fakta json E7g): 3 timeout (j390, j915, j5671) — std client
//	Timeout 30s (sinta/client.go) tapi user akses manual & bilang "bisa,
//	agak lama loading" → FALSE-NEGATIVE (situs hidup, pelan). 1 404 via
//	http (j6008 — user akses dgn https → OK). 3 sisanya user konfirmasi
//	memang tak bisa (j1623/j3995 404, j4040 dns) → jujur dicoba ulang utk
//	konfirmasi.
//
//	Eksperimen: URL **https-prefer** + std GET **timeout 60s** (2× attempt,
//	backoff 2s) → 403 → fallback Client.Get (hybrid). Pagu ≤14 panggilan;
//	0 tulis db; hasil = data/stage2/e7g-manual-retry.json + fixture tak perlu
//	(status-only).
const paguEksperimenE7gm = 14

type e7gmHasil struct {
	ID          int64  `json:"journal_id"`
	Nama        string `json:"nama"`
	URLDB       string `json:"url_db"`
	URLDiuji    string `json:"url_diuji"`
	VerifUser   string `json:"verifikasi_user"` // hasil cek manual user 8 Okt
	Upaya       int    `json:"upaya"`
	HTTP        int    `json:"http"`
	Cls         string `json:"klasifikasi"`
	Final       string `json:"final_url,omitempty"`
	Ms          int64  `json:"ms"`
	Error       string `json:"error,omitempty"`
	SolvesDelta int    `json:"solves_delta,omitempty"`
}

type e7gmJSON struct {
	Dibuat  string         `json:"dibuat"`
	Pagu    int            `json:"pagu_get"`
	Get     int            `json:"get"`
	Hasil   []e7gmHasil    `json:"hasil"`
	Ringkas map[string]int `json:"ringkasan"`
	Tulis   *e7gwTulisJSON `json:"tulis,omitempty"` // hasil poin 1 approve 8 Okt
}

// verifUser8Okt = hasil cek manual user (8 Okt 2026) utk 7 baris manual E7g.
var verifUser8Okt = map[int64]string{
	390:  "bisa (agak lama loading)",
	915:  "bisa",
	1623: "tidak — 404 / error",
	3995: "tidak — 404 / error",
	4040: "tidak — 404 / error",
	5671: "bisa (agak lama loading)",
	6008: "bisa (via https)",
}

func runE7GM(fixturesDir string, delayMin, delayMax time.Duration) error {
	srcPath := filepath.Join(filepath.Dir(fixturesDir), "e7g-results.json")
	outPath := filepath.Join(filepath.Dir(fixturesDir), "e7g-manual-retry.json")

	srcRaw, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("baca %s: %w", srcPath, err)
	}
	var src e7gHasilJSON
	if err := json.Unmarshal(srcRaw, &src); err != nil {
		return fmt.Errorf("parse %s: %w", srcPath, err)
	}

	// resume: id → hasil lama (skip bila sudah ada)
	st := e7gmJSON{Pagu: paguEksperimenE7gm}
	ada := map[int64]e7gmHasil{}
	if raw, err := os.ReadFile(outPath); err == nil {
		var old e7gmJSON
		if json.Unmarshal(raw, &old) == nil {
			st = old
			for _, h := range old.Hasil {
				ada[h.ID] = h
			}
		}
	}
	st.Hasil = []e7gmHasil{}
	if st.Dibuat == "" {
		st.Dibuat = time.Now().UTC().Format(time.RFC3339)
	}

	var target []e7gCekHasil
	for _, c := range src.Cek {
		if _, ok := verifUser8Okt[c.ID]; ok {
			target = append(target, c)
		}
	}
	if len(target) != len(verifUser8Okt) {
		return fmt.Errorf("target tak lengkap: %d dari %d baris manual", len(target), len(verifUser8Okt))
	}

	fmt.Printf("== E7GM retry 7 baris manual E7g (pagu ≤%d, timeout 120s, https-prefer) ==\n",
		paguEksperimenE7gm)

	lim := sinta.NewLimiter(delayMin, delayMax)
	hc := garuda.NewClient("e7gm-hybrid", uaDefault, delayMin, delayMax) // utk fallback 403
	// http.Client std dgn METRICS transport sinta, timeout 120s (bukan 30s
	// default) — eksperimen khusus "situs lama loading": fakta run 60s utk
	// j390 butuh 57s; j5671 (user: bisa) gagal 2×60s → naik 120s utk retry.
	httpSlow := sinta.NewHTTPClient("e7gm")
	httpSlow.Timeout = 120 * time.Second

	st.Get = 0
	ambil := func() error {
		if st.Get >= paguEksperimenE7gm {
			return fmt.Errorf("pagu E7gm habis (%d)", paguEksperimenE7gm)
		}
		st.Get++
		return nil
	}
	simpan := func() error {
		st.Ringkas = map[string]int{}
		for _, h := range st.Hasil {
			st.Ringkas[h.Cls]++
		}
		w, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(outPath, w, 0o644)
	}

	get60 := func(rawURL string) (int, string, int64, error) {
		lim.Wait()
		req, err := sinta.NewGET(rawURL, uaDefault, "")
		if err != nil {
			return 0, "", 0, err
		}
		start := time.Now()
		resp, err := httpSlow.Do(req)
		elapsed := time.Since(start).Milliseconds()
		if err != nil {
			return 0, "", elapsed, err
		}
		defer resp.Body.Close()
		final := ""
		if resp.Request != nil && resp.Request.URL != nil {
			final = resp.Request.URL.String()
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<20))
		return resp.StatusCode, final, elapsed, nil
	}

	httpsPrefer := func(u string) string {
		if len(u) > 7 && u[:7] == "http://" {
			return "https://" + u[7:]
		}
		return u
	}

	for _, c0 := range target {
		if _, ok := ada[c0.ID]; ok {
			st.Hasil = append(st.Hasil, ada[c0.ID])
			continue
		}
		h := e7gmHasil{ID: c0.ID, Nama: c0.Nama, URLDB: c0.URL,
			URLDiuji: httpsPrefer(c0.URL), VerifUser: verifUser8Okt[c0.ID]}
		for upaya := 1; upaya <= 2; upaya++ {
			if err := ambil(); err != nil {
				_ = simpan()
				return err
			}
			stt, final, ms, gerr := get60(h.URLDiuji)
			h.Upaya, h.HTTP, h.Final, h.Ms = upaya, stt, final, ms
			if gerr != nil {
				h.Error = gerr.Error()
			}
			// 403 → fallback hybrid (Client.Get: std 30s + tls/solver).
			if stt == 403 {
				s0 := hc.Solves()
				_, stt2, gerr2 := hc.Get(h.URLDiuji, "")
				h.SolvesDelta = hc.Solves() - s0
				h.HTTP, h.Upaya = stt2, upaya+10
				h.Cls = garuda.KlasifikasiE7g(stt2, gerr2)
				if gerr2 != nil {
					h.Error = gerr2.Error()
				}
				break
			}
			// 200 / status final non-transien → selesai; transien → retry 1×.
			if gerr == nil && stt != 0 && stt < 500 && stt != 429 {
				h.Cls = garuda.KlasifikasiE7g(stt, nil)
				break
			}
			if upaya == 2 {
				h.Cls = garuda.KlasifikasiE7g(stt, gerr)
				break
			}
			time.Sleep(2 * time.Second)
		}
		fmt.Printf("j%-6d http=%-4d cls=%-10s upaya=%d %dms %s\n    db=%s\n    uji=%s\n    user=%s\n",
			h.ID, h.HTTP, h.Cls, h.Upaya, h.Ms, h.Nama, h.URLDB, h.URLDiuji, h.VerifUser)
		st.Hasil = append(st.Hasil, h)
	}
	if err := simpan(); err != nil {
		return err
	}

	fmt.Printf("\n== E7GM SELESAI == GET %d/%d\n", st.Get, st.Pagu)
	for k, v := range st.Ringkas {
		fmt.Printf("  %-12s %d\n", k, v)
	}
	fmt.Printf("JSON: %s\n", outPath)
	return nil
}
