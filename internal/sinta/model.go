package sinta

type Journal struct {
	ID               int // SINTA journal ID, diambil dari URL /journals/profile/{id}
	Name             string
	ProfileURL       string
	GoogleScholarURL string
	WebsiteURL       string
	EditorURL        string
	Affiliation      string
	AffiliationURL   string
	ISSNPrint        string
	ISSNElectronic   string
	SubjectArea      string // raw, bisa berisi beberapa subjek dipisah koma
	SintaRank        string // raw badge text, misal "S1 Accredited"
	IsScopus         bool
	IsGaruda         bool
	GarudaURL        string
	Impact           float64
	H5Index          int64
	Citations5yr     int64
	CitationsTotal   int64
	SourcePage       int // halaman listing tempat kartu ini ditemukan, untuk audit trail
}

// PageResult menampung hasil parsing satu halaman listing.
type PageResult struct {
	Journals     []Journal
	CurrentPage  int
	TotalPages   int // dari teks "Page X of N"
	TotalRecords int // dari teks "Total Records N"
}
