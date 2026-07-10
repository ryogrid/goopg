package parser

import (
	"math"
	"strconv"
	"strings"
)

// Microsecond scales for interval sub-day fields. Defined here (the parser
// package owns interval-body decoding) and mirrored by the executor's own
// copies for its Datum/format math; the two sets are trivially identical
// constants — the *logic* that consumes them lives only here.
const (
	usecsPerDay    = 24 * 60 * 60 * 1_000_000
	usecsPerHour   = 3600 * 1_000_000
	usecsPerMinute = 60 * 1_000_000
	usecsPerSecond = 1_000_000
	usecsPerMilli  = 1_000
)

// intervalFieldMask is a bitmask of the interval field types already consumed by
// a partially-decoded interval body, mirroring PostgreSQL's DecodeInterval
// `fmask`/`tmask` accounting (postgres/src/backend/utils/adt/datetime.c): after
// each field is decoded, its type-mask (tmask) is checked against the running
// fmask and a non-zero intersection is a bad-format error — this is how PG
// rejects a repeated field like `interval '1 h 2 h'` / `interval '1 mon 2 mons'`
// or a unit that collides with a preceding `HH:MM:SS` time word
// (`interval '1 h 05:00:00'`). The individual bits mirror the DTK_M(field) bit
// positions closely enough for collision detection (the absolute values are
// irrelevant; only distinctness and the SECOND/ALL_SECS grouping matter).
type intervalFieldMask uint32

const (
	imYear intervalFieldMask = 1 << iota
	imMonth
	imDay
	imHour
	imMinute
	imSecond
	imMillisecond
	imMicrosecond
	imWeek
	imDecade
	imCentury
	imMillennium
)

// imAllSecs mirrors PostgreSQL DTK_ALL_SECS_M: a SECOND field carrying a
// fractional part also occupies the millisecond/microsecond slots, so
// `interval '1.5 sec 200 ms'` is a collision while `interval '1 sec 200 ms'`
// is not. imTime mirrors DTK_TIME_M: a `HH:MM[:SS]` time word occupies HOUR,
// MINUTE and all the SECOND slots at once.
const (
	imAllSecs = imSecond | imMillisecond | imMicrosecond
	imTime    = imHour | imMinute | imAllSecs
)

// intervalUnitMask returns the field-mask bit(s) a `<magnitude> <unit>` pair
// contributes, mirroring the per-unit tmask DecodeInterval assigns. A SECOND
// unit carrying a fractional magnitude widens to imAllSecs (DTK_ALL_SECS_M),
// matching PostgreSQL's `if (fval == 0) tmask = DTK_M(SECOND); else tmask =
// DTK_ALL_SECS_M`. The unit must already be canonicalised (see
// canonicalIntervalUnit). Returns 0 for an unrecognised unit.
func intervalUnitMask(unit string, fractional bool) intervalFieldMask {
	switch unit {
	case "year":
		return imYear
	case "month":
		return imMonth
	case "week":
		return imWeek
	case "decade":
		return imDecade
	case "century":
		return imCentury
	case "millennium":
		return imMillennium
	case "day":
		return imDay
	case "hour":
		return imHour
	case "minute":
		return imMinute
	case "second":
		if fractional {
			return imAllSecs
		}
		return imSecond
	case "millisecond":
		return imMillisecond
	case "microsecond":
		return imMicrosecond
	default:
		return 0
	}
}

// ParseIntervalMagnitude splits an interval magnitude token into its integer
// part (val) and fractional part (fval, |fval|<1, sharing val's sign),
// mirroring how PostgreSQL's DecodeInterval feeds a numeric field to the
// Adjust* helpers (postgres/src/backend/utils/adt/datetime.c): the integer
// portion scales exactly while the fraction spills into the next-smaller unit.
// "1.5" → (1, 0.5); "-1.5" → (-1, -0.5); ".5" → (0, 0.5); "1" → (1, 0).
// Returns ok=false for a non-numeric body.
//
// Moved here from the executor (M0122 interval work) so the parser's Form-2
// tokenizer and the executor's typed-literal / cast paths share one
// implementation — avoiding the sibling-path drift that separate copies invite.
func ParseIntervalMagnitude(s string) (val int64, fval float64, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		return n, 0, true
	}
	intPart, fracPart := s[:dot], s[dot+1:]
	neg := false
	switch {
	case strings.HasPrefix(intPart, "-"):
		neg, intPart = true, intPart[1:]
	case strings.HasPrefix(intPart, "+"):
		intPart = intPart[1:]
	}
	if intPart == "" {
		intPart = "0"
	}
	iv, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	var fv float64
	if fracPart != "" {
		f, ferr := strconv.ParseFloat("0."+fracPart, 64)
		if ferr != nil {
			return 0, 0, false
		}
		fv = f
	}
	if neg {
		iv, fv = -iv, -fv
	}
	return iv, fv, true
}

// intervalFractMicros scales a sub-unit fraction to microseconds, mirroring
// PostgreSQL's AdjustFractMicroseconds (postgres/src/backend/utils/adt/
// datetime.c): truncate toward zero, then round the leftover with a strict
// >0.5 / <-0.5 comparison (an exact half stays truncated). frac is assumed to
// have |frac|<1.
func intervalFractMicros(frac float64, scale int64) int64 {
	if frac == 0 {
		return 0
	}
	f := frac * float64(scale)
	usec := int64(f)
	f -= float64(usec)
	if f > 0.5 {
		usec++
	} else if f < -0.5 {
		usec--
	}
	return usec
}

// IntervalUnitToParts converts an integer magnitude (val) plus fractional
// remainder (fval) in the given singular, lower-cased unit into interval
// months/days/micros components, mirroring PostgreSQL's DecodeInterval
// fractional-spill rules (postgres/src/backend/utils/adt/datetime.c):
//   - fractional years  → months (rint(fval*12), sub-month discarded)
//   - fractional months → days (int(fval*30)) + remainder micros
//   - fractional days   → micros (fval*USECS_PER_DAY)
//   - fractional h/m/s/ms→ micros ((val+fval)*scale)
//
// The unit must already be canonicalised to the singular base form (see
// canonicalIntervalUnit). Returns ok=false for an unrecognised unit.
func IntervalUnitToParts(val int64, fval float64, unit string) (months, days int32, micros int64, ok bool) {
	switch unit {
	case "day":
		return 0, int32(val), intervalFractMicros(fval, usecsPerDay), true
	case "week":
		// AdjustDays(val,7) + AdjustFractDays(fval,7): whole days then the
		// leftover fraction spills to micros (postgres DecodeInterval DTK_WEEK).
		f := fval * 7
		extraDays := int64(f)
		f -= float64(extraDays)
		return 0, int32(val*7 + extraDays), intervalFractMicros(f, usecsPerDay), true
	case "month":
		// AdjustFractDays(fval, DAYS_PER_MONTH): whole days then remainder → micros.
		f := fval * 30
		extraDays := int64(f)
		f -= float64(extraDays)
		return int32(val), int32(extraDays), intervalFractMicros(f, usecsPerDay), true
	case "year":
		// AdjustFractYears discards any sub-month remainder (round-half-to-even).
		extraMonths := int64(math.RoundToEven(fval * 12))
		return int32(val*12 + extraMonths), 0, 0, true
	case "decade":
		// AdjustYears(val,10)+AdjustFractYears(fval,10): 1 decade = 120 months.
		extraMonths := int64(math.RoundToEven(fval * 10 * 12))
		return int32(val*120 + extraMonths), 0, 0, true
	case "century":
		// AdjustYears(val,100)+AdjustFractYears(fval,100): 1 century = 1200 months.
		extraMonths := int64(math.RoundToEven(fval * 100 * 12))
		return int32(val*1200 + extraMonths), 0, 0, true
	case "millennium":
		// AdjustYears(val,1000)+AdjustFractYears(fval,1000): 1 mil = 12000 months.
		extraMonths := int64(math.RoundToEven(fval * 1000 * 12))
		return int32(val*12000 + extraMonths), 0, 0, true
	case "hour":
		return 0, 0, val*usecsPerHour + intervalFractMicros(fval, usecsPerHour), true
	case "minute":
		return 0, 0, val*usecsPerMinute + intervalFractMicros(fval, usecsPerMinute), true
	case "second":
		return 0, 0, val*usecsPerSecond + intervalFractMicros(fval, usecsPerSecond), true
	case "millisecond":
		return 0, 0, val*usecsPerMilli + intervalFractMicros(fval, usecsPerMilli), true
	case "microsecond":
		// AdjustMicroseconds(val,fval,1): sub-microsecond fraction is discarded.
		return 0, 0, val + intervalFractMicros(fval, 1), true
	default:
		return 0, 0, 0, false
	}
}

// canonicalIntervalUnit maps a unit word (any accepted spelling, any case) to
// the singular base form IntervalUnitToParts switches on. It accepts the full
// unit words the single-field literal grammar already supports plus the
// abbreviations PostgreSQL's intervalout emits (`mon`/`mons`, `min`/`mins`,
// `sec`/`secs`, `hr`/`hrs`) so goopg can re-parse its own interval output.
// It also accepts the week/decade/century/millennium/microsecond units and
// their `dec`/`cent`/`mil`/`us`/`usec` abbreviations (unimplemented_feat
// #5(c); microsecond fractions below 1µs are discarded), plus every
// single-letter unit PostgreSQL's interval-decoding table (deltatktbl in
// postgres/src/backend/utils/adt/datetime.c) recognises: `y`=year, `c`=century,
// `w`=week, `d`=day, `h`=hour, `s`=second, and critically `m`=MINUTE — in an
// interval literal `m` is unambiguously minute (deltatktbl), never month
// (`interval '1 m'` → 00:01:00, `interval '1 y 2 m'` → 1 year 00:02:00). There
// is NO positional ambiguity: DecodeInterval reads the unit token via
// DecodeUnits→deltatktbl regardless of neighbouring fields (unimplemented_feat
// #5(d-ii)). Units that appear in deltatktbl but have no case in
// DecodeInterval's per-unit switch (`quarter`/`qtr`, the `timezone*`/`tz`
// tokens) are intentionally NOT accepted — PostgreSQL rejects them with
// DTERR_BAD_FORMAT (`interval '1 qtr'`/`interval '1 tz'` → 22007), so they must
// fall through to the caller's error path here too.
func canonicalIntervalUnit(w string) (string, bool) {
	switch strings.ToLower(w) {
	case "year", "years", "yr", "yrs", "y":
		return "year", true
	case "month", "months", "mon", "mons":
		return "month", true
	case "week", "weeks", "w":
		return "week", true
	case "decade", "decades", "dec", "decs":
		return "decade", true
	case "century", "centuries", "cent", "c":
		return "century", true
	case "millennium", "millennia", "mil", "mils":
		return "millennium", true
	case "day", "days", "d":
		return "day", true
	case "hour", "hours", "hr", "hrs", "h":
		return "hour", true
	case "minute", "minutes", "min", "mins", "m":
		return "minute", true
	case "second", "seconds", "sec", "secs", "s":
		return "second", true
	case "millisecond", "milliseconds", "ms", "msec", "msecs":
		return "millisecond", true
	case "microsecond", "microseconds", "us", "usec", "usecs", "usecond", "useconds":
		return "microsecond", true
	default:
		return "", false
	}
}

// parseYearMonthField decodes a SQL-standard "years-months" interval field
// (`1-2` → 1 year 2 months), mirroring PostgreSQL's DecodeInterval DTK_NUMBER
// hyphen branch (postgres/src/backend/utils/adt/datetime.c): a signed integer
// year value, a hyphen, then a month count that must satisfy 0 ≤ months < 12,
// with nothing trailing (an empty month tail, `1-`/`-1-`, is a bare year).
// The result is a whole-month value years*12 ± months
// (PG sets type DTK_MONTH unconditionally, so the field contributes months
// only — never days or micros). A leading `-` on the whole field flips the
// sign of BOTH the year and the month component (PG: `if (*field[i] == '-')
// val2 = -val2` on top of strtoi64's already-negative year), so
// `-1-2` → -14 months. Returns ok=false when the token is not exactly
// `<int>-<int>` with the month part in range — the caller then falls through
// to plain magnitude parsing, matching PG's DTERR_BAD_FORMAT /
// DTERR_FIELD_OVERFLOW rejection for `1-2-3` / `1-2x` / `1-12` / `1--2`.
func parseYearMonthField(f string) (months int32, ok bool) {
	if f == "" {
		return 0, false
	}
	// A leading '+'/'-' is the year's sign, not the field separator; the
	// separating hyphen is the first '-' AFTER that optional sign.
	start := 0
	if f[0] == '+' || f[0] == '-' {
		start = 1
	}
	rel := strings.IndexByte(f[start:], '-')
	if rel < 0 {
		return 0, false // no separator → not a year-month field
	}
	sep := start + rel
	year, err := strconv.ParseInt(f[:sep], 10, 64)
	if err != nil {
		return 0, false
	}
	// The month part must be a bare non-negative integer < MONTHS_PER_YEAR
	// with nothing trailing (Go's ParseInt requires the whole string to be a
	// valid integer, reproducing PG's `*cp != '\0'` bad-format check for
	// `1-2-3`, `1-2x`, `1-2.5`; the range check reproduces `val2 < 0 ||
	// val2 >= MONTHS_PER_YEAR` for `1--2`, `1-12`, `1-13`). An EMPTY month part
	// (`1-` → 1 year, `-1-` → -1 years) is a bare year: PostgreSQL's
	// `strtoint(cp + 1, ...)` on the empty tail returns 0 with no error, so the
	// field contributes years only.
	monStr := f[sep+1:]
	var mon int64
	if monStr != "" {
		var err error
		mon, err = strconv.ParseInt(monStr, 10, 64)
		if err != nil || mon < 0 || mon >= 12 {
			return 0, false
		}
	}
	if f[0] == '-' {
		mon = -mon
	}
	total := year*12 + mon
	if total > math.MaxInt32 || total < math.MinInt32 {
		return 0, false
	}
	return int32(total), true
}

// parseIntervalTimeToken parses a `[+-]HH:MM[:SS[.ffffff]]` time word from a
// multi-field interval body into a signed microsecond count. The sign, if
// present, applies to the whole time component (PostgreSQL DecodeTime +
// leading sign handling). Returns ok=false for anything that isn't a valid
// colon-delimited time.
func parseIntervalTimeToken(tok string) (micros int64, ok bool) {
	neg := false
	switch {
	case strings.HasPrefix(tok, "-"):
		neg, tok = true, tok[1:]
	case strings.HasPrefix(tok, "+"):
		tok = tok[1:]
	}
	parts := strings.Split(tok, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	hh, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || hh < 0 {
		return 0, false
	}
	mm, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || mm < 0 {
		return 0, false
	}
	micros = hh*usecsPerHour + mm*usecsPerMinute
	if len(parts) == 3 {
		// Seconds field may carry a fractional part (`06.789`). Reuse the
		// magnitude splitter + AdjustFractMicroseconds rounding so the
		// sub-second handling matches every other interval entry point.
		sv, sf, sok := ParseIntervalMagnitude(parts[2])
		if !sok || sv < 0 {
			return 0, false
		}
		micros += sv*usecsPerSecond + intervalFractMicros(sf, usecsPerSecond)
	}
	if neg {
		micros = -micros
	}
	return micros, true
}

// inDatetktbl reports whether an all-letter token is a key in PostgreSQL's
// absolute date/time keyword table (datetktbl in
// postgres/src/backend/utils/adt/datetime.c). ParseDateTime consults this table
// to decide whether a letter-run glued directly before a digit starts a fresh
// field or is swallowed into an invalid DTK_DATE run (see expandIntervalFields).
// Only the pure-letter keys are listed — the sign-prefixed ones (`+infinity`,
// `-infinity`) can never appear as a bare letter-run. The interval-unit letters
// that PostgreSQL happens to also keep here (`d`,`h`,`m`,`s`,`y`,`mon`,`dec`)
// are exactly the ones for which a glued form like `1d2h` / `1mon2d` / `1dec2d`
// parses, while `day`/`week`/`min`/`sec`/`w`/`c` are absent, so `1day2h` /
// `1w2d` error — matching PostgreSQL byte-for-byte.
func inDatetktbl(w string) bool {
	switch strings.ToLower(w) {
	case "ad", "allballs", "am", "apr", "april", "at", "aug", "august",
		"bc", "d", "dec", "december", "dow", "doy", "dst", "epoch",
		"feb", "february", "fri", "friday", "h", "infinity", "isodow",
		"isoyear", "j", "jan", "january", "jd", "jul", "julian", "july",
		"jun", "june", "m", "mar", "march", "may", "mm", "mon", "monday",
		"nov", "november", "now", "oct", "october", "on", "pm", "s",
		"sat", "saturday", "sep", "sept", "september", "sun", "sunday",
		"t", "thu", "thur", "thurs", "thursday", "today", "tomorrow",
		"tue", "tues", "tuesday", "wed", "wednesday", "weds", "y",
		"yesterday":
		return true
	default:
		return false
	}
}

// expandIntervalFields splits each whitespace-delimited interval field into the
// number / unit-word tokens PostgreSQL's ParseDateTime lexer
// (postgres/src/backend/utils/adt/datetime.c) would produce, so glued forms like
// `2h30m`, `1.5h`, `-5h` and `1y2mon` decode identically to their spaced
// equivalents (`2 h 30 m`, `1.5 h`, `-5 h`, `1 y 2 mon`).
//
// PostgreSQL's lexer starts a fresh field at every digit↔letter boundary, with
// one twist: when an all-letter run is immediately followed by a digit it first
// checks whether that run is a token in the absolute-datetime table (datetktbl);
// if it is NOT, the run and the following alphanumerics are swallowed into a
// single invalid DTK_DATE field, which DecodeInterval then rejects. That is why
// `1d2h`/`1mon2d` (`d`,`mon` ∈ datetktbl) parse but `1day2h`/`1week2d`
// (`day`,`week` ∉ datetktbl) error. Fields carrying a ':' (time words) or an
// internal '-' (SQL year-month, `1-2`) are left intact — the caller decodes them
// whole, exactly as PG's DTK_TIME / DTK_DATE branches do.
//
// Returns ok=false when a field would form such a swallowed invalid DTK_DATE, so
// the caller can reject the whole literal.
func expandIntervalFields(raw []string) (out []string, ok bool) {
	out = make([]string, 0, len(raw))
	for _, f := range raw {
		// Time words (HH:MM[:SS]) are decoded whole by the caller — BUT
		// PostgreSQL's DTK_TIME lexer collects only digits, ':' and '.', so a
		// glued trailing unit word starts a fresh DTK_STRING field (`12:00h` →
		// `12:00` + `h`, the `h` then absorbed as a no-op). Peel any trailing
		// ASCII-letter run and re-tokenize it; the time prefix is left intact.
		// (`12:00h30m` still errors: it splits to `12:00`+`h`+`30`+`m`, and the
		// `30 m` MINUTE bit collides with the time word's MINUTE slot.)
		if strings.ContainsRune(f, ':') {
			letterAt := -1
			for k := 0; k < len(f); k++ {
				if isASCIILetter(f[k]) {
					letterAt = k
					break
				}
			}
			if letterAt < 0 || !strings.ContainsRune(f[:letterAt], ':') {
				out = append(out, f)
				continue
			}
			restToks, rok := splitAlphaNumRuns(f[letterAt:])
			if !rok {
				return nil, false
			}
			out = append(out, f[:letterAt])
			out = append(out, restToks...)
			continue
		}
		// A leading '+'/'-' is the field's sign, not a separator.
		sign, body := "", f
		if len(body) > 0 && (body[0] == '+' || body[0] == '-') {
			sign, body = body[:1], body[1:]
		}
		// An internal '-' marks a SQL year-month field (`1-2`, `-1-2`) or an
		// exotic hyphenated token; leave it whole for parseYearMonthField —
		// UNLESS it is an unsigned field with a trailing fractional-seconds run
		// or a glued unit word that PostgreSQL's ParseDateTime lexer would split
		// off. PG lexes `1-2.5` as a DTK_DATE field (`1-2`) plus a fresh
		// DTK_NUMBER field (`.5`), so the fraction decodes as seconds (`1-2.5` →
		// 1 year 2 mons + 0.5 s); `1-.5` likewise splits into `1-` + `.5`; and
		// `1-2h` splits into `1-2` + a DTK_STRING `h` unit word that is then
		// absorbed (`1-2h` → 1 year 2 mons). See splitYearMonthTrailer. A SIGNED
		// field (`-1-2.5`, `+1-2h`) is kept whole: PG lexes it as a single DTK_TZ
		// token, whose collection rules differ (it swallows '.', stops at a
		// letter), so the split is deferred for the signed case.
		if strings.ContainsRune(body, '-') {
			if sign == "" {
				if pre, rest, ok := splitYearMonthTrailer(body); ok {
					restToks, rok := splitAlphaNumRuns(rest)
					if !rok {
						return nil, false
					}
					out = append(out, pre)
					out = append(out, restToks...)
					continue
				}
			}
			out = append(out, f)
			continue
		}
		toks, tok := splitAlphaNumRuns(body)
		if !tok {
			return nil, false
		}
		// Reattach the leading sign to the first (numeric) token.
		if sign != "" {
			toks[0] = sign + toks[0]
		}
		out = append(out, toks...)
	}
	return out, true
}

// splitAlphaNumRuns splits a sign-stripped, colon-free, dash-free interval field
// body into maximal letter runs and numeric runs (digits with optional embedded
// '.'), reproducing the digit↔letter field boundaries of PostgreSQL's
// ParseDateTime lexer. A letter run glued immediately before a digit that is NOT
// an absolute-datetime token is the swallowed-DTK_DATE case PostgreSQL rejects
// (see expandIntervalFields), so this returns ok=false for it. Any other
// character (the body is expected to be alphanumeric plus '.') also yields
// ok=false.
func splitAlphaNumRuns(body string) (toks []string, ok bool) {
	if body == "" {
		return nil, false
	}
	for i := 0; i < len(body); {
		c := body[i]
		switch {
		case isASCIILetter(c):
			j := i
			for j < len(body) && isASCIILetter(body[j]) {
				j++
			}
			word := body[i:j]
			// A letter run glued before a digit that datetktbl does not
			// recognise is swallowed into an invalid DTK_DATE by PostgreSQL.
			if j < len(body) && isASCIIDigit(body[j]) && !inDatetktbl(word) {
				return nil, false
			}
			toks = append(toks, word)
			i = j
		case isASCIIDigit(c) || c == '.':
			j := i
			for j < len(body) && (isASCIIDigit(body[j]) || body[j] == '.') {
				j++
			}
			toks = append(toks, body[i:j])
			i = j
		default:
			return nil, false
		}
	}
	return toks, true
}

func isASCIILetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isASCIIDigit(c byte) bool  { return c >= '0' && c <= '9' }

// splitYearMonthTrailer reproduces the two PostgreSQL ParseDateTime lexer splits
// that the coarser whitespace tokenizer misses on an unsigned digit-led SQL
// year-month field carrying a trailing run.
//
//  1. Fractional-seconds tail (`1-2.5`, `1-.5`, `0-2.5`, `1-2.5day`): PostgreSQL
//     reads the `<digits>-<digits>` run as a DTK_DATE field and then, because '.'
//     is neither alphanumeric nor the '-' delimiter, stops and starts a fresh
//     field at the '.', so the remainder decodes on its own (`1-2.5` → 1 year 2
//     mons + 0.5 s; `1-2.5day` → the `.5day` remainder splits again into
//     `.5`+`day` → +12:00:00). A '.' tail splits even when the month part is
//     empty (`1-.5`, `0-.5`).
//  2. Unit-word tail glued to a NON-EMPTY month (`1-2h`, `1-2mon3d`, `1-2h30m`):
//     the DTK_DATE collection stops at the first non-delimiter letter, which
//     starts a fresh DTK_STRING field, so the trailing unit word decodes on its
//     own and is later absorbed as a no-op (`1-2h` → 1 year 2 mons). This tail
//     only splits when the month part has at least one digit: with an EMPTY
//     month, PostgreSQL's else-branch collects the letters INTO the DTK_DATE
//     token (`isalnum`), yielding one malformed year-month token that errors
//     (`1-h`, `1-day`) — goopg reproduces that by leaving such a body whole.
//
// Returns ok=true with pre = the `<digits>-<digits?>` field and rest = the
// remainder beginning at the '.'/letter. Any other body — no tail (`1-2`, `1-`),
// a second '-' (`1-2-3`, `1--2`), an empty-month letter tail (`1-h`), or a
// non-digit head — returns ok=false so the caller decodes it whole (matching PG,
// which either accepts the bare year-month or rejects the malformed field).
func splitYearMonthTrailer(body string) (pre, rest string, ok bool) {
	if len(body) == 0 || !isASCIIDigit(body[0]) {
		return "", "", false
	}
	i := 0
	for i < len(body) && isASCIIDigit(body[i]) { // year digits
		i++
	}
	if i >= len(body) || body[i] != '-' {
		return "", "", false
	}
	i++ // consume the '-' separator
	monStart := i
	for i < len(body) && isASCIIDigit(body[i]) { // month digits (may be zero)
		i++
	}
	if i >= len(body) {
		return "", "", false // bare `<year>-<month?>`, no trailer to split
	}
	switch {
	case body[i] == '.':
		// Fractional-seconds tail; splits regardless of month digits.
		return body[:i], body[i:], true
	case isASCIILetter(body[i]) && i > monStart:
		// Unit-word tail, only after a non-empty month (`1-2h`, not `1-h`).
		return body[:i], body[i:], true
	default:
		// End of field, a second '-' (`1-2-3`), or an empty-month letter tail
		// (`1-h`) — left for the whole-field year-month decoder / error path.
		return "", "", false
	}
}

// ParseIntervalBody decodes a full free-form interval literal body into
// months/days/micros components, mirroring PostgreSQL's interval_in
// (postgres/src/backend/utils/adt/timestamp.c): it first tries the free-form
// `<magnitude> <unit>` / `HH:MM:SS` decoder (DecodeInterval, here
// decodeIntervalFields), and only when that fails does it fall back to ISO 8601
// duration parsing (DecodeISO8601Interval, here decodeISO8601Interval) — exactly
// PostgreSQL's "if those functions think it's a bad format, try ISO8601 style"
// ordering. A body the free-form decoder accepts is therefore never overridden
// by the ISO reading.
//
// This is the single tokenizer shared by the parser's Form-2 typed-literal
// path and the executor's `::interval` / CAST path (the sibling paths the
// practice card warns must not diverge).
//
// Returns ok=false for any body that neither decoder accepts.
func ParseIntervalBody(body string) (months, days int32, micros int64, ok bool) {
	if mo, d, mu, fok := decodeIntervalFields(body); fok {
		return mo, d, mu, true
	}
	// PostgreSQL feeds the ORIGINAL (untrimmed) string to DecodeISO8601Interval,
	// whose `str[0] != 'P'` guard makes a leading space fail — so pass body
	// verbatim rather than trimming, keeping the space-sensitivity byte-accurate.
	return decodeISO8601Interval(body)
}

// scanISONumberPrefix returns the leading run of s that strtod would consume as
// a decimal number (optional sign, integer digits, optional `.frac`, optional
// `[eE][+-]?digits` exponent) plus the remainder, mirroring how PostgreSQL's
// ParseISO8601Number hands strtod the field start. It does not skip leading
// whitespace (ISO 8601 durations never contain internal spaces), so a
// space-prefixed field yields an empty number and the caller rejects it.
func scanISONumberPrefix(s string) (num, rest string) {
	i, n := 0, len(s)
	if i < n && (s[i] == '+' || s[i] == '-') {
		i++
	}
	for i < n && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i < n && s[i] == '.' {
		i++
		for i < n && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	}
	if i < n && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < n && (s[j] == '+' || s[j] == '-') {
			j++
		}
		k := j
		for k < n && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k > j { // a bare `e` with no exponent digits is not part of the number
			i = k
		}
	}
	return s[:i], s[i:]
}

// parseISO8601Number consumes a leading numeric field from an ISO 8601 duration,
// returning its integer part (truncated toward zero), fractional remainder
// (|fval|<1, sharing the sign) and the unconsumed tail, mirroring PostgreSQL's
// ParseISO8601Number (postgres/src/backend/utils/adt/datetime.c): a field with
// no numeric content, or a magnitude outside ±1e15 (PG's isnan / overflow
// guard), is rejected (ok=false).
func parseISO8601Number(s string) (val int64, fval float64, rest string, ok bool) {
	num, r := scanISONumberPrefix(s)
	hasDigit := false
	for i := 0; i < len(num); i++ {
		if num[i] >= '0' && num[i] <= '9' {
			hasDigit = true
			break
		}
	}
	if !hasDigit {
		return 0, 0, s, false
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || math.IsNaN(f) || f < -1.0e15 || f > 1.0e15 {
		return 0, 0, s, false
	}
	ip := math.Trunc(f) // truncate toward zero, matching PG's floor/-floor split
	return int64(ip), f - ip, r, true
}

// iso8601IntegerWidth counts the integral digits of an ISO 8601 numeric field
// (ignoring a leading '-' and any fractional part), mirroring PostgreSQL's
// ISO8601IntegerWidth. PostgreSQL uses this to recognise the "basic" alternative
// format, where an 8-digit date field (`P00020607`) or a 6-digit time field
// (`T013000`) packs multiple components into one number.
func iso8601IntegerWidth(field string) int {
	if len(field) > 0 && field[0] == '-' {
		field = field[1:]
	}
	w := 0
	for w < len(field) && field[w] >= '0' && field[w] <= '9' {
		w++
	}
	return w
}

// clampISOInterval bounds an accumulated ISO 8601 interval to the on-disk
// Interval field widths (month/day are int32, time is int64), mirroring
// PostgreSQL's itmin2interval overflow check (total months against INT_MAX).
// Returns ok=false on overflow.
func clampISOInterval(months, days, micros int64) (int32, int32, int64, bool) {
	if months > math.MaxInt32 || months < math.MinInt32 ||
		days > math.MaxInt32 || days < math.MinInt32 {
		return 0, 0, 0, false
	}
	return int32(months), int32(days), micros, true
}

// decodeISO8601Interval decodes an ISO 8601 duration into interval
// months/days/micros, mirroring PostgreSQL's DecodeISO8601Interval
// (postgres/src/backend/utils/adt/datetime.c). It accepts both the "format with
// designators" (`P1Y2M3DT4H5M6S`, `PT1H30M`, `P1.5D`; a `W` week field may coexist
// with others and decimals are allowed in any field) and the "alternative format"
// — basic (`P00020607T013000`) and extended (`P0002-06-07T01:30:00`). Before the
// `T`, the unit suffixes are Y/M/W/D; after it, H/M/S. Every designator field and
// every extended-alternative field routes through the shared IntervalUnitToParts
// (the same fractional-spill math the free-form decoder uses), so the two paths
// cannot drift; only the two "basic" packed formats need inline component math.
//
// PostgreSQL only reaches this decoder when the free-form DecodeInterval returns
// DTERR_BAD_FORMAT (interval_in, timestamp.c), so ParseIntervalBody calls it only
// as a fallback. Returns ok=false for anything that is not a well-formed ISO 8601
// duration.
func decodeISO8601Interval(str string) (months, days int32, micros int64, ok bool) {
	if len(str) < 2 || str[0] != 'P' {
		return 0, 0, 0, false
	}
	str = str[1:]
	datepart := true
	havefield := false
	var mo, dy, us int64
	add := func(unit string, val int64, fval float64) {
		m, d, mu, _ := IntervalUnitToParts(val, fval, unit)
		mo += int64(m)
		dy += int64(d)
		us += mu
	}
	for len(str) > 0 {
		if str[0] == 'T' { // begin the time part
			datepart = false
			havefield = false
			str = str[1:]
			continue
		}
		fieldstart := str
		val, fval, next, nok := parseISO8601Number(str)
		if !nok {
			return 0, 0, 0, false
		}
		str = next
		var unit byte // '\0' when the number ran to end-of-string
		if len(str) > 0 {
			unit = str[0]
			str = str[1:]
		}
		if datepart {
			switch unit { // before T: Y M W D (+ alternative formats)
			case 'Y':
				add("year", val, fval)
			case 'M':
				add("month", val, fval)
			case 'W':
				add("week", val, fval)
			case 'D':
				add("day", val, fval)
			case 'T', 0, '-':
				// ISO 8601 4.4.3.3 alternative format. Basic: an 8-digit date
				// field packs YYYYMMDD into one number (only as the first field).
				if (unit == 'T' || unit == 0) && iso8601IntegerWidth(fieldstart) == 8 && !havefield {
					mo += (val/10000)*12 + (val/100)%100
					dy += val % 100
					us += intervalFractMicros(fval, usecsPerDay)
					if unit == 0 {
						return clampISOInterval(mo, dy, us)
					}
					datepart = false // unit == 'T'
					havefield = false
					continue
				}
				// Extended alternative format (`0002-06-07`), also the T/'\0'
				// fall-through when the date field was not 8 digits wide.
				if havefield {
					return 0, 0, 0, false
				}
				add("year", val, fval)
				if unit == 0 {
					return clampISOInterval(mo, dy, us)
				}
				if unit == 'T' {
					datepart = false
					havefield = false
					continue
				}
				// unit == '-': the month, then (after another '-') the day.
				if val, fval, str, nok = parseISO8601Number(str); !nok {
					return 0, 0, 0, false
				}
				add("month", val, fval)
				if len(str) == 0 {
					return clampISOInterval(mo, dy, us)
				}
				if str[0] == 'T' {
					datepart = false
					havefield = false
					continue
				}
				if str[0] != '-' {
					return 0, 0, 0, false
				}
				str = str[1:]
				if val, fval, str, nok = parseISO8601Number(str); !nok {
					return 0, 0, 0, false
				}
				add("day", val, fval)
				if len(str) == 0 {
					return clampISOInterval(mo, dy, us)
				}
				if str[0] == 'T' {
					datepart = false
					havefield = false
					continue
				}
				return 0, 0, 0, false
			default:
				return 0, 0, 0, false
			}
		} else {
			switch unit { // after T: H M S (+ alternative formats)
			case 'H':
				add("hour", val, fval)
			case 'M':
				add("minute", val, fval)
			case 'S':
				add("second", val, fval)
			case 0, ':':
				// Basic alternative format: a 6-digit time field packs HHMMSS.
				if unit == 0 && iso8601IntegerWidth(fieldstart) == 6 && !havefield {
					us += (val/10000)*usecsPerHour + ((val/100)%100)*usecsPerMinute +
						(val%100)*usecsPerSecond + intervalFractMicros(fval, 1)
					return clampISOInterval(mo, dy, us)
				}
				// Extended alternative format (`01:30:00`).
				if havefield {
					return 0, 0, 0, false
				}
				us += val*usecsPerHour + intervalFractMicros(fval, usecsPerHour)
				if unit == 0 {
					return clampISOInterval(mo, dy, us)
				}
				if val, fval, str, nok = parseISO8601Number(str); !nok {
					return 0, 0, 0, false
				}
				us += val*usecsPerMinute + intervalFractMicros(fval, usecsPerMinute)
				if len(str) == 0 {
					return clampISOInterval(mo, dy, us)
				}
				if str[0] != ':' {
					return 0, 0, 0, false
				}
				str = str[1:]
				if val, fval, str, nok = parseISO8601Number(str); !nok {
					return 0, 0, 0, false
				}
				us += val*usecsPerSecond + intervalFractMicros(fval, usecsPerSecond)
				if len(str) == 0 {
					return clampISOInterval(mo, dy, us)
				}
				return 0, 0, 0, false
			default:
				return 0, 0, 0, false
			}
		}
		havefield = true
	}
	return clampISOInterval(mo, dy, us)
}

// decodeIntervalFields decodes a free-form interval literal body into
// months/days/micros components, mirroring PostgreSQL's DecodeInterval
// (postgres/src/backend/utils/adt/datetime.c) for the field shapes goopg
// supports: any number of `<magnitude> <unit>` pairs interleaved in any order
// with `[+-]HH:MM[:SS[.ffffff]]` time words. Each field carries its own sign,
// e.g. `-1 day 05:00:00` = (days -1, +5h) and `1 day -05:00:00` = (days 1, -5h).
//
// A unitless number defaults to SECONDS (`interval '5'` → 00:00:05,
// `interval '1 day 5'` → 1 day 00:00:05), mirroring PostgreSQL's
// DecodeInterval, whose DTK_NUMBER branch resolves an unspecified field via
// the typmod `range` switch, falling through to DTK_SECOND for the default
// full-range typmod (unimplemented_feat #5(d-i)). PostgreSQL scans fields
// right-to-left carrying the rightmost unit leftward, so the only unitless
// field that decodes without a field-mask collision is a SINGLE trailing
// value: two bare numbers, or a bare number before a `<num> <unit>` pair,
// both re-use the same carried/default field and error (`interval '1 2 days'`,
// `interval '5 garbage'`). goopg reproduces that by accepting a unitless
// number only as the FINAL field, and only when the SECOND slot is still free
// — a preceding time word (`HH:MM[:SS]`, always DTK_TIME_M ⊇ SECOND) or an
// explicit seconds unit occupies it, making the default-seconds field a
// collision (`interval '1 day 05:00:00 5'` errors, matching PG).
//
// Returns ok=false for any body that doesn't decompose cleanly.
func decodeIntervalFields(body string) (months, days int32, micros int64, ok bool) {
	// Expand glued `<magnitude><unit>` fields (`2h30m`, `1.5h`, `1y2mon`) into
	// separate tokens first, so the field loop below sees the same token stream
	// whether the literal was written glued or spaced. expandIntervalFields
	// rejects the swallowed-DTK_DATE forms PostgreSQL rejects (`1day2h`,
	// `1w2d`), keeping glued and spaced parsing in lock-step with PG.
	fields, ok := expandIntervalFields(strings.Fields(strings.TrimSpace(body)))
	if !ok {
		return 0, 0, 0, false
	}
	if len(fields) == 0 {
		return 0, 0, 0, false
	}
	consumedAny := false
	// fmask accumulates the field-mask bits already set, exactly like
	// DecodeInterval's fmask: a field whose tmask intersects fmask is a
	// bad-format collision (`interval '1 h 2 h'`, `interval '1 mon 2 mons'`,
	// `interval '1 h 05:00:00'`, `interval '05:00:00 5'`).
	var fmask intervalFieldMask
	add := func(tmask intervalFieldMask) bool {
		if fmask&tmask != 0 {
			return false
		}
		fmask |= tmask
		return true
	}
	// prevAbsorbs tracks whether the field just consumed was a year-month or a
	// time field — the two field kinds that, in PostgreSQL's right-to-left
	// DecodeInterval, reset `type`/`parsing_unit_val` WITHOUT consuming a
	// magnitude. A bare unit word immediately following such a field is
	// therefore swallowed as a no-op: `1-2h`/`1-2 h` → 1 year 2 mons, `12:00 h`
	// → 12:00:00 (the year-month DTK_MONTH override / DTK_TIME both discard the
	// pending unit `type`). A lone unit word anywhere else — leading, or after a
	// number-pair or another absorbed unit — is DTERR_BAD_FORMAT (`5 h mon`,
	// `1-2 h h`, `1 day mon` all error), mirroring DecodeInterval's dangling /
	// "consecutive unhandled units" rejection.
	prevAbsorbs := false
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if strings.ContainsRune(f, ':') {
			// Time component (HH:MM[:SS]). PostgreSQL's DecodeTimeForInterval
			// stamps DTK_TIME_M (HOUR|MINUTE|all SECOND slots) whether or not a
			// seconds subfield is present, so a time word collides with any
			// preceding HOUR/MINUTE/SECOND field.
			mu, tok := parseIntervalTimeToken(f)
			if !tok || !add(imTime) {
				return 0, 0, 0, false
			}
			micros += mu
			consumedAny = true
			prevAbsorbs = true
			continue
		}
		// SQL-standard year-month field (`1-2` → 1 year 2 months). A
		// self-contained field that contributes months only (PostgreSQL's
		// DTK_NUMBER hyphen branch, type DTK_MONTH); it occupies the MONTH slot,
		// so `interval '1-2 3 mons'` collides.
		if ym, isYM := parseYearMonthField(f); isYM {
			if !add(imMonth) {
				return 0, 0, 0, false
			}
			months += ym
			consumedAny = true
			prevAbsorbs = true
			continue
		}
		val, fval, mok := ParseIntervalMagnitude(f)
		if !mok {
			// Not a magnitude: the only field shape still acceptable here is a
			// bare unit word swallowed by a preceding year-month/time field
			// (see prevAbsorbs). It contributes nothing and adds no fmask bit,
			// so a later collision is decided by the real fields around it.
			if prevAbsorbs {
				if _, uok := canonicalIntervalUnit(f); uok {
					prevAbsorbs = false
					continue
				}
			}
			return 0, 0, 0, false
		}
		// A real magnitude field consumes any pending unit type (it is not a
		// candidate to be swallowed), so a following lone unit word is a
		// collision, not an absorb.
		prevAbsorbs = false
		// A magnitude followed by a unit word forms a `<num> <unit>` pair.
		if i+1 < len(fields) {
			unit, uok := canonicalIntervalUnit(fields[i+1])
			if !uok {
				// A non-final magnitude with no following unit word is the
				// ambiguous type-carry case PostgreSQL rejects (a second bare
				// number, or one preceding a `<num> <unit>` pair / time word).
				return 0, 0, 0, false
			}
			i++ // consume the unit word
			m, d, mu, pok := IntervalUnitToParts(val, fval, unit)
			if !pok || !add(intervalUnitMask(unit, fval != 0)) {
				return 0, 0, 0, false
			}
			months += m
			days += d
			micros += mu
			consumedAny = true
			continue
		}
		// Trailing unitless magnitude: default to SECONDS per PostgreSQL's
		// full-range typmod. A collision with an already-occupied SECOND slot
		// is a bad-format error, exactly as DecodeInterval's `tmask & fmask`
		// check rejects it (a fractional value widens to imAllSecs).
		m, d, mu, pok := IntervalUnitToParts(val, fval, "second")
		secondMask := imSecond
		if fval != 0 {
			secondMask = imAllSecs
		}
		if !pok || !add(secondMask) {
			return 0, 0, 0, false
		}
		months += m
		days += d
		micros += mu
		consumedAny = true
	}
	if !consumedAny {
		return 0, 0, 0, false
	}
	return months, days, micros, true
}
