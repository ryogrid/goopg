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
// that a 1-or-2-digit leading field selects ('02-5-1', '5-1-2002'), '/'
// separators, and the 3-digit day-of-year field. (The BC era suffix is handled
// by era.go, and the run-together date form by NormalizeDateTimeInput below.)
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
	return normalizeInput(s, false, false)
}

// NormalizeDateTimeInput is NormalizeInput in DecodeDateTime's context — the one
// PostgreSQL uses for `date`, `timestamp` and `timestamptz` input, as opposed to
// DecodeTimeOnly's, which NormalizeInput models.
//
// The single difference is the RUN-TOGETHER numeric date. PG's field splitter
// hands a separator-less digit run to DecodeNumberField (datetime.c), which
// reads it as a date whenever the date is not yet complete — "Start from end and
// consider first 2 as Day, next 2 as Month, and the rest as Year":
//
//	"20200101"        -> 2020-01-01
//	"200101"          -> 2020-01-01   (2-digit year, see below)
//	"2020101"         -> 0202-01-01   (a 3-digit year is just "the rest")
//	"20200101 040506" -> 2020-01-01 04:05:06
//
// Only DecodeDateTime reaches that arm: DecodeTimeOnly starts with DTK_DATE_M
// already set in fmask, so the same six digits are hhmmss there ('040506'::time
// is 04:05:06 while '040506'::date is 2004-05-06). That is why this is a second
// entry point rather than a widening of NormalizeInput — the callers that decode
// a bare `time` / `timetz` must keep the old reading.
//
// bc reproduces ValidateDate()'s ordering: the 2-digit-year windowing (< 70 is
// 20xx, otherwise 19xx) is an `else if` AFTER the BC branch, so an era suffix
// suppresses it — '200101 BC' is 0020-01-01 BC, not 2020-01-01 BC. Callers split
// the era off with SplitEra before normalising, so they must pass its answer in.
//
// No range validation happens here either: '20201301' normalises to 2020-13-01
// and the caller's parser still rejects it.
func NormalizeDateTimeInput(s string, bc bool) string {
	return normalizeInput(s, true, bc)
}

func normalizeInput(s string, runTogetherDate, bc bool) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return trimmed
	}

	// Split a leading date token from the remainder on the first ' ', 'T' or
	// 't'. PostgreSQL's ParseDateTime treats whitespace as a field break, and
	// the ISO 8601 'T' as an ordinary one in either case — `2020-01-01t10:00`
	// decodes exactly like `2020-01-01 10:00`. Go's layouts cannot express
	// "either case", so the separator is folded to upper 'T' here.
	datePart := trimmed
	sepIdx := -1
	for i := 0; i < len(trimmed); i++ {
		if c := trimmed[i]; c == ' ' || c == 'T' || c == 't' {
			datePart, sepIdx = trimmed[:i], i
			break
		}
	}

	padded, ok := padDateFields(datePart)
	if !ok && runTogetherDate {
		padded, ok = expandRunTogetherDate(datePart, bc)
	}
	if ok {
		if sepIdx < 0 {
			return padded
		}
		sep := " "
		if trimmed[sepIdx] != ' ' {
			sep = "T"
		}
		return padded + sep + padTimeFields(canonicalZulu(trimmed[sepIdx+1:]))
	}

	// No leading ISO date — the whole string may still start with a bare
	// time-of-day ("3:4:5", "3:4:5-07", "3:4:5 PM").
	return padTimeFields(canonicalZulu(trimmed))
}

// canonicalZulu folds PostgreSQL's spellings of the UTC zone token onto the one
// spelling Go's `Z07*` layout elements recognise: an uppercase 'Z' attached
// directly to the time. PG reaches 'Z' through datetbl's DTZ entry, which its
// field splitter finds whether it is written 'Z' or 'z' and whether or not
// whitespace precedes it, so `10:00:00Z`, `10:00:00z` and `10:00:00 Z` are the
// same instant to PG but three different strings to time.Parse.
//
// The rewrite is deliberately narrow: the letter must be the last character and
// what remains after dropping it and any trailing space must end in a digit.
// That keeps it off timezone ABBREVIATIONS that merely end in the same letter
// ('10:00:00 NZ' is left for the caller's parser to judge) and off the verbose
// natural-language spelling handled elsewhere.
func canonicalZulu(s string) string {
	if s == "" {
		return s
	}
	if c := s[len(s)-1]; c != 'Z' && c != 'z' {
		return s
	}
	head := strings.TrimRight(s[:len(s)-1], " \t")
	if head == "" {
		return s
	}
	if c := head[len(head)-1]; c < '0' || c > '9' {
		return s
	}
	return head + "Z"
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

// expandRunTogetherDate rewrites a separator-less numeric date token into the
// zero-padded "YYYY-MM-DD" spelling, reproducing DecodeNumberField's date arm
// (postgres/src/backend/utils/adt/datetime.c):
//
//	if (len >= 6) { mday = last 2 digits; mon = the 2 before them; year = the rest }
//
// The token must be digits ONLY: a decimal point sends upstream down the
// fractional-seconds branch instead, which then demands a 4- or 6-digit TIME
// remainder, so '20200101.5' is a syntax error to PG and stays one here.
//
// A year field of exactly two digits sets DecodeNumberField's *is2digits, which
// ValidateDate() later windows onto 1970..2069 — but only when the value is not
// BC (the branches are `else if`), hence the parameter.
//
// Years wider than four digits are emitted verbatim ('202001011' is PG's
// 20200-10-11). Go's "2006" layout element cannot read those back, so the caller
// reports a syntax error where upstream reports the date; every such year is far
// outside goopg's KindTime carrier anyway (see the ledger row for the carrier).
func expandRunTogetherDate(s string, bc bool) (string, bool) {
	digits, i, ok := digitRun(s, 0)
	if !ok || i != len(s) || len(digits) < 6 {
		return s, false
	}
	year := digits[:len(digits)-4]
	if len(year) == 2 && !bc {
		// ValidateDate(): "process 1 or 2-digit input as 1970-2069 AD".
		n := int(year[0]-'0')*10 + int(year[1]-'0')
		if n < 70 {
			n += 2000
		} else {
			n += 1900
		}
		year = string([]byte{
			byte('0' + n/1000), byte('0' + n/100%10),
			byte('0' + n/10%10), byte('0' + n%10),
		})
	}
	var b strings.Builder
	b.Grow(len(digits) + 2)
	writePadded(&b, year, 4)
	b.WriteByte('-')
	b.WriteString(digits[len(digits)-4 : len(digits)-2])
	b.WriteByte('-')
	b.WriteString(digits[len(digits)-2:])
	return b.String(), true
}

// RunTogetherDateIsTimeAmbiguous reports whether s begins with a separator-less
// digit run that BOTH DecodeNumberField arms accept: four digits (hhmm) or six
// (hhmmss) are a time-of-day to DecodeTimeOnly and a date to DecodeDateTime, so
// '040506' is 04:05:06 as a `time` and 2004-05-06 as a `date`.
//
// PostgreSQL never has to choose — transformExpr coerces the unknown literal to
// the target type before the input function runs, so the fmask it starts from
// already says which reading applies. goopg has one text→Datum path that reaches
// the parse with no target type in hand (tryParseStringAs, for a cross-kind
// comparison against a KindTime datum); this predicate lets it keep the
// time-of-day reading for exactly the ambiguous widths instead of guessing.
func RunTogetherDateIsTimeAmbiguous(s string) bool {
	trimmed := strings.TrimSpace(s)
	tok := trimmed
	for i := 0; i < len(trimmed); i++ {
		if c := trimmed[i]; c == ' ' || c == 'T' || c == 't' {
			tok = trimmed[:i]
			break
		}
	}
	digits, i, ok := digitRun(tok, 0)
	return ok && i == len(tok) && (len(digits) == 4 || len(digits) == 6)
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
