package corpus

// ==== stopwords.go — stopword id/en embedded (doc 43 §5.4: file dlm repo,
// versioned, deterministik — fase C4) ====

import (
	"embed"
	"strings"
)

//go:embed stopwords/id.txt stopwords/en.txt
var stopwordFS embed.FS

// idStop / enStop = set kata henti (parse sekali saat package load —
// deterministik: isi file = kebenaran).
var idStop, enStop = mustLoadStopwords("stopwords/id.txt"), mustLoadStopwords("stopwords/en.txt")

func mustLoadStopwords(path string) map[string]bool {
	b, err := stopwordFS.ReadFile(path)
	if err != nil {
		panic("corpus: stopword file " + path + ": " + err.Error())
	}
	set := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		set[strings.ToLower(line)] = true
	}
	if len(set) == 0 {
		panic("corpus: stopword file " + path + " kosong")
	}
	return set
}

// stopwordSetByLang = set stopword by bahasa artikel (§4.1: by language tag;
// mixed/unknown → gabungan — teks dwibahasa aman).
func stopwordSetByLang(lang string) map[string]bool {
	switch lang {
	case LanguageID:
		return idStop
	case LanguageEN:
		return enStop
	}
	merged := make(map[string]bool, len(idStop)+len(enStop))
	for k := range idStop {
		merged[k] = true
	}
	for k := range enStop {
		merged[k] = true
	}
	return merged
}
