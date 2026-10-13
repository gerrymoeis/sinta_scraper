// Package ojs = logika murni Fase 3 stage `ojs` (doc 42 §7 + §11 — aturan
// LOCKED dari probe P1–P3, 9 Okt 2026). Pure logic tanpa IO (db/HTTP) supaya
// gampang diuji — driver/pemanggilan HTTP ada di F3.3 (stage wiring), pola
// sama dgn internal/garuda (client.go/stage.go pemanggil, parse/match murni).
package ojs

// Flag fase utk phase2_progress.flags (doc 42 §11.4 — SetPhase2 pola garuda).
// SOURCE_RESOLVED & NO_FEE_PAGE di-set driver F3.3 (butir keputusan stage);
// sisanya dihasilkan Resolve() dari hasil GET.
const (
	FlagSourceResolved     = "SOURCE_RESOLVED"
	FlagRedirectHostChange = "REDIRECT_HOST_CHANGE"
	FlagRedirectOffsite    = "REDIRECT_OFFSITE"
	FlagWAFBlocked         = "WAF_BLOCKED"
	FlagUnreachable        = "UNREACHABLE"
	FlagServerDown         = "SERVER_DOWN"
	FlagStaleURL           = "STALE_URL"
	FlagNoFeePage          = "NO_FEE_PAGE"
	// FlagPolicyPartial = terminal dgn catatan jujur (Opsi 2 approve 13 Okt):
	// canonical ketemu + ≥1 link policy sukses + ≥1 link network PERSISTEN
	// (≥2 run berturut — bukti journal_policy sebelum GET). Fees mungkin
	// terlewat dari link yang gagal — bila fees nil, NO_FEE_PAGE TIDAK ditulis
	// (sebagian halaman belum terevaluasi).
	FlagPolicyPartial = "POLICY_PARTIAL"
)
