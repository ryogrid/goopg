package catalog

import (
	"strings"
	"unicode/utf8"
)

// PadBpchar blank-pads s out to the character width t declares, reproducing the
// "blank pad the string if necessary" step of upstream's bpchar_input
// (postgres/src/backend/utils/adt/varchar.c).
//
// goopg stores a bpchar value TRIMMED — internal/executor/codec.go's
// coerceTextLikeDatum strips trailing spaces so that the heap image stays
// compact and compareDatum's padding-insensitive bpchar equality holds — where
// upstream stores it PADDED. Upstream's bpcharout is a bare
// TextDatumGetCString that trims nothing, and bpcharsend IS textsend, so every
// boundary that renders a bpchar column's value has to put the padding back:
// a `SELECT`'s DataRow, `COPY … TO` in text/CSV/binary, and a pgoutput change
// message all carry the full N characters on real PG. Measured against PG 18.3
// on a `char(10)` column holding 'ab': `SELECT c` returns 10 bytes, `COPY TO`
// (text) writes 10, `COPY TO (FORMAT binary)` writes a length-10 field.
//
// It lives here, on the package that owns Type, because both internal/executor
// and internal/wal must apply the identical rule and neither may import the
// other (Hard-won Rule #2 — the render siblings cannot be allowed to drift).
//
// Only a type carrying an explicit length modifier pads:
//
//   - a bare `char` (no Args) is pg_type OID 18, a 1-byte internal type that is
//     not bpchar at all;
//   - a bare `bpchar` is upstream typmod -1, and bpchar_input's
//     `atttypmod < VARHDRSZ` arm sets maxlen to the actual string length, i.e.
//     also no padding.
//
// The width counts CHARACTERS, not bytes (upstream measures with
// pg_mbstrlen_with_len before converting maxlen to a byte length), so a
// multibyte value pads by rune count.
func PadBpchar(t Type, s string) string {
	if t.IsArray || len(t.Args) == 0 {
		return s
	}
	switch strings.ToLower(t.Name) {
	case "char", "bpchar", "character":
	default:
		return s
	}
	n := int(t.Args[0])
	if n <= 0 {
		return s
	}
	if c := utf8.RuneCountInString(s); c < n {
		return s + strings.Repeat(" ", n-c)
	}
	return s
}
