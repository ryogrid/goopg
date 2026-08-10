package pgdatetime

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// Errors returned by ParseTimeOfDay. The caller maps them onto goopg's SQLSTATEs:
// ErrTimeBadFormat is 22007 (invalid input syntax), ErrTimeFieldOverflow is
// 22008 (date/time field value out of range) — the same split PostgreSQL makes
// between DTERR_BAD_FORMAT and DTERR_FIELD_OVERFLOW in DateTimeParseError().
var (
	ErrTimeBadFormat     = errors.New("pgdatetime: invalid time format")
	ErrTimeFieldOverflow = errors.New("pgdatetime: time field value out of range")
)

// TimeOfDay is a decoded time-of-day, straight out of field decoding and
// BEFORE the type-level range check. Hour may be 24 (or larger, which the
// caller rejects) and Sec may be 60, exactly as PostgreSQL's DecodeTimeCommon
// leaves them for time_in()/timestamp_in() to validate.
type TimeOfDay struct {
	Hour int
	Min  int
	Sec  int
	Nsec int
}

// ParseTimeOfDay decodes the time-of-day part of a PostgreSQL date/time input
// string the way the server does, replacing a fixed Go layout table.
//
// PostgreSQL never matches time input against layouts. ParseDateTime() splits
// the string into fields and DecodeTimeCommon() / DecodeNumberField()
// (postgres/src/backend/utils/adt/datetime.c) assign a role to each numeric run
// afterwards, which is why all of these are ordinary input:
//
//	"10:00"        -> 10:00:00     (missing seconds are zero)
//	"10::00"       -> 10:00:00     (an empty subfield decodes as 0 — strtoint
//	"10:"          -> 10:00:00      does not advance and leaves the value 0)
//	":10:00"       -> 10:00:00     (leading punctuation is a mere delimiter)
//	"10:00.5"      -> 00:10:00.5   (two fields plus a fraction are MINUTE TO
//	                                SECOND, so the fields SHIFT right)
//	"040506"       -> 04:05:06     (DecodeNumberField: a 6-digit run is hhmmss)
//	"0405"         -> 04:05:00     (a 4-digit run is hhmm)
//	"040506.5"     -> 04:05:06.5   (the fraction is split off FIRST, so the
//	                                remaining run still picks its hhmmss role)
//	"T040506"      -> 04:05:06     (the ISO 8601 date/time separator)
//	"10:00AM"      -> 10:00:00     (the tokenizer breaks alpha off digits, so
//	                                the meridiem needs no space)
//	"12:00 AM"     -> 00:00:00     (hour 12 AM is hour 0 — goopg used to answer
//	                                12:00:00 here, a two-place-value error)
//	"allballs"     -> 00:00:00     (the datetktbl special token)
//
// goopg parsed all of these against the layout table in copy_text.go, which can
// express none of them, so each spelling above was rejected outright — except
// the last two, which were silently decoded WRONG.
//
// The caller keeps ownership of everything that is not field decoding: the date
// prefix, the time zone, and the final range check (hour 24, second 60).
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return TimeOfDay{}, ErrTimeBadFormat
	}

	// The meridiem is a plain DTK_STRING field for PostgreSQL, so it may be
	// space-separated ("4:05 PM") or run into the digits ("4:05PM"): the
	// tokenizer ends a numeric field at the first non-digit anyway.
	s, meridiem, err := splitMeridiem(s)
	if err != nil {
		return TimeOfDay{}, err
	}

	// ParseDateTime ignores punctuation that starts a field ("ignore other
	// punctuation but use as delimiter"), and treats the ISO 8601 'T' as an
	// ordinary field break in either case.
	s = strings.TrimLeft(s, ":.,;-/")
	if len(s) > 0 && (s[0] == 'T' || s[0] == 't') {
		s = s[1:]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return TimeOfDay{}, ErrTimeBadFormat
	}

	var tod TimeOfDay
	if strings.ContainsRune(s, ':') {
		tod, err = decodeTimeColon(s)
	} else if lower := strings.ToLower(s); lower == "allballs" {
		// datetktbl: {"allballs", DTZ, 0} — the zero time, zero zone.
		tod = TimeOfDay{}
	} else {
		tod, err = decodeTimeNumberField(s)
	}
	if err != nil {
		return TimeOfDay{}, err
	}

	// DecodeDateTime's DTK_AM/DTK_PM arms: an hour past 12 with a meridiem is a
	// range error, hour 12 AM is hour 0, and any other AM hour is unchanged.
	switch meridiem {
	case meridiemAM:
		if tod.Hour > 12 {
			return TimeOfDay{}, ErrTimeFieldOverflow
		}
		if tod.Hour == 12 {
			tod.Hour = 0
		}
	case meridiemPM:
		if tod.Hour > 12 {
			return TimeOfDay{}, ErrTimeFieldOverflow
		}
		if tod.Hour < 12 {
			tod.Hour += 12
		}
	}
	return tod, nil
}

const nsecPerDay int64 = 24 * 60 * 60 * 1_000_000_000

// totalNsec is the time of day as a plain offset from midnight. Because hour 24
// and second 60 are both legal decoded fields, the offset can be exactly — but
// never more than — one whole day.
func (t TimeOfDay) totalNsec() int64 {
	return ((int64(t.Hour)*60+int64(t.Min))*60+int64(t.Sec))*1_000_000_000 + int64(t.Nsec)
}

// Overflows reimplements time_overflows() (postgres/src/backend/utils/adt/date.c):
// the fields are range-checked individually — deliberately admitting hour 24 and
// second 60 — and then the TOTAL is checked against 24:00:00, which is what makes
// '24:00:00' and '23:59:60' legal while '24:00:00.5', '23:59:60.5' and '24:00:01'
// are not. Upstream calls it from DecodeDateTime and DecodeTimeOnly, i.e. on the
// timestamp path as well as the time-only one; goopg checked only the latter.
func (t TimeOfDay) Overflows() bool {
	if t.Hour < 0 || t.Hour > 24 ||
		t.Min < 0 || t.Min > 59 ||
		t.Sec < 0 || t.Sec > 60 ||
		t.Nsec < 0 || t.Nsec > 1_000_000_000 {
		return true
	}
	return t.totalNsec() > nsecPerDay
}

// Normalize range-checks a decoded time of day and folds the two spellings that
// carry into the next day into a plain time plus a day count.
//
// PostgreSQL never folds during decoding: DecodeDateTime leaves hour 24 and
// second 60 in the struct, and the carry happens in tm2timestamp(), which simply
// adds date2j(y,m,d) * USECS_PER_DAY to the time-of-day microseconds. So
// '2020-01-01 24:00:00' and '2020-01-01 23:59:60' both come out as
// 2020-01-02 00:00:00, and '2020-01-01 10:00:60' as 2020-01-01 10:01:00 — a leap
// second below the day boundary just rolls the minute.
//
// dayCarry is 1 exactly when the time of day is the full day, and the returned
// TimeOfDay is then midnight. Callers that do NOT compose a date with the time
// must ignore dayCarry: date_in() never calls tm2timestamp, which is why
// '2020-01-01 24:00:00'::date is 2020-01-01 and not the next day.
func (t TimeOfDay) Normalize() (TimeOfDay, int, error) {
	if t.Overflows() {
		return TimeOfDay{}, 0, ErrTimeFieldOverflow
	}
	if t.totalNsec() == nsecPerDay {
		return TimeOfDay{}, 1, nil
	}
	if t.Sec == 60 {
		t.Sec = 0
		t.Min++
		if t.Min == 60 {
			t.Min = 0
			t.Hour++
		}
	}
	return t, 0, nil
}

// CanonicalizeTimeToken rewrites the time-of-day token at the head of s into
// the zero-padded `HH:MM:SS[.ffffff]` spelling a Go layout can parse, consuming
// an AM/PM marker that follows it, and returns the string with the rest (a zone
// suffix, typically) left byte-identical, together with the day carry
// Normalize produced.
//
// The error is ErrTimeBadFormat when the head is not a time PostgreSQL would
// decode (the caller then leaves the string alone and lets the layout table have
// its own say), and ErrTimeFieldOverflow when it decodes to an out-of-range
// value — a distinction the caller needs, because upstream answers 22007 for the
// first and 22008 for the second.
//
// It exists because goopg's timestamp text input is layout-driven end to end:
// the date and zone parts of '2020-01-01 12:00 AM' are perfectly ordinary to
// the layout table, and only the time token is beyond it. Canonicalizing just
// that token lets the timestamp path reuse the field decoder above without
// re-implementing date and zone decoding.
func CanonicalizeTimeToken(s string) (string, int, error) {
	end := 0
	for end < len(s) && (isDigit(s[end]) || s[end] == ':' || s[end] == '.') {
		end++
	}
	if end == 0 {
		return s, 0, ErrTimeBadFormat
	}
	token, rest := s[:end], s[end:]

	// Take a following AM/PM marker into the token: it changes the hour, so it
	// cannot be left behind in the untouched remainder.
	trimmed := strings.TrimLeft(rest, " ")
	if len(trimmed) >= 2 {
		switch strings.ToUpper(trimmed[:2]) {
		case "AM", "PM":
			if len(trimmed) == 2 || !isAlpha(trimmed[2]) {
				token += " " + trimmed[:2]
				rest = trimmed[2:]
			}
		}
	}

	tod, err := ParseTimeOfDay(token)
	if err != nil {
		return s, 0, err
	}
	tod, dayCarry, err := tod.Normalize()
	if err != nil {
		return s, 0, err
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	writePadded(&b, strconv.Itoa(tod.Hour), 2)
	b.WriteByte(':')
	writePadded(&b, strconv.Itoa(tod.Min), 2)
	b.WriteByte(':')
	writePadded(&b, strconv.Itoa(tod.Sec), 2)
	if tod.Nsec > 0 {
		frac := strings.TrimRight(strconv.Itoa(1_000_000_000 + tod.Nsec)[1:], "0")
		b.WriteByte('.')
		b.WriteString(frac)
	}
	b.WriteString(rest)
	return b.String(), dayCarry, nil
}

type meridiemKind int

const (
	meridiemNone meridiemKind = iota
	meridiemAM
	meridiemPM
)

// splitMeridiem removes a trailing am/pm token and reports which one it was.
// Only that exact token is consumed: "10:00 A.M." keeps its suffix so the
// caller's zone handling decides its fate, matching PostgreSQL, which looks
// "a.m." up as a time zone and fails.
func splitMeridiem(s string) (string, meridiemKind, error) {
	if len(s) < 2 {
		return s, meridiemNone, nil
	}
	tail := strings.ToUpper(s[len(s)-2:])
	kind := meridiemNone
	switch tail {
	case "AM":
		kind = meridiemAM
	case "PM":
		kind = meridiemPM
	default:
		return s, meridiemNone, nil
	}
	rest := strings.TrimSpace(s[:len(s)-2])
	if rest == "" {
		// "AM" alone is not a time.
		return s, meridiemNone, ErrTimeBadFormat
	}
	// Guard against swallowing the tail of a longer alphabetic token such as a
	// zone abbreviation; PostgreSQL's tokenizer would have kept that whole.
	if c := rest[len(rest)-1]; isAlpha(c) {
		return s, meridiemNone, nil
	}
	return rest, kind, nil
}

// decodeTimeColon implements DecodeTimeCommon() for the colon-separated forms.
func decodeTimeColon(s string) (TimeOfDay, error) {
	hour, rest, err := scanInt(s)
	if err != nil {
		return TimeOfDay{}, err
	}
	if rest == "" || rest[0] != ':' {
		return TimeOfDay{}, ErrTimeBadFormat
	}
	min, rest, err := scanInt(rest[1:])
	if err != nil {
		return TimeOfDay{}, err
	}

	var sec, nsec int
	switch {
	case rest == "":
		// hh:mm — seconds default to zero.
	case rest[0] == '.':
		// "always assume mm:ss.sss is MINUTE TO SECOND": the two fields shift
		// one place right and the hour becomes zero.
		nsec, rest, err = parseFractionalSecond(rest)
		if err != nil {
			return TimeOfDay{}, err
		}
		if rest != "" {
			return TimeOfDay{}, ErrTimeBadFormat
		}
		sec, min, hour = min, hour, 0
	case rest[0] == ':':
		sec, rest, err = scanInt(rest[1:])
		if err != nil {
			return TimeOfDay{}, err
		}
		if rest != "" {
			if rest[0] != '.' {
				return TimeOfDay{}, ErrTimeBadFormat
			}
			nsec, rest, err = parseFractionalSecond(rest)
			if err != nil {
				return TimeOfDay{}, err
			}
			if rest != "" {
				return TimeOfDay{}, ErrTimeBadFormat
			}
		}
	default:
		return TimeOfDay{}, ErrTimeBadFormat
	}

	// DecodeTimeCommon's sanity check. The hour range belongs to the caller —
	// 24:00:00 is a valid time and 25:00:00 is not, but only time_in() knows.
	if hour < 0 || min < 0 || min > 59 || sec < 0 || sec > 60 || nsec < 0 || nsec > 1_000_000_000 {
		return TimeOfDay{}, ErrTimeFieldOverflow
	}
	return TimeOfDay{Hour: hour, Min: min, Sec: sec, Nsec: nsec}, nil
}

// decodeTimeNumberField implements the time arms of DecodeNumberField(): a
// run-together numeric field is hhmmss when six digits long and hhmm when four,
// with any fractional part split off before the length is measured.
func decodeTimeNumberField(s string) (TimeOfDay, error) {
	nsec := 0
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		var rest string
		var err error
		nsec, rest, err = parseFractionalSecond(s[dot:])
		if err != nil {
			return TimeOfDay{}, err
		}
		if rest != "" {
			return TimeOfDay{}, ErrTimeBadFormat
		}
		s = s[:dot]
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return TimeOfDay{}, ErrTimeBadFormat
		}
	}
	var tod TimeOfDay
	switch len(s) {
	case 6:
		tod = TimeOfDay{Hour: atoiPrefix(s[0:2]), Min: atoiPrefix(s[2:4]), Sec: atoiPrefix(s[4:6])}
	case 4:
		tod = TimeOfDay{Hour: atoiPrefix(s[0:2]), Min: atoiPrefix(s[2:4])}
	default:
		return TimeOfDay{}, ErrTimeBadFormat
	}
	tod.Nsec = nsec
	if tod.Min > 59 || tod.Sec > 60 {
		return TimeOfDay{}, ErrTimeFieldOverflow
	}
	return tod, nil
}

// parseFractionalSecond mirrors ParseFractionalSecond(): the fraction is read
// as a double and rounded to microsecond resolution. s must start with '.'.
func parseFractionalSecond(s string) (nsec int, rest string, err error) {
	end := 1
	for end < len(s) && isDigit(s[end]) {
		end++
	}
	if end == 1 {
		// A bare '.' parses as 0.0 in strtod terms only if digits follow; PG's
		// strtod on "." consumes nothing and ParseFractionalSecond errors.
		return 0, "", ErrTimeBadFormat
	}
	frac, convErr := strconv.ParseFloat("0"+s[:end], 64)
	if convErr != nil {
		return 0, "", ErrTimeBadFormat
	}
	usec := int(math.Round(frac * 1e6))
	return usec * 1000, s[end:], nil
}

// scanInt reads the leading decimal run of s. An empty run yields 0 without
// consuming anything, which is precisely what strtoint() does for DecodeTime's
// subfields and why "10::00" and "10:" are accepted.
func scanInt(s string) (val int, rest string, err error) {
	end := 0
	for end < len(s) && isDigit(s[end]) {
		end++
	}
	if end == 0 {
		return 0, s, nil
	}
	if end > 9 {
		return 0, "", ErrTimeFieldOverflow
	}
	return atoiPrefix(s[:end]), s[end:], nil
}

func atoiPrefix(s string) int {
	v := 0
	for i := 0; i < len(s); i++ {
		v = v*10 + int(s[i]-'0')
	}
	return v
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
