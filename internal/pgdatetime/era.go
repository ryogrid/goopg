package pgdatetime

import (
	"errors"
	"strings"
	"time"
)

// ErrFieldOutOfRange is the field-range failure PostgreSQL reports as SQLSTATE
// 22008 ("date/time field value out of range"), as opposed to the syntax
// failure (22007) a caller reports when nothing recognises the input at all.
// It is returned by ApplyEra for the one range rule the era model owns: there
// is no year zero.
var ErrFieldOutOfRange = errors.New("date/time field value out of range")

// SplitEra strips PostgreSQL's era token — the ADBC field of datetbl
// (postgres/src/backend/utils/adt/datetime.c) — off the end of a date/time
// input string, returning the remainder and whether the era is BC.
//
//	"2020-01-01 BC"          -> "2020-01-01",          bc=true
//	"2020-01-01bc"           -> "2020-01-01",          bc=true   (attached, any case)
//	"2020-01-01 10:00:00 AD" -> "2020-01-01 10:00:00", bc=false
//	"2020-01-01"             -> "2020-01-01",          bc=false
//
// PostgreSQL reaches the token through its field splitter, so the whitespace
// before it is optional and the spelling is case-insensitive: '2020-01-01BC',
// '2020-01-01 bc' and '2020-01-01 BC' are one date. Only a TRAILING token is
// recognised, matching upstream in practice — a leading 'BC 2020-01-01' is
// rejected by date_in (verified on PG 18.3), because DecodeDateTime has already
// committed the first field to the date by the time the era arrives.
//
// The rewrite is deliberately as narrow as canonicalZulu's: what precedes the
// token (after dropping any whitespace) must end in a DIGIT. That keeps the
// split off inputs where 'bc'/'ad' is the tail of something else and leaves a
// doubled era ('2020-01-01 BC BC') for the caller's parser to reject, as PG
// does. 'B.C.' is NOT an era spelling — PG reads it as a timezone name and
// fails with "time zone \"b.c.\" not recognized".
func SplitEra(s string) (string, bool) {
	trimmed := strings.TrimRight(s, " \t")
	if len(trimmed) < 2 {
		return s, false
	}
	tail := trimmed[len(trimmed)-2:]
	bc := false
	switch {
	case strings.EqualFold(tail, "BC"):
		bc = true
	case strings.EqualFold(tail, "AD"):
	default:
		return s, false
	}
	head := strings.TrimRight(trimmed[:len(trimmed)-2], " \t")
	if head == "" {
		return s, false
	}
	if c := head[len(head)-1]; c < '0' || c > '9' {
		return s, false
	}
	return head, bc
}

// ApplyEra converts a time whose YEAR field carries the era-relative year the
// input spelled (PG's tm_year before DecodeDateTime's era fixup) into the
// astronomical year goopg stores, where 1 BC is year 0 and 2 BC is year -1 —
// the same convention j2date/date2j use, so the stored value stays directly
// comparable and printable by FormatDate/FormatTimestamp.
//
// It also enforces the one range rule that belongs to the era model: there is
// no year zero in either era, so '0000-01-01' and '0000-01-01 BC' are both
// ErrFieldOutOfRange. Upstream raises this from ValidateDate() (datetime.c)
// via the DTERR_FIELD_OVERFLOW → 22008 mapping, not the 22007 syntax path —
// goopg previously accepted '0000-01-01' and stored a value that printed back
// as an unrelated date (1753-08-29).
func ApplyEra(t time.Time, bc bool) (time.Time, error) {
	y := t.Year()
	if y == 0 {
		return time.Time{}, ErrFieldOutOfRange
	}
	if !bc {
		return t, nil
	}
	if y < 0 {
		// The year field was already negative before the era was applied, so
		// the input spelled both a sign and an era ("-0001-01-01 BC"). PG has
		// no such form; refuse rather than guess which one wins.
		return time.Time{}, ErrFieldOutOfRange
	}
	return time.Date(-(y - 1), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(),
		t.Nanosecond(), t.Location()), nil
}

// AstronomicalYear is ApplyEra's year conversion without a time.Time to carry
// it in — it exists so a caller holding just the digits a date token spelled
// (DateTokenYear) can compute the astronomical year, and therefore run
// ValidateDayOfMonth, BEFORE attempting to parse the rest of the token. ok is
// false for the no-year-zero case (`year <= 0` under BC — 0 is the "no year
// zero" refusal itself, and DateTokenYear never returns a negative year), so
// the caller skips the day-of-month check and leaves that refusal to
// ApplyEra's own error, run downstream as before.
func AstronomicalYear(year int, bc bool) (int, bool) {
	if !bc {
		return year, true
	}
	if year <= 0 {
		return 0, false
	}
	return -(year - 1), true
}

// EraYear splits an astronomical year into the year DIGITS PostgreSQL prints
// and whether the era marker " BC" follows: year 0 prints "1 BC", year -1
// prints "2 BC" (EncodeDateOnly, datetime.c: `(tm->tm_year > 0) ? tm->tm_year
// : -(tm->tm_year - 1)` plus the trailing " BC" for tm_year <= 0).
func EraYear(year int) (int, bool) {
	if year > 0 {
		return year, false
	}
	return -(year - 1), true
}
