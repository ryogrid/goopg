package executor

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/catalog"
)

// COPY TEXT format (the default for `COPY ... TO/FROM STDOUT/STDIN`):
// columns separated by `\t`, rows terminated by `\n`, NULL written as
// `\N`. Backslash, newline, carriage return, and tab are escaped on
// output; backslash escapes (\b \f \n \r \t \v \\, \NNN octal,
// \xHH hex, plus any other \<c> falling through to c) are decoded on
// input. The format is documented in docs/design/0014-copy.md and
// mirrors upstream's src/backend/commands/copyfromparse.c / copyto.c.
//
// The codec deals in single rows. The wire layer splits incoming
// CopyData payloads into lines before calling DecodeCopyTextRow and
// concatenates EncodeCopyTextRow output into CopyData frames; v0
// emits one row per CopyData frame.

// EncodeCopyTextRow appends one COPY-text-formatted row to dst,
// including the trailing newline.
func EncodeCopyTextRow(dst []byte, row Row, cols []catalog.Column) ([]byte, error) {
	if len(row) != len(cols) {
		return nil, fmt.Errorf("EncodeCopyTextRow: %d cols vs %d datums", len(cols), len(row))
	}
	for i, c := range cols {
		if i > 0 {
			dst = append(dst, '\t')
		}
		d := row[i]
		if d.IsNull() {
			dst = append(dst, '\\', 'N')
			continue
		}
		s, err := datumToCopyText(c.Type, d)
		if err != nil {
			return nil, err
		}
		dst = appendCopyTextEscaped(dst, s)
	}
	dst = append(dst, '\n')
	return dst, nil
}

// DecodeCopyTextRow parses a single COPY-text-formatted row (with no
// trailing newline) into Datums shaped by cols. Returns an error
// when the field count doesn't match cols, or a column value can't
// parse into its declared type.
func DecodeCopyTextRow(line []byte, cols []catalog.Column, nullStr string) (Row, error) {
	fields, err := splitCopyTextFields(line, nullStr)
	if err != nil {
		return nil, err
	}
	if len(fields) != len(cols) {
		return nil, fmt.Errorf("COPY: row has %d fields, expected %d", len(fields), len(cols))
	}
	row := make(Row, len(cols))
	for i, raw := range fields {
		if raw.isNull {
			row[i] = NullDatum
			continue
		}
		d, err := copyTextToDatum(cols[i].Type, raw.bytes)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", cols[i].Name, err)
		}
		row[i] = d
	}
	return row, nil
}

type copyField struct {
	bytes  []byte
	isNull bool
}

// splitCopyTextFields splits one COPY-text row on unescaped tabs and
// decodes per-byte backslash escapes. A field whose only content is
// the unescaped sequence `\N` is the SQL NULL sentinel; that's
// distinct from the literal two-byte string "\N", which would have
// to be written `\\N` on the wire.
func splitCopyTextFields(line []byte, nullStr string) ([]copyField, error) {
	// Phase 1: split on unescaped tabs. Backslash escapes are NOT
	// processed here — we first find tab boundaries, then unescape
	// each field in phase 2. This prevents a trailing `\` in a
	// field from consuming the tab separator as `\t`.
	var rawFields [][]byte
	start := 0
	for i := 0; i < len(line); i++ {
		if line[i] == '\t' {
			rawFields = append(rawFields, line[start:i])
			start = i + 1
		}
	}
	rawFields = append(rawFields, line[start:])

	nullBytes := []byte(nullStr)
	out := make([]copyField, 0, len(rawFields))
	for _, raw := range rawFields {
		if bytes.Equal(raw, nullBytes) {
			out = append(out, copyField{isNull: true})
			continue
		}
		cf, err := unescapeCopyTextField(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, cf)
	}
	return out, nil
}

// unescapeCopyTextField processes escape sequences in one COPY TEXT
// field (no tab separators). Returns the decoded field or an error.
func unescapeCopyTextField(raw []byte) (copyField, error) {
	// Fast path: no backslash means no escaping needed.
	if bytes.IndexByte(raw, '\\') < 0 {
		cp := make([]byte, len(raw))
		copy(cp, raw)
		return copyField{bytes: cp}, nil
	}

	var field []byte
	isNull := false
	i := 0
	for i < len(raw) {
		b := raw[i]
		if b != '\\' {
			field = append(field, b)
			i++
			continue
		}
		// Escape sequence
		if i+1 >= len(raw) {
			return copyField{}, fmt.Errorf("COPY: trailing backslash")
		}
		c := raw[i+1]
		i += 2
		switch c {
		case 'N':
			if len(field) == 0 && !isNull {
				isNull = true
			}
			field = append(field, 'N')
		case 'b':
			field = append(field, 0x08)
		case 'f':
			field = append(field, 0x0c)
		case 'n':
			field = append(field, '\n')
		case 'r':
			field = append(field, '\r')
		case 't':
			field = append(field, '\t')
		case 'v':
			field = append(field, 0x0b)
		case '\\':
			field = append(field, '\\')
		case 'x', 'X':
			v, n := decodeHexEscape(raw[i:])
			if n == 0 {
				field = append(field, c)
			} else {
				field = append(field, v)
				i += n
			}
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v := c - '0'
			extra := 0
			for extra < 2 && i+extra < len(raw) {
				nc := raw[i+extra]
				if nc < '0' || nc > '7' {
					break
				}
				v = v<<3 | (nc - '0')
				extra++
			}
			i += extra
			field = append(field, v)
		default:
			field = append(field, c)
		}
	}
	if isNull && len(field) == 1 && field[0] == 'N' {
		return copyField{isNull: true}, nil
	}
	cp := make([]byte, len(field))
	copy(cp, field)
	return copyField{bytes: cp}, nil
}

func decodeHexEscape(rest []byte) (byte, int) {
	var v byte
	n := 0
	for n < 2 && n < len(rest) {
		c := rest[n]
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return v, n
		}
		v = v<<4 | d
		n++
	}
	return v, n
}

// appendCopyTextEscaped appends s to dst with the COPY TEXT escapes
// applied. Backslash, newline, carriage return, and tab are escaped;
// other characters pass through unchanged.
func appendCopyTextEscaped(dst []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			dst = append(dst, s[i])
		}
	}
	return dst
}

// datumToCopyText renders a non-null Datum into the byte string
// COPY TEXT expects, before per-byte escaping.
func datumToCopyText(t catalog.Type, d Datum) (string, error) {
	switch t.Name {
	case "int4", "integer", "int", "int8", "bigint":
		if d.Kind != KindInt {
			return "", fmt.Errorf("expected int datum for %s, got kind %d", t.Name, d.Kind)
		}
		return strconv.FormatInt(d.Int, 10), nil
	case "bool", "boolean":
		if d.Kind != KindBool {
			return "", fmt.Errorf("expected bool datum, got kind %d", d.Kind)
		}
		if d.BoolValue() {
			return "t", nil
		}
		return "f", nil
	case "timestamp", "timestamptz":
		if d.Kind != KindTime {
			return "", fmt.Errorf("expected time datum, got kind %d", d.Kind)
		}
		return d.TimeValue().UTC().Format("2006-01-02 15:04:05.000000"), nil
	default:
		switch d.Kind {
		case KindString:
			return d.StringValue(), nil
		case KindBytes:
			return string(d.BytesValue()), nil
		case KindInt:
			return strconv.FormatInt(d.Int, 10), nil
		case KindBool:
			if d.BoolValue() {
				return "t", nil
			}
			return "f", nil
		case KindNumeric:
			return numericText(d), nil
		default:
			return "", fmt.Errorf("kind %d cannot encode as %s in COPY TEXT", d.Kind, t.Name)
		}
	}
}

// copyTextToDatum is the inverse of datumToCopyText. raw is the
// already-unescaped byte representation of one column value.
func copyTextToDatum(t catalog.Type, raw []byte) (Datum, error) {
	switch t.Name {
	case "int4", "integer", "int", "int8", "bigint":
		v, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return Datum{}, fmt.Errorf("invalid integer %q: %w", string(raw), err)
		}
		return Datum{Kind: KindInt, Int: v}, nil
	case "bool", "boolean":
		switch string(raw) {
		case "t", "true", "T", "TRUE", "y", "Y", "yes", "1":
			return NewBoolDatum(true), nil
		case "f", "false", "F", "FALSE", "n", "N", "no", "0":
			return NewBoolDatum(false), nil
		default:
			return Datum{}, fmt.Errorf("invalid boolean %q", string(raw))
		}
	case "timestamp", "timestamptz", "date":
		ts, err := parseCopyTimestamp(string(raw))
		if err != nil {
			return Datum{}, err
		}
		return NewTimeDatum(ts), nil
	case "time":
		ts, err := parseTimeString(string(raw))
		if err != nil {
			return Datum{}, err
		}
		return NewTimeDatum(ts), nil
	case "timetz":
		ts, offsetSecs, err := parseTimeTZString(string(raw))
		if err != nil {
			return Datum{}, err
		}
		return NewTimeTZDatum(ts, offsetSecs), nil
	case "numeric", "decimal":
		text := string(raw)
		// M0058-0003: int64 fast path for integer-valued NUMERIC. The
		// HammerDB COPY stream is overwhelmingly small integers since
		// all integer columns are typed NUMERIC.
		if v, scale, ok := parseNumericFast(text); ok {
			return Datum{Kind: KindNumeric, Int: v, Scale: scale}, nil
		}
		// Validate the text is a valid numeric literal before
		// storing.  This catches column-alignment bugs at COPY
		// time instead of silently storing wrong-byte data that
		// surfaces later as DecodeRow errors.
		m, s, err := parseNumeric(text)
		if err != nil {
			return Datum{}, fmt.Errorf("invalid numeric %q: %w", text, err)
		}
		return newNumeric(m, int(s)), nil
	default:
		// text / varchar / char / unknown — keep as String.
		return NewStringDatum(string(raw)), nil
	}
}

// parseTimeString parses a PostgreSQL time string (HH:MM, HH:MM:SS, HH:MM:SS.ffffff)
// into a time.Time anchored at 1970-01-01 UTC. Timezone designators like " PST", " EDT",
// " AM", " PM" are accepted and handled. Full timestamp strings (with date) strip the
// date component to return just the time portion. Timezone-name-only strings like
// "15:36:39 America/New_York" are rejected with an error.
func parseTimeString(s string) (time.Time, error) {
	orig := s
	s = strings.TrimSpace(s)

	// Full timestamp with date prefix: strip date, keep time part.
	// e.g. "2003-03-07 15:36:39 America/New_York" → "15:36:39"
	// PostgreSQL accepts timezone names in full timestamp→time casts (strips them).
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		// Extract time portion after the date (YYYY-MM-DD ).
		rest := strings.TrimSpace(s[10:])
		// Strip any timezone suffix (abbreviation, offset, or named zone like America/New_York).
		if idx := strings.Index(rest, " "); idx >= 0 {
			rest = rest[:idx]
		}
		s = rest
	}

	// Detect and reject named timezone in bare time strings (e.g. "15:36:39 America/New_York").
	if idx := strings.Index(s, " "); idx >= 0 {
		tz := s[idx+1:]
		if strings.Contains(tz, "/") {
			return time.Time{}, &ExecError{Code: "22007",
				Message: fmt.Sprintf("invalid input syntax for type time: %q", orig)}
		}
	}

	// Strip AM/PM suffix.
	upper := strings.ToUpper(s)
	isPM := false
	if strings.HasSuffix(upper, " PM") {
		isPM = true
		s = strings.TrimSpace(s[:len(s)-3])
	} else if strings.HasSuffix(upper, " AM") {
		s = strings.TrimSpace(s[:len(s)-3])
	}

	// Strip timezone abbreviation suffix (e.g. " PST", " EDT", "+05", "+05:30").
	// Only strip if it's after the time portion.
	if idx := strings.LastIndex(s, " "); idx >= 0 {
		s = s[:idx]
	} else if plus := strings.LastIndex(s, "+"); plus > 2 {
		s = s[:plus]
	} else if minus := strings.LastIndex(s, "-"); minus > 2 {
		s = s[:minus]
	}

	// Pre-process special time strings that Go's time.Parse can't handle:
	// - Hour=24 (midnight-of-next-day): normalize to a parseable form first.
	// - Second=60 (leap second): replace with 59 and add 1 sec in post-processing.
	var origHour int = -1
	hasLeapSec := false
	if len(s) >= 2 {
		if h, err := strconv.Atoi(s[:2]); err == nil && h >= 24 {
			origHour = h
			s = "00" + s[2:] // Replace with "00" so time.Parse succeeds
		}
	}
	// Detect ":60" leap-second pattern (HH:MM:60 or HH:MM:60.xxx).
	if len(s) >= 8 && s[5] == ':' {
		secStr := s[6:]
		// Take up to 2 digit characters for the seconds value.
		end := 0
		for end < len(secStr) && secStr[end] >= '0' && secStr[end] <= '9' {
			end++
		}
		if end == 2 {
			if secStr[:2] == "60" {
				hasLeapSec = true
				s = s[:6] + "59" + secStr[2:] // replace :60 with :59
			}
		}
	}

	// Try time layouts.
	layouts := []string{
		"15:04:05.000000",
		"15:04:05.99999",
		"15:04:05.9999",
		"15:04:05.999",
		"15:04:05.99",
		"15:04:05.9",
		"15:04:05",
		"15:04",
	}
	var t time.Time
	var parseErr error
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			t = parsed
			parseErr = nil
			break
		} else {
			parseErr = err
		}
	}
	if parseErr != nil {
		return time.Time{}, &ExecError{Code: "22007",
			Message: fmt.Sprintf("invalid input syntax for type time: %q", orig)}
	}

	// Capture the actual parsed components.
	// If we substituted the hour (for h>=24), use origHour instead.
	parsedH := t.Hour()
	if origHour >= 0 {
		parsedH = origHour
	}

	// Apply AM/PM to parsedH.
	if isPM && parsedH < 12 {
		parsedH += 12
	}

	h, m, sec, ns := parsedH, t.Minute(), t.Second(), t.Nanosecond()

	// Handle leap second: 23:59:60 → 24:00:00 (or carry).
	if hasLeapSec {
		sec = 60
	}
	if sec == 60 {
		sec = 0
		m++
		if m == 60 {
			m = 0
			h++
		}
	}

	// Handle extra fractional precision beyond microseconds (6 digits):
	// Round nanoseconds to nearest microsecond. If rounding causes carry, propagate.
	if ns%1000 >= 500 {
		ns = ((ns / 1000) + 1) * 1000
	} else {
		ns = (ns / 1000) * 1000
	}
	// Carry nanosecond overflow.
	if ns >= 1_000_000_000 {
		ns -= 1_000_000_000
		sec++
		if sec >= 60 {
			sec -= 60
			m++
			if m >= 60 {
				m -= 60
				h++
			}
		}
	}

	// 24:00:00 is a valid time (midnight); h=24 with m=0, s=0, ns=0 is allowed.
	// h > 24, or h=24 with any m/s/ns > 0 are invalid.
	if h > 24 || (h == 24 && (m > 0 || sec > 0 || ns > 0)) {
		return time.Time{}, &ExecError{Code: "22008",
			Message: fmt.Sprintf("date/time field value out of range: %q", orig)}
	}

	// Anchor to epoch date 1970-01-01 UTC. For h=24, store as next-day midnight.
	if h == 24 {
		return time.Date(1970, 1, 2, 0, 0, 0, 0, time.UTC), nil
	}
	return time.Date(1970, 1, 1, h, m, sec, ns, time.UTC), nil
}

// tzAbbrevOffsets maps common timezone abbreviations to their UTC offsets
// in seconds east of UTC (positive = east/ahead, negative = west/behind).
// Matches PostgreSQL's built-in abbreviation table (pg_timezone_abbrevs).
// Where abbreviations conflict (IST, CST) we follow PG's default table.
var tzAbbrevOffsets = map[string]int{
	// Universal
	"UTC": 0, "GMT": 0, "Z": 0, "UT": 0,
	// North America — Standard
	"NST":  -12600, // Newfoundland Standard
	"AST":  -14400, // Atlantic Standard
	"EST":  -18000, // Eastern Standard
	"CST":  -21600, // Central Standard (US; PG default for CST)
	"MST":  -25200, // Mountain Standard
	"PST":  -28800, // Pacific Standard
	"AKST": -32400, // Alaska Standard
	"HST":  -36000, // Hawaii Standard
	// North America — Daylight
	"NDT":  -9000,  // Newfoundland Daylight
	"ADT":  -10800, // Atlantic Daylight
	"EDT":  -14400, // Eastern Daylight
	"CDT":  -18000, // Central Daylight
	"MDT":  -21600, // Mountain Daylight
	"PDT":  -25200, // Pacific Daylight
	"AKDT": -28800, // Alaska Daylight
	"HDT":  -32400, // Hawaii Daylight
	// Europe
	"WET":  0,     // Western European
	"CET":  3600,  // Central European Standard
	"MET":  3600,  // Middle European
	"EET":  7200,  // Eastern European Standard
	"WEST": 3600,  // Western European Summer
	"CEST": 7200,  // Central European Summer
	"MEST": 7200,  // Middle European Summer
	"EEST": 10800, // Eastern European Summer
	"BST":  3600,  // British Summer
	"IST":  3600,  // Irish Summer (PG default for IST — India uses +05:30 explicitly)
	"MSK":  10800, // Moscow
	// Asia/Pacific
	"JST":  32400, // Japan Standard
	"KST":  32400, // Korea Standard
	"AEST": 36000, // Australia Eastern Standard
	"AEDT": 39600, // Australia Eastern Daylight
}

// parseTZOffset parses an explicit timezone offset string like "+05", "-07", "+05:30", "-04:00"
// into seconds east of UTC. Returns (offsetSecs, ok).
func parseTZOffset(s string) (int, bool) {
	if len(s) < 2 {
		return 0, false
	}
	sign := 1
	if s[0] == '-' {
		sign = -1
	} else if s[0] == '+' {
		sign = 1
	} else {
		return 0, false
	}
	rest := s[1:]
	// Try HH:MM or HH
	var h, m int
	if idx := strings.Index(rest, ":"); idx >= 0 {
		hh, err1 := strconv.Atoi(rest[:idx])
		mm, err2 := strconv.Atoi(rest[idx+1:])
		if err1 != nil || err2 != nil {
			return 0, false
		}
		h, m = hh, mm
	} else {
		hh, err := strconv.Atoi(rest)
		if err != nil {
			return 0, false
		}
		h = hh
	}
	if h < 0 || h > 15 || m < 0 || m > 59 {
		return 0, false
	}
	return sign * (h*3600 + m*60), true
}

// parseTimeTZString parses a PostgreSQL timetz string and returns the local
// time (anchored at 1970-01-01 UTC) and the timezone offset in seconds east
// of UTC (positive = east/ahead, negative = west/behind).
//
// Supported inputs:
//   - "HH:MM:SS.ffffff +HH" / "-HH" / "+HH:MM" explicit offset
//   - "HH:MM PDT" / "PST" / etc. timezone abbreviations
//   - "YYYY-MM-DD HH:MM:SS America/New_York" full timestamp with named TZ
//   - "HH:MM:SS" bare time (offset defaults to +00)
//
// Inputs with named timezone in bare time strings (no date) are rejected.
func parseTimeTZString(s string) (time.Time, int, error) {
	orig := s
	s = strings.TrimSpace(s)

	offsetSecs := 0

	// Full timestamp with date prefix: "YYYY-MM-DD HH:MM:SS[±HH[:MM]] [TZ]"
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		dateStr := s[:10]
		rest := strings.TrimSpace(s[10:])
		// Split time portion from space-separated timezone suffix
		var timeStr, tzStr string
		if idx := strings.Index(rest, " "); idx >= 0 {
			timeStr = rest[:idx]
			tzStr = strings.TrimSpace(rest[idx+1:])
		} else {
			timeStr = rest
		}
		if tzStr != "" {
			// Named timezone like "America/New_York"
			if strings.Contains(tzStr, "/") {
				loc, err := time.LoadLocation(tzStr)
				if err == nil {
					// Parse the full datetime in the named timezone
					full := dateStr + " " + timeStr
					for _, layout := range []string{"2006-01-02 15:04:05.999999", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
						if ts, e := time.ParseInLocation(layout, full, loc); e == nil {
							_, off := ts.Zone()
							offsetSecs = off // seconds east of UTC
							// Extract H/M/S from local time
							lt := ts.In(loc)
							t, err := parseTimeString(lt.Format("15:04:05.999999"))
							if err != nil {
								break
							}
							return t, offsetSecs, nil
						}
					}
				}
				// Named zone but couldn't load → strip to just time, offset = 0
			} else if off, ok := parseTZOffset(tzStr); ok {
				offsetSecs = off
			} else if off, ok := tzAbbrevOffsets[strings.ToUpper(tzStr)]; ok {
				offsetSecs = off
			}
		}
		// timeStr may have an inline numeric offset like "13:30:25.575401-04" or "13:30:25+05:30"
		// Extract it before passing to parseTimeString.
		if offsetSecs == 0 {
			if plus := strings.LastIndex(timeStr, "+"); plus > 2 {
				if off, ok := parseTZOffset(timeStr[plus:]); ok {
					offsetSecs = off
					timeStr = timeStr[:plus]
				}
			} else if minus := strings.LastIndex(timeStr, "-"); minus > 2 {
				if off, ok := parseTZOffset(timeStr[minus:]); ok {
					offsetSecs = off
					timeStr = timeStr[:minus]
				}
			}
		}
		// Fall through: just parse the time portion
		t, err := parseTimeString(timeStr)
		if err != nil {
			return time.Time{}, 0, wrapTimeTZError(err, orig)
		}
		return t, offsetSecs, nil
	}

	// Bare time string — reject named timezones (e.g. "15:36:39 America/New_York")
	if idx := strings.Index(s, " "); idx >= 0 {
		tzPart := strings.TrimSpace(s[idx+1:])
		if strings.Contains(tzPart, "/") {
			return time.Time{}, 0, &ExecError{Code: "22007",
				Message: fmt.Sprintf("invalid input syntax for type time with time zone: %q", orig)}
		}
	}

	// Strip AM/PM first (before timezone extraction)
	upper := strings.ToUpper(s)
	isPM := false
	if strings.HasSuffix(upper, " PM") {
		isPM = true
		s = strings.TrimSpace(s[:len(s)-3])
		upper = strings.ToUpper(s)
	} else if strings.HasSuffix(upper, " AM") {
		s = strings.TrimSpace(s[:len(s)-3])
		upper = strings.ToUpper(s)
	}

	// Extract timezone: space-separated abbreviation or explicit offset after time.
	// Unrecognized suffixes are rejected (PostgreSQL errors on unknown TZ abbreviations).
	if idx := strings.LastIndex(s, " "); idx >= 0 {
		tzPart := s[idx+1:]
		timePart := s[:idx]
		// Try as abbreviation
		if off, ok := tzAbbrevOffsets[strings.ToUpper(tzPart)]; ok {
			offsetSecs = off
			s = timePart
		} else if off, ok := parseTZOffset(tzPart); ok {
			offsetSecs = off
			s = timePart
		} else {
			// Unrecognized abbreviation — reject with error (e.g. "m2", "MSK m2")
			return time.Time{}, 0, &ExecError{Code: "22007",
				Message: fmt.Sprintf("invalid input syntax for type time with time zone: %q", orig)}
		}
	} else {
		// No space — check for inline +HH or -HH suffix
		if plus := strings.LastIndex(s, "+"); plus > 2 {
			if off, ok := parseTZOffset(s[plus:]); ok {
				offsetSecs = off
				s = s[:plus]
			}
		} else if minus := strings.LastIndex(s, "-"); minus > 2 {
			if off, ok := parseTZOffset(s[minus:]); ok {
				offsetSecs = off
				s = s[:minus]
			}
		}
	}

	// Now parse the bare time portion (re-apply isPM if needed)
	if isPM {
		// Prepend PM back since parseTimeString handles it
		s = s + " PM"
	}

	t, err := parseTimeString(s)
	if err != nil {
		return time.Time{}, 0, wrapTimeTZError(err, orig)
	}
	return t, offsetSecs, nil
}

// wrapTimeTZError converts a time-parsing error into a timetz error.
// Range errors (22008) are preserved with "time with time zone" wording.
// Syntax errors (22007) are re-wrapped with the original full string.
func wrapTimeTZError(err error, orig string) error {
	if ee, ok := err.(*ExecError); ok && ee.Code == "22008" {
		return &ExecError{Code: "22008",
			Message: fmt.Sprintf("date/time field value out of range: %q", orig)}
	}
	return &ExecError{Code: "22007",
		Message: fmt.Sprintf("invalid input syntax for type time with time zone: %q", orig)}
}

// parseCopyTimestamp accepts the layouts upstream's COPY TEXT input
// commonly produces: with or without fractional seconds, with or
// without timezone.
func parseCopyTimestamp(s string) (time.Time, error) {
	layouts := []string{
		"2006-01-02",
		"2006-01-02 15:04:05.000000",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05.000000-07",
		"2006-01-02 15:04:05-07",
		time.RFC3339,
		time.RFC3339Nano,
	}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts.UTC(), nil
		}
	}
	// Try verbose natural-language format used by PostgreSQL's datetime output
	// e.g. "Tuesday, February 22, 2022 2:22:22.00 PM GMT+05:00".
	if ts, err := parseFullTimestamp(s); err == nil {
		return ts, nil
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", s)
}

// parseTimestampInfinityLiteral recognises PostgreSQL's special timestamp input
// spellings that have no finite time.Time representation: 'infinity' / '+infinity'
// (DTK_LATE) and '-infinity' (DTK_EARLY). Matching is case-insensitive on the
// trimmed text, mirroring the RESERV token lookup in datetime.c (the
// {"+infinity",…,DTK_LATE}, {LATE,…,DTK_LATE} and {EARLY,…,DTK_EARLY} entries in
// datetbl, consumed by DecodeDateTime). Returns the matching KindTime ±infinity
// sentinel Datum and true, or (zero, false) when s is not an infinity literal.
// (unimplemented_feat #5(d-iv))
func parseTimestampInfinityLiteral(s string) (Datum, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "infinity", "+infinity":
		return NewTimestampInfinity(true), true
	case "-infinity":
		return NewTimestampInfinity(false), true
	}
	return Datum{}, false
}

// parseFullTimestamp parses PostgreSQL verbose timestamp strings such as
// "Tuesday, February 22, 2022 2:22:22.00 PM GMT+05:00".
// Timezone offset follows ISO convention: +05:00 means UTC+5.
func parseFullTimestamp(s string) (time.Time, error) {
	// Strip optional leading day-of-week prefix "Monday, ".
	if idx := strings.Index(s, ", "); idx > 0 && idx < 12 {
		// Only strip if the text before the comma looks like a weekday name.
		prefix := strings.TrimSpace(s[:idx])
		weekdays := []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}
		for _, wd := range weekdays {
			if strings.EqualFold(prefix, wd) {
				s = strings.TrimSpace(s[idx+2:])
				break
			}
		}
	}
	// Normalize timezone abbreviation+offset like "GMT+05:00" → "+05:00".
	// GMT prefix is dropped; the +/-HH:MM offset is kept (ISO convention:
	// +05:00 means UTC+5, consistent with PostgreSQL's timestamptz_in).
	s = normalizeTimestampTZ(s)

	// Try layouts for "Month D, YYYY H:MM:SS[.ff] AM/PM ±HH:MM" and variants.
	layouts := []string{
		"January 2, 2006 3:04:05.999 PM -07:00",
		"January 2, 2006 3:04:05.99 PM -07:00",
		"January 2, 2006 3:04:05.9 PM -07:00",
		"January 2, 2006 3:04:05 PM -07:00",
		"January 2, 2006 15:04:05.999 -07:00",
		"January 2, 2006 15:04:05 -07:00",
		"January 2, 2006 3:04:05.999 PM",
		"January 2, 2006 3:04:05 PM",
		"January 2, 2006 15:04:05.999",
		"January 2, 2006 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parseFullTimestamp: cannot parse %q", s)
}

// normalizeTimestampTZ converts POSIX-style timezone names like "GMT+05:00" or
// "UTC-08:00" into ISO-style numeric offsets that Go's time.Parse understands.
// In POSIX convention, GMT+H means UTC-H (sign is inverted vs ISO 8601), so we
// flip the sign when stripping the prefix.  A bare "GMT"/"UTC" maps to "+00:00".
func normalizeTimestampTZ(s string) string {
	for _, prefix := range []string{"GMT", "UTC"} {
		upper := strings.ToUpper(s)
		if idx := strings.Index(upper, prefix); idx >= 0 {
			after := s[idx+len(prefix):]
			if len(after) > 0 && (after[0] == '+' || after[0] == '-') {
				// Flip sign: POSIX "GMT+5" = ISO "-05:00"
				flipped := "-"
				if after[0] == '-' {
					flipped = "+"
				}
				s = s[:idx] + flipped + after[1:]
				return strings.TrimSpace(s)
			} else if len(after) == 0 || after[0] == ' ' {
				s = s[:idx] + "+00:00" + after
				return strings.TrimSpace(s)
			}
		}
	}
	return s
}
