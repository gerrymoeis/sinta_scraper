package corpus

// ==== consts.go — alias konstanta whitelist dari storage (satu sumber
// kebenaran: DDL/stage corpus = kelas yg sama dgn writer artikel) ====

import "sinta-scraper/internal/storage"

const (
	LicenseOpen      = storage.LicenseOpen
	LicenseFreeNotOA = storage.LicenseFreeNotOA
	LicensePaid      = storage.LicensePaid
	LicenseUnknown   = storage.LicenseUnknown
	LanguageID       = storage.LanguageID
	LanguageEN       = storage.LanguageEN
	LanguageMixed    = storage.LanguageMixed
	LanguageUnknown  = storage.LanguageUnknown
)
