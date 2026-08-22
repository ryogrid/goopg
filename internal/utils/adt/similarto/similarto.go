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

// ErrTooManyQuoteSeparators is returned by ConvertSubstring when a
// SUBSTRING(... SIMILAR pattern ESCAPE escape) pattern contains more than
// two escape-double-quote part separators. PG oracle: regexp.c:940-944 —
// ERRCODE_INVALID_USE_OF_ESCAPE_CHARACTER (2200C), no errposition (matches
// PG, which raises this ereport with no cursor location).
var ErrTooManyQuoteSeparators = errors.New("SQL regular expression may not contain more than two escape-double-quote separators")

// Convert rewrites a SQL SIMILAR TO pattern into a POSIX ERE, anchored to
// match the whole string (`^(?: ... )$`, PG oracle: regexp.c:809-866). escape
// is the escape string: DefaultEscape when the SQL source had no ESCAPE
// clause, "" to mean no escape character at all (`ESCAPE ''`), or a single
// (possibly multi-byte) character validated by ValidateEscape beforehand —
// callers must call ValidateEscape first, Convert does not re-validate.
//
// Not ported here: the escape-double-quote ("...#"...#"...") part-separator
// convention (regexp.c:920-953) that PG's similar_escape_internal applies
// unconditionally to both SIMILAR TO and SUBSTRING callers. goopg gates it
// behind ConvertSubstring's substringMode instead, so Convert's behavior for
// existing SIMILAR TO callers is unchanged: any '"' byte in a SIMILAR TO
// pattern falls through to the generic "escaped character" handling below
// (`\"`), never the part-separator rewrite. M0134-0070.
func Convert(pattern, escape string) string {
	// substringMode is always false here, so convert never returns a
	// non-nil error (the only error path is gated on substringMode) —
	// discard it. See convert's doc comment for the shared state machine.
	out, _ := convert(pattern, escape, false)
	return out
}

// ConvertSubstring rewrites a SQL:1999/2003 SUBSTRING(... SIMILAR pattern
// ESCAPE escape) pattern into a POSIX ERE, additionally honoring the
// escape-double-quote ("...#"...#"...") part-separator convention (PG
// oracle: regexp.c:920-953, :1033-1063) that plain SIMILAR TO's Convert does
// not implement. Zero separators yields the same anchored-whole-match ERE as
// Convert (no capturing group — SUBSTRING then returns the whole match).
// One separator makes part2 (the text after the separator) a capturing
// group with an empty part3. Two separators produce
// "^(?:part1){1,1}?(part2){1,1}(?:part3)$" with part2 captured. More than
// two separators is ErrTooManyQuoteSeparators (SQLSTATE 2200C).
func ConvertSubstring(pattern, escape string) (string, error) {
	return convert(pattern, escape, true)
}

// convert is the shared state machine behind Convert and ConvertSubstring
// (PG oracle: regexp.c:768-1063, similar_escape_internal). When
// substringMode is true, an escaped double-quote outside any bracket
// expression is treated as an escape-double-quote part separator
// (regexp.c:920-953) instead of a plain escaped character, and the function
// can return ErrTooManyQuoteSeparators; when false (Convert's case), '"' is
// never special and the returned error is always nil.
func convert(pattern, escape string, substringMode bool) (string, error) {
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
	nquotes := 0 // escape-double-quote separators seen so far; substringMode only

	for _, c := range pattern {
		switch {
		case afterEscape:
			if substringMode && c == '"' && bracketDepth < 1 {
				// Escape-double-quote part separator. PG oracle:
				// regexp.c:920-953.
				switch nquotes {
				case 0:
					b.WriteString("){1,1}?(")
				case 1:
					b.WriteString("){1,1}(?:")
				default:
					return "", ErrTooManyQuoteSeparators
				}
				nquotes++
			} else {
				// Any character may be escaped; emitted as \<char> verbatim.
				// PG oracle: regexp.c:954-970.
				b.WriteByte('\\')
				b.WriteRune(c)
				charClassPos = 3
			}
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

	// PG oracle: regexp.c:1033-1063 — the trailer is unconditionally ")$"
	// regardless of nquotes; it closes whichever group is currently open
	// (the initial "^(?:" when nquotes==0, part2's capturing "(" when
	// nquotes==1, or part3's "(?:" when nquotes==2), which is exactly what
	// produces the documented part1/part2/part3 shapes without any
	// nquotes-based special-casing here.
	b.WriteString(")$")
	return b.String(), nil
}
