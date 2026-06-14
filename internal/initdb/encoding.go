package initdb

import "fmt"

// Encoding-name handling for `goopg init -E/--encoding`, ported from
// PostgreSQL's src/common/encnames.c and src/include/mb/pg_wchar.h.
//
// initdb selects the default encoding for the new cluster's databases. When
// the user passes -E/--encoding, the name is validated and mapped to a pg_enc
// integer ID that lands in pg_database.encoding. goopg's locale handling is
// still fixed at C / UTF8 (the --locale family is a separate, deferred initdb
// option), so when no -E is given the default encoding is UTF8 — the value
// goopg has always written into pg_database. This file adds only the
// name→ID validation/mapping; the locale-derived default and the
// encoding↔locale mismatch checks (check_locale_encoding,
// check_icu_locale_encoding) arrive with the --locale work.

// pgEncSQLASCII completes the pg_enc integer set begun in
// pg_conversion_bootstrap.go (which omitted PG_SQL_ASCII=0 because no
// conversion references it). PG_SQL_ASCII=0 in pg_wchar.h.
const pgEncSQLASCII int32 = 0

// pgEncodingBELast is PG_ENCODING_BE_LAST (= PG_KOI8U) from pg_wchar.h.
// PG_VALID_BE_ENCODING(enc) is `enc >= 0 && enc <= pgEncodingBELast`; encodings
// with a higher ID are client-only (SJIS, BIG5, GBK, UHC, GB18030, JOHAB,
// SHIFT_JIS_2004) and are not legal server-side encodings.
const pgEncodingBELast = 34 // PG_KOI8U

// pgEncNames maps a pg_enc integer ID to its canonical encoding name, mirroring
// pg_enc2name_tbl in encnames.c (DEF_ENC2NAME(name, codepage) → name). The
// slice index IS the encoding ID. Used by pgEncodingToChar for error messages
// and the pg_database canonical-name display.
var pgEncNames = [...]string{
	"SQL_ASCII", "EUC_JP", "EUC_CN", "EUC_KR", "EUC_TW", "EUC_JIS_2004",
	"UTF8", "MULE_INTERNAL", "LATIN1", "LATIN2", "LATIN3", "LATIN4",
	"LATIN5", "LATIN6", "LATIN7", "LATIN8", "LATIN9", "LATIN10",
	"WIN1256", "WIN1258", "WIN866", "WIN874", "KOI8R", "WIN1251",
	"WIN1252", "ISO_8859_5", "ISO_8859_6", "ISO_8859_7", "ISO_8859_8",
	"WIN1250", "WIN1253", "WIN1254", "WIN1255", "WIN1257", "KOI8U",
	"SJIS", "BIG5", "GBK", "UHC", "GB18030", "JOHAB", "SHIFT_JIS_2004",
}

// pgEncnameTbl maps a *cleaned* encoding alias (see cleanEncodingName) to its
// pg_enc integer ID, mirroring pg_encname_tbl in encnames.c. The upstream
// table is a binary-searched array of already-cleaned keys; a Go map gives the
// same lookup semantics. Every alias upstream recognizes is present so the set
// of accepted -E names is byte-identical to initdb's.
var pgEncnameTbl = map[string]int32{
	"abc":          pgEncWIN1258,
	"alt":          pgEncWIN866,
	"big5":         pgEncBIG5,
	"euccn":        pgEncEUCCN,
	"eucjis2004":   pgEncEUCJIS2004,
	"eucjp":        pgEncEUCJP,
	"euckr":        pgEncEUCKR,
	"euctw":        pgEncEUCTW,
	"gb18030":      pgEncGB18030,
	"gbk":          pgEncGBK,
	"iso88591":     pgEncLATIN1,
	"iso885910":    pgEncLATIN6,
	"iso885913":    pgEncLATIN7,
	"iso885914":    pgEncLATIN8,
	"iso885915":    pgEncLATIN9,
	"iso885916":    pgEncLATIN10,
	"iso88592":     pgEncLATIN2,
	"iso88593":     pgEncLATIN3,
	"iso88594":     pgEncLATIN4,
	"iso88595":     pgEncISO88595,
	"iso88596":     pgEncISO88596,
	"iso88597":     pgEncISO88597,
	"iso88598":     pgEncISO88598,
	"iso88599":     pgEncLATIN5,
	"johab":        pgEncJOHAB,
	"koi8":         pgEncKOI8R,
	"koi8r":        pgEncKOI8R,
	"koi8u":        pgEncKOI8U,
	"latin1":       pgEncLATIN1,
	"latin10":      pgEncLATIN10,
	"latin2":       pgEncLATIN2,
	"latin3":       pgEncLATIN3,
	"latin4":       pgEncLATIN4,
	"latin5":       pgEncLATIN5,
	"latin6":       pgEncLATIN6,
	"latin7":       pgEncLATIN7,
	"latin8":       pgEncLATIN8,
	"latin9":       pgEncLATIN9,
	"mskanji":      pgEncSJIS,
	"muleinternal": pgEncMULEINTERNAL,
	"shiftjis":     pgEncSJIS,
	"shiftjis2004": pgEncSHIFTJIS2004,
	"sjis":         pgEncSJIS,
	"sqlascii":     pgEncSQLASCII,
	"tcvn":         pgEncWIN1258,
	"tcvn5712":     pgEncWIN1258,
	"uhc":          pgEncUHC,
	"unicode":      pgEncUTF8,
	"utf8":         pgEncUTF8,
	"vscii":        pgEncWIN1258,
	"win":          pgEncWIN1251,
	"win1250":      pgEncWIN1250,
	"win1251":      pgEncWIN1251,
	"win1252":      pgEncWIN1252,
	"win1253":      pgEncWIN1253,
	"win1254":      pgEncWIN1254,
	"win1255":      pgEncWIN1255,
	"win1256":      pgEncWIN1256,
	"win1257":      pgEncWIN1257,
	"win1258":      pgEncWIN1258,
	"win866":       pgEncWIN866,
	"win874":       pgEncWIN874,
	"win932":       pgEncSJIS,
	"win936":       pgEncGBK,
	"win949":       pgEncUHC,
	"win950":       pgEncBIG5,
	"windows1250":  pgEncWIN1250,
	"windows1251":  pgEncWIN1251,
	"windows1252":  pgEncWIN1252,
	"windows1253":  pgEncWIN1253,
	"windows1254":  pgEncWIN1254,
	"windows1255":  pgEncWIN1255,
	"windows1256":  pgEncWIN1256,
	"windows1257":  pgEncWIN1257,
	"windows1258":  pgEncWIN1258,
	"windows866":   pgEncWIN866,
	"windows874":   pgEncWIN874,
	"windows932":   pgEncSJIS,
	"windows936":   pgEncGBK,
	"windows949":   pgEncUHC,
	"windows950":   pgEncBIG5,
}

// cleanEncodingName lowercases an encoding name and strips every non-alphanumeric
// character, mirroring clean_encoding_name in encnames.c. This is what lets
// "UTF-8", "utf_8", and "UTF8" all resolve to the same alias-table key.
func cleanEncodingName(name string) string {
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

// pgCharToEncoding maps an encoding name (any recognized alias, in any case
// with arbitrary punctuation) to its pg_enc integer ID, or -1 if unknown.
// Mirrors pg_char_to_encoding in encnames.c, including the NAMEDATALEN guard
// (names of 64+ bytes are rejected outright).
func pgCharToEncoding(name string) int32 {
	if name == "" {
		return -1
	}
	if len(name) >= 64 { // NAMEDATALEN
		return -1
	}
	if enc, ok := pgEncnameTbl[cleanEncodingName(name)]; ok {
		return enc
	}
	return -1
}

// pgValidServerEncoding returns the pg_enc ID for name if it is a valid
// server-side encoding, else -1. Mirrors pg_valid_server_encoding in
// encnames.c: recognize the name, then require PG_VALID_BE_ENCODING.
func pgValidServerEncoding(name string) int32 {
	enc := pgCharToEncoding(name)
	if enc < 0 || enc > pgEncodingBELast {
		return -1
	}
	return enc
}

// pgEncodingToChar returns the canonical name for a pg_enc ID, or "" for an
// out-of-range ID. Mirrors pg_encoding_to_char in encnames.c.
func pgEncodingToChar(enc int32) string {
	if enc < 0 || int(enc) >= len(pgEncNames) {
		return ""
	}
	return pgEncNames[enc]
}

// resolveEncoding ports initdb.c get_encoding_id (initdb.c:846). An empty name
// yields the default encoding (UTF8 — goopg's fixed C/UTF8 locale, pending the
// --locale option). A non-empty name is validated as a server-side encoding;
// an unknown name, or a client-only encoding such as SJIS/BIG5, is rejected
// with initdb's exact wording.
func resolveEncoding(name string) (int32, error) {
	if name == "" {
		return pgEncUTF8, nil
	}
	if enc := pgValidServerEncoding(name); enc >= 0 {
		return enc, nil
	}
	return 0, fmt.Errorf("goopg init: %q is not a valid server encoding name", name)
}
