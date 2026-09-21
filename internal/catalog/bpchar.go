package catalog

import (
	"strings"
	"unicode/utf8"
)

// PadBpchar blank-pads s out to the character width t declares, reproducing the
// "blank pad the string if necessary" step of upstream's bpchar_input
// (postgres/src/backend/utils/adt/varchar.c).
//
// Since M0143-0007b (2026-09-22) goopg stores a width-carrying bpchar PADDED,
// as upstream does — `coerceTextLikeDatum` (internal/executor/codec.go) applies
// this function, and `ToastLargeColumnsIfNeeded` applies it BEFORE deciding
// whether to toast, which is upstream's pad-at-input order. This function is
// therefore a NO-OP on anything goopg writes today.
//
// It is still called at every render boundary, and must stay called: heap
// images written BEFORE that change are trimmed, and those rows must keep
// rendering at the declared width. Upstream's bpcharout is a bare
// TextDatumGetCString that trims nothing, and bpcharsend IS textsend, so every
// boundary that renders a bpchar column's value carries the full N characters:
// a `SELECT`'s DataRow, `COPY … TO` in text/CSV/binary, and a pgoutput change
// message. Measured against PG 18.3 on a `char(10)` column holding 'ab':
// `SELECT c` returns 10 bytes, `COPY TO` (text) writes 10, `COPY TO (FORMAT
// binary)` writes a length-10 field.
//
// Note which functions do NOT pad: `length()` is bpcharlen and STRIPS trailing
// blanks (`length('x'::char(4096))` is 1), while `octet_length()` is
// bpcharoctetlen and reports the padded size. Same type, opposite treatment,
// because upstream defines them differently.
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
