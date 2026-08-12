package executor

import (
	"fmt"
	"strings"
	"time"
)

// Trailing-field classification for TIME / TIMETZ text input.
//
// M0119-0006: goopg used to treat *every* trailing space-separated token of a
// `time`/`timetz` literal as a timezone and throw it away unvalidated
// (`stripTimeZoneSuffix`), so `'10:00 A.M.'::time` silently became `10:00:00`
// where PostgreSQL raises `time zone "a.m." not recognized`, and
// `'10:00 GARBAGE'::time` became `10:00:00` where PostgreSQL raises 22007.
// Accepting nonsense as a zone is worse than rejecting a spelling PG accepts:
// the value that lands in the column is a guess with no diagnostic.
//
// PostgreSQL reaches its three different answers in two stages, and the stage
// that decides *which* error you get is the tokenizer, not the decoder:
//
//	ParseDateTime()  (postgres/src/backend/utils/adt/datetime.c:105 tokenizer)
//	  A leading run of letters is DTK_STRING — unless the character right after
//	  it is '.', '/' or '-', in which case the whole token becomes DTK_DATE
//	  (the shape a zone *name* has), or is '+'/a digit AND the letters are not a
//	  datetktbl keyword, which is DTK_DATE too. The token is lowercased on the
//	  way in. **datetktbl (datetime.c:105) contains NO timezone abbreviations at
//	  all** — they live in the separately GUC-configurable abbreviation table
//	  DecodeTimezoneAbbrev() consults — so `utc`/`gmt`/`pst` all fail that
//	  keyword probe and `'10:00 UTC+5'` becomes ONE DTK_DATE token read as a
//	  POSIX zone spec (hence `-05`, not UTC-plus-five-hours).
//	DecodeTimeOnly()
//	  DTK_STRING → DecodeTimezoneAbbrev() (the abbreviation table), then
//	    datetktbl for `am`/`pm` (AMPM), `bc`/`ad` (ADBC), `mon`, `january`,
//	    `allballs`, `today`, …; a datetktbl hit whose type is not a zone one is
//	    still DTERR_BAD_FORMAT → 22007, and so is a miss. Neither table is the
//	    zoneinfo database: `'10:00 Japan'` is 22007 even though `Japan` is a
//	    real zone name, because a bare word never reaches pg_tzset().
//	  DTK_DATE → pg_tzset() on the lowercased token; failure is
//	    DTERR_BAD_TIMEZONE → 22023 `time zone "%s" not recognized`. Success
//	    then needs pg_get_timezone_offset(): a fixed-offset zone resolves
//	    without a date, a DST zone does not and yields DTERR_BAD_FORMAT
//	    (22007) unless the input also carried a date — which is why
//	    `'10:00 Etc/GMT'::time` is accepted and
//	    `'10:00 America/New_York'::time` is not.
//
// Era and meridiem are ordinary fields, not zones, so they may follow a zone:
// `'10:00:00 PST BC'` and `'10:00 AM BC'` both parse. Two zone tokens do not
// (`'10:00 pst pdt'` is 22007) — DecodeTimeOnly's fmask rejects the repeat.
type zoneTokenKind int

const (
	// zoneTokenMeridiem is `AM`/`PM`: an AMPM field ParseTimeOfDay owns, so
	// the scan must stop and leave it attached. Stripping it here is what once
	// turned '12:00 AM' into noon.
	zoneTokenMeridiem zoneTokenKind = iota
	// zoneTokenEra is `BC`/`AD`: an ADBC field. A time-of-day has no year for
	// the era to shift, so PG decodes and ignores it.
	zoneTokenEra
	// zoneTokenOffset is a token that resolves to a fixed offset without a
	// date: an abbreviation from the core table, an explicit `+05`/`-04:30`,
	// a POSIX-style `UTC-5`, or a fixed-offset zoneinfo name.
	zoneTokenOffset
	// zoneTokenNamedNeedsDate is a real zoneinfo name whose offset varies with
	// the date (any DST zone). Accepted only when the input carried a date.
	zoneTokenNamedNeedsDate
	// zoneTokenNotRecognised is a token shaped like a zone name that names no
	// zone → 22023, carrying the lowercased spelling PG reports.
	zoneTokenNotRecognised
	// zoneTokenBadFormat is everything else → 22007.
	zoneTokenBadFormat
)

// classifyZoneToken decides what a single trailing field of a time/timetz input
// is, mirroring the ParseDateTime → DecodeTimeOnly pair documented above.
// offsetSecs is meaningful only for zoneTokenOffset (seconds east of UTC).
func classifyZoneToken(tok string) (kind zoneTokenKind, offsetSecs int) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return zoneTokenBadFormat, 0
	}

	alpha := leadingAlphaRun(tok)

	// Wholly alphabetic: the core keyword table and nothing else.
	if len(alpha) == len(tok) {
		switch strings.ToUpper(tok) {
		case "AM", "PM":
			return zoneTokenMeridiem, 0
		case "BC", "AD":
			return zoneTokenEra, 0
		}
		if off, ok := tzAbbrevOffsets[strings.ToUpper(tok)]; ok {
			return zoneTokenOffset, off
		}
		// A bare word never reaches pg_tzset(), so a valid zone *name* spelled
		// without punctuation ('Japan', 'EST5EDT') is 22007, not 22023.
		return zoneTokenBadFormat, 0
	}

	// An explicit numeric displacement ('+05', '-04:30', '+0530').
	if off, ok := parseTZOffset(tok); ok {
		return zoneTokenOffset, off
	}

	// Not the DTK_DATE shape? Then it stays DTK_STRING and fails the keyword
	// lookup: '12', '-', '.', 'a_b', 'xy:zw' ('_' and ':' cannot be the first
	// punctuation). The '+'/digit arm is conditional on the letters NOT naming
	// a datetktbl keyword, exactly as the tokenizer's datebsearch probe is.
	if len(alpha) == 0 {
		return zoneTokenBadFormat, 0
	}
	switch next := tok[len(alpha)]; {
	case next == '.' || next == '/' || next == '-':
	case next == '+' || (next >= '0' && next <= '9'):
		if pgDateTimeKeywords[strings.ToLower(alpha)] {
			return zoneTokenBadFormat, 0
		}
	default:
		return zoneTokenBadFormat, 0
	}

	// POSIX-style `<abbrev>±hh[:mm]`. POSIX states the offset to ADD to local
	// time to reach UTC, so its sign is the opposite of the SQL displacement:
	// TZ='UTC-5' is UTC+05:00 (verified against PG 18.3:
	// `'10:00 UTC-5'::timetz` is `10:00:00+05`).
	if off, ok := parsePOSIXZoneOffset(tok[len(alpha):]); ok {
		return zoneTokenOffset, -off
	}

	// A zone name proper. pg_tzset() is case-insensitive; Go's loader is not,
	// so try the spelling as written before the lowercased one.
	for _, name := range []string{tok, strings.ToLower(tok)} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			continue
		}
		if off, fixed := fixedZoneOffset(loc); fixed {
			return zoneTokenOffset, off
		}
		return zoneTokenNamedNeedsDate, 0
	}
	return zoneTokenNotRecognised, 0
}

// pgDateTimeKeywords is the set of wholly-alphabetic tokens in upstream's
// datetktbl (postgres/src/backend/utils/adt/datetime.c:105). It is used for one
// purpose only: the tokenizer's `datebsearch(field, datetktbl, …) == NULL` probe,
// which decides whether letters followed by '+' or a digit open a zone NAME or
// close a keyword field. Note what it does NOT contain — a single timezone
// abbreviation. Those live in the GUC-selected abbreviation table
// (`timezone_abbreviations`), which is why `utc+5` is one zone-name token.
var pgDateTimeKeywords = map[string]bool{
	"infinity": true, "ad": true, "allballs": true, "am": true, "apr": true,
	"april": true, "at": true, "aug": true, "august": true, "bc": true,
	"d": true, "dec": true, "december": true, "dow": true, "doy": true,
	"dst": true, "epoch": true, "feb": true, "february": true, "fri": true,
	"friday": true, "h": true, "isodow": true, "isoyear": true, "j": true,
	"jan": true, "january": true, "jd": true, "jul": true, "julian": true,
	"july": true, "jun": true, "june": true, "m": true, "mar": true,
	"march": true, "may": true, "mm": true, "mon": true, "monday": true,
	"nov": true, "november": true, "now": true, "oct": true, "october": true,
	"on": true, "pm": true, "s": true, "sat": true, "saturday": true,
	"sep": true, "sept": true, "september": true, "sun": true, "sunday": true,
	"t": true, "thu": true, "thur": true, "thurs": true, "thursday": true,
	"today": true, "tomorrow": true, "tue": true, "tues": true, "tuesday": true,
	"wed": true, "wednesday": true, "weds": true, "y": true, "yesterday": true,
}

// leadingAlphaRun returns the leading run of ASCII letters of s. ParseDateTime
// uses isalpha() on the C locale, so only ASCII letters open a text field.
func leadingAlphaRun(s string) string {
	i := 0
	for i < len(s) && ((s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z')) {
		i++
	}
	return s[:i]
}

// parsePOSIXZoneOffset reads the `±hh[:mm[:ss]]` tail of a POSIX TZ
// specification. It is deliberately separate from parseTZOffset: that one
// requires a two-digit hour (the SQL displacement spelling) while POSIX allows
// a single digit and no upper bound below 24 that PG enforces here
// ('utc-25'::timetz is `+25` on PG 18.3).
func parsePOSIXZoneOffset(s string) (int, bool) {
	if len(s) < 2 || (s[0] != '+' && s[0] != '-') {
		return 0, false
	}
	sign := 1
	if s[0] == '-' {
		sign = -1
	}
	parts := strings.Split(s[1:], ":")
	if len(parts) > 3 {
		return 0, false
	}
	total := 0
	for i, p := range parts {
		if p == "" || len(p) > 2 {
			return 0, false
		}
		v := 0
		for j := 0; j < len(p); j++ {
			if p[j] < '0' || p[j] > '9' {
				return 0, false
			}
			v = v*10 + int(p[j]-'0')
		}
		switch i {
		case 0:
			total += v * 3600
		case 1:
			total += v * 60
		case 2:
			total += v
		}
	}
	return sign * total, true
}

// fixedZoneOffset answers pg_get_timezone_offset()'s question — does this zone
// have one offset for all time? — by sampling, since Go exposes no transition
// list. The probe set spans the whole span of tzdata's DST rules (1901 predates
// them, 2100 postdates every current rule) at both solstice sides of each year,
// which separates a fixed zone (Etc/GMT, UTC) from every DST zone in the
// database. A zone whose only transitions fall outside the probe set would be
// misjudged; see the deferral-ledger row for M0119-0006 (2026-08-13).
func fixedZoneOffset(loc *time.Location) (int, bool) {
	probes := []time.Time{
		time.Date(1901, 1, 15, 12, 0, 0, 0, time.UTC),
		time.Date(1901, 7, 15, 12, 0, 0, 0, time.UTC),
		time.Date(1970, 1, 15, 12, 0, 0, 0, time.UTC),
		time.Date(1970, 7, 15, 12, 0, 0, 0, time.UTC),
		time.Date(2000, 1, 15, 12, 0, 0, 0, time.UTC),
		time.Date(2000, 7, 15, 12, 0, 0, 0, time.UTC),
		time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
		time.Date(2025, 7, 15, 12, 0, 0, 0, time.UTC),
		time.Date(2100, 1, 15, 12, 0, 0, 0, time.UTC),
		time.Date(2100, 7, 15, 12, 0, 0, 0, time.UTC),
	}
	_, want := probes[0].In(loc).Zone()
	for _, p := range probes[1:] {
		if _, got := p.In(loc).Zone(); got != want {
			return 0, false
		}
	}
	return want, true
}

// stripValidatedZoneSuffix peels the trailing zone/era fields off a time-only
// input, returning the remaining time text and the zone offset the fields named
// (0 and false when none did). It replaces the former stripTimeZoneSuffix,
// which dropped every trailing token unvalidated.
//
// hasDate says the input carried a date, which is what lets a DST zone name
// resolve (DecodeTimeOnly's `(fmask & DTK_DATE_M) != DTK_DATE_M` test).
// typeName is the SQL type spelled in the 22007 message; orig is the input as
// the user wrote it, which is what PG quotes in every one of these errors.
func stripValidatedZoneSuffix(s string, hasDate bool, typeName, orig string) (string, int, bool, error) {
	badFormat := func() error {
		return &ExecError{Code: "22007",
			Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, orig)}
	}

	offsetSecs, haveOffset := 0, false
	for {
		idx := strings.LastIndex(s, " ")
		if idx < 0 {
			break
		}
		tok := strings.TrimSpace(s[idx+1:])
		kind, off := classifyZoneToken(tok)
		switch kind {
		case zoneTokenMeridiem:
			// ParseTimeOfDay's field; stop here so it survives.
			return s, offsetSecs, haveOffset, nil
		case zoneTokenEra:
			// Decoded and discarded: a time has no year to shift.
		case zoneTokenOffset:
			if haveOffset {
				// Two zone fields — '10:00 pst pdt'.
				return "", 0, false, badFormat()
			}
			offsetSecs, haveOffset = off, true
		case zoneTokenNamedNeedsDate:
			if !hasDate {
				return "", 0, false, badFormat()
			}
			// The date-bearing callers resolve the zone themselves; here the
			// field is merely consumed.
		case zoneTokenNotRecognised:
			return "", 0, false, &ExecError{Code: "22023",
				Message: fmt.Sprintf("time zone %q not recognized", strings.ToLower(tok))}
		default:
			return "", 0, false, badFormat()
		}
		s = strings.TrimSpace(s[:idx])
	}

	// An offset attached to the time with no space ('10:00+05'). Guarded by
	// `> 2` so the '-' of a date and the sign of a lone field cannot match.
	//
	// The trailing `Z` is here rather than in the token loop because
	// pgdatetime.NormalizeInput folds a spaced, lowercase or attached zulu into
	// one attached uppercase `Z` before this runs, so `'10:00 Z'` never reaches
	// the loop as a field of its own — which is why it used to fall through to
	// ParseTimeOfDay and fail as a malformed hour.
	if !haveOffset {
		if body, ok := strings.CutSuffix(s, "Z"); ok && body != "" {
			return body, 0, true, nil
		}
		if plus := strings.LastIndex(s, "+"); plus > 2 {
			if off, ok := parseTZOffset(s[plus:]); ok {
				return s[:plus], off, true, nil
			}
			return s[:plus], 0, false, nil
		} else if minus := strings.LastIndex(s, "-"); minus > 2 {
			if off, ok := parseTZOffset(s[minus:]); ok {
				return s[:minus], off, true, nil
			}
			return s[:minus], 0, false, nil
		}
	}
	return s, offsetSecs, haveOffset, nil
}
