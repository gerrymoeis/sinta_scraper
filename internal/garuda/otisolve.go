package garuda

// Probe A (approve user 4 Okt 2026 — doc 35 §6, laporan "pure-Go lintas-OS"):
// solver challenge Cloudflare murni-Go, TANPA Python/nodriver — chromedp
// menjalankan browser Chromium ADAPTIF bawaan-OS (approve #4: Chrome bila ada,
// lalu Edge, lalu Chromium; bila tak ada = error jujur) dgn:
//   - daftar flag MIRROR nodriver (fakta nodriver/core/config.py:116-129,174-200)
//   - TANPA --enable-automation (pembeda chromedp-gagal 2× vs nodriver-sukses;
//     flag itu hanya ada di chromedp.DefaultExecAllocatorOptions — TIDAK kita pakai)
//   - port debug SPESIFIK (≠0) + transport PORT+WS via remote.WebSocket —
//     fakta MDN/Chromium issue 40685960: port=0 / pipe → navigator.webdriver
//     =true (widget Turnstile tolak mount); port spesifik = false. Inilah
//     pembeda chromedp-gagal vs nodriver-sukses (probe 4 Okt), BUKAN flag
//   - headed (nodriver headless = GAGAL, headed = SOLVED — doc 34 §6c)
//   - strategi tunggu mirror solve_cf.py (judul bersih + settle 1,5 dtk)
// BrowserSolve = drop-in solverFunc (kontrak identik runSolver: cookie, ua, err)
// — integrasi ke Hybrid = satu baris bila probe menang.

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	remote "github.com/chromedp/chromedp/remote"
)

const (
	solveTimeout = 45 * time.Second // nodriver solve = 7,5 s; 45 s = margin 6×
	browserEnv   = "SINTA_BROWSER"  // override path browser (utk probe ronde 2)
)

// FindBrowser = browser Chromium ADAPTIF (approve user 4 Okt #4: "kalau ada
// chrome boleh chrome, kalau hanya ada edge ya edge, kalau bisa chromium ya
// chromium; tak ada = jujur bilang tidak bisa"). Env SINTA_BROWSER (path
// absolut) menang bila di-set. Error memuat daftar kandidat yg dicek.
func FindBrowser() (string, error) {
	if p := os.Getenv(browserEnv); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s=%q tidak bisa diakses: %w", browserEnv, p, err)
		}
		return p, nil
	}
	var checked []string
	for _, cand := range browserCandidates(runtime.GOOS) {
		checked = append(checked, cand)
		if filepath.IsAbs(cand) || strings.ContainsAny(cand, `/\`) {
			if _, err := os.Stat(cand); err == nil {
				return cand, nil
			}
			continue
		}
		if p, err := exec.LookPath(cand); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("browser Chromium (Chrome/Edge/Chromium) tak ditemukan utk GOOS=%s — dicek: %s; install salah satu atau set %s=<path> (tanpa browser Chromium, solve challenge = TIDAK BISA)",
		runtime.GOOS, strings.Join(checked, ", "), browserEnv)
}

// browserCandidates = kandidat path/nama per GOOS, urut ADAPTIF (approve #4):
// **Chrome → Edge → Chromium** per OS (siapa pun yg ada dipakai; dua-duanya
// Chromium + CDP). Safari & Firefox mustahil — tak punya CDP (fakta doc 35 §6).
func browserCandidates(goos string) []string {
	switch goos {
	case "windows":
		var chrome, edge []string
		for _, env := range []string{"PROGRAMFILES", "PROGRAMFILES(X86)", "PROGRAMW6432", "LOCALAPPDATA"} {
			base := os.Getenv(env)
			if base == "" {
				continue
			}
			chrome = append(chrome, filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"))
			edge = append(edge, filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
		chrome = append(chrome, "chrome.exe")
		edge = append(edge, "msedge.exe")
		return append(chrome, append(edge, "chromium.exe", "chromium")...)
	case "darwin":
		home := os.Getenv("HOME")
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			filepath.Join(home, "Applications", "Google Chrome.app", "Contents", "MacOS", "Google Chrome"),
			filepath.Join(home, "Applications", "Microsoft Edge.app", "Contents", "MacOS", "Microsoft Edge"),
			filepath.Join(home, "Applications", "Chromium.app", "Contents", "MacOS", "Chromium"),
		}
	default: // linux, *bsd, dll
		return []string{
			"google-chrome", "google-chrome-stable", "google-chrome-beta", "google-chrome-unstable",
			"microsoft-edge", "microsoft-edge-stable",
			"chromium", "chromium-browser",
		}
	}
}

// otiFlag = satu flag peluncuran. Value "" = switch polos (--nama);
// Value terisi = --nama=nilai.
type otiFlag struct {
	Name  string
	Value string
}

// otiFlags = MIRROR PERSIS nodriver Config._default_browser_args + __call__
// (fakta config.py:116-129,174-200; nodriver mengurutkan argumen — urutan di
// sini bebas). TIDAK ada --enable-automation & --headless di sini — dua-duanya
// sinyal deteksi / membuat gagal (doc 34 §6c baris 4-7).
func otiFlags() []otiFlag {
	return []otiFlag{
		{Name: "remote-allow-origins", Value: "*"},
		{Name: "no-first-run"},
		{Name: "no-service-autorun"},
		{Name: "no-default-browser-check"},
		{Name: "homepage", Value: "about:blank"},
		{Name: "no-pings"},
		{Name: "password-store", Value: "basic"},
		{Name: "disable-infobars"},
		{Name: "disable-breakpad"},
		{Name: "disable-dev-shm-usage"},
		{Name: "disable-session-crashed-bubble"},
		{Name: "disable-search-engine-choice-screen"},
		{Name: "disable-features", Value: "IsolateOrigins,site-per-process"},
	}
}

// otiAllocOpts = opsi chromedp: ExecPath browser + flag mirror nodriver +
// remote.WebSocket + port debug SPESIFIK (≠0). FAKTA KUNCI (MDN + Chromium
// issue 40685960): --remote-debugging-port=0 / --remote-debugging-pipe MEMBUAT
// navigator.webdriver=true; port spesifik = false — itulah pembeda kenapa
// nodriver (port acak spesifik) lolos & chromedp default (pipe/port-0) gagal
// (fakta probe 4 Okt: wd true/true/false). TANPA chromedp.DefaultExecAllocatorOptions
// (yg memasang enable-automation + headless).
func otiAllocOpts(execPath string) ([]chromedp.ExecAllocatorOption, error) {
	port, err := freeDebugPort()
	if err != nil {
		return nil, fmt.Errorf("port debug: %w", err)
	}
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(execPath),
		remote.WebSocket,
		chromedp.Flag("remote-debugging-port", port),
	}
	for _, f := range otiFlags() {
		if f.Value == "" {
			opts = append(opts, chromedp.Flag(f.Name, true))
		} else {
			opts = append(opts, chromedp.Flag(f.Name, f.Value))
		}
	}
	return opts, nil
}

// freeDebugPort = port TCP bebas 127.0.0.1 (paritas nodriver util.free_port).
// WAJIB spesifik ≠ 0 — lihat otiAllocOpts. Risiko race port dicuri diterima
// (nodriver pun sama).
func freeDebugPort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}

// BrowserSolve = solve challenge Cloudflare 1 URL via browser bawaan-OS →
// cookie (semua cookie host target, header "k=v; k2=v2") + UA browser sungguhan.
// Gagal = error (kontrak identik runSolver — drop-in solverFunc).
func BrowserSolve(rawURL string) (string, string, error) {
	exe, err := FindBrowser()
	if err != nil {
		return "", "", err
	}
	allocOpts, err := otiAllocOpts(exe)
	if err != nil {
		return "", "", err
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()
	ctx, cancel := context.WithTimeout(browserCtx, solveTimeout)
	defer cancel()

	host := hostOf(rawURL)
	diag := os.Getenv("SINTA_OTI_DIAG") != ""
	var cookie, title string
	var cleanAt time.Time
	tick := 0
	wait := chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		for {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("solve timeout %s (%s) — judul terakhir: %q", host, err, title)
			}
			tick++
			// cookie cf_clearance = kebenaran mutlak (python: baca via CDP juga)
			res, err := cdp.Call(ctx, t, network.GetCookies, network.GetCookiesParams{})
			if err == nil {
				if s := joinCookies(res.Cookies, host); s != "" {
					cookie = s
					return nil
				}
			}
			// mirror solve_cf.py: judul bersih ("just a moment" hilang) +
			// settle 1,5 dtk → baca cookie final
			if s, terr := chromedp.Evaluate[string](`document.title`)(ctx, t); terr == nil {
				title = s
			}
			challenged := strings.Contains(strings.ToLower(title), "just a moment")
			if title != "" && !challenged {
				if cleanAt.IsZero() {
					cleanAt = time.Now()
				}
				if time.Since(cleanAt) >= 1500*time.Millisecond {
					if res, err := cdp.Call(ctx, t, network.GetCookies, network.GetCookiesParams{}); err == nil {
						cookie = joinCookies(res.Cookies, host)
					}
					return nil
				}
			} else {
				cleanAt = time.Time{}
			}
			if diag && (tick%3 == 0 || tick%15 == 0) {
				if s, derr := chromedp.Evaluate[string](`JSON.stringify({t:document.title,v:document.visibilityState,f:document.hasFocus(),wd:navigator.webdriver,fr:window.frames.length})`)(ctx, t); derr == nil {
					fmt.Fprintf(os.Stderr, "otidiag t=%ds %s\n", tick*400/1000, s)
				}
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("solve timeout %s (%s) — judul terakhir: %q", host, ctx.Err(), title)
			case <-time.After(400 * time.Millisecond):
			}
		}
	})
	// navigasi (1 GET — reload pasca-solve ikut dihitung server) + tunggu cookie
	if err := chromedp.Do(ctx, chromedp.Navigate(rawURL), wait); err != nil {
		return "", "", err
	}
	ua, err := chromedp.Run(ctx, chromedp.Evaluate[string](`navigator.userAgent`))
	if err != nil {
		ua = ""
	}
	return cookie, ua, nil
}

// joinCookies = saring cookie milik host target → string header Cookie.
// Domain ".joiv.org" (titik depan) match "joiv.org"; cookie host lain dibuang.
func joinCookies(cks []*network.Cookie, host string) string {
	var parts []string
	for _, c := range cks {
		d := strings.TrimPrefix(c.Domain, ".")
		if d == "" || (d != host && !strings.HasSuffix(host, "."+d)) {
			continue
		}
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// WAFConfirm = GET konfirmasi probe: tls-client (profil Chrome_152) dgn
// cookie+UA hasil BrowserSolve — ekivalen bukti doc 34 §6b baris 8/9
// ("tls-client + cookie = 200"). Dipakai -otismoke.
func WAFConfirm(rawURL, cookie, ua string) (int, []byte, error) {
	f, err := newTLSFetcher()
	if err != nil {
		return 0, nil, err
	}
	return f.get(rawURL, DefaultReferer, ua, cookie)
}

// kontrak: BrowserSolve drop-in pengganti runSolver bila probe menang.
var _ solverFunc = BrowserSolve
