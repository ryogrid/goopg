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
// #5(c); microsecond fractions below 1µs are discarded); single-letter
// forms (`w`/`c`) remain out of scope (deferred, d-ii positional ambiguity).
func canonicalIntervalUnit(w string) (string, bool) {
	switch strings.ToLower(w) {
	case "year", "years", "yr", "yrs":
		return "year", true
	case "month", "months", "mon", "mons":
		return "month", true
	case "week", "weeks":
		return "week", true
	case "decade", "decades", "dec", "decs":
		return "decade", true
	case "century", "centuries", "cent":
		return "century", true
	case "millennium", "millennia", "mil", "mils":
		return "millennium", true
	case "day", "days":
		return "day", true
	case "hour", "hours", "hr", "hrs":
		return "hour", true
	case "minute", "minutes", "min", "mins":
		return "minute", true
	case "second", "seconds", "sec", "secs":
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
// with nothing trailing. The result is a whole-month value years*12 ± months
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
	// val2 >= MONTHS_PER_YEAR` for `1--2`, `1-12`, `1-13`).
	mon, err := strconv.ParseInt(f[sep+1:], 10, 64)
	if err != nil || mon < 0 || mon >= 12 {
		return 0, false
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

// ParseIntervalBody decodes a full free-form interval literal body into
// months/days/micros components, mirroring PostgreSQL's DecodeInterval
// (postgres/src/backend/utils/adt/datetime.c) for the field shapes goopg
// supports: any number of `<magnitude> <unit>` pairs interleaved in any order
// with `[+-]HH:MM[:SS[.ffffff]]` time words. Each field carries its own sign,
// e.g. `-1 day 05:00:00` = (days -1, +5h) and `1 day -05:00:00` = (days 1, -5h).
//
// This is the single tokenizer shared by the parser's Form-2 typed-literal
// path and the executor's `::interval` / CAST path (the sibling paths the
// practice card warns must not diverge).
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
func ParseIntervalBody(body string) (months, days int32, micros int64, ok bool) {
	fields := strings.Fields(strings.TrimSpace(body))
	if len(fields) == 0 {
		return 0, 0, 0, false
	}
	consumedAny := false
	secondsOccupied := false // SECOND field-mask bit already set by a prior field
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if strings.ContainsRune(f, ':') {
			// Time component (HH:MM[:SS]). PostgreSQL's DecodeTimeForInterval
			// stamps DTK_TIME_M (HOUR|MINUTE|SECOND) whether or not a seconds
			// subfield is present, so a time word always occupies the SECOND
			// slot for the default-unit collision check below.
			mu, tok := parseIntervalTimeToken(f)
			if !tok {
				return 0, 0, 0, false
			}
			micros += mu
			secondsOccupied = true
			consumedAny = true
			continue
		}
		// SQL-standard year-month field (`1-2` → 1 year 2 months). A
		// self-contained field that contributes months only (PostgreSQL's
		// DTK_NUMBER hyphen branch, type DTK_MONTH); it consumes no following
		// unit word and never occupies the SECOND slot.
		if ym, isYM := parseYearMonthField(f); isYM {
			months += ym
			consumedAny = true
			continue
		}
		val, fval, mok := ParseIntervalMagnitude(f)
		if !mok {
			return 0, 0, 0, false
		}
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
			if !pok {
				return 0, 0, 0, false
			}
			months += m
			days += d
			micros += mu
			if unit == "second" {
				secondsOccupied = true
			}
			consumedAny = true
			continue
		}
		// Trailing unitless magnitude: default to SECONDS per PostgreSQL's
		// full-range typmod. A collision with an already-occupied SECOND slot
		// is a bad-format error, exactly as DecodeInterval's `tmask & fmask`
		// check rejects it.
		if secondsOccupied {
			return 0, 0, 0, false
		}
		m, d, mu, pok := IntervalUnitToParts(val, fval, "second")
		if !pok {
			return 0, 0, 0, false
		}
		months += m
		days += d
		micros += mu
		secondsOccupied = true
		consumedAny = true
	}
	if !consumedAny {
		return 0, 0, 0, false
	}
	return months, days, micros, true
}
