package corpus

// ==== apimerge.go — API-merge sbg PRIMER pengganti OAI (doc 43 §5.3 —
// C3b opsi A, di-approve user 27 Okt 2026) ====
//
// Rantai fallback bila OAI gagal (WAF 403/468, 404, DNS mati):
//
//	OAI ──gagal──> Crossref primer (1 GET, sort=published desc)
//	                └─kosong──> DOAJ (1 GET, last_updated desc)
//	                        └─kosong──> OAIMatik (antrean landing/solver — C3b
//	                                    tahap berikut; jujur tanpa baris)
//
// KUNCI DESAIN: adapter Crossref/DOAJ → []OAIRecord, sehingga pipeline
// YANG SAMA dipakai penuh tanpa cabang: FilterOwnRecords (netral — source
// = nama jurnal) · FilterLatestVolume (vol dari citation rekonstruksi
// "…, Vol(16), Iss(2)" → ParseCitationVol) · deriveArticle (lisensi §5.5
// tetap dari works — 1 GET Crossref primer diparse DUA fungsi:
// ParseCrossrefPrimera utk artikel + ParseCrossrefWorks utk license/subject).
//
// Fakta verifikasi 27 Okt (j1099 IJAL, 820 items): sort=published desc
// bekerja (5 teratas = 2026) · 99,9% ada vol/iss · 0 tanpa URL/DOI ·
// 41 tanpa judul (dilewati — derive wajib judul) · 153 tanpa abstrak
// (nullable jujur). Route /journals/{issn}/works MENOLAK select
// `language` (400 validation-failure — pola G10) → bahasa dideteksi
// dari teks (DetectLanguage) · DOAJ `year` = STRING (bukan number) +
// `sort=last_updated:desc` valid 200 · ISSN DOAJ WAJIB format dash
// (tanpa dash = total 0 SILENT — fakta probe).

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sinta-scraper/internal/storage"
)

// crossrefPrimeraURL = 1 GET Crossref sbg sumber PRIMER: urut terbitan
// TERBARU (sort=published&order=desc — menjamin volume terakhir masuk
// rows=1000 utk jurnal besar; tanpa sort = urut relevansi → vol terakhir
// bisa terlempar). select = field adapter + license (§5.5) — TANPA
// `language` (tidak valid utk route ini → 400, fakta G10-variant 27 Okt).
func crossrefPrimeraURL(issn, mailto string) string {
	u := fmt.Sprintf("https://api.crossref.org/journals/%s/works?rows=1000&sort=published&order=desc&select=DOI,title,abstract,issued,volume,issue,URL,container-title,subject,license,publisher,page",
		strings.TrimSpace(issn))
	if mailto != "" {
		u += "&mailto=" + strings.TrimSpace(mailto)
	}
	return u
}

// doajArticlesURL = 1 GET DOAJ sbg fallback kedua. ISSN WAJIB dash
// (NNNN-NNNN) — tanpa dash total=0 tanpa error (fakta probe 27 Okt) →
// sisipkan di sini (caller mengirim polos, pola Crossref).
func doajArticlesURL(issn string) string {
	s := strings.ReplaceAll(strings.TrimSpace(issn), "-", "")
	if len(s) == 8 {
		s = s[:4] + "-" + s[4:]
	}
	// pageSize=100 = cap DOAJ; urut last_updated desc → rilis terbaru dulu
	// (vol terakhir paling mungkin masuk halaman-1).
	return "https://doaj.org/api/search/articles/issn:" + s + "?pageSize=100&sort=last_updated:desc"
}

// ---- adapter Crossref → OAIRecord ----

// crPrimeraEnv = envelope works?select=... (field adapter + license).
type crPrimeraEnv struct {
	Message struct {
		TotalItems int `json:"total-results"`
		Items      []struct {
			DOI      string   `json:"DOI"`
			Title    []string `json:"title"`
			Abstract string   `json:"abstract"`
			Issued   struct {
				DateParts [][]int `json:"date-parts"`
			} `json:"issued"`
			Volume         string   `json:"volume"`
			Issue          string   `json:"issue"`
			URL            string   `json:"URL"`
			ContainerTitle []string `json:"container-title"`
			Subject        []string `json:"subject"`
			License        []struct {
				URL string `json:"URL"`
			} `json:"license"`
			Publisher string `json:"publisher"`
		} `json:"items"`
	} `json:"message"`
}

// ParseCrossrefPrimera = body works → []OAIRecord (fungsi murni). Item
// tanpa judul ATAU tanpa DOI+URL dilewati (deriveArticle wajib keduanya —
// jujur: 41/820 tanpa judul pada fakta IJAL). Urutan respons dipertahankan
// (sort published desc = deterministik).
func ParseCrossrefPrimera(body []byte) ([]OAIRecord, error) {
	var env crPrimeraEnv
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("crossref-primera: body bukan JSON valid: %w", err)
	}
	out := make([]OAIRecord, 0, len(env.Message.Items))
	for _, it := range env.Message.Items {
		title := strings.TrimSpace(strings.Join(it.Title, " "))
		if title == "" {
			continue // tanpa judul — tak layak derive (jujur)
		}
		doi := strings.ToLower(strings.TrimSpace(it.DOI))
		landing := strings.TrimSpace(it.URL)
		if doi == "" && landing == "" {
			continue // tanpa identitas — record_id & article_url wajib (jujur)
		}
		ids := make([]string, 0, 2)
		if landing != "" {
			ids = append(ids, landing)
		}
		if doi != "" {
			ids = append(ids, "doi:"+doi)
		}
		rec := OAIRecord{
			// record_id deterministik per jurnal; OAI mati → identifier
			// asli memang tak tersedia (provenance "api" di deriveArticle).
			Identifier:  "crossref:" + firstNonEmpty(doi, landing),
			Title:       title,
			Subjects:    it.Subject,
			Identifiers: ids,
			Source:      crCitation(it.ContainerTitle, it.Volume, it.Issue),
			Publishers:  pubSlice(it.Publisher),
		}
		if a := stripJATS(it.Abstract); a != "" {
			rec.Descriptions = []string{a}
		}
		if d := crDate(it.Issued.DateParts); d != "" {
			rec.Dates = []string{d}
		}
		for _, lic := range it.License {
			if lic.URL != "" {
				rec.Rights = []string{lic.URL} // §5.5 turunan: licenseFromOAI utk OAI mati
				break
			}
		}
		out = append(out, rec)
	}
	return out, nil
}

// crCitation = rekonstruksi citation OJS dari container-title + vol/iss
// ("Indonesian Journal of Applied Linguistics, Vol(16), Iss(2)") —
// format persis dc:source → ParseCitationVol & FilterOwnRecords bekerja
// tanpa perubahan. Tanpa vol → nama saja (vol=0 → FilterLatestVolume
// fallback tahun — pola vol_fallback yang sudah jujur).
func crCitation(container []string, vol, iss string) string {
	var b strings.Builder
	if len(container) > 0 {
		b.WriteString(strings.TrimSpace(container[0]))
	}
	if v := strings.TrimSpace(vol); v != "" {
		fmt.Fprintf(&b, ", Vol(%s)", v)
	}
	if n := strings.TrimSpace(iss); n != "" {
		fmt.Fprintf(&b, ", Iss(%s)", n)
	}
	return b.String()
}

// crDate = date-parts → dc:date ("2026-10-23" | "2026-10" | "2026").
// Nilai di luar rentang wajar dibuang (RecordYear baca tahunnya saja).
func crDate(parts [][]int) string {
	if len(parts) == 0 || len(parts[0]) == 0 {
		return ""
	}
	p := parts[0]
	y := p[0]
	if y < 1900 || y > 2100 {
		return ""
	}
	switch {
	case len(p) >= 3 && p[1] >= 1 && p[1] <= 12 && p[2] >= 1 && p[2] <= 31:
		return fmt.Sprintf("%04d-%02d-%02d", y, p[1], p[2])
	case len(p) >= 2 && p[1] >= 1 && p[1] <= 12:
		return fmt.Sprintf("%04d-%02d", y, p[1])
	default:
		return strconv.Itoa(y)
	}
}

func pubSlice(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return []string{strings.TrimSpace(s)}
}

// ---- adapter DOAJ → OAIRecord ----

// doajEnv = envelope /api/search/articles (v4). Year = `any` karena fakta
// probe 27 Okt: DOAJ mengirim year sebagai STRING ("2021") — JSON number
// pasti gagal unmarshal.
type doajEnv struct {
	Total   int `json:"total"`
	Results []struct {
		ID      string `json:"id"`
		Bibjson struct {
			Title      string `json:"title"`
			Abstract   string `json:"abstract"`
			Year       any    `json:"year"`
			DOI        string `json:"doi"`
			Language   string `json:"language"`
			Identifier []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"identifier"`
			Link []struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"link"`
			Journal struct {
				Title  string `json:"title"`
				Volume string `json:"volume"`
				Issue  string `json:"issue"`
			} `json:"journal"`
			Subject []struct {
				Term string `json:"term"`
			} `json:"subject"`
			License struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"license"`
		} `json:"bibjson"`
	} `json:"results"`
}

// ParseDOAJArticles = body DOAJ → []OAIRecord (fungsi murni). Item tanpa
// judul ATAU tanpa (link|doi|id) dilewati — pola Crossref di atas.
func ParseDOAJArticles(body []byte) ([]OAIRecord, error) {
	var env doajEnv
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("doaj: body bukan JSON valid: %w", err)
	}
	out := make([]OAIRecord, 0, len(env.Results))
	for _, r := range env.Results {
		b := r.Bibjson
		title := strings.TrimSpace(b.Title)
		if title == "" {
			continue
		}
		ids := make([]string, 0, 3)
		for _, l := range b.Link {
			if u := strings.TrimSpace(l.URL); u != "" {
				ids = append(ids, u)
				break
			}
		}
		doi := strings.ToLower(strings.TrimSpace(b.DOI))
		if doi == "" {
			for _, idf := range b.Identifier {
				if strings.EqualFold(idf.Type, "doi") {
					doi = strings.ToLower(strings.TrimSpace(idf.ID))
					break
				}
			}
		}
		if doi != "" {
			ids = append(ids, "doi:"+doi)
		}
		// record_id: id DOAJ (global unik) — fallback doi/link bila id kosong.
		recID := firstNonEmpty(strings.TrimSpace(r.ID), doi)
		if recID == "" {
			if len(ids) > 0 {
				recID = ids[0]
			} else {
				continue // tanpa identitas sama sekali — lewat (jujur)
			}
		}
		rec := OAIRecord{
			Identifier:  "doaj:" + recID,
			Title:       title,
			Subjects:    termsOf(b.Subject),
			Identifiers: ids,
			Source:      crCitation([]string{b.Journal.Title}, b.Journal.Volume, b.Journal.Issue),
			Publishers:  pubSlice(b.Journal.Title),
		}
		if a := strings.TrimSpace(b.Abstract); a != "" {
			rec.Descriptions = []string{a} // DOAJ = teks polos (tanpa JATS)
		}
		if y := anyYear(b.Year); y != "" {
			rec.Dates = []string{y}
		}
		if l := strings.TrimSpace(b.License.URL); l != "" {
			rec.Rights = []string{l}
		}
		if lg := strings.TrimSpace(b.Language); lg != "" {
			rec.Languages = []string{lg}
		}
		out = append(out, rec)
	}
	return out, nil
}

func termsOf(subs []struct {
	Term string `json:"term"`
}) []string {
	var out []string
	for _, s := range subs {
		if t := strings.TrimSpace(s.Term); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// anyYear = year fleksibel DOAJ (string "2021" / number 2021 / null).
// Rentang wajar dibuang (RecordYear hanya butuh yyyy).
func anyYear(v any) string {
	var n int
	switch t := v.(type) {
	case string:
		n, _ = strconv.Atoi(strings.TrimSpace(t))
	case float64:
		n = int(t)
	case json.Number:
		n, _ = strconv.Atoi(t.String())
	default:
		return ""
	}
	if n < 1900 || n > 2100 {
		return ""
	}
	return strconv.Itoa(n)
}

// ---- rantai fallback ----

// fetchAPIFallback = C3b opsi A: Crossref primer → DOAJ (murni GET; tanpa
// tulis). Return (records, works, src, err). works = pelengkap lisensi
// §5.5 (hanya Crossref — DOAJ datang tanpa index per-DOI; lisensi DOAJ
// masuk via rec.Rights). err = keduanya gagal/kosong → caller OAI MATI
// (antrean landing/solver).
func fetchAPIFallback(cfg StageConfig, rep *StageReport, t *storage.CorpusTarget,
	now time.Time) ([]OAIRecord, CrossrefWorks, string, error) {

	issn := strings.ReplaceAll(firstNonEmpty(t.ISSN, t.EISSN), "-", "")
	if issn == "" {
		return nil, CrossrefWorks{}, "", fmt.Errorf("API-merge butuh ISSN (j%d %q tanpa ISSN)", t.ID, t.Name)
	}

	var crErr, djErr error
	var crSt, djSt int

	// 1) Crossref primer (1 GET) — sukses bila ≥1 record layak derive.
	body, st, _, err := getRetry(cfg, crossrefPrimeraURL(issn, cfg.CrossrefMailto), "")
	rep.CrossrefGET++
	crSt, crErr = st, err
	if err == nil && st == 200 {
		recs, perr := ParseCrossrefPrimera(body)
		if perr != nil {
			crErr = perr
		} else if len(recs) > 0 {
			// 1 GET, 2 guna: body yang sama diparse utk artikel + lisensi.
			works, _ := ParseCrossrefWorks(body, now) // gagal parse lisensi ≠ fatal (lisensi = pelengkap)
			return recs, works, storage.ArticleSourceCrossref, nil
		} else {
			crErr = fmt.Errorf("0 record layak derive")
		}
	}

	// 2) DOAJ (1 GET).
	body2, st2, _, err2 := getRetry(cfg, doajArticlesURL(issn), "")
	rep.DoajGET++
	djSt, djErr = st2, err2
	if err2 == nil && st2 == 200 {
		recs, perr := ParseDOAJArticles(body2)
		if perr != nil {
			djErr = perr
		} else if len(recs) > 0 {
			return recs, CrossrefWorks{}, storage.ArticleSourceDOAJ, nil
		} else {
			djErr = fmt.Errorf("0 record layak derive")
		}
	}

	return nil, CrossrefWorks{}, "", fmt.Errorf("crossref st=%d/%v · doaj st=%d/%v", crSt, crErr, djSt, djErr)
}
