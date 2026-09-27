package sinta

type Journal struct {
	ID                  int
	Name                string
	SINTAProfileURL     string
	GoogleScholarURL    string
	OJSURL              string
	EditorURL           string
	University          string
	AffiliationName     string
	AffiliationURL      string
	PrintISSN           string
	ElectronicISSN      string
	SubjectArea         string
	SintaRank           int
	IsScopus            bool
	IsGaruda            bool
	ScopusURL           string
	GarudaURL           string
	DOAJURL             string
	Impact              float64
	H5Index             int
	CitationsLast5Years int
	CitationsTotal      int
	SourcePage          int
}

type FilterPageResult struct {
	SintaRank     int
	Journals      []Journal
	CurrentPage   int
	TotalPages    int
	TotalJournals int
}
