package catalog

// pgConvEncNames maps a pg_enc integer ID to its canonical encoding name,
// mirroring pg_enc2name_tbl in src/common/encnames.c (DEF_ENC2NAME(name,
// codepage) → name). The slice index IS the encoding ID. This is the same table
// initdb carries (internal/initdb/encoding.go pgEncNames); it is duplicated here
// — rather than shared — because initdb cannot be imported from catalog without
// a cycle, and the encoding-ID↔name mapping is an immutable PostgreSQL constant.
// Used by EncodingIDToName (pg_encoding_to_char) and EncodingNameToID
// (CREATE CONVERSION FOR 'x' TO 'y'). DU-002 slice 399 (M0119-0004).
var pgConvEncNames = [...]string{
	"SQL_ASCII", "EUC_JP", "EUC_CN", "EUC_KR", "EUC_TW", "EUC_JIS_2004",
	"UTF8", "MULE_INTERNAL", "LATIN1", "LATIN2", "LATIN3", "LATIN4",
	"LATIN5", "LATIN6", "LATIN7", "LATIN8", "LATIN9", "LATIN10",
	"WIN1256", "WIN1258", "WIN866", "WIN874", "KOI8R", "WIN1251",
	"WIN1252", "ISO_8859_5", "ISO_8859_6", "ISO_8859_7", "ISO_8859_8",
	"WIN1250", "WIN1253", "WIN1254", "WIN1255", "WIN1257", "KOI8U",
	"SJIS", "BIG5", "GBK", "UHC", "GB18030", "JOHAB", "SHIFT_JIS_2004",
}

// EncodingIDToName returns the canonical encoding name for a pg_enc integer ID,
// or "" for an out-of-range ID. Mirrors pg_encoding_to_char in encnames.c — the
// SQL builtin pg_encoding_to_char(int4) that pg_dump's dumpConversion calls on
// pg_conversion.conforencoding / contoencoding. DU-002 slice 399.
func EncodingIDToName(id int32) string {
	if id < 0 || int(id) >= len(pgConvEncNames) {
		return ""
	}
	return pgConvEncNames[id]
}

// cleanConvEncodingName lowercases an encoding name and strips every
// non-alphanumeric character, mirroring clean_encoding_name in encnames.c. This
// is what lets "UTF-8", "utf_8", and "UTF8" all resolve to the same key.
func cleanConvEncodingName(name string) string {
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

// pgConvEncAliases maps a cleaned (lowercased, punctuation-stripped) encoding
// alias to its canonical pg_enc name, mirroring pg_encname_tbl in encnames.c.
// PostgreSQL's pg_char_to_encoding does a binary search over that table — the
// canonical names themselves are *also* entries (e.g. "utf8", "latin1"), so this
// table is the complete name→ID source, not just the non-canonical extras. The
// canonical names from pgConvEncNames are still tried first by EncodingNameToID
// as a fast path; this map adds the aliases PostgreSQL accepts that do not clean
// to a canonical name (e.g. "unicode"→UTF8, "windows1252"→WIN1252, "mskanji"→
// SJIS, "iso88591"→LATIN1). Cleaned keys, canonical-name values. DU-002 slice
// 400 (closes slice-399 deferral (a)).
var pgConvEncAliases = map[string]string{
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

// EncodingNameToID resolves an encoding name (any case, with arbitrary
// punctuation that clean_encoding_name strips) to its pg_enc integer ID, or -1
// if unknown. It first matches a canonical pg_enc2name name, then falls back to
// the pg_encname_tbl alias table (so "unicode" → UTF8, "windows1252" → WIN1252,
// "mskanji" → SJIS resolve just as pg_char_to_encoding does in encnames.c).
// DU-002 slice 400.
func EncodingNameToID(name string) int32 {
	key := cleanConvEncodingName(name)
	for i, n := range pgConvEncNames {
		if cleanConvEncodingName(n) == key {
			return int32(i)
		}
	}
	if canonical, ok := pgConvEncAliases[key]; ok {
		for i, n := range pgConvEncNames {
			if n == canonical {
				return int32(i)
			}
		}
	}
	return -1
}
