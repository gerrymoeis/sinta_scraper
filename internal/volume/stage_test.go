package volume

import (
	"path/filepath"
	"sinta-scraper/internal/sinta"
	"sinta-scraper/internal/storage"
	"strings"
	"testing"
)

// ==== stage_test.go â€” driver Tahap 3 (fake PlainGetter â€” mock DB+HTML, tanpa
// jaringan; urutan revisi approve user 14 Okt 2026) ====
//
// 4 kasus: OJS happy (archive paginasi + Crossref H17 + TOC window) Â·
// non-OJS (Crossref derive) Â· archive WAF 403 (parsial jujur) Â· resume skip.

// fakeGetter = PlainGetter tiruan pola ojs.stage_test (map URL â†’ resp);
// seq = urutan respons utk URL (429 lalu 200 â€” uji retry transien).
type fakeGetter struct {
	responses map[string]fakeResp
	seq       map[string][]fakeResp
	seqIdx    map[string]int
	gets      int
}

type fakeResp struct {
	body   []byte
	status int
	final  string
	err    error
}

func (f *fakeGetter) GetPlain(rawURL, referer string) ([]byte, int, string, error) {
	f.gets++
	if f.seq != nil {
		if arr, ok := f.seq[rawURL]; ok {
			if f.seqIdx == nil {
				f.seqIdx = map[string]int{}
			}
			i := f.seqIdx[rawURL]
			if i < len(arr) {
				f.seqIdx[rawURL] = i + 1
				r := arr[i]
				return r.body, r.status, r.final, r.err
			}
			r := arr[len(arr)-1] // habis â†’ terakhir diulang
			return r.body, r.status, r.final, r.err
		}
	}
	r, ok := f.responses[rawURL]
	if !ok {
		return nil, 404, rawURL, nil
	}
	return r.body, r.status, r.final, r.err
}

// seedVolume = jurnal + profil sumber (platform+canonical â€” input VolumeTargets).
func seedVolume(t *testing.T, st *storage.Store, id int64, platform, canonical, issn string) {
	t.Helper()
	if _, err := st.UpsertJournals([]sinta.Journal{{
		ID: int(id), Name: "Jurnal Test", SintaRank: 1, SourcePage: 1,
	}}); err != nil {
		t.Fatalf("UpsertJournals: %v", err)
	}
	if err := st.UpsertSourceProfile(storage.SourceProfile{
		JournalID: id, Platform: platform, Evidence: "open-journal-systems",
		Confidence: "high", CanonicalURL: canonical,
	}); err != nil {
		t.Fatalf("UpsertSourceProfile: %v", err)
	}
	if issn != "" {
		if _, err := st.UpdateJournalsE7(id, "print_issn", issn); err != nil {
			t.Fatalf("set issn: %v", err)
		}
	}
}

// homeDgnArchive = halaman home OJS dgn link archive absolut.
func homeDgnArchive(base string) []byte {
	return []byte(`<html><body><a href="` + base + `/issue/archive">Archive</a>
` + strings.Repeat("teks home yang cukup panjang. ", 30) + `</body></html>`)
}

// archivePage = halaman archive dgn baris issue (url, teks) + (opsional) offset.
func archivePage(base string, issues [][2]string, offsetNext string) []byte {
	var b strings.Builder
	b.WriteString(`<html><body><ul>`)
	for _, u := range issues {
		b.WriteString(`<li><a href="` + u[0] + `">` + u[1] + `</a></li>`)
	}
	b.WriteString(`</ul>`)
	if offsetNext != "" {
		b.WriteString(`<a href="` + base + `/issue/archive?offset=` + offsetNext + `">Next</a>`)
	}
	b.WriteString(`</body></html>`)
	return []byte(b.String())
}

// tocPage = halaman TOC dgn n artikel unik.
func tocPage(n int) []byte {
	var b strings.Builder
	b.WriteString(`<html><head><title>Vol. 12 No. 1</title></head><body>`)
	for i := 0; i < n; i++ {
		b.WriteString(`<a href="https://x.test/article/view/` + itoaT(100+i) + `">Art</a>`)
	}
	b.WriteString(`</body></html>`)
	return []byte(b.String())
}

// crossrefBody = works items dgn VI+date (pola probep4 select).
func crossrefBody(items ...string) []byte {
	return []byte(`{"message":{"items":[` + strings.Join(items, ",") + `]}}`)
}

func crItem(vol, issue, y, m int) string {
	return `{"volume":"` + itoaT(vol) + `","issue":"` + itoaT(issue) +
		`","published":{"date-parts":[[` + itoaT(y) + `,` + itoaT(m) + `,1]]}}`
}

func itoaT(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func crossrefURL(issn string) string {
	return "https://api.crossref.org/journals/" + issn +
		"/works?rows=1000&sort=published&order=desc&select=volume,issue,published"
}

// TestStageVolumeHappyOJS â€” archive 2 halaman (offset) + Crossref H17 +
// TOC 3 issue window â†’ baris lengkap, complete=true, Verified=true.
func TestStageVolumeHappyOJS(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "vol.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	base := "https://j1.test/index.php/j1"
	seedVolume(t, st, 1, "ojs", base, "1234-5678")

	g := &fakeGetter{responses: map[string]fakeResp{
		base: {body: homeDgnArchive(base), status: 200, final: base},
		base + "/issue/archive": {
			body: archivePage(base, [][2]string{
				{base + "/issue/view/2", "Vol. 12 No. 1 (Maret 2025)"},
				{base + "/issue/view/1", "Vol. 11 No. 2 (September 2024)"},
			}, "6"),
			status: 200, final: base + "/issue/archive",
		},
		base + "/issue/archive?offset=6": {
			body:   archivePage(base, [][2]string{{base + "/issue/view/0", "Vol. 11 No. 1 (Maret 2024)"}}, ""),
			status: 200, final: base + "/issue/archive?offset=6",
		},
		crossrefURL("12345678"): {
			body: crossrefBody(
				crItem(12, 1, 2025, 3), crItem(12, 1, 2025, 3), crItem(12, 1, 2025, 4),
				crItem(11, 2, 2024, 9), crItem(11, 2, 2024, 9),
				crItem(11, 1, 2024, 3), crItem(11, 1, 2024, 3),
			),
			status: 200, final: crossrefURL("12345678"),
		},
		base + "/issue/view/2": {body: tocPage(3), status: 200, final: base + "/issue/view/2"},
		base + "/issue/view/1": {body: tocPage(2), status: 200, final: base + "/issue/view/1"},
		base + "/issue/view/0": {body: tocPage(2), status: 200, final: base + "/issue/view/0"},
	}}

	rep, err := RunVolumeStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunVolumeStage: %v", err)
	}
	if !rep.Verified {
		t.Fatalf("Verified=false: %s", rep.VerifyMsg)
	}
	if rep.Processed != 1 || rep.ArchiveGET != 2 || rep.TOCGET != 3 || rep.CrossrefGET != 1 {
		t.Errorf("processed=%d archiveGET=%d tocGET=%d crossrefGET=%d â€” mau 1/2/3/1",
			rep.Processed, rep.ArchiveGET, rep.TOCGET, rep.CrossrefGET)
	}

	v, ok, err := st.VolumeStats(1)
	if err != nil || !ok {
		t.Fatalf("VolumeStats: %v ok=%v", err, ok)
	}
	if v.Source != "ojs" || !v.IsComplete {
		t.Errorf("source=%q complete=%v â€” mau ojs/true", v.Source, v.IsComplete)
	}
	if v.AnchorYear != 2025 || v.WindowStart != 2023 || v.WindowEnd != 2025 {
		t.Errorf("anchor=%d window=%d-%d â€” mau 2025/2023/2025", v.AnchorYear, v.WindowStart, v.WindowEnd)
	}
	// Resume: run ke-2 = skip + 0 GET.
	getsBefore := g.gets
	rep2, err := RunVolumeStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if rep2.Skip != 1 || g.gets != getsBefore {
		t.Errorf("run2 skip=%d gets=%d (before=%d) â€” mau skip=1 tanpa GET", rep2.Skip, g.gets, getsBefore)
	}
}

// TestStageVolumeNonOJS â€” platform custom: tanpa archive/TOC, Crossref derive,
// source='crossref' (approve user 14 Okt).
func TestStageVolumeNonOJS(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "vol.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	seedVolume(t, st, 2, "custom", "https://j2.test/", "8765-4321")

	g := &fakeGetter{responses: map[string]fakeResp{
		crossrefURL("87654321"): {
			body: crossrefBody(
				crItem(5, 2, 2025, 6), crItem(5, 2, 2025, 6),
				crItem(5, 1, 2024, 6),
			),
			status: 200, final: crossrefURL("87654321"),
		},
	}}

	rep, err := RunVolumeStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunVolumeStage: %v", err)
	}
	if rep.Processed != 1 || g.gets != 1 {
		t.Errorf("processed=%d gets=%d â€” mau 1/1 (crossref saja)", rep.Processed, g.gets)
	}
	v, ok, _ := st.VolumeStats(2)
	if !ok || v.Source != "crossref" || v.AnchorYear != 2025 {
		t.Errorf("got ok=%v source=%q anchor=%d â€” mau true/crossref/2025", ok, v.Source, v.AnchorYear)
	}
}

// TestStageVolumeArchiveWAF â€” archive 403: parsial jujur (is_complete=0),
// anchor crossref-only fallback, Verified=false (resume retry).
func TestStageVolumeArchiveWAF(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "vol.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	base := "https://j3.test/index.php/j3"
	seedVolume(t, st, 3, "ojs", base, "1111-2222")

	g := &fakeGetter{responses: map[string]fakeResp{
		base:                    {body: homeDgnArchive(base), status: 200, final: base},
		base + "/issue/archive": {body: []byte("forbidden"), status: 403, final: base + "/issue/archive"},
		crossrefURL("11112222"): {
			body:   crossrefBody(crItem(7, 1, 2025, 1), crItem(6, 2, 2024, 7)),
			status: 200, final: crossrefURL("11112222"),
		},
	}}

	rep, err := RunVolumeStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunVolumeStage: %v", err)
	}
	if rep.Partial != 1 || rep.Verified {
		t.Errorf("partial=%d verified=%v â€” mau 1/false (archive 403 parsial)", rep.Partial, rep.Verified)
	}
	v, ok, _ := st.VolumeStats(3)
	if !ok || v.IsComplete {
		t.Errorf("ok=%v complete=%v â€” mau true/false (baris tersimpan parsial)", ok, v.IsComplete)
	}
	if v.AnchorYear != 2025 {
		t.Errorf("anchor=%d â€” mau 2025 (crossref global fallback)", v.AnchorYear)
	}
}

// TestStageVolumeTOCArticleless â€” TOC GET 200 tapi 0 artikel (template beda /
// issue kosong): key tetap dlm map (H18 "sudah dicek") â†’ complete=true,
// bukan parsial FOREVER (Gap-A review integrasi).
func TestStageVolumeTOCArticleless(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "vol.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	base := "https://j5.test/index.php/j5"
	seedVolume(t, st, 5, "ojs", base, "9999-0000")

	g := &fakeGetter{responses: map[string]fakeResp{
		base: {body: homeDgnArchive(base), status: 200, final: base},
		base + "/issue/archive": {
			body: archivePage(base, [][2]string{
				{base + "/issue/view/9", "Vol. 3 No. 1 (2025)"},
			}, ""),
			status: 200, final: base + "/issue/archive",
		},
		crossrefURL("99990000"): {
			body:   crossrefBody(crItem(3, 1, 2025, 2), crItem(3, 1, 2025, 2)),
			status: 200, final: crossrefURL("99990000"),
		},
		base + "/issue/view/9": {body: tocPage(0), status: 200, final: base + "/issue/view/9"},
	}}

	rep, err := RunVolumeStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunVolumeStage: %v", err)
	}
	if rep.Processed != 1 || rep.Partial != 0 {
		t.Errorf("processed=%d partial=%d â€” mau 1/0 (TOC 0-artikel = sudah dicek)", rep.Processed, rep.Partial)
	}
	v, ok, _ := st.VolumeStats(5)
	if !ok || !v.IsComplete {
		t.Errorf("ok=%v complete=%v â€” mau true (H18: TOCable terpenuhi)", ok, v.IsComplete)
	}
	if v.ArticlesPerVolumeAvg != 0 {
		t.Errorf("D2 = %v â€” mau 0 jujur (0 artikel terhitung)", v.ArticlesPerVolumeAvg)
	}
}

// TestStageVolumeCrossref429Retry â€” Crossref 429 (rate-limit transien) lalu
// 200: getRetry menangani (Gap-B review integrasi) â€” processed=1, bukan parsial.
func TestStageVolumeCrossref429Retry(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "vol.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	base := "https://j6.test/index.php/j6"
	seedVolume(t, st, 6, "ojs", base, "5555-6666")

	g := &fakeGetter{
		responses: map[string]fakeResp{
			base: {body: homeDgnArchive(base), status: 200, final: base},
			base + "/issue/archive": {
				body: archivePage(base, [][2]string{
					{base + "/issue/view/3", "Vol. 2 No. 1 (2025)"},
				}, ""),
				status: 200, final: base + "/issue/archive",
			},
			base + "/issue/view/3": {body: tocPage(2), status: 200, final: base + "/issue/view/3"},
		},
		seq: map[string][]fakeResp{
			crossrefURL("55556666"): {
				{status: 429, final: crossrefURL("55556666")}, // attempt 1
				{body: crossrefBody(crItem(2, 1, 2025, 1)), status: 200,
					final: crossrefURL("55556666")}, // retry
			},
		},
	}

	rep, err := RunVolumeStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunVolumeStage: %v", err)
	}
	if rep.Processed != 1 || rep.Partial != 0 {
		t.Errorf("processed=%d partial=%d â€” mau 1/0 (429 transien tertangani retry)", rep.Processed, rep.Partial)
	}
	// Rep menghitung PEMANGGILAN (per jurnal/URL — pola stage); attempt
	// retry internal tak dihitung (GET attempt riil = f.gets utk URL tsb).
	if rep.CrossrefGET != 1 {
		t.Errorf("crossrefGET=%d — mau 1 (panggilan getCrossref; attempt 429+200 dlm getRetry)", rep.CrossrefGET)
	}
	if got := g.gets; got < 5 {
		t.Errorf("gets=%d — mau ≥5 (home+archive+toc+crossref 429+crossref 200)", got)
	}
}

// TestStageVolumeTOC404Permanent — TOC GET 404 permanen (issue dihapus):
// tandai sudah-dicek (Gap-D) → complete=true, bukan parsial FOREVER.
func TestStageVolumeTOC404Permanent(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "vol.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	base := "https://j7.test/index.php/j7"
	seedVolume(t, st, 7, "ojs", base, "7777-8888")

	g := &fakeGetter{responses: map[string]fakeResp{
		base: {body: homeDgnArchive(base), status: 200, final: base},
		base + "/issue/archive": {
			body: archivePage(base, [][2]string{
				{base + "/issue/view/2", "Vol. 4 No. 1 (2025)"},
				{base + "/issue/view/1", "Vol. 3 No. 2 (2024)"},
			}, ""),
			status: 200, final: base + "/issue/archive",
		},
		crossrefURL("77778888"): {
			body:   crossrefBody(crItem(4, 1, 2025, 3), crItem(3, 2, 2024, 9)),
			status: 200, final: crossrefURL("77778888"),
		},
		base + "/issue/view/2": {body: tocPage(2), status: 200, final: base + "/issue/view/2"},
		base + "/issue/view/1": {body: []byte("Not Found"), status: 404, final: base + "/issue/view/1"},
	}}

	rep, err := RunVolumeStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunVolumeStage: %v", err)
	}
	if rep.Processed != 1 || rep.Partial != 0 {
		t.Errorf("processed=%d partial=%d — mau 1/0 (404 permanen = sudah-dicek)", rep.Processed, rep.Partial)
	}
	v, ok, _ := st.VolumeStats(7)
	if !ok || !v.IsComplete {
		t.Errorf("ok=%v complete=%v — mau true (Gap-D: 404 permanen tak menuntut retry)", ok, v.IsComplete)
	}
}

// TestStageVolumeWindowBulanan — 14 issue dlm window (> cap lama 12):
// semua ter-TOC dgn DefaultMaxTOC=40 (Gap-C) → complete=true, D2 rata2
// semua issue (bukan parsial FOREVER spt dgn cap 12).
func TestStageVolumeWindowBulanan(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "vol.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	base := "https://j8.test/index.php/j8"
	seedVolume(t, st, 8, "ojs", base, "3333-4444")

	const n = 14 // > 12 (cap lama) — jurnal bulanan mini utk bukti cap 40
	var issues [][2]string
	crItems := make([]string, 0, n)
	for i := 0; i < n; i++ {
		u := base + "/issue/view/" + itoaT(100+i)
		issues = append(issues, [2]string{u, "Vol. " + itoaT(i+1) + " No. 1 (2025)"})
		crItems = append(crItems, crItem(i+1, 1, 2025, 1+(i%12)))
	}
	g := &fakeGetter{responses: map[string]fakeResp{
		base: {body: homeDgnArchive(base), status: 200, final: base},
		base + "/issue/archive": {
			body: archivePage(base, issues, ""), status: 200, final: base + "/issue/archive",
		},
		crossrefURL("33334444"): {
			body:   crossrefBody(crItems...),
			status: 200, final: crossrefURL("33334444"),
		},
	}}
	for i := 0; i < n; i++ {
		u := base + "/issue/view/" + itoaT(100+i)
		g.responses[u] = fakeResp{body: tocPage(5), status: 200, final: u}
	}

	rep, err := RunVolumeStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunVolumeStage: %v", err)
	}
	if rep.Processed != 1 || rep.Partial != 0 {
		t.Errorf("processed=%d partial=%d — mau 1/0 (14 issue < cap 40)", rep.Processed, rep.Partial)
	}
	if rep.TOCGET != n {
		t.Errorf("tocGET=%d — mau %d (SEMUA issue window ter-TOC)", rep.TOCGET, n)
	}
	v, ok, _ := st.VolumeStats(8)
	if !ok || !v.IsComplete {
		t.Errorf("ok=%v complete=%v — mau true (Gap-C: cap 40 muat window bulanan)", ok, v.IsComplete)
	}
	if v.ArticlesPerVolumeAvg != 5 {
		t.Errorf("D2 = %v — mau 5 (rata2 semua %d issue)", v.ArticlesPerVolumeAvg, n)
	}
}

// TestStageVolumeOJSNoISSN — OJS tanpa ISSN: crossref MUSTAHIL (Gap-E) →
// TANPA set parsial; archive+TOC lengkap → complete=true, 0 crossref GET.
func TestStageVolumeOJSNoISSN(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "vol.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	base := "https://j9.test/index.php/j9"
	seedVolume(t, st, 9, "ojs", base, "") // tanpa ISSN

	g := &fakeGetter{responses: map[string]fakeResp{
		base: {body: homeDgnArchive(base), status: 200, final: base},
		base + "/issue/archive": {
			body: archivePage(base, [][2]string{
				{base + "/issue/view/5", "Vol. 2 No. 1 (2025)"},
			}, ""),
			status: 200, final: base + "/issue/archive",
		},
		base + "/issue/view/5": {body: tocPage(3), status: 200, final: base + "/issue/view/5"},
	}}

	rep, err := RunVolumeStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunVolumeStage: %v", err)
	}
	if rep.Processed != 1 || rep.Partial != 0 || rep.CrossrefGET != 0 {
		t.Errorf("processed=%d partial=%d crossrefGET=%d — mau 1/0/0 (Gap-E: tanpa ISSN = mustahil, bukan parsial)",
			rep.Processed, rep.Partial, rep.CrossrefGET)
	}
	v, ok, _ := st.VolumeStats(9)
	if !ok || !v.IsComplete || v.Source != "ojs" {
		t.Errorf("ok=%v complete=%v source=%q — mau true/true/ojs", ok, v.IsComplete, v.Source)
	}
	if v.ArticlesPerVolumeAvg != 3 {
		t.Errorf("D2 = %v — mau 3 (TOC archive saja)", v.ArticlesPerVolumeAvg)
	}
}

// TestStageVolumeVol0SkipTOC — baris archive Vol=0 (volno gagal parse) dgn
// URL: TOC TIDAK di-GET (di luar WindowIssues G2 — Minor-1) → tocGET hanya
// utk issue VI valid.
func TestStageVolumeVol0SkipTOC(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "vol.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	base := "https://ja.test/index.php/ja"
	seedVolume(t, st, 10, "ojs", base, "1212-3434")

	g := &fakeGetter{responses: map[string]fakeResp{
		base: {body: homeDgnArchive(base), status: 200, final: base},
		base + "/issue/archive": {
			body: archivePage(base, [][2]string{
				{base + "/issue/view/7", "Vol. 5 No. 1 (2025)"}, // VI valid
				{base + "/issue/view/6", "Desember 2025"},       // Vol=0 (parse gagal)
			}, ""),
			status: 200, final: base + "/issue/archive",
		},
		crossrefURL("12123434"): {
			body:   crossrefBody(crItem(5, 1, 2025, 3)),
			status: 200, final: crossrefURL("12123434"),
		},
		base + "/issue/view/7": {body: tocPage(2), status: 200, final: base + "/issue/view/7"},
		// view/6 sengaja TIDAK disediakan — bila GET, fakeGetter balas 404
	}}

	rep, err := RunVolumeStage(st, StageConfig{Client: g, RetryDelay: 0})
	if err != nil {
		t.Fatalf("RunVolumeStage: %v", err)
	}
	if rep.TOCGET != 1 {
		t.Errorf("tocGET=%d — mau 1 (Vol=0 di-skip — Minor-1)", rep.TOCGET)
	}
	if rep.Processed != 1 || rep.Partial != 0 {
		t.Errorf("processed=%d partial=%d — mau 1/0", rep.Processed, rep.Partial)
	}
}
