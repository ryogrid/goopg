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

// EncodingNameToID resolves a canonical encoding name (any case, with arbitrary
// punctuation that clean_encoding_name strips) to its pg_enc integer ID, or -1
// if unknown. It recognizes the 42 canonical pg_enc2name names; full alias
// resolution (e.g. "unicode" → UTF8, "windows1252" → WIN1252, the pg_encname_tbl
// in encnames.c) is deferred — pg_dump always emits the canonical name, so a
// dump→reload→dump cycle round-trips with canonical-name-only resolution.
// DU-002 slice 399.
func EncodingNameToID(name string) int32 {
	key := cleanConvEncodingName(name)
	for i, n := range pgConvEncNames {
		if cleanConvEncodingName(n) == key {
			return int32(i)
		}
	}
	return -1
}
