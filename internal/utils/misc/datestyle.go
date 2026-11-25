package misc

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// mergeDateStyle implements PostgreSQL's check_datestyle GUC check hook
// (postgres/src/backend/commands/variable.c): DateStyle is a two-component
// value (a display STYLE — ISO/SQL/Postgres/German — and a field ORDER —
// YMD/DMY/MDY) packed into one comma-separated string, but a SET only
// specifies the token(s) it wants to change. `SET datestyle = 'SQL'` must
// keep the session's current order component rather than resetting it, so
// the merge starts from `current` (the effective value before this SET) and
// only overwrites the component(s) actually named in newValue. GERMAN also
// implies DMY order unless the same SET explicitly names an order. DEFAULT
// resolves recursively against bootVal. Conflicting tokens for the same
// component (e.g. "ISO, SQL") or an unrecognized keyword are rejected,
// matching upstream's ok=false path. Returns the canonical "<Style>, <Order>"
// form guc_tables.c's assign_datestyle stores.
func mergeDateStyle(current, bootVal, newValue string) (string, error) {
	style, order := ParseDateStyleValue(current)
	haveStyle, haveOrder := false, false

	for raw := range strings.SplitSeq(newValue, ",") {
		tok := strings.TrimSpace(raw)
		if tok == "" {
			return "", fmt.Errorf("invalid value for parameter \"datestyle\": %q", newValue)
		}
		switch {
		case strings.EqualFold(tok, "ISO"):
			if haveStyle && style != "ISO" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			style, haveStyle = "ISO", true
		case strings.EqualFold(tok, "SQL"):
			if haveStyle && style != "SQL" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			style, haveStyle = "SQL", true
		case len(tok) >= 8 && strings.EqualFold(tok[:8], "POSTGRES"):
			if haveStyle && style != "Postgres" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			style, haveStyle = "Postgres", true
		case strings.EqualFold(tok, "GERMAN"):
			if haveStyle && style != "German" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			style, haveStyle = "German", true
			if !haveOrder {
				order = "DMY"
			}
		case strings.EqualFold(tok, "YMD"):
			if haveOrder && order != "YMD" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			order, haveOrder = "YMD", true
		case strings.EqualFold(tok, "DMY") || (len(tok) >= 4 && strings.EqualFold(tok[:4], "EURO")):
			if haveOrder && order != "DMY" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			order, haveOrder = "DMY", true
		case strings.EqualFold(tok, "MDY") || strings.EqualFold(tok, "US") || (len(tok) >= 7 && strings.EqualFold(tok[:7], "NONEURO")):
			if haveOrder && order != "MDY" {
				return "", fmt.Errorf("conflicting \"datestyle\" specifications")
			}
			order, haveOrder = "MDY", true
		case strings.EqualFold(tok, "DEFAULT"):
			defStyle, defOrder := ParseDateStyleValue(bootVal)
			if !haveStyle {
				style = defStyle
			}
			if !haveOrder {
				order = defOrder
			}
		default:
			return "", fmt.Errorf("unrecognized key word: %q", tok)
		}
	}
	return style + ", " + order, nil
}

// ParseDateStyleValue extracts the (style, order) pair from an already
// well-formed "<Style>, <Order>" DateStyle string (the only shape
// mergeDateStyle ever writes). Falls back to ISO/MDY for a component the
// string doesn't mention, so a malformed or partial `current` never panics.
// Exported for output formatters (executor/server/COPY) that need to render
// a value according to the session's effective DateStyle.
func ParseDateStyleValue(s string) (style, order string) {
	style, order = "ISO", "MDY"
	for raw := range strings.SplitSeq(s, ",") {
		tok := strings.TrimSpace(raw)
		switch {
		case strings.EqualFold(tok, "ISO"):
			style = "ISO"
		case strings.EqualFold(tok, "SQL"):
			style = "SQL"
		case len(tok) >= 8 && strings.EqualFold(tok[:8], "POSTGRES"):
			style = "Postgres"
		case strings.EqualFold(tok, "GERMAN"):
			style = "German"
		case strings.EqualFold(tok, "YMD"):
			order = "YMD"
		case strings.EqualFold(tok, "DMY") || (len(tok) >= 4 && strings.EqualFold(tok[:4], "EURO")):
			order = "DMY"
		case strings.EqualFold(tok, "MDY") || strings.EqualFold(tok, "US") || (len(tok) >= 7 && strings.EqualFold(tok[:7], "NONEURO")):
			order = "MDY"
		}
	}
	return style, order
}

// FormatDate renders a DATE value's calendar part as text according to the
// given DateStyle (style, order) pair, matching PostgreSQL's EncodeDateOnly
// (postgres/src/backend/utils/adt/datetime.c). ISO output is order-
// independent; German is always DD.MM.YYYY regardless of order (mergeDateStyle
// already forces order=DMY when style=German is set, but FormatDate doesn't
// depend on that invariant). Unrecognized style falls back to ISO, matching
// ParseDateStyleValue's own default.
func FormatDate(t time.Time, style, order string) string {
	y, era := eraYear(t)
	_, mo, d := t.Date()
	switch style {
	case "SQL":
		if order == "DMY" {
			return numericDate(d, int(mo), y, '/') + era
		}
		return numericDate(int(mo), d, y, '/') + era
	case "Postgres":
		if order == "DMY" {
			return numericDate(d, int(mo), y, '-') + era
		}
		return numericDate(int(mo), d, y, '-') + era
	case "German":
		return numericDate(d, int(mo), y, '.') + era
	default:
		// review/260831 UT-9: ISO is the default DateStyle and therefore the
		// shape almost every result row is rendered in; isoDate writes the digits
		// into a stack buffer and allocates exactly the returned string.
		return isoDate(y, int(mo), d, era)
	}
}

// numericDate renders "AA<sep>BB<sep>YYYY".
func numericDate(a, b, year int, sep byte) string {
	var buf [32]byte
	out := appendPad2(buf[:0], a)
	out = append(out, sep)
	out = appendPad2(out, b)
	out = append(out, sep)
	out = appendYear4(out, year)
	return string(out)
}

// clockText renders " HH:MM:SS".
func clockText(t time.Time) string {
	hh, mi, ss := t.Clock()
	var buf [16]byte
	out := append(buf[:0], ' ')
	out = appendPad2(out, hh)
	out = append(out, ':')
	out = appendPad2(out, mi)
	out = append(out, ':')
	out = appendPad2(out, ss)
	return string(out)
}

// appendPad2 appends v as at least two digits ("07", "42", "123"), the way
// %02d prints it.
func appendPad2(dst []byte, v int) []byte {
	if v < 0 {
		dst = append(dst, '-')
		v = -v
	}
	if v < 10 {
		return append(dst, '0', byte('0'+v))
	}
	if v < 100 {
		return append(dst, byte('0'+v/10), byte('0'+v%10))
	}
	return strconv.AppendInt(dst, int64(v), 10)
}

// appendYear4 appends the year as at least four digits ("0001", "2020",
// "294276"), the way %04d prints it. The value is the DISPLAY year, so it is
// already positive (eraDisplay has mapped year 0 to "1 BC").
func appendYear4(dst []byte, y int) []byte {
	if y < 0 {
		dst = append(dst, '-')
		y = -y
	}
	if y < 10000 {
		return append(dst, byte('0'+y/1000%10), byte('0'+y/100%10),
			byte('0'+y/10%10), byte('0'+y%10))
	}
	return strconv.AppendInt(dst, int64(y), 10)
}

// isoDate renders "YYYY-MM-DD" + era.
func isoDate(y, mo, d int, era string) string {
	var b [32]byte
	buf := appendYear4(b[:0], y)
	buf = append(buf, '-')
	buf = appendPad2(buf, mo)
	buf = append(buf, '-')
	buf = appendPad2(buf, d)
	buf = append(buf, era...)
	return string(buf)
}

// isoTimestamp renders "YYYY-MM-DD HH:MM:SS" + frac for the display year y.
func isoTimestamp(t time.Time, y int, frac string) string {
	_, mo, d := t.Date()
	hh, mi, ss := t.Clock()
	var b [48]byte
	buf := appendYear4(b[:0], y)
	buf = append(buf, '-')
	buf = appendPad2(buf, int(mo))
	buf = append(buf, '-')
	buf = appendPad2(buf, d)
	buf = append(buf, ' ')
	buf = appendPad2(buf, hh)
	buf = append(buf, ':')
	buf = appendPad2(buf, mi)
	buf = append(buf, ':')
	buf = appendPad2(buf, ss)
	buf = append(buf, frac...)
	return string(buf)
}

// eraYear gives the year digits PostgreSQL prints for t and the trailing era
// marker. Instants count years astronomically, as PG's date2j/j2date do: 1 BC is
// year 0 and 2 BC is year -1, printed as `-(year - 1)` with " BC" appended after
// everything else, including the time of day and the zone (datetime.c).
//
// Only the digits change. Month, day and clock come from t itself: rebuilding a
// time with the display year would turn a BC leap day into March 1, since the
// display year is not the leap year.
func eraYear(t time.Time) (int, string) {
	y := t.Year()
	if y > 0 {
		return y, ""
	}
	return -(y - 1), " BC"
}

// EraYear exports eraYear for the type-agnostic Datum renderers.
func EraYear(t time.Time) (int, string) { return eraYear(t) }

// fracSecondsSuffix renders t's sub-second component as a leading-dot suffix
// (e.g. ".5", ".000123"), trimmed of trailing zeros, or "" when there is no
// fractional part — matching PostgreSQL's AppendSeconds
// (postgres/src/backend/utils/adt/datetime.c), which omits the decimal point
// entirely for a zero fsec and otherwise prints only the significant digits.
// goopg stores microsecond resolution, matching AppendTimestampSeconds's
// MAX_TIMESTAMP_PRECISION=6 ceiling.
func fracSecondsSuffix(t time.Time) string {
	ns := t.Nanosecond()
	if ns == 0 {
		return ""
	}
	micro := ns / 1000
	var frac [6]byte
	for i := 5; i >= 0; i-- {
		frac[i] = byte('0' + micro%10)
		micro /= 10
	}
	end := len(frac)
	for end > 0 && frac[end-1] == '0' {
		end--
	}
	if end == 0 {
		return ""
	}
	return "." + string(frac[:end])
}

// FormatTimestamp renders a TIMESTAMP/TIMESTAMPTZ value's text according to
// the given DateStyle (style, order) pair, matching PostgreSQL's
// EncodeDateTime (postgres/src/backend/utils/adt/datetime.c) with
// print_tz=false — i.e. `timestamp without time zone`. TIMESTAMPTZ has its own
// entry point, FormatTimestampTZ (timestamptz_out.go), which converts to the
// session TimeZone and prints the zone. Fractional seconds are
// trimmed of trailing zeros (and omitted entirely when zero) via
// fracSecondsSuffix, matching PostgreSQL's AppendSeconds rather than always
// padding to 6 digits.
func FormatTimestamp(t time.Time, style, order string) string {
	body, era := formatTimestampBody(t, style, order)
	return body + era
}

// formatTimestampBody is FormatTimestamp split at the seam upstream's
// EncodeDateTime has: everything up to (but not including) the zone, and the
// trailing era marker, which upstream appends AFTER the zone
// ("0001-02-28 10:00:00+05:53:28 BC"). FormatTimestamp joins the two directly;
// FormatTimestampTZ splices the encoded zone between them.
func formatTimestampBody(t time.Time, style, order string) (body, era string) {
	frac := fracSecondsSuffix(t)
	y, era := eraYear(t)
	_, moName, d := t.Date()
	mo := int(moName)
	switch style {
	case "SQL":
		if order == "DMY" {
			return numericDate(d, mo, y, '/') + clockText(t) + frac, era
		}
		return numericDate(mo, d, y, '/') + clockText(t) + frac, era
	case "Postgres":
		// The weekday and month name belong to the stored date.
		var year [16]byte
		suffix := frac + " " + string(appendYear4(year[:0], y))
		if order == "DMY" {
			return t.Format("Mon 02 Jan 15:04:05") + suffix, era
		}
		return t.Format("Mon Jan 02 15:04:05") + suffix, era
	case "German":
		return numericDate(d, mo, y, '.') + clockText(t) + frac, era
	default:
		// review/260831 UT-9: the ISO default, see isoDate.
		return isoTimestamp(t, y, frac), era
	}
}
