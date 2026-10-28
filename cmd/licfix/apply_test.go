package main

// Unit test D4 (apply.go): provMergeArt (merge provenance only-if-empty),
// resolvedLegacy (whitelist tulis), loadLegacyArtefak (guard artefak).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sinta-scraper/internal/storage"
)

// TestProvMergeArt — kunci lama TAK boleh tertimpa; JSON rusak → error.
func TestProvMergeArt(t *testing.T) {
	// 1. kunci kosong/tidak ada → terisi
	got, err := provMergeArt(`{"embargo":""}`, map[string]string{"license_src": "policy:copyright"})
	if err != nil || !strings.Contains(got, `"license_src":"policy:copyright"`) {
		t.Errorf("isi kunci baru: got %q err=%v", got, err)
	}
	// 2. kunci SUDAH berisi BUKTI NYATA → menang (jangan timpa jejak stage)
	got, err = provMergeArt(`{"oa_src":"crossref"}`, map[string]string{"oa_src": "doaj"})
	if err != nil || !strings.Contains(got, `"oa_src":"crossref"`) {
		t.Errorf("kunci berbukti harus menang: got %q err=%v", got, err)
	}
	// 2b. nilai placeholder "unknown" BUKAN bukti → boleh ditimpa jejak baru
	got, err = provMergeArt(`{"license_src":"unknown"}`, map[string]string{"license_src": "home"})
	if err != nil || !strings.Contains(got, `"license_src":"home"`) {
		t.Errorf(`"unknown" harus ditimpa: got %q err=%v`, got, err)
	}
	// 3. nilai kosong di set → di-skip (tulis "" tak berguna)
	got, err = provMergeArt(`{}`, map[string]string{"license_page": ""})
	if err != nil || strings.Contains(got, "license_page") {
		t.Errorf("nilai kosong harus di-skip: got %q err=%v", got, err)
	}
	// 4. provenance rusak → error (jangan turunkan data tak terbaca)
	if _, err := provMergeArt(`{oops`, map[string]string{"a": "b"}); err == nil {
		t.Error("JSON rusak harus error")
	}
	// 5. provenance kosong → {} + set
	got, err = provMergeArt("", map[string]string{"license_src": "doaj"})
	if err != nil || !json.Valid([]byte(got)) {
		t.Errorf("provenance kosong: got %q err=%v", got, err)
	}
	// 6. kunci lain tetap utuh
	got, err = provMergeArt(`{"license_src":"home","x":1}`, map[string]string{"license_src": "doaj", "license_page": "https://p"})
	if err != nil || !strings.Contains(got, `"license_src":"home"`) || !strings.Contains(got, `"x":1`) ||
		!strings.Contains(got, "https://p") {
		t.Errorf("merge penuh: got %q err=%v", got, err)
	}
}

// TestResolvedLegacy — hanya 4 kelas whitelist; unresolved/PAGU TIDAK ditulis.
func TestResolvedLegacy(t *testing.T) {
	for _, s := range []string{storage.LicenseOpen, storage.LicenseFreeNotOA, storage.LicensePaid} {
		if !resolvedLegacy(s) {
			t.Errorf("%s seharusnya resolved", s)
		}
	}
	for _, s := range []string{storage.LicenseUnknown, "unresolved", "PAGU", "", "OPEN"} {
		if resolvedLegacy(s) {
			t.Errorf("%q seharusnya TIDAK ditulis", s)
		}
	}
}

// TestLoadLegacyArtefak — artefak valid lolos; status liar / id 0 → fatal.
// (fatal = os.Exit → pakai sub-proses? cukup uji jalur lolos di sini; jalur
// fatal diverifikasi manual saat run D4.)
func TestLoadLegacyArtefak(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "legacy.json")
	body := `[{"journal_id":1,"name":"J","rows":5,"license_status":"open",
		"license_src":"policy:copyright","license_page":"https://x/p",
		"license_url":"https://creativecommons.org/licenses/by/4.0/","doaj":"MATCH"}]`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ents := loadLegacyArtefak(p)
	if len(ents) != 1 || ents[0].Status != storage.LicenseOpen || !resolvedLegacy(ents[0].Status) {
		t.Errorf("load: %+v", ents)
	}
}
