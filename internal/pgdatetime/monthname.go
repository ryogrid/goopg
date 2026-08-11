package pgdatetime

import "strings"

// monthNameValue is the MONTH subset of PostgreSQL's datetktbl
// (postgres/src/backend/utils/adt/datetime.c) — the 21 spellings DecodeSpecial
// resolves to a month number. Lookup is case-insensitive upstream (the table is
// searched with a lowercased copy of the field), and there is no "first three
// letters" rule: the token must match a table entry exactly, which is why
// 'sept' works but 'septem' does not.
var monthNameValue = map[string]int{
	"jan": 1, "january": 1,
	"feb": 2, "february": 2,
	"mar": 3, "march": 3,
	"apr": 4, "april": 4,
	"may": 5,
	"jun": 6, "june": 6,
	"jul": 7, "july": 7,
	"aug": 8, "august": 8,
	"sep": 9, "sept": 9, "september": 9,
	"oct": 10, "october": 10,
	"nov": 11, "november": 11,
	"dec": 12, "december": 12,
}

// normalizeTextualMonthDate rewrites a leading date written with a TEXTUAL
// month into the zero-padded numeric "YYYY-MM-DD" spelling the Go layout tables
// downstream accept, and returns whatever follows it (a time of day and/or a
// zone) untouched.
//
//	"2002-May-1"          -> "2002-05-01", ""
//	"May 1, 2002 10:20"   -> "2002-05-01", " 10:20"
//	"1-May-02"            -> "2002-05-01", ""      (2-digit year windowed)
//
// # Why the month's POSITION does not matter
//
// DecodeDate (datetime.c) splits the token into alnum runs and then runs TWO
// passes: "look first for text fields, since that will be unambiguous month",
// and only afterwards "pick up remaining numeric fields". So the month is
// already in fmask when the first numeric run is decoded, no matter where it
// was written, and 'May 1 2002', '1-May-2002' and '2002-May-1' all take the
// identical path through DecodeNumber.
//
// # How the two numeric fields are assigned
//
// DecodeNumber's `case DTK_M(MONTH)` arm (the first numeric run, month already
// known) reads the run as a YEAR when `flen >= 3 || DateOrder == DATEORDER_YMD`
// and as a DAY otherwise; the second run then lands in `case DTK_M(YEAR) |
// DTK_M(MONTH)` (day) or `case DTK_M(MONTH) | DTK_M(DAY)` (year). Upstream's
// own comment names the result: "We want to support the variants MON-DD-YYYY,
// DD-MON-YYYY, and YYYY-MON-DD as unambiguous inputs."
//
// goopg does not model the DateOrder GUC on input at all (see padDateFields),
// so only the DateOrder-INDEPENDENT readings are reproduced here — which is
// every one of those three variants, because the textual month is what removes
// the ambiguity. The two order-dependent arms are deliberately absent:
// DecodeNumber's YMD branch (which would read a 1-or-2-digit LEADING field as a
// year) and the `flen >= 3 && *is2digits` swap that repairs it, both of which
// are reachable only under `DateOrder == DATEORDER_YMD`. Under the MDY default
// goopg assumes, '02-May-1' is day 2 of year 1 → 2001-05-02, and that is what
// this produces.
//
// A year field of one or two digits is windowed onto 1970..2069 exactly as
// expandRunTogetherDate does it, and for the same reason: DecodeNumber sets
// *is2digits for `flen <= 2` and ValidateDate() applies the window later —
// unless the era suffix already claimed the value, hence bc.
//
// ok is false for anything that is not this shape, so every other spelling
// keeps whatever behaviour it had before.
func normalizeTextualMonthDate(s string, bc bool) (dateToken, rest string, ok bool) {
	if s == "" || !isAlnumByte(s[0]) {
		return "", "", false
	}

	var nums []string
	month := 0
	i, end := 0, 0
	for len(nums) < 2 || month == 0 {
		for i < len(s) && !isAlnumByte(s[i]) {
			// Only date separators may appear inside the date token. PG's own
			// splitter skips ANY non-alnum run, but it only ever sees a token
			// that ParseDateTime already classified as a date; scanning the raw
			// input here means ':' would happily glue a time of day onto a
			// month name ('10:00 May' is a time plus a month to PG — an error —
			// not the date 2000-05-10).
			if !isDateSeparatorByte(s[i]) {
				return "", "", false
			}
			i++
		}
		if i >= len(s) {
			return "", "", false
		}
		start := i
		if isDigitByte(s[i]) {
			for i < len(s) && isDigitByte(s[i]) {
				i++
			}
			if len(nums) == 2 {
				return "", "", false // a third numeric run: not this shape
			}
			nums = append(nums, s[start:i])
		} else {
			for i < len(s) && isAlphaByte(s[i]) {
				i++
			}
			if month != 0 {
				return "", "", false // two month names: DecodeDate's `fmask & dmask` reject
			}
			m, isMonth := monthNameValue[strings.ToLower(s[start:i])]
			if !isMonth {
				return "", "", false
			}
			month = m
		}
		end = i
	}

	yearStr, dayStr := nums[0], nums[1]
	if len(nums[0]) < 3 {
		yearStr, dayStr = nums[1], nums[0]
	}
	if len(dayStr) > 2 {
		// DecodeNumber would take it as the day and let ValidateDate reject it,
		// but the emitted token has to stay the fixed-width shape
		// DateTokenMonthDay reads, so leave the input to the caller's parser.
		return "", "", false
	}
	if len(yearStr) <= 2 && !bc {
		yearStr = windowTwoDigitYear(yearStr)
	}

	// What follows the date must be a fresh field, i.e. start with whitespace.
	// The ISO 'T' separator is NOT accepted after a textual month, and that is
	// upstream's behaviour rather than an omission: ParseDateTime hands
	// '2002-May-1T10:20:30' to DecodeDate as one field, whose splitter then
	// reads '1T10' as a digit run followed by an alpha run that is no month —
	// PG answers "invalid input syntax for type timestamp" where the identical
	// text with a numeric month parses.
	if rest := s[end:]; rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return "", "", false
	}

	var b strings.Builder
	b.Grow(len(s) + 4)
	writePadded(&b, yearStr, 4)
	b.WriteByte('-')
	b.WriteByte(byte('0' + month/10))
	b.WriteByte(byte('0' + month%10))
	b.WriteByte('-')
	writePadded(&b, dayStr, 2)
	return b.String(), s[end:], true
}

// joinNormalizedDate re-attaches the remainder normalizeTextualMonthDate split
// off, running the same time-of-day canonicalisation the numeric date arm of
// normalizeInput applies to its own remainder. The remainder is always either
// empty or space-separated (see the refusal at the end of
// normalizeTextualMonthDate), so there is no 'T' case here.
func joinNormalizedDate(dateToken, rest string) string {
	r := strings.TrimLeft(rest, " \t")
	if r == "" {
		return dateToken
	}
	return dateToken + " " + padTimeFields(canonicalZulu(r))
}

// windowTwoDigitYear applies ValidateDate()'s "process 1 or 2-digit input as
// 1970-2069 AD" window to a 1-or-2-digit year run. Shared in spirit with
// expandRunTogetherDate's inline copy, which windows a run-together year.
func windowTwoDigitYear(year string) string {
	n := 0
	for i := 0; i < len(year); i++ {
		n = n*10 + int(year[i]-'0')
	}
	if n < 70 {
		n += 2000
	} else {
		n += 1900
	}
	return string([]byte{
		byte('0' + n/1000), byte('0' + n/100%10),
		byte('0' + n/10%10), byte('0' + n%10),
	})
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

func isAlphaByte(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isAlnumByte(c byte) bool { return isDigitByte(c) || isAlphaByte(c) }

// isDateSeparatorByte lists the separators that may sit between the fields of a
// date token: the three DecodeDateTime itself uses to recognise a field as a
// date ('-', '/', '.'), plus the whitespace and comma its field splitter breaks
// on ('May 1, 2002').
func isDateSeparatorByte(c byte) bool {
	switch c {
	case '-', '/', '.', ',', ' ', '\t':
		return true
	}
	return false
}
