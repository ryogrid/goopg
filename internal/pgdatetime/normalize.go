// Package pgdatetime holds the PostgreSQL-faithful part of date/time *input*
// field decoding that goopg's per-site Go `time.Parse` layouts cannot express.
//
// PostgreSQL does not parse date/time input against fixed layouts. `date_in`
// runs ParseDateTime() to split the string into fields and then DecodeDate() /
// DecodeNumber() (postgres/src/backend/utils/adt/datetime.c) to interpret each
// numeric run on its own, so a month or day written with a single digit is
// accepted exactly like a zero-padded one: '2002-5-1' and '2002-05-01' are the
// same date. Go's layout parser is the opposite — "2006-01-02" demands two
// digits for month and day, and "15:04:05" demands two for minute and second.
//
// goopg parses date/time text with Go layouts at a dozen call sites (the
// executor cast path, the COPY TEXT reader, the cross-kind comparison coercion,
// pg_input_is_valid). Every one of them therefore rejected the unpadded
// spelling, and — because the comparison path treats a failed coercion as
// "not equal" rather than an error — `d_date = '2002-5-01'` silently matched
// zero rows instead of raising. That was M0125-0007: three TPC-DS queries
// (Q16/Q94/Q95) returned a wrong answer with no diagnostic.
//
// NormalizeInput is the one shared place that reconciles the two models: it
// rewrites the ISO-ordered numeric spellings PostgreSQL accepts into the
// zero-padded canonical form the Go layouts expect, and leaves everything else
// byte-identical so the existing layout tables keep deciding what is valid.
// It performs NO range validation — '2002-13-01' normalises unchanged and the
// downstream parser still rejects it — because PostgreSQL likewise defers field
// validation to ValidateDate() after decoding.
//
// Deliberately NOT handled here (each is a separate, still-unimplemented gap;
// see .ralph/deferral_ledger.md for M0125-0007): textual month names
// ('2002-May-1', 'May 1, 2002'), the DateStyle-dependent MDY/DMY field orders
// that a 1-or-2-digit leading field selects ('02-5-1', '5-1-2002'), the
// run-together form ('20020501'), '/' separators, the 3-digit day-of-year
// field, 2-digit years, and the BC era suffix.
package pgdatetime

import "strings"

// NormalizeInput rewrites an ISO-ordered numeric date/time input string into
// the zero-padded spelling Go's fixed layouts accept, mirroring what
// PostgreSQL's field-at-a-time decoder does implicitly.
//
//	"2002-5-1"            -> "2002-05-01"
//	"2002-5-1 3:4:5"      -> "2002-05-01 03:04:05"
//	"2002-05-01 10:00"    -> "2002-05-01 10:00:00"  (DecodeTime: no seconds = 0)
//	"2002-5-1T3:4:5.25Z"  -> "2002-05-01T03:04:05.25Z"
//	"3:4:5 PM"            -> "03:04:05 PM"
//	" 2002-05-01 "        -> "2002-05-01"     (PG trims surrounding space)
//	"2002-May-1"          -> "2002-May-1"     (unchanged; not our subset)
//
// Anything that is not recognisably an ISO numeric date and/or time is returned
// with only surrounding whitespace removed, so a caller can use NormalizeInput
// unconditionally in front of an existing layout table.
func NormalizeInput(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return trimmed
	}

	// Split a leading date token from the remainder on the first ' ' or 'T'.
	// PostgreSQL's ParseDateTime treats whitespace as a field break; 'T' is the
	// RFC 3339 separator that several of goopg's layouts use.
	datePart := trimmed
	sepIdx := -1
	for i := 0; i < len(trimmed); i++ {
		if c := trimmed[i]; c == ' ' || c == 'T' {
			datePart, sepIdx = trimmed[:i], i
			break
		}
	}

	if padded, ok := padDateFields(datePart); ok {
		if sepIdx < 0 {
			return padded
		}
		return padded + trimmed[sepIdx:sepIdx+1] + padTimeFields(trimmed[sepIdx+1:])
	}

	// No leading ISO date — the whole string may still start with a bare
	// time-of-day ("3:4:5", "3:4:5-07", "3:4:5 PM").
	return padTimeFields(trimmed)
}

// padDateFields zero-pads the three numeric fields of an ISO "Y-M-D" token.
//
// The leading field must carry at least three digits. That is not a stylistic
// choice: it is DecodeNumber's own rule (`if (flen >= 3 || DateOrder ==
// DATEORDER_YMD)` — datetime.c), the point at which PostgreSQL stops guessing
// and commits to reading the field as a year. With one or two digits the
// meaning depends on the DateStyle GUC ('02-5-1' is 2001-02-05 under the
// default MDY order), which goopg does not model, so those forms are left alone
// rather than silently assigned a possibly-wrong reading.
//
// Month and day are capped at two digits for the same reason: a three-digit
// second field is PostgreSQL's day-of-year form, not a month.
func padDateFields(s string) (string, bool) {
	y, i, ok := digitRun(s, 0)
	if !ok || i >= len(s) || s[i] != '-' {
		return s, false
	}
	m, j, ok := digitRun(s, i+1)
	if !ok || j >= len(s) || s[j] != '-' {
		return s, false
	}
	d, k, ok := digitRun(s, j+1)
	if !ok || k != len(s) {
		return s, false
	}
	if len(y) < 3 || len(m) > 2 || len(d) > 2 {
		return s, false
	}
	if len(y) >= 4 && len(m) == 2 && len(d) == 2 {
		return s, true // already canonical — no allocation
	}
	var b strings.Builder
	b.Grow(len(s) + 3)
	writePadded(&b, y, 4)
	b.WriteByte('-')
	writePadded(&b, m, 2)
	b.WriteByte('-')
	writePadded(&b, d, 2)
	return b.String(), true
}

// padTimeFields zero-pads the leading "h:m[:s]" of a time-of-day token and
// returns the rest of the string untouched, so fractional seconds, numeric
// offsets, AM/PM markers and timezone names all survive verbatim. A string
// that does not begin with a numeric time is returned unchanged.
//
// An ABSENT seconds field is supplied as ":00" rather than left out. PostgreSQL
// decodes a time-of-day field by field (DecodeTime, datetime.c): it requires
// hour and minute, and only reads seconds `if (*cp == ':')`, leaving tm_sec = 0
// otherwise — so `10:00` IS `10:00:00`, and `2020-01-01 10:00` is a perfectly
// ordinary timestamp. goopg's layout tables are not uniform about it (the typed
// -literal path in expr.go lists "2006-01-02 15:04", parseCopyTimestamp does
// not), which is how an INSERT of '2020-01-01 10:00' into a `timestamp` column
// raised 22007 while the same text as a literal parsed. Supplying the field here
// makes every call site agree, the same way the padding above does.
//
// Two spellings PG accepts are deliberately NOT rewritten, because guessing
// would trade a loud error for a silently wrong time (ledger, M0119-0006):
// `10:00.5`, where PG's DecodeNumberField reads the fractional field as
// MM:SS.frac (`00:10:00.5`, NOT `10:00:00.5`), and `10::00`, an empty minute
// field PG's field splitter tolerates. A trailing empty seconds field (`10:00:`)
// IS handled, since it is unambiguously "no seconds".
func padTimeFields(s string) string {
	h, i, ok := digitRun(s, 0)
	if !ok || len(h) > 2 || i >= len(s) || s[i] != ':' {
		return s
	}
	m, j, ok := digitRun(s, i+1)
	if !ok || len(m) > 2 {
		return s
	}
	sec, rest := "", s[j:]
	secAbsent := false
	switch {
	case j >= len(s):
		secAbsent = true // "10:00"
	case s[j] == ':':
		var k int
		if sec, k, ok = digitRun(s, j+1); ok && len(sec) <= 2 {
			rest = s[k:]
		} else if j+1 == len(s) {
			sec, rest, secAbsent = "", "", true // "10:00:" — empty trailing field
		} else {
			sec = "" // malformed seconds: leave it for the parser to reject
		}
	case s[j] == '.':
		// "10:00.5": a fractional field after the SECOND numeric run means
		// something else entirely to PG — not ours to normalise.
	default:
		secAbsent = true // "10:00+05", "10:00 PM", "10:00Z"
	}
	if secAbsent {
		sec = "00"
	}
	if len(h) == 2 && len(m) == 2 && len(sec) == 2 && !secAbsent {
		return s // already canonical
	}
	var b strings.Builder
	b.Grow(len(s) + 3)
	writePadded(&b, h, 2)
	b.WriteByte(':')
	writePadded(&b, m, 2)
	if sec != "" {
		b.WriteByte(':')
		writePadded(&b, sec, 2)
	}
	b.WriteString(rest)
	return b.String()
}

// digitRun returns the maximal run of ASCII digits starting at start, the index
// just past it, and whether the run was non-empty.
func digitRun(s string, start int) (string, int, bool) {
	i := start
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return "", start, false
	}
	return s[start:i], i, true
}

// writePadded writes v left-padded with '0' to at least width bytes. A run
// already at or beyond width is written verbatim, so a five-digit year keeps
// all five digits.
func writePadded(b *strings.Builder, v string, width int) {
	for n := len(v); n < width; n++ {
		b.WriteByte('0')
	}
	b.WriteString(v)
}
