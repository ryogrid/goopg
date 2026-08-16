package misc

import "fmt"

// pgEncNames is the canonical encoding name table, mirroring pg_enc2name_tbl
// in src/common/encnames.c. The slice index IS the encoding ID. This is the
// same table carried by internal/catalog/encoding.go and
// internal/initdb/encoding.go; it is duplicated here — rather than importing
// either — to avoid import cycles (config is a leaf package). The table is an
// immutable PostgreSQL constant. M0122-0008.
var pgEncNames = [...]string{
	"SQL_ASCII", "EUC_JP", "EUC_CN", "EUC_KR", "EUC_TW", "EUC_JIS_2004",
	"UTF8", "MULE_INTERNAL", "LATIN1", "LATIN2", "LATIN3", "LATIN4",
	"LATIN5", "LATIN6", "LATIN7", "LATIN8", "LATIN9", "LATIN10",
	"WIN1256", "WIN1258", "WIN866", "WIN874", "KOI8R", "WIN1251",
	"WIN1252", "ISO_8859_5", "ISO_8859_6", "ISO_8859_7", "ISO_8859_8",
	"WIN1250", "WIN1253", "WIN1254", "WIN1255", "WIN1257", "KOI8U",
	"SJIS", "BIG5", "GBK", "UHC", "GB18030", "JOHAB", "SHIFT_JIS_2004",
}

// cleanEncName lowercases and strips every non-alphanumeric character from an
// encoding name, mirroring clean_encoding_name in encnames.c. This is what
// lets "UTF-8", "utf_8", and "UTF8" all resolve to the same key.
func cleanEncName(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			out = append(out, c)
		}
	}
	return string(out)
}

// pgEncAliases maps a cleaned encoding alias to its canonical name, mirroring
// pg_encname_tbl in encnames.c.
var pgEncAliases = map[string]string{
	"abc":          "WIN1258",
	"alt":          "WIN866",
	"euccn":        "EUC_CN",
	"eucjis2004":   "EUC_JIS_2004",
	"eucjp":        "EUC_JP",
	"euckr":        "EUC_KR",
	"euctw":        "EUC_TW",
	"iso88591":     "LATIN1",
	"iso885910":    "LATIN6",
	"iso885913":    "LATIN7",
	"iso885914":    "LATIN8",
	"iso885915":    "LATIN9",
	"iso885916":    "LATIN10",
	"iso88592":     "LATIN2",
	"iso88593":     "LATIN3",
	"iso88594":     "LATIN4",
	"iso88595":     "ISO_8859_5",
	"iso88596":     "ISO_8859_6",
	"iso88597":     "ISO_8859_7",
	"iso88598":     "ISO_8859_8",
	"iso88599":     "LATIN5",
	"koi8":         "KOI8R",
	"latin1":       "LATIN1",
	"latin5":       "LATIN5",
	"latin6":       "LATIN6",
	"latin7":       "LATIN7",
	"latin8":       "LATIN8",
	"latin9":       "LATIN9",
	"mskanji":      "SJIS",
	"muleinternal": "MULE_INTERNAL",
	"shiftjis":     "SJIS",
	"sqlascii":     "SQL_ASCII",
	"tcvn":         "WIN1258",
	"tcvn5712":     "WIN1258",
	"unicode":      "UTF8",
	"vscii":        "WIN1258",
	"win":          "WIN1251",
	"win1250":      "WIN1250",
	"win1251":      "WIN1251",
	"win1252":      "WIN1252",
	"win1253":      "WIN1253",
	"win1254":      "WIN1254",
	"win1255":      "WIN1255",
	"win1256":      "WIN1256",
	"win1257":      "WIN1257",
	"win1258":      "WIN1258",
	"win932":       "SJIS",
	"win936":       "GBK",
	"win949":       "UHC",
	"win950":       "BIG5",
	"windows1250":  "WIN1250",
	"windows1251":  "WIN1251",
	"windows1252":  "WIN1252",
	"windows1253":  "WIN1253",
	"windows1254":  "WIN1254",
	"windows1255":  "WIN1255",
	"windows1256":  "WIN1256",
	"windows1257":  "WIN1257",
	"windows1258":  "WIN1258",
	"windows866":   "WIN866",
	"windows874":   "WIN874",
	"windows932":   "SJIS",
	"windows936":   "GBK",
	"windows949":   "UHC",
	"windows950":   "BIG5",
}

// encodingNameToCanonical resolves an encoding name (any case, with arbitrary
// punctuation that cleanEncName strips) to its canonical encoding name, or ""
// if unknown. Canonical names are matched first by cleaning both sides; then
// the alias table is consulted.
func encodingNameToCanonical(name string) string {
	key := cleanEncName(name)
	for _, n := range pgEncNames {
		if cleanEncName(n) == key {
			return n
		}
	}
	if canonical, ok := pgEncAliases[key]; ok {
		return canonical
	}
	return ""
}

// checkClientEncoding is the CheckFn for the client_encoding GUC. It validates
// that the value resolves to a known PG encoding name. Unlike
// catalog.ValidServerEncodingName (which rejects client-only encodings like
// SJIS/BIG5), this accepts ALL valid PG encodings — client_encoding is per-
// connection and can be set to any encoding the client supports.
// goopg does not do actual encoding conversion; this is pure name validation
// so that invalid encoding names are rejected with the correct SQLSTATE
// (22023 / ERRCODE_INVALID_PARAMETER_VALUE) rather than silently accepted.
// M0122-0008.
func checkClientEncoding(value string) error {
	if encodingNameToCanonical(value) == "" {
		return fmt.Errorf("invalid value for parameter %q: %q",
			"client_encoding", value)
	}
	return nil
}
