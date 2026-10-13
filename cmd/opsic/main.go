// Program opsic: Opsi C doc 42 §7.5 (approve user 13 Okt 2026) — fix data
// kandidat http→https utk 2 jurnal unreachable dgn fakta :443 terbuka tapi :80
// refused (j79 shirkah.or.id · j663 mji.ui.ac.id — diagnosa F3.4 §7.4).
//
// Verifikasi GET https via garuda.Client.GetPlain (jalur representatif
// pipeline — redirect otomatis, status jujur). TULIS kandidat journal_urls
// BARU (url=https, source sinta, kind ojs) HANYA bila semua target 200;
// selain itu exit 1 tanpa menyentuh db (pol E7gm/E7gw: gagal verify = jangan
// tulis). Baris http lama DIBIARKAN (riwayat E7g — jujur); journals.ojs_url
// (kolom K1) TIDAK disentuh — di luar kebutuhan resolve stage ojs.
//
// Tulis idempoten (UpsertJournalURL ON CONFLICT; checked_at/waktu tetap →
// ulang RowsAffected 0). Backup db dijalankan eksekutor SEBELUM program
// (pola E7gw: backup → tulis → verifikasi). Alat sekali pakai; BUKAN bagian
// dari pipeline (-stages).
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"sinta-scraper/internal/garuda"
	"sinta-scraper/internal/storage"
)

// target = satu kasus Opsi C: id jurnal, URL http asli (jejak — tidak dihapus),
// URL https hasil upgrade skema (path identik — hanya skema berubah).
type target struct {
	jid       int
	httpAsli  string
	httpsBaru string
}

var targets = []target{
	{79, "http://shirkah.or.id/new-ojs/index.php/home", "https://shirkah.or.id/new-ojs/index.php/home"},
	{663, "http://mji.ui.ac.id/journal/index.php/mji", "https://mji.ui.ac.id/journal/index.php/mji"},
}

func intPtr(v int) *int { return &v }

func main() {
	dbPath := flag.String("db", "data/stage2/sinta-r2-enrich.db", "path db kerja Tahap 2")
	flag.Parse()

	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 sinta-scraper/0.1 (riset-akademik; gerry.23164@mhs.unesa.ac.id)"
	c := garuda.NewClient("opsic", ua, time.Second, 3*time.Second)

	fmt.Printf("== OPSIC verify + tulis %d kandidat http→https ==\n", len(targets))

	// ---- 1. VERIFY — GET https (pola GetPlain; 200 = layak tulis) -----------
	type hasil struct {
		status int
		final  string
		bytes  int
	}
	oks := make([]hasil, 0, len(targets))
	gagal := false
	for _, t := range targets {
		body, st, final, err := c.GetPlain(t.httpsBaru, "")
		h := hasil{status: st, final: final, bytes: len(body)}
		fmt.Printf("verify j%d %s → status=%d bytes=%d final=%q err=%v\n",
			t.jid, t.httpsBaru, st, h.bytes, final, err)
		if err != nil || st != 200 {
			gagal = true
			continue
		}
		oks = append(oks, h)
	}
	if gagal {
		fmt.Fprintln(os.Stderr, "VERIFY GAGAL (status ≠ 200 / err) — TIDAK MENULIS apa pun; lihat fakta di atas")
		os.Exit(1)
	}

	// ---- 2. TULIS idempoten (waktu tetap → ulang = 0) -----------------------
	store, err := storage.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "buka db: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	proses := func(label string) (int, error) {
		n := 0
		for i, t := range targets {
			final := oks[i].final
			if final == "" {
				final = t.httpsBaru
			}
			m, err := store.UpsertJournalURL(int64(t.jid), t.httpsBaru, "ojs", "sinta",
				intPtr(oks[i].status), final, now)
			if err != nil {
				return n, fmt.Errorf("tulis j%d: %w", t.jid, err)
			}
			n += int(m)
		}
		fmt.Printf("%s: journal_urls berubah = %d baris\n", label, n)
		return n, nil
	}
	u1, err := proses("tulis")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	u2, err := proses("ulang (idempoten)")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if u2 != 0 {
		fmt.Fprintf(os.Stderr, "IDEMPOTEN GAGAL: ulang mengubah %d baris (harus 0)\n", u2)
		os.Exit(1)
	}
	fmt.Printf("VERIFIKASI OK · tulis=%d · idempoten ulang=0 · ojs_url journals TIDAK disentuh (K1)\n", u1)
}
