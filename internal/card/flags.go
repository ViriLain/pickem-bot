package card

import "strings"

// subdivisionFlags are the home nations with their own flag tag sequences.
var subdivisionFlags = map[string]string{
	"ENG": "\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F",
	"SCO": "\U0001F3F4\U000E0067\U000E0062\U000E0073\U000E0063\U000E0074\U000E007F",
	"WAL": "\U0001F3F4\U000E0067\U000E0062\U000E0077\U000E006C\U000E0073\U000E007F",
}

// countryISO maps FIFA (and common ESPN/IOC/ISO alpha-3 variants) codes to
// ISO 3166-1 alpha-2, from which the regional-indicator flag is built.
var countryISO = map[string]string{
	// 2026 World Cup hosts and qualifiers
	"USA": "US", "MEX": "MX", "CAN": "CA",
	"JPN": "JP", "IRN": "IR", "UZB": "UZ", "KOR": "KR", "JOR": "JO", "AUS": "AU",
	"QAT": "QA", "KSA": "SA", "SAU": "SA", "IRQ": "IQ",
	"ARG": "AR", "BRA": "BR", "ECU": "EC", "URU": "UY", "URY": "UY", "COL": "CO",
	"PAR": "PY", "PRY": "PY",
	"NZL": "NZ",
	"MAR": "MA", "TUN": "TN", "EGY": "EG", "ALG": "DZ", "DZA": "DZ", "GHA": "GH",
	"CPV": "CV", "RSA": "ZA", "ZAF": "ZA", "SEN": "SN", "CIV": "CI", "COD": "CD",
	"FRA": "FR", "CRO": "HR", "HRV": "HR", "NOR": "NO", "POR": "PT", "PRT": "PT",
	"NED": "NL", "NLD": "NL", "GER": "DE", "DEU": "DE", "SUI": "CH", "CHE": "CH",
	"ESP": "ES", "BEL": "BE", "AUT": "AT",
	"CUW": "CW", "HAI": "HT", "HTI": "HT", "PAN": "PA", "JAM": "JM", "SUR": "SR",
	"NCL": "NC", "BOL": "BO",
	"ITA": "IT", "TUR": "TR", "UKR": "UA", "SWE": "SE", "POL": "PL", "CZE": "CZ",
	"SVK": "SK", "DEN": "DK", "DNK": "DK", "IRL": "IE", "BIH": "BA", "KOS": "XK",
	"ALB": "AL", "ROU": "RO", "MKD": "MK",
	// other common national sides
	"CHI": "CL", "CHL": "CL", "PER": "PE", "VEN": "VE",
	"CRC": "CR", "HON": "HN", "SLV": "SV", "GUA": "GT", "TRI": "TT",
	"NGA": "NG", "CMR": "CM", "MLI": "ML", "BFA": "BF", "GUI": "GN", "GAB": "GA",
	"ZAM": "ZM", "ANG": "AO",
	"CHN": "CN", "IND": "IN", "UAE": "AE", "ARE": "AE", "OMA": "OM", "BHR": "BH",
	"SYR": "SY", "PLE": "PS", "VIE": "VN", "THA": "TH", "IDN": "ID", "PHI": "PH",
	"GRE": "GR", "SRB": "RS", "SVN": "SI", "HUN": "HU", "FIN": "FI", "ISL": "IS",
	"ISR": "IL", "RUS": "RU", "GEO": "GE", "ARM": "AM", "AZE": "AZ", "BUL": "BG",
	"MNE": "ME", "LUX": "LU", "CYP": "CY", "EST": "EE", "LVA": "LV", "LTU": "LT",
	"BLR": "BY", "MDA": "MD", "KAZ": "KZ",
}

// FlagEmoji returns the flag emoji for a FIFA/ESPN three-letter country
// code, or "" when unknown.
func FlagEmoji(abbrev string) string {
	code := strings.ToUpper(strings.TrimSpace(abbrev))
	if f, ok := subdivisionFlags[code]; ok {
		return f
	}
	iso := countryISO[code]
	if len(iso) != 2 {
		return ""
	}
	const base = 0x1F1E6 - 'A'
	return string([]rune{rune(iso[0]) + base, rune(iso[1]) + base})
}
