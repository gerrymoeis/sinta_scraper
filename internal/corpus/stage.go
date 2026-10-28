package corpus

// ==== stage.go — driver stage corpus PASS 1 (doc 43 §5.3 — spec approve
// user 14 Okt 2026; fase C3) ====
//
// Per jurnal: OAI ListRecords (resumable loop cap DefaultMaxPages) →
// FilterLatestVolume (client-side, 0 GET) → Crossref 1 GET (merge lisensi/
// subject per DOI) → derive (split abstrak · language · lisensi 4 kelas ·
// keyword author→crossref) → UpsertArticle. Kategori jujur pola volume:
// Skip/NoCanonical/OAIMatik/OAIEmpty/NoISSN/NoArticles/Partial/Err.
// Fallback landing OAI-mati = C3b TERPISAH (keputusan user — lihat doc 43).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"sinta-scraper/internal/storage"
)

// errOAINoRecords = noRecordsMatch (OAI hidup tapi 0 record utk scope) —
// kategori OAIEmpty (jujur, bukan OAI-matik).
var errOAINoRecords = errors.New("oai: noRecordsMatch (0 record untuk scope)")

// PlainGetter = kontrak GET tunggal tanpa retry (dipenuhi garuda.Client
// GetPlain — main.go menyuntik client; pola volume.StageConfig).
type PlainGetter interface {
	GetPlain(rawURL, referer string) ([]byte, int, string, error)
}

// SolverGetter = GET anti-WAF utk C3b tahap C (approve user 27 Okt 2026):
// garuda.Client.Get = std → bila 403 → Hybrid (tls-client Chrome_152 →
// challenge → BrowserSolve chromedp → cf_clearance per-host). Signature 3
// nilai (tanpa final URL) — berbeda dari GetPlain.
type SolverGetter interface {
	Get(rawURL, referer string) ([]byte, int, error)
}

// DefaultMaxPages = safety cap halaman resumption ListRecords per jurnal
// (OJS batch ~100; jurnal 12.000+ artikel = 120 halaman — O-C4 naik dari 40:
// audit G14 — 9 jurnal "OAI mati" sebenarnya cuma kelewat cap lama). Flag
// `-oai-max-pages` (main.go) meng-override per-run.
const DefaultMaxPages = 120

// StageConfig = konfigurasi stage corpus (diisi main.go).
type StageConfig struct {
	Ranks          []int
	Refresh        bool
	VolN           int     // jumlah volume terakhir utk scope (≤0 → 1; flag -vol-n)
	MaxPages       int     // cap halaman resumption OAI per jurnal (≤0 → DefaultMaxPages; flag -oai-max-pages)
	JournalIDs     []int64 // scope eksplisit (flag -journal, smoke C6): kosong = semua; ID wajib ketemu semua
	Client         PlainGetter
	Solver         SolverGetter  // C3b-C: fallback 403/468 → tls-client + BrowserSolve — HANYA bila SolverMode
	SolverMode     bool          // aktifkan jalur solver di getRetry (E7g: jalur normal TETAP GetPlain murni)
	RetryDelay     time.Duration // jeda antar retry transien (0 = test)
	CrossrefMailto string
	Logf           func(format string, args ...any)
	Now            time.Time // inject utk lisensi embargo (0 = time.Now — test deterministik)
}

// StageReport = ringkasan jujur (log + verifikasi exit code).
type StageReport struct {
	Target          int
	Skip            int // resume: jurnal dgn artikel complete
	NoCanonical     int // canonical kosong — 0 GET, tanpa baris
	OAIMatik        int // OAI gagal total (status/parse) — antrean C3b fallback landing
	OAIEmpty        int // noRecordsMatch / 0 record scope (jujur — jurnal tanpa artikel terbit)
	NoISSN          int // ISSN kosong — Crossref skip (OAI tetap jalan — pola Gap-E)
	NoArticles      int // OAI OK tapi 0 record lolos scope vol (jujur)
	Processed       int // jurnal dgn ≥1 baris complete
	Partial         int // jurnal dgn sebagian record gagal derive (resume retry)
	Err             int // murni network (rerun wajib — TANPA baris)
	OAIGet          int
	CrossrefGET     int
	DoajGET         int // GET DOAJ (C3b opsi A — fallback kedua setelah Crossref)
	APIFallback     int // jurnal terselamatkan API-merge (OAI gagal → crossref|doaj sbg primer)
	LandingGET      int // GET landing (C3b tahap B — homepage/TOC/WP-REST)
	LandingOK       int // jurnal terselamatkan landing (OAI+API kosong)
	SolverOK        int // jurnal terselamatkan solver chromedp (C3b-C — OAI via jalur anti-WAF)
	ArticlesWritten int
	EmbargoFound    int // artikel kena embargo Crossref (audit §5.5)
	FlagVolMiss     int // record ikut via flag vol_missing (audit scope)
	FlagVolFall     int // jurnal fallback tahun (audit scope)

	Verified  bool
	VerifyMsg string
}

// RunCorpusStage = orkestrator pass 1. Error hanya kegagalan fatal
// (db/target kosong); kegagalan per-jurnal masuk report.
func RunCorpusStage(store *storage.Store, cfg StageConfig) (*StageReport, error) {
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if cfg.Client == nil {
		return nil, fmt.Errorf("corpus: StageConfig.Client wajib (PlainGetter)")
	}
	volN := cfg.VolN
	if volN < 1 {
		volN = 1
	}
	now := cfg.Now
	if now.IsZero() {
		now = time.Now()
	}

	rep := &StageReport{}
	targets, err := store.CorpusTargets(cfg.Ranks)
	if err != nil {
		return nil, fmt.Errorf("baca target: %w", err)
	}
	if len(cfg.JournalIDs) > 0 {
		if targets, err = filterCorpusTargets(targets, cfg.JournalIDs); err != nil {
			return nil, err
		}
	}
	rep.Target = len(targets)
	if len(targets) == 0 {
		return nil, fmt.Errorf("target kosong — tidak ada baris journals utk scope -rank/-journal ini")
	}
	logf("[corpus] target = %d baris (refresh=%v volN=%d scopeJurnal=%d)", len(targets), cfg.Refresh, volN, len(cfg.JournalIDs))

	for i := range targets {
		t := &targets[i]
		if !cfg.Refresh && t.Complete {
			rep.Skip++
			continue
		}
		if err := processCorpusJournal(store, cfg, rep, t, volN, now, logf); err != nil {
			return nil, err // fatal db — bukan kegagalan per-jurnal
		}
	}

	stageVerify(rep, logf)
	return rep, nil
}

// filterCorpusTargets = scope -journal (smoke C6): semua ID wajib ketemu
// (typo = error hening-harus-nyala; dedupe otomatis via map) — bukan
// "sebagian yang ada dipakai" (validitas: salah ID jangan sunyi).
func filterCorpusTargets(targets []storage.CorpusTarget, ids []int64) ([]storage.CorpusTarget, error) {
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make([]storage.CorpusTarget, 0, len(want))
	found := make(map[int64]bool, len(want))
	for i := range targets {
		if want[targets[i].ID] {
			out = append(out, targets[i])
			found[targets[i].ID] = true
		}
	}
	var missing []int64
	for _, id := range ids {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("corpus: -journal ID %v tak ditemukan di target (cek typo / scope -rank)", missing)
	}
	return out, nil
}

// processCorpusJournal = 1 jurnal: OAI loop → filter vol → Crossref →
// derive per record → tulis.
func processCorpusJournal(store *storage.Store, cfg StageConfig, rep *StageReport,
	t *storage.CorpusTarget, volN int, now time.Time, logf func(string, ...any)) error {

	// ── Basis OAI (prioritas E5 — TEMUAN G1 fix, doc 34 §4): link RESMI
	// garuda_oai_url (252 terisi — 113/252 beda dari canonical+/oai) dulu;
	// fallback pola lama canonical+"/oai" (C3). Keduanya kosong = NoCanonical
	// jujur (0 GET) — jurnal WAF dgn link resmi tetap diproses.
	oaiBase := strings.TrimRight(t.OAIResmi, "/")
	if oaiBase == "" {
		if t.Canonical == "" {
			rep.NoCanonical++
			logf("[corpus] j%d %q: TANPA KANONIK & TANPA LINK RESMI (jujur — 0 GET)", t.ID, t.Name)
			return nil
		}
		oaiBase = strings.TrimRight(t.Canonical, "/") + "/oai"
	}

	// ── OAI ListRecords (primer, E5) — loop resumption ──
	works := CrossrefWorks{Items: map[string]CrossrefItem{}}
	origin := storage.ArticleSourceOAI // asal artikel: oai | crossref | doaj
	recs, oaiErr := fetchListRecords(cfg, rep, oaiBase)
	if oaiErr != nil {
		if errors.Is(oaiErr, errOAINoRecords) {
			rep.OAIEmpty++
			logf("[corpus] j%d %q: OAI 0 record (noRecordsMatch — jurnal tanpa artikel terbit)", t.ID, t.Name)
			return nil
		}
		// ── C3b opsi A (approve 27 Okt 2026): API-merge sbg PRIMER ──
		// WAF 403/468 · 404 · DNS mati → Crossref → DOAJ → bila keduanya
		// kosong → LANDING (C3b tahap B) → tetap OAI MATI (jujur).
		apiRecs, apiWorks, apiSrc, ferr := fetchAPIFallback(cfg, rep, t, now)
		if ferr != nil {
			// ── C3b tahap B (approve 27 Okt): landing resmi 2 jurnal khusus ──
			landRecs, lerr := fetchLandingFallback(cfg, rep, t)
			if lerr != nil {
				// ── C3b tahap C (approve 27 Okt): SOLVER chromedp ──
				// 2 jurnal WAF tanpa API apa pun & bukan host landing
				// (j668 IJIMS · j692 Kukila): GET ulang OAI lewat jalur
				// anti-WAF (tls-client → BrowserSolve) — OAI ASLI kembali
				// jadi primer (provenance oai penuh). noRecordsMatch =
				// jurnal kosong (OAIEmpty), bukan mati.
				if cfg.Solver != nil {
					cfgS := cfg
					cfgS.SolverMode = true // 403/468 → Solver.Get di getRetry
					recsS, serr := fetchListRecords(cfgS, rep, oaiBase)
					switch {
					case serr == nil && len(recsS) > 0:
						recs, oaiErr = recsS, nil
						rep.SolverOK++
						logf("[corpus] j%d %q: API+landing kosong → SOLVER chromedp: OAI OK — %d record (primer)",
							t.ID, t.Name, len(recs))
					case errors.Is(serr, errOAINoRecords):
						rep.OAIEmpty++
						logf("[corpus] j%d %q: solver OK tapi OAI 0 record (noRecordsMatch — jurnal kosong)", t.ID, t.Name)
						return nil
					default:
						logf("[corpus] j%d %q: solver chromedp ikut gagal (%v)", t.ID, t.Name, serr)
					}
				}
				if oaiErr != nil {
					rep.OAIMatik++
					logf("[corpus] j%d %q: OAI MATI (%v) + API kosong (%v) + landing gagal (%v) — jujur tanpa baris",
						t.ID, t.Name, oaiErr, ferr, lerr)
					return nil
				}
			} else {
				rep.LandingOK++
				recs, origin = landRecs, storage.ArticleSourceLand
				logf("[corpus] j%d %q: OAI gagal (%v) → API kosong → LANDING: %d record (primer)",
					t.ID, t.Name, oaiErr, len(recs))
			}
		} else {
			rep.APIFallback++
			recs, works, origin = apiRecs, apiWorks, apiSrc
			logf("[corpus] j%d %q: OAI gagal (%v) → API-merge %s: %d record (primer)",
				t.ID, t.Name, oaiErr, origin, len(recs))
		}
	}
	if len(recs) == 0 {
		rep.OAIEmpty++
		logf("[corpus] j%d %q: OAI 0 record (kosong — jujur)", t.ID, t.Name)
		return nil
	}

	// ── Guard identitas (G16): endpoint aggregate = lintas-jurnal ──
	if own, dropped := FilterOwnRecords(recs, t.Name); dropped > 0 {
		logf("[corpus] j%d %q: guard identitas — %d record asing dibuang (endpoint aggregate), %d milik tersisa",
			t.ID, t.Name, dropped, len(own))
		recs = own
	}

	// ── Filter volume terakhir (client-side, 0 GET) ──
	f := FilterLatestVolume(recs, volN)
	if f.Flag == "vol_fallback" {
		rep.FlagVolFall++
	}
	if len(f.Picked) == 0 {
		rep.NoArticles++
		logf("[corpus] j%d %q: scope vol-%d 0 record (flag=%s — jujur)", t.ID, t.Name, volN, f.Flag)
		return nil
	}

	// ── Crossref 1 GET (ISSN; kosong = skip jujur — pola Gap-E volume) ──
	// HANYA jalur OAI: jalur API-merge sudah membawa works dari GET primer
	// (satu GET diparse dua fungsi — tanpa GET ganda).
	if origin == storage.ArticleSourceOAI {
		issn := strings.ReplaceAll(firstNonEmpty(t.ISSN, t.EISSN), "-", "") // pola volume stage
		if issn == "" {
			rep.NoISSN++
		} else {
			body, st, _, err := getRetry(cfg, crossrefWorksURL(issn, cfg.CrossrefMailto), "")
			rep.CrossrefGET++
			if err != nil || st != 200 {
				logf("[corpus] j%d %q: crossref status=%d err=%v (artikel tetap — lisensi dari OAI)", t.ID, t.Name, st, err)
			} else if w, perr := ParseCrossrefWorks(body, now); perr != nil {
				logf("[corpus] j%d %q: crossref parse: %v", t.ID, t.Name, perr)
			} else {
				works = w
			}
		}
	}

	// ── Derive per record → tulis ──
	nWritten, nPartial := 0, 0
	for _, rec := range f.Picked {
		row, ok, msg := deriveArticle(t, rec, works, f, volN, origin)
		if !ok {
			nPartial++ // record gagal derive (jujur — tanpa baris; resume retry)
			logf("[corpus] j%d %q: record %q GAGAL DERIVE — %s", t.ID, t.Name, rec.Identifier, msg)
			continue
		}
		if err := store.UpsertArticle(row); err != nil {
			return err
		}
		nWritten++
		if strings.Contains(row.Provenance, `"embargo":"embargo"`) {
			rep.EmbargoFound++
		}
	}
	rep.ArticlesWritten += nWritten
	if f.Flag == "vol_missing" {
		rep.FlagVolMiss += nWritten
	}
	switch {
	case nWritten > 0 && nPartial == 0:
		rep.Processed++
	case nWritten > 0:
		rep.Partial++
	case nPartial > 0:
		rep.Err++ // semua record gagal derive = jurnal kosong penuh — rerun
	default:
		rep.Err++
	}
	logf("[corpus] j%d %q: vol=%d year=%d flag=%q record=%d tulis=%d partial=%d crossref=%d",
		t.ID, t.Name, f.Vol, f.Year, f.Flag, len(f.Picked), nWritten, nPartial, len(works.Items))
	return nil
}

// fetchListRecords = loop ListRecords?metadataPrefix=oai_dc + resumptionToken
// (cap cfg.MaxPages / DefaultMaxPages). Return semua record (termasuk deleted —
// FilterLatestVolume yang buang). OAI mati/parse-err = error bernama (caller
// kategori OAIMatik). Body disanitize (O-C4) sebelum parse — server OAI riil
// kirim U+0000/U+0002 di tengah XML valid (audit G14: 16 jurnal).
// Fallback http→https (C6-D): 8 dari 45 jurnal "server-side" = server sudah
// hanya layani https (http ditutup/404/5xx) — cek HANYA di halaman-1 & saat
// gagal (0 GET tambahan bila http normal).
func fetchListRecords(cfg StageConfig, rep *StageReport, oaiBase string) ([]OAIRecord, error) {
	maxPages := cfg.MaxPages
	if maxPages <= 0 {
		maxPages = DefaultMaxPages
	}
	var all []OAIRecord
	next := oaiBase + "?verb=ListRecords&metadataPrefix=oai_dc"
	for page := 0; page < maxPages; page++ {
		body, st, _, err := getRetry(cfg, next, "")
		rep.OAIGet++
		// ── fallback https (C6-D riset 25 Okt): halaman-1 gagal + basis masih
		// http → ulang sekali dgn https; selanjutnya token lanjut di scheme yg
		// terbukti jalan. Gagal tetap = jujur OAI mati (tanpa loop upgrade).
		if page == 0 && (err != nil || st != 200) && strings.HasPrefix(oaiBase, "http://") {
			oaiBase = "https://" + strings.TrimPrefix(oaiBase, "http://")
			next = oaiBase + "?verb=ListRecords&metadataPrefix=oai_dc"
			if cfg.Logf != nil {
				cfg.Logf("[corpus] OAI http→https (http gagal status=%d err=%v): %s", st, err, oaiBase)
			}
			body, st, _, err = getRetry(cfg, next, "")
			rep.OAIGet++
		}
		if err != nil || st != 200 {
			return nil, fmt.Errorf("GET %s status=%d err=%v", next, st, err)
		}
		clean := string(sanitizeOAIBody(body))
		recs, token, perr := ParseListRecords(clean)
		if perr != nil {
			if OAIErrorIs(clean, "noRecordsMatch") {
				return nil, errOAINoRecords
			}
			return nil, perr
		}
		all = append(all, recs...)
		if token == "" {
			return all, nil // halaman terakhir — normal
		}
		next = oaiBase + "?verb=ListRecords&resumptionToken=" + url.QueryEscape(token)
	}
	return nil, fmt.Errorf("resumption > %d halaman (cap safety)", maxPages)
}

// sanitizeOAIBody = buang karakter XML-illegal dari body OAI — O-C4 (audit
// G14: 16 dari 75 "OAI mati" = XML valid tapi server kirim byte NUL/U+0002 di
// tengah → xml.Unmarshal gagal; putaran ke-2: U+FFFE/U+FFFF noncharacter
// legal-range-nya #x20-#xD7FF & #xE000-#xFFFD). \t \n \r = legal → dipertahankan.
// Fast path: body bersih (mayoritas) = 1 scan, tanpa alokasi.
func sanitizeOAIBody(b []byte) []byte {
	for i := 0; i < len(b); i++ {
		if illegalXMLSeq(b, i) == 0 {
			continue
		}
		out := make([]byte, 0, len(b))
		out = append(out, b[:i]...)
		for j := i; j < len(b); {
			if n := illegalXMLSeq(b, j); n > 0 {
				j += n // buang n byte ilegal (1 utk control, 3 utk U+FFFE/FFFF)
				continue
			}
			out = append(out, b[j])
			j++
		}
		return out
	}
	return b
}

// illegalXMLSeq = panjang sequence XML-1.0 ilegal di posisi i (0 = legal):
//   - control < 0x20 kecuali \t \n \r (U+0000–U+0008 · U+000B · U+000C · U+000E–U+001F)
//   - U+FFFE / U+FFFF (noncharacter; UTF-8 = EF BF BE / EF BF BF)
func illegalXMLSeq(b []byte, i int) int {
	c := b[i]
	if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
		return 1
	}
	if c == 0xEF && i+2 < len(b) && b[i+1] == 0xBF && (b[i+2] == 0xBE || b[i+2] == 0xBF) {
		return 3
	}
	return 0
}

// deriveArticle = 1 record → ArticleRow (murni CPU). ok=false = record tak
// layak tulis (deleted / tanpa judul / tanpa URL) — msg utk log jujur.
// origin = asal record (oai|crossref|doaj — C3b opsi A): menentukan kolom
// source + provenance (bukan OAI → "oai" di provenance = "" + kunci "api";
// provenance = jejak jujur ke mana datangnya baris ini).
func deriveArticle(t *storage.CorpusTarget, rec OAIRecord, works CrossrefWorks,
	f VolFilter, volN int, origin string) (storage.ArticleRow, bool, string) {

	if rec.Deleted {
		return storage.ArticleRow{}, false, "header deleted"
	}
	title := strings.TrimSpace(rec.Title)
	if title == "" {
		return storage.ArticleRow{}, false, "tanpa judul"
	}
	doi := PickDOI(rec.Identifiers)
	articleURL := PickURL(rec.Identifiers)
	if articleURL == "" && doi != "" {
		articleURL = "https://doi.org/" + doi // fallback landing DOI (valid URL — jujur via provenance)
	}
	if articleURL == "" {
		return storage.ArticleRow{}, false, "tanpa URL & tanpa DOI"
	}

	absID, absEN := SplitAbstract(rec.Descriptions)
	lang := DetectLanguage(rec.Languages, absID, absEN,
		title+" "+strings.Join(rec.Subjects, " "))

	// Lisensi 4 kelas: Crossref dulu utk ber-DOI (§5.5) → OAI rights → unknown.
	status, licURL, src := LicenseUnknown, "", "unknown"
	provEmbargo := ""
	crItem, crHit := works.Items[strings.ToLower(doi)]
	if doi != "" && crHit {
		if s, u, ok := licenseFromCrossref(crItem); ok {
			status, licURL, src = s, u, "crossref"
			if crItem.Embargo {
				provEmbargo = "embargo"
			}
		}
	}
	if src == "unknown" {
		if s, u, ok := licenseFromOAI(rec.Rights); ok {
			status, licURL, src = s, u, "oai"
		}
	}

	// ==== 2 atribut C7 (doc 43 §6.x.8 D1) — turunan 0-GET dari bukti di atas.
	// license_type: URL Crossref/OAI → teks rights → (tak ketemu) unknown
	// jujur. oa_status: bukti per-artikel Crossref (embargo/free-to-read) dulu
	// → pernyataan OAI → unknown (CC-saja TIDAK dianggap gratis — hybrid).
	licType := LicenseTypeOf(licURL, rec.Rights...)
	oa, oaSrc := "", ""
	if doi != "" && crHit {
		oa, _ = OACrossref(crItem)
		if oa != "" {
			oaSrc = "crossref"
		}
	}
	if oa == "" {
		if s, ok := OAFromOAI(rec.Rights); ok {
			oa, oaSrc = s, "oai"
		}
	}
	if oa == "" {
		oa, oaSrc = OAUnknown, "unknown" // probe D3 — jangan ditebak
	}

	oaiRef := ""
	if origin == storage.ArticleSourceOAI {
		if t.Canonical != "" {
			oaiRef = t.Canonical + "/oai"
		} else {
			// canonical kosong (j668/j692 — tanpa baris source_profile):
			// jejak = URL OAI RESMI yang benar-benar dipakai (E5), bukan
			// "/oai" yang tak menunjuk apa-apa.
			oaiRef = t.OAIResmi
		}
	}
	provMap := map[string]any{
		"oai": oaiRef, "scope_vol": volN, "scope_flag": f.Flag,
		"license_src": src, "oa_src": oaSrc,
		"vol_parsed": f.Vol, "year": rec.RecordYear(),
		"embargo": provEmbargo, "subjects_oai": len(rec.Subjects),
	}
	switch origin {
	case storage.ArticleSourceCrossref, storage.ArticleSourceDOAJ:
		provMap["api"] = origin // jejak API publik
	case storage.ArticleSourceLand:
		// landing: kolom source='landing' + record_id=URL halaman resmi
		// sudah jadi jejak — TANPA kunci ganda (minimalisme).
	}
	prov, _ := json.Marshal(provMap)
	vol, no := ParseCitationVol(rec.Source) // nullable jujur: 0 = gagal parse
	return storage.ArticleRow{
		JournalID: t.ID, RecordID: rec.Identifier,
		VolumeNo: vol, IssueNo: no,
		Year:  rec.RecordYear(),
		Title: title, DOI: doi, ArticleURL: articleURL,
		AbstractID: absID, AbstractEN: absEN, Language: lang,
		KeywordsJSON:  KeywordJSON(rec.Subjects, crItem.Subject, lang),
		LicenseStatus: status, LicenseURL: licURL,
		OAStatus: oa, LicenseType: licType,
		Source:     origin,
		IsComplete: true,
		Provenance: string(prov),
	}, true, ""
}

// crossrefWorksURL = 1 GET works per jurnal (rows=1000 — semua artikel utk
// index DOI; polite via mailto; select = field korpus §5.3 butir 4).
// TEMUAN G10 (smoke C6): `free-to-read` BUKAN select valid di route
// /journals/{issn}/works → Crossref 400 validation-failure utk SEMUA jurnal
// (lisensi Crossref gagal masuk senyap) — dibuang dari select; parser tetap
// siap bila server mengirim field tsb (unit test crossref_test).
func crossrefWorksURL(issn, mailto string) string {
	u := fmt.Sprintf("https://api.crossref.org/journals/%s/works?rows=1000&select=DOI,license,subject,abstract",
		url.PathEscape(issn))
	if mailto != "" {
		u += "&mailto=" + url.QueryEscape(mailto)
	}
	return u
}

// getRetry = GET + retry 1× transien (pola volume.getRetry).
// C3b tahap C (approve 27 Okt): bila SolverMode AKTIF (hanya fallback
// terakhir — E7g: jalur normal TETAP GetPlain murni tanpa hybrid) dan
// balasan = WAF 403/468 → GET ulang via Solver (garuda.Client.Get →
// tls-client → challenge → BrowserSolve chromedp → cf_clearance per-host;
// halaman resumption berikutnya juga lewat sini — cookie cache membuatnya
// murah tanpa solve ulang).
func getRetry(cfg StageConfig, rawURL, referer string) ([]byte, int, string, error) {
	body, st, final, err := cfg.Client.GetPlain(rawURL, referer)
	if err == nil && !transien(st) {
		if b, s, e, hit := solverGet(cfg, rawURL, referer, st); hit {
			return b, s, rawURL, e
		}
		return body, st, final, nil
	}
	if cfg.RetryDelay > 0 {
		time.Sleep(cfg.RetryDelay)
	}
	body2, st2, final2, err2 := cfg.Client.GetPlain(rawURL, referer)
	if err2 == nil && !transien(st2) {
		if b, s, e, hit := solverGet(cfg, rawURL, referer, st2); hit {
			return b, s, rawURL, e
		}
		return body2, st2, final2, nil
	}
	if err2 != nil {
		return body2, st2, final2, err2
	}
	return body2, st2, final2, nil
}

// solverGet = gerbang solver: hit=true hanya bila SolverMode + status WAF
// (403 CF/umum · 468 SafeLine). Gagal solve → balasan jujur apa adanya
// (status non-200 → caller menanganinya sebagai kegagalan).
func solverGet(cfg StageConfig, rawURL, referer string, st int) (body []byte, status int, err error, hit bool) {
	if !cfg.SolverMode || cfg.Solver == nil || (st != 403 && st != 468) {
		return nil, 0, nil, false
	}
	b, s, e := cfg.Solver.Get(rawURL, referer)
	return b, s, e, true
}

func transien(st int) bool { return st == 0 || st == 429 || st >= 500 }

func stageVerify(rep *StageReport, logf func(string, ...any)) {
	// Catatan: NoISSN TIDAK terminal di stage ini (OAI tetap jalan — pola
	// Gap-E volume) → hanya statistik, TIDAK dijumlah utk cek drift.
	total := rep.Skip + rep.NoCanonical + rep.OAIMatik + rep.OAIEmpty +
		rep.NoArticles + rep.Processed + rep.Partial + rep.Err
	var masalah []string
	if total != rep.Target {
		masalah = append(masalah, fmt.Sprintf("drift: kategori=%d ≠ target=%d", total, rep.Target))
	}
	if rep.Err > 0 {
		masalah = append(masalah, fmt.Sprintf("%d jurnal gagal (rerun wajib)", rep.Err))
	}
	if rep.Partial > 0 {
		masalah = append(masalah, fmt.Sprintf("%d jurnal parsial (resume retry)", rep.Partial))
	}
	if rep.OAIMatik > 0 {
		masalah = append(masalah, fmt.Sprintf("%d jurnal OAI mati (antrean fallback C3b)", rep.OAIMatik))
	}
	if len(masalah) == 0 {
		rep.Verified = true
		rep.VerifyMsg = fmt.Sprintf("done %d/%d | skip %d | oai_empty %d | no_articles %d | no_issn %d | articles %d | oai_get %d | crossref_get %d",
			rep.Processed, rep.Target, rep.Skip, rep.OAIEmpty, rep.NoArticles,
			rep.NoISSN, rep.ArticlesWritten, rep.OAIGet, rep.CrossrefGET)
	} else {
		rep.Verified = false
		rep.VerifyMsg = fmt.Sprintf("%d masalah: %v", len(masalah), masalah)
	}
	logf("[corpus] verifikasi: %s → VERIFIED=%v", rep.VerifyMsg, rep.Verified)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
