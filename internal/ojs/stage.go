package ojs

import (
	"encoding/json"
	"fmt"
	"time"

	"sinta-scraper/internal/storage"
)

// ==== stage.go — driver stage ojs (doc 42 §4 F3.3 — approve user 13 Okt 2026) ====
//
// GET nyata pola stage garuda: target dari OjsTargets (journal_urls +
// garuda_home_url), retry 1× transien (429/5xx/network — §7.1 butir 7; client
// GetPlain tanpa retry by design), Resolve → canonical, lalu (bila canonical
// ketemu) Detect + FindPolicyLinks + ExtractFees HANYA di halaman policy
// (LOCKED §7.3 — home body TIDAK dievaluasi utk fee).
//
// Terminal (SOURCE_RESOLVED) = canonical ketemu DAN semua GET policy selesai
// (sukses/non-transien). Canonical kosong (WAF/down/404 semua) = flag jujur
// TANPA terminal → rerun mencoba lagi (§7.1 butir 5 — jangan blokir state).
// Policy GET gagal jaringan BELUM persisten = canonical ada tapi fees mungkin
// terlewat → TANPA terminal (validitas #1 > pagu GET #2/#3; rerun mengulang).
// Kecuali Opsi 2 (approve 13 Okt): persisten ≥2 run + ≥1 link lain respons
// final → terminal jujur dgn flag POLICY_PARTIAL (lihat cabang processJournal).
//
// Opsi B (approve user 13 Okt 2026): murni network PERSISTEN (≥2 run berturut
// semua kandidat status 0 — bukti journal_urls: checked_at terisi + http_status
// NULL sebelum GET run ini) = kategori FINAL jujur `Unreachable` utk Verified,
// TETAP tanpa SOURCE_RESOLVED → run berikutnya tetap mencoba ulang (pemulihan
// otomatis bila host bangkit; tanpa GET tambahan).

// PlainGetter = kontrak GET tunggal tanpa retry (dipenuhi garuda.Client
// GetPlain — driver ojs TIDAK import garuda; main.go menyuntik client).
type PlainGetter interface {
	GetPlain(rawURL, referer string) ([]byte, int, string, error)
}

// Pagu driver (default bila config ≤0): kandidat per jurnal (P1 butir 1 —
// semua source; bukti 1 sumber bisa salah/stub/mati) & link policy per jurnal
// (fallback §7.3 butir 5 — 2 GET/jurnal tipikal: home + 1 policy; 3 = buffer
// theme multi-halaman bukti P3 j681).
const (
	DefaultMaxCand   = 4
	DefaultMaxPolicy = 3
)

// StageConfig = konfigurasi stage ojs (diisi main.go).
type StageConfig struct {
	Ranks      []int // scope sinta_rank (ParseRankSet; nil = semua)
	Refresh    bool  // abaikan resume SOURCE_RESOLVED
	Client     PlainGetter
	MaxCand    int           // kandidat/jurnal (≤0 → DefaultMaxCand)
	MaxPolicy  int           // link policy/jurnal (≤0 → DefaultMaxPolicy)
	RetryDelay time.Duration // jeda antar retry transien (0 = tanpa jeda — test)
	Logf       func(format string, args ...any)
}

// StageReport = ringkasan jujur (utk log + verifikasi exit code).
type StageReport struct {
	Target        int
	Skip          int // resume (flag SOURCE_RESOLVED lama)
	NoCandidate   int // tanpa kandidat URL (0 GET — tak ada yg bisa diproses)
	Resolved      int // canonical ketemu + SOURCE_RESOLVED terminal
	NoCanonical   int // canonical kosong — minimal 1 respons final (403/404/5xx), flags jujur, TANPA terminal (rerun)
	Err           int // canonical kosong murni network (semua status 0 — transient, pola garuda ViewErr; rerun wajib)
	Unreachable   int // murni network PERSISTEN ≥2 run (bukti journal_urls sebelum GET — opsi B approve 13 Okt): FINAL jujur utk Verified, TANPA SOURCE_RESOLVED (run depan tetap mencoba ulang)
	FeesFound     int // journals.author_fees terisi
	NoFeePage     int // canonical ada, fees nil di semua halaman policy (flag jujur)
	PolicyGET     int // GET halaman policy
	PolicyErr     int // jurnal dgn policy GET network-gagal (BELUM terminal — rerun)
	PolicyPartial int // terminal POLICY_PARTIAL persisten (Opsi 2 — fees mungkin terlewat)

	Verified  bool
	VerifyMsg string
}

// RunOJSStage = orkestrator stage ojs (F3.3). Error hanya utk kegagalan fatal
// (db/query/target kosong); kegagalan per-jurnal masuk report → Verified=false
// → exit code jujur dari main; resume mengulang hanya yang belum terminal.
func RunOJSStage(store *storage.Store, cfg StageConfig) (*StageReport, error) {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if cfg.Client == nil {
		return nil, fmt.Errorf("ojs: StageConfig.Client wajib (PlainGetter)")
	}
	maxCand := cfg.MaxCand
	if maxCand <= 0 {
		maxCand = DefaultMaxCand
	}
	maxPolicy := cfg.MaxPolicy
	if maxPolicy <= 0 {
		maxPolicy = DefaultMaxPolicy
	}

	rep := &StageReport{}
	targets, err := store.OjsTargets(cfg.Ranks)
	if err != nil {
		return nil, fmt.Errorf("baca target: %w", err)
	}
	rep.Target = len(targets)
	if len(targets) == 0 {
		return nil, fmt.Errorf("target kosong — tidak ada baris journals utk scope -rank ini (cek -rank & isi db)")
	}
	logf("[ojs] target = %d baris (refresh=%v maxCand=%d maxPolicy=%d)",
		len(targets), cfg.Refresh, maxCand, maxPolicy)

	for i := range targets {
		t := &targets[i]
		if !cfg.Refresh && t.Resolved {
			rep.Skip++
			continue
		}
		if err := processJournal(store, cfg, rep, t, maxCand, maxPolicy, logf); err != nil {
			return nil, err // fatal db — bukan kegagalan per-jurnal
		}
	}

	if err := stageVerify(store, cfg, rep, logf); err != nil {
		return nil, err
	}
	return rep, nil
}

// processJournal = satu jurnal: GET kandidat → Resolve → (canonical) Detect +
// policy + fees → SetPhase2. Kategori report: NoCandidate | NoCanonical |
// Resolved (terminal) | PolicyErr (canonical ada, belum terminal).
func processJournal(store *storage.Store, cfg StageConfig, rep *StageReport,
	t *storage.OjsTarget, maxCand, maxPolicy int, logf func(string, ...any)) error {

	// Kandidat: journal_urls + garuda_home_url (dedupe; §7.1 butir 1 — semua
	// source; home dari Garuda = kandidat tambahan bila belum di journal_urls).
	cands := dedupeCandidates(t, maxCand)
	if len(cands) == 0 {
		rep.NoCandidate++
		logf("[ojs] j%d %q: TANPA KANDIDAT URL (jujur — 0 GET; tak bisa diproses)", t.ID, t.Name)
		return nil
	}

	// GET tiap kandidat (retry 1× transien) + upsert hasil jujur ke
	// journal_urls (riwayat E7g dipertahankan; st=0/network → http_status
	// NULL + checked_at terisi — konvensi urls.go).
	gcands := make([]Candidate, 0, len(cands))
	bodies := make(map[string][]byte, len(cands))
	finals := make(map[string]string, len(cands))
	statuses := make(map[string]int, len(cands))
	for _, c := range cands {
		body, st, final, _ := getRetry(cfg, c.URL, "")
		bodies[c.URL], finals[c.URL], statuses[c.URL] = body, final, st
		gcands = append(gcands, Candidate{
			URL: c.URL, Source: c.Source, Status: st,
			FinalURL: final, BodyLen: len(body),
		})
		var stPtr *int
		if st != 0 {
			s := st
			stPtr = &s
		}
		if _, err := store.UpsertJournalURL(t.ID, c.URL, "ojs", c.Source,
			stPtr, final, nowRFC3339()); err != nil {
			return fmt.Errorf("ojs: upsert kandidat j%d %s: %w", t.ID, c.URL, err)
		}
	}

	out := Resolve(gcands)

	// Canonical kosong → flag jujur TANPA terminal (rerun mencoba lagi).
	if out.CanonicalURL == "" {
		// Murni network (semua status 0 — transient, pola garuda ViewErr).
		allNetwork := true
		for _, c := range gcands {
			if c.Status != 0 {
				allNetwork = false
				break
			}
		}
		if allNetwork && prevRunAllZero(cands) {
			// Opsi B (approve 13 Okt): persisten ≥2 run status-0 berturut =
			// kategori FINAL jujur Unreachable utk Verified. TANPA terminal
			// (SOURCE_RESOLVED) — run depan tetap mencoba ulang utk pemulihan.
			rep.Unreachable++
			if err := store.SetPhase2(t.ID, "", out.Flags, nil,
				"murni network persisten ≥2 run (Unreachable final jujur — run depan tetap mencoba ulang)"); err != nil {
				return fmt.Errorf("ojs: flag j%d: %w", t.ID, err)
			}
			logf("[ojs] j%d %q: canonical GAGAL — UNREACHABLE PERSISTEN (%d kandidat status-0 ≥2 run berturut; flags=%v) — kategori final jujur utk Verified, TANPA terminal (run depan mencoba lagi)",
				t.ID, t.Name, len(cands), out.Flags)
			return nil
		}
		if allNetwork {
			rep.Err++ // transien (belum cukup bukti persistensi) → rerun wajib
		} else {
			rep.NoCanonical++ // minimal 1 respons final (403/404/5xx)
		}
		flags := out.Flags
		if err := store.SetPhase2(t.ID, "", flags, nil, ""); err != nil {
			return fmt.Errorf("ojs: flag j%d: %w", t.ID, err)
		}
		logf("[ojs] j%d %q: canonical GAGAL (%d kandidat; flags=%v network_murni=%v) — tanpa terminal, rerun mencoba lagi",
			t.ID, t.Name, len(cands), flags, allNetwork)
		return nil
	}

	// ---- canonical ketemu: mark + detect + policy + fees ----------------
	wURL := out.CanonicalURL
	wSt, wBody, wFinal := statuses[wURL], bodies[wURL], finals[wURL]

	if err := store.MarkCanonicalURL(t.ID, wURL, statusPtr(wSt), wFinal, nowRFC3339()); err != nil {
		return fmt.Errorf("ojs: canonical j%d: %w", t.ID, err)
	}
	det := Detect(wSt, wBody)
	if err := store.UpsertSourceProfile(storage.SourceProfile{
		JournalID: t.ID, Platform: det.Platform, Evidence: det.Evidence,
		Confidence: det.Confidence, OJSVersionHint: det.OJSVersion,
		CanonicalURL: wFinal,
	}); err != nil {
		return fmt.Errorf("ojs: profile j%d: %w", t.ID, err)
	}

	// FindPolicyLinks: base = final_url canonical (fallback asal bila "").
	base := wFinal
	if base == "" {
		base = wURL
	}
	links := FindPolicyLinks(base, wBody, maxPolicy)

	// GET tiap link policy (Referer = canonical — navigasi natural). ExtractFees
	// HANYA utk body 200 lolos link (LOCKED §7.3 butir 2 — home tak dievaluasi).
	// PolicyPrevMap dibaca SEBELUM GET (bukti persistensi — Opsi 2; jangan
	// ditimpa hasil run ini — pola OjsCand utk unreachable persisten).
	prevPol, err := store.PolicyPrevMap(t.ID)
	if err != nil {
		return fmt.Errorf("ojs: policy prev j%d: %w", t.ID, err)
	}
	var (
		rows         []storage.PolicyRow
		fees         *FeeEntry
		policyNetErr bool
		netFail      []string // URL policy dgn hasil network run ini
	)
	for _, l := range links {
		rep.PolicyGET++
		pbody, pst, _, perr := getRetry(cfg, l.URL, base)
		rows = append(rows, storage.PolicyRow{
			PolicyType: ClassifyPolicyType(l.URL),
			URL:        l.URL,
			HTTPStatus: pst,
		})
		if perr != nil || pst == 0 {
			policyNetErr = true // jaringan gagal setelah retry — fees mungkin di halaman ini
			netFail = append(netFail, l.URL)
			continue
		}
		if pst == 200 && fees == nil {
			fees = ExtractFees(l.URL, pbody) // pertama non-nil menang (skor desc)
		}
	}
	if len(rows) > 0 {
		if err := store.UpsertPolicies(t.ID, rows); err != nil {
			return fmt.Errorf("ojs: policy j%d: %w", t.ID, err)
		}
	}

	flags := out.Flags
	if policyNetErr {
		// Opsi 2 (approve 13 Okt 2026): SEMUA link yang gagal network run ini
		// juga network di run lalu (journal_policy: http_status 0 + checked_at
		// terisi) = PERSISTEN → terminal jujur dgn flag POLICY_PARTIAL. Syarat
		// tambahan: ≥1 link lain selesai non-network (respons final — fees
		// bila ada dari link 200 saja). All-network persisten = BELUM terminal
		// (nol halaman terbaca → fees mustahil; belum teramati di run mana pun).
		// Tanpa persetujuan ini: exit 1 permanen (keputusan LOCKED F3.3).
		persisten := len(netFail) > 0
		for _, u := range netFail {
			p, ok := prevPol[u]
			if !ok || p.HTTPStatus != 0 || p.CheckedAt == "" {
				persisten = false
				break
			}
		}
		adaResponsFinal := false
		for _, r := range rows {
			if r.HTTPStatus != 0 {
				adaResponsFinal = true
				break
			}
		}
		if persisten && adaResponsFinal {
			if err := simpanFees(store, t.ID, fees, rep); err != nil {
				return err
			}
			// fees nil → TANPA NO_FEE_PAGE (sebagian halaman belum terevaluasi
			// — jangan klaim "tidak ada fee page").
			flags = append(flags, FlagPolicyPartial, FlagSourceResolved)
			lastErr := fmt.Sprintf("%d link policy gagal jaringan persisten (POLICY_PARTIAL — fees mungkin terlewat dari link gagal)", len(netFail))
			if err := store.SetPhase2(t.ID, "", flags, nil, lastErr); err != nil {
				return fmt.Errorf("ojs: flag j%d: %w", t.ID, err)
			}
			rep.Resolved++
			rep.PolicyPartial++
			logf("[ojs] j%d %q: canonical=%q policy=%d fees=%v POLICY_PARTIAL (%d link persisten network) terminal",
				t.ID, t.Name, wURL, len(links), fees != nil, len(netFail))
			return nil
		}
		// Belum persisten (transien/ link baru) — BELUM terminal (tanpa
		// SOURCE_RESOLVED; tanpa NO_FEE_PAGE — fee page mungkin gagal di-GET).
		rep.PolicyErr++
		if err := store.SetPhase2(t.ID, "", flags, nil, "policy GET gagal jaringan (belum terminal)"); err != nil {
			return fmt.Errorf("ojs: flag j%d: %w", t.ID, err)
		}
		logf("[ojs] j%d %q: canonical=%q policy GET GAGAL jaringan (%d link; fees=%v) — BELUM terminal, rerun mengulang",
			t.ID, t.Name, wURL, len(links), fees != nil)
		return nil
	}

	// Terminal: canonical ketemu + semua policy GET selesai.
	if err := simpanFees(store, t.ID, fees, rep); err != nil {
		return err
	}
	if fees == nil {
		// Canonical ada, 0 link ATAU semua link sukses tapi ExtractFees nil →
		// NO_FEE_PAGE jujur (§7.3 butir 5 — jangan nebak).
		rep.NoFeePage++
		flags = append(flags, FlagNoFeePage)
	}
	flags = append(flags, FlagSourceResolved)
	if err := store.SetPhase2(t.ID, "", flags, nil, ""); err != nil {
		return fmt.Errorf("ojs: flag j%d: %w", t.ID, err)
	}
	rep.Resolved++
	logf("[ojs] j%d %q: canonical=%q platform=%s policy=%d fees=%v terminal",
		t.ID, t.Name, wURL, det.Platform, len(links), fees != nil)
	return nil
}

// simpanFees = tulis author_fees + provenance (dipanggil terminal normal &
// POLICY_PARTIAL — dedup tulis fees; rep.FeesFound dinaikkan bila fees nilai).
func simpanFees(store *storage.Store, journalID int64, fees *FeeEntry, rep *StageReport) error {
	if fees == nil {
		return nil
	}
	raw, err := json.Marshal(fees)
	if err != nil {
		return fmt.Errorf("ojs: marshal fees j%d: %w", journalID, err)
	}
	if _, err := store.SetAuthorFees(journalID, string(raw)); err != nil {
		return fmt.Errorf("ojs: fees j%d: %w", journalID, err)
	}
	if err := store.MergeProvenance(journalID, "journals_author_fees", storage.ProvEntry{
		Value: string(raw), Source: "ojs", Confidence: 1.0,
	}); err != nil {
		return fmt.Errorf("ojs: prov fees j%d: %w", journalID, err)
	}
	rep.FeesFound++
	return nil
}

// stageVerify = verifikasi jujur utk exit code (pola stageVerify garuda).
// Syarat Verified: (a) setiap target masuk kategori jujur (skip/no-candidate/
// resolved (termasuk **POLICY_PARTIAL persisten** — Opsi 2)/no-canonical/
// unreachable persisten — canonical-ketemu+policy-err & murni-network-belum-
// persisten = belum termasuk), (b) nol error network murni transien (rerun
// wajib), (c) nol policy GET error BELUM-terminal. Opsi B approve 13 Okt:
// Unreachable = FINAL jujur (bukti ≥2 run status-0 berturut) — TIDAK
// memblokir Verified; baris tetap di-GET ulang tiap run (pemulihan otomatis
// bila host bangkit → terminal normal run depan).
func stageVerify(store *storage.Store, cfg StageConfig, rep *StageReport, logf func(string, ...any)) error {
	terisi, err := store.CountSourceResolved(cfg.Ranks)
	if err != nil {
		return fmt.Errorf("verifikasi: %w", err)
	}
	done := rep.Skip + rep.NoCandidate + rep.Resolved + rep.NoCanonical + rep.Unreachable
	var masalah []string
	if done+rep.Err+rep.PolicyErr != rep.Target {
		masalah = append(masalah, fmt.Sprintf("kategori %d+err %d+policy_err %d ≠ target %d (bug hitung?)",
			done, rep.Err, rep.PolicyErr, rep.Target))
	}
	if terisi != rep.Skip+rep.Resolved {
		masalah = append(masalah, fmt.Sprintf("SOURCE_RESOLVED db=%d, report skip+resolved=%d (drift?)",
			terisi, rep.Skip+rep.Resolved))
	}
	if rep.Err > 0 {
		masalah = append(masalah, fmt.Sprintf("%d jurnal canonical gagal murni network (transien — rerun wajib)", rep.Err))
	}
	if rep.PolicyErr > 0 {
		masalah = append(masalah, fmt.Sprintf("%d jurnal policy GET gagal jaringan (BELUM terminal — rerun mengulang)", rep.PolicyErr))
	}
	if len(masalah) == 0 {
		rep.Verified = true
		rep.VerifyMsg = fmt.Sprintf("SOURCE_RESOLVED %d/%d | skip %d | tanpa kandidat %d | canonical gagal %d | unreachable persisten %d (final jujur) | policy_partial %d (persisten) | fees %d | no_fee_page %d",
			terisi, rep.Target, rep.Skip, rep.NoCandidate, rep.NoCanonical, rep.Unreachable, rep.PolicyPartial, rep.FeesFound, rep.NoFeePage)
	} else {
		rep.Verified = false
		rep.VerifyMsg = fmt.Sprintf("%d masalah: %v", len(masalah), masalah)
	}
	logf("[ojs] verifikasi: %s → VERIFIED=%v", rep.VerifyMsg, rep.Verified)
	return nil
}

// prevRunAllZero = SEMUA kandidat sudah pernah di-GET pada run SEBELUMNYA dan
// hasilnya murni network (checked_at terisi + http_status NULL — konvensi
// urls.go "dicek tapi jaringan gagal"). Bukti persistensi ≥2 run status-0
// berturut utk kategori Unreachable (opsi B approve 13 Okt). Kandidat baru
// (belum pernah di-GET) ATAU ada yang punya respons final → false (bukti
// belum cukup — tetap Err transien, rerun wajib).
func prevRunAllZero(cands []storage.OjsCand) bool {
	if len(cands) == 0 {
		return false
	}
	for _, c := range cands {
		if c.CheckedAt == "" || c.HTTPStatus != nil {
			return false
		}
	}
	return true
}

// dedupeCandidates = journal_urls + garuda_home_url (source "garuda"),
// dedupe per URL, cap maxCand (deterministik: urutan db + home di belakang).
func dedupeCandidates(t *storage.OjsTarget, maxCand int) []storage.OjsCand {
	seen := make(map[string]bool, len(t.Candidates)+1)
	out := make([]storage.OjsCand, 0, len(t.Candidates)+1)
	for _, c := range t.Candidates {
		if c.URL == "" || seen[c.URL] {
			continue
		}
		seen[c.URL] = true
		out = append(out, c)
	}
	if t.HomeURL != "" && !seen[t.HomeURL] {
		out = append(out, storage.OjsCand{URL: t.HomeURL, Source: "garuda"})
	}
	if maxCand > 0 && len(out) > maxCand {
		out = out[:maxCand]
	}
	return out
}

// getRetry = GET tunggal + retry 1× transien (status 0/429/5xx/network —
// §7.1 butir 7; GetPlain tanpa retry by design pagu per percobaan).
func getRetry(cfg StageConfig, rawURL, referer string) ([]byte, int, string, error) {
	body, st, final, err := cfg.Client.GetPlain(rawURL, referer)
	if err == nil && !transien(st) {
		return body, st, final, nil
	}
	if cfg.RetryDelay > 0 {
		time.Sleep(cfg.RetryDelay)
	}
	body2, st2, final2, err2 := cfg.Client.GetPlain(rawURL, referer)
	if err2 == nil && !transien(st2) {
		return body2, st2, final2, nil
	}
	// Retry gagal juga — kembalikan hasil terakhir apa adanya (st/network
	// jujur; err2 utk body-read gagal dgn status tetap).
	if err2 != nil {
		return body2, st2, final2, err2
	}
	return body2, st2, final2, nil
}

// transien = status layak retry 1× (429 WAF-limit & 5xx server; 403 WAF &
// 404 = final — §7.1 butir 7: retry transien, bukan semua).
func transien(st int) bool {
	return st == 0 || st == 429 || st >= 500
}

func statusPtr(st int) *int {
	if st == 0 {
		return nil // tak ada respons (network) — konvensi urls.go
	}
	s := st
	return &s
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
