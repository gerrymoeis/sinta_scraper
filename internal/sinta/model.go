package sinta

type Journal struct {
	ID                  int
	Name                string
	SINTAProfileURL     string
	GoogleScholarURL    string
	OJSURL              string
	EditorURL           string
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

// FieldChange = satu perubahan field terdeteksi saat upsert (doc 16 Bagian 3.1).
type FieldChange struct {
	Field string // nama kolom db (snake_case)
	Old   string
	New   string
}

// JournalChange = kartu yang kontenya berubah pada satu upsert batch.
type JournalChange struct {
	ID     int
	Name   string
	Fields []FieldChange
}

// UpsertReport = hasil klasifikasi upsert: baru / diperbarui / tidak berubah.
type UpsertReport struct {
	New       int
	Updated   int
	Unchanged int
	Changes   []JournalChange // hanya diisi untuk yang Updated
}
