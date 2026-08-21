// Package similarto ports PostgreSQL's similar_escape_internal (PG oracle:
// postgres/src/backend/utils/adt/regexp.c:768-1063), the SQL "SIMILAR TO"
// pattern → POSIX ERE rewrite. It is a leaf package (no goopg internal
// imports) so both internal/parser (constant-fold at parse time, M0134-0070)
// and internal/executor (runtime evaluation, and any future non-constant
// SIMILAR TO fallback) can share exactly one copy of the conversion logic —
// see CLAUDE.md's "no forked sibling logic" rule.
package similarto

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// DefaultEscape is the escape string used when a SIMILAR TO pattern has no
// explicit ESCAPE clause. PG oracle: regexp.c:785-790 ("No ESCAPE clause
// provided; default to backslash as escape").
const DefaultEscape = `\`

// ErrInvalidEscape is returned by ValidateEscape when the escape string is
// more than one character. PG oracle: regexp.c:797-806 —
// ERRCODE_INVALID_ESCAPE_SEQUENCE (22025), hint "Escape string must be empty
// or one character."
var ErrInvalidEscape = errors.New("invalid escape string")

// ValidateEscape checks an explicit ESCAPE clause operand per PG's
// similar_escape_internal: empty means "no escape character" (always valid),
// and anything longer than one character is ErrInvalidEscape. Multi-byte
// escape characters count as one character (PG oracle: regexp.c uses
// pg_mbstrlen_with_len, not byte length).
func ValidateEscape(escape string) error {
	if utf8.RuneCountInString(escape) > 1 {
		return ErrInvalidEscape
	}
	return nil
}

// Convert rewrites a SQL SIMILAR TO pattern into a POSIX ERE, anchored to
// match the whole string (`^(?: ... )$`, PG oracle: regexp.c:809-866). escape
// is the escape string: DefaultEscape when the SQL source had no ESCAPE
// clause, "" to mean no escape character at all (`ESCAPE ''`), or a single
// (possibly multi-byte) character validated by ValidateEscape beforehand —
// callers must call ValidateEscape first, Convert does not re-validate.
//
// Not ported: the escape-double-quote ("...#"...#"...") part-separator
// convention (regexp.c:920-953) used only by SUBSTRING(... SIMILAR ...
// ESCAPE ...), a separate, currently unimplemented goopg feature — SIMILAR
// TO never produces that convention, so any '"' byte in a SIMILAR TO pattern
// falls through to the generic "escaped character" / bracket-expression /
// literal-copy handling below, matching regexp.c's behavior for that case
// exactly (the '"'-is-special branch is gated on nquotes/bracket_depth
// bookkeeping that only SUBSTRING's caller ever populates meaningfully).
// M0134-0070.
func Convert(pattern, escape string) string {
	var hasEscape bool
	var escRune rune
	if escape != "" {
		hasEscape = true
		escRune, _ = utf8.DecodeRuneInString(escape)
	}

	var b strings.Builder
	b.Grow(len(pattern)*3 + 8)
	b.WriteString("^(?:")

	bracketDepth := 0 // square bracket nesting level
	charClassPos := 0 // position inside a character class; see regexp.c:843-850
	afterEscape := false

	for _, c := range pattern {
		switch {
		case afterEscape:
			// Any character may be escaped; emitted as \<char> verbatim.
			// PG oracle: regexp.c:954-970 (minus the escape-double-quote
			// branch, see doc comment above).
			b.WriteByte('\\')
			b.WriteRune(c)
			charClassPos = 3
			afterEscape = false
		case hasEscape && c == escRune:
			// SQL escape character: dropped from output, next char escapes.
			afterEscape = true
		case bracketDepth > 0:
			// Inside a bracket expression: copy verbatim (doubling a literal
			// backslash, which isn't the SQL escape char here), tracking
			// nesting to find the real closing ']'. PG oracle: regexp.c:977-1032.
			if c == '\\' {
				b.WriteByte('\\')
			}
			b.WriteRune(c)
			switch {
			case c == ']' && charClassPos > 2:
				bracketDepth--
			case c == '[':
				bracketDepth++
				charClassPos = 3
			case c == '^':
				charClassPos++
			default:
				charClassPos = 3
			}
		case c == '[':
			b.WriteRune(c)
			bracketDepth = 1
			charClassPos = 1
		case c == '%':
			b.WriteString(".*")
		case c == '_':
			b.WriteByte('.')
		case c == '(':
			// SQL grouping paren → non-capturing regex group; only the
			// opening paren is rewritten, ')' passes through unchanged.
			b.WriteString("(?:")
		case c == '\\' || c == '.' || c == '^' || c == '$':
			b.WriteByte('\\')
			b.WriteRune(c)
		default:
			b.WriteRune(c)
		}
	}

	b.WriteString(")$")
	return b.String()
}
