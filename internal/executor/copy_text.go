package executor

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/pgdatetime"
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
// including the trailing newline. dateStyle/dateOrder select the DATE
// column rendering (PostgreSQL's DateStyle GUC style/order components,
// e.g. "ISO"/"MDY" — see config.ParseDateStyleValue); pass "ISO", "MDY"
// for the boot default.
func EncodeCopyTextRow(dst []byte, row Row, cols []catalog.Column, dateStyle, dateOrder, timeZone string) ([]byte, error) {
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
		s, err := datumToCopyText(c.Type, d, dateStyle, dateOrder, timeZone)
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
	return datumsFromCopyFields(fields, cols)
}

// datumsFromCopyFields converts already-split COPY fields into a Row
// shaped by cols. Shared by the TEXT and CSV readers so the two formats
// cannot drift in how a field's bytes become a Datum (the field SPLIT
// differs between the formats; the per-field input-function call does
// not). Callers check the field count first — the two formats report a
// mismatch with different messages.
func datumsFromCopyFields(fields []copyField, cols []catalog.Column) (Row, error) {
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
func datumToCopyText(t catalog.Type, d Datum, dateStyle, dateOrder, timeZone string) (string, error) {
	// Array columns FIRST. A user array column is
	// catalog.Type{Name:<ELEMENT type>, IsArray:true}, so every arm of the
	// switch below would claim the array under its ELEMENT's name and reject
	// the "{1,2}" KindString datum the heap decode produced — `COPY … TO` of an
	// int4[] column was a flat "expected int datum for int4, got kind 3", and a
	// date[] column "expected time datum for date, got kind 3" (only text[]
	// worked, by falling through to the default arm). Upstream's CopyOneRowTo
	// (postgres/src/backend/commands/copyto.c) calls the COLUMN's output
	// function, which for an array column is array_out. goopg renders the array
	// text at heap-decode time (decodeArrayValuePGStyled, under this session's
	// DateStyle/TimeZone), so array_out's job is already done and the text is
	// emitted as-is; the caller's per-byte TEXT escaping and the CSV writer's
	// quoting then apply to it exactly as they do to any other string, which is
	// what makes `{"has,comma"}` come out CSV-quoted like upstream. Same
	// IsArray-before-the-switch guard internal/wal/pgoutput.go and
	// encodeValuePG carry. M0119-0006.
	if t.IsArray {
		if d.Kind != KindString {
			return "", fmt.Errorf("expected array text datum for %s[], got kind %d", t.Name, d.Kind)
		}
		return d.StringValue(), nil
	}
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
	case "date":
		// Previously unhandled: a DATE column fell through to the KindTime
		// gap in the default branch below and made `COPY <table> TO/FROM`
		// hard-error on any table with a date column. M-NIGHTLY (run
		// 20260714-011651) DateStyle output-rendering follow-up.
		if d.Kind != KindTime {
			return "", fmt.Errorf("expected time datum for date, got kind %d", d.Kind)
		}
		return config.FormatDate(d.TimeValue(), dateStyle, dateOrder), nil
	case "timestamp":
		// DateStyle-aware, mirroring the "date" case above and dispatch.go's
		// appendTypedCellText. M-NIGHTLY (run 20260714-011651) DateStyle
		// output-rendering follow-up.
		if d.Kind != KindTime {
			return "", fmt.Errorf("expected time datum, got kind %d", d.Kind)
		}
		return config.FormatTimestamp(d.TimeValue().UTC(), dateStyle, dateOrder), nil
	case "timestamptz":
		// COPY TO emits the same text timestamptz_out does, so this must track
		// dispatch.go's appendTypedCellText exactly: convert into the session
		// TimeZone and print the zone (`COPY z TO STDOUT` under
		// TimeZone='Asia/Kolkata' is "2020-06-15 15:30:00+05:30" upstream).
		// Split off from the shared "timestamp" case in M0119-0006.
		if d.Kind != KindTime {
			return "", fmt.Errorf("expected time datum, got kind %d", d.Kind)
		}
		return config.FormatTimestampTZ(d.TimeValue(), dateStyle, dateOrder, timeZone), nil
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
	// Array columns FIRST, the sibling of datumToCopyText's guard above (Rule
	// #2: the two must agree on the type-set). Without it `COPY t FROM` into an
	// int4[] column took the int4 arm and failed 'invalid integer "{1,2}"',
	// while a date[] column pushed the whole array text through
	// parseCopyTimestampZone. The canonical in-memory array value is its
	// "{1,2}" text (encodeValuePG's IsArray branch hands exactly that to
	// encodeArrayValuePG, which parses the elements and builds the ArrayType
	// blob), so — like the INSERT path — this returns the unescaped text
	// unchanged and lets the encoder do the element work. M0119-0006.
	if t.IsArray {
		return NewStringDatum(string(raw)), nil
	}
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
		// The column's own type decides whether a zone in the text is applied or
		// thrown away (tsZoneMode): a COPY of '2020-01-02 02:00:00+05:30' into a
		// date column is 2020-01-02 upstream, not the previous day.
		ts, err := parseCopyTimestampZone(string(raw), tsZoneModeForType(t.Name))
		if err != nil {
			return Datum{}, err
		}
		// M0119-0006 (41st slice): the same three-way tag the heap decode applies
		// (decodeValuePG, codec.go). tsZoneModeForType above already reads t.Name
		// to decide whether the zone belongs to the VALUE; the subtype records
		// which type it belonged to, so the type-agnostic renderers agree with
		// the typed ones. Hard-won Rule #2.
		switch {
		case isTimestampTZTypeName(t.Name):
			return NewTimestampTZDatum(ts), nil
		case isDateType(t.Name):
			return NewDateDatum(ts), nil
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
	// M0125-0007: pad unpadded hour/minute/second (and any leading date) up
	// front. Everything below indexes fixed offsets — the date-prefix probe
	// `s[4] == '-' && s[7] == '-'`, the hour-24 rewrite on s[:2], the
	// leap-second probe on s[5] — so it only ever worked on padded input.
	s = pgdatetime.NormalizeInput(s)

	// Full timestamp with date prefix: strip date, keep time part.
	// e.g. "2003-03-07 15:36:39 America/New_York" → "15:36:39"
	// PostgreSQL accepts timezone names in full timestamp→time casts (strips them).
	hadDatePrefix := false
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		// Extract time portion after the date (YYYY-MM-DD ); any zone suffix
		// it carries is dropped by stripTimeZoneSuffix below.
		hadDatePrefix = true
		s = strings.TrimSpace(s[10:])
	}

	// Detect and reject named timezone in bare time strings (e.g. "15:36:39 America/New_York").
	if !hadDatePrefix {
		if idx := strings.Index(s, " "); idx >= 0 && strings.Contains(s[idx+1:], "/") {
			return time.Time{}, &ExecError{Code: "22007",
				Message: fmt.Sprintf("invalid input syntax for type time: %q", orig)}
		}
	}

	// Strip the timezone suffix (e.g. " PST", " EDT", "+05", "+05:30"); a
	// time-only value has no zone to apply it to. The AM/PM marker is NOT a
	// zone and must survive — dropping it here is what made
	// '2020-01-01 12:00 AM'::timestamp lose its meridiem.
	s = stripTimeZoneSuffix(s)

	// M0119-0006: field decoding proper is PostgreSQL's, not a layout table's —
	// see pgdatetime.ParseTimeOfDay for the forms this unlocks ('10:00.5' as
	// MINUTE TO SECOND, '040506', '10::00', 'allballs', hour-12 AM).
	tod, err := pgdatetime.ParseTimeOfDay(s)
	if err != nil {
		if errors.Is(err, pgdatetime.ErrTimeFieldOverflow) {
			return time.Time{}, &ExecError{Code: "22008",
				Message: fmt.Sprintf("date/time field value out of range: %q", orig)}
		}
		return time.Time{}, &ExecError{Code: "22007",
			Message: fmt.Sprintf("invalid input syntax for type time: %q", orig)}
	}

	h, m, sec, ns := tod.Hour, tod.Min, tod.Sec, tod.Nsec

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

	// The range check and the hour-24 / leap-second fold are upstream's
	// time_overflows() plus tm2time()'s composition, shared with the timestamp
	// path through pgdatetime — the two used to carry their own copies, and only
	// this one had them (a timestamp rejected '24:00:00' outright).
	norm, dayCarry, err := pgdatetime.TimeOfDay{Hour: h, Min: m, Sec: sec, Nsec: ns}.Normalize()
	if err != nil {
		return time.Time{}, &ExecError{Code: "22008",
			Message: fmt.Sprintf("date/time field value out of range: %q", orig)}
	}

	// Anchor to epoch date 1970-01-01 UTC. A whole-day time (24:00:00, 23:59:60)
	// is stored as next-day midnight, which is how it round-trips as 24:00:00.
	return time.Date(1970, 1, 1+dayCarry, norm.Hour, norm.Min, norm.Sec, norm.Nsec, time.UTC), nil
}

// stripTimeZoneSuffix removes the zone part of a time-only input: every
// space-separated trailing token ("PST", "+05:30", "America/New_York") plus an
// attached numeric offset ("10:00+05"). A time has no date to resolve a zone
// against, so PostgreSQL's time_in likewise decodes and then ignores it.
//
// The AM/PM marker is explicitly NOT a zone token. It is an ordinary field to
// PostgreSQL's splitter, and stripping it here was how goopg turned
// '2020-01-01 12:00 AM' into a parse failure and '12:00 AM' into noon.
func stripTimeZoneSuffix(s string) string {
	for {
		idx := strings.LastIndex(s, " ")
		if idx < 0 {
			break
		}
		switch strings.ToUpper(strings.TrimSpace(s[idx+1:])) {
		case "AM", "PM":
			return s
		}
		s = strings.TrimSpace(s[:idx])
	}
	if plus := strings.LastIndex(s, "+"); plus > 2 {
		return s[:plus]
	} else if minus := strings.LastIndex(s, "-"); minus > 2 {
		return s[:minus]
	}
	return s
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
	// M0125-0007: as in parseTimeString — the date-prefix probe below is a
	// fixed-offset test (s[4], s[7], s[:10]) that assumes zero-padded fields.
	s = pgdatetime.NormalizeInput(s)

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

	// Detach the AM/PM marker before timezone extraction so the zone scan below
	// does not mistake it for an abbreviation; it is re-attached verbatim for
	// parseTimeString, which owns the meridiem rules (hour 12 AM is hour 0).
	upper := strings.ToUpper(s)
	meridiem := ""
	if strings.HasSuffix(upper, " PM") || strings.HasSuffix(upper, " AM") {
		meridiem = s[len(s)-3:]
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

	// Now parse the bare time portion with its meridiem re-attached.
	s += meridiem

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

// pgTimestampLayouts is the single layout table every timestamp/timestamptz
// *text input* path in goopg parses against, shared so the COPY TEXT reader
// (parseCopyTimestamp) and the typed-literal path (evalTypedStringLit) cannot
// drift apart again — they disagreed twice already, most recently about the
// seconds-less `HH:MM` form, which made an INSERT of '2020-01-01 10:00' raise
// 22007 while the identical text as a literal parsed.
//
// PostgreSQL does not use layouts at all: ParseDateTime() splits the input into
// fields and DecodeDateTime() interprets each one, so the date/time separator
// may be a space or a `T`/`t` (datetime.c treats the ISO 8601 `T` as an
// ordinary field break), and the zone may be spelled `Z`, `+05`, `+0530` or
// `+05:30` — `Z` being the DTZ entry for UTC in datetbl. Go's parser needs one
// layout per spelling, so the zone-bearing forms are enumerated here with Go's
// `Z07*` elements (each of which matches a literal `Z` as well as its numeric
// offset). Case and a space before a lone `Z` are folded upstream of this table
// by pgdatetime.NormalizeInput, which also supplies an absent seconds field, so
// no `t`-separator or lowercase-`z` layout is needed.
//
// Zone-bearing layouts come first so an explicit offset is honoured before the
// zone-less fallbacks treat the wall clock as UTC. A fractional-seconds field
// needs no layout of its own: Go's parser accepts one after the seconds field
// even when the layout does not mention it.
var pgTimestampLayouts = []string{
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05Z0700",
	"2006-01-02 15:04:05Z07",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04:05Z07",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04Z07",
	"2006-01-02 15:04",
	"2006-01-02",
}

// tsZoneMode says what a timestamp text-input path must do with a time zone
// field it decoded out of the input, and it is NOT a stylistic choice: upstream
// decodes the zone for every one of these types and then keeps it for exactly
// one of them.
//
// PostgreSQL's timestamp_in, date_in and timestamptz_in (backend/utils/adt/
// timestamp.c, date.c) all call the SAME DecodeDateTime(), which fills `tzp`
// whenever the input carries an offset. What differs is the call after it:
// timestamptz_in passes `&tz` to tm2timestamp() so the offset shifts the wall
// clock onto the UTC line, while timestamp_in passes NULL and date_in never
// looks at the zone at all — so `'2020-01-01 10:00:00+05:30'::timestamp` IS
// `2020-01-01 10:00:00`, the offset having been parsed and thrown away.
//
// goopg had one shared layout table with Go `Z07*` elements and converted every
// result with .UTC(), which is the timestamptz rule applied to all three. The
// results were silently wrong, never errors: that literal answered
// `2020-01-01 04:30:00` as a timestamp, and a date was off by a WHOLE DAY
// whenever the offset crossed midnight (`'2020-01-02 02:00:00+05:30'::date`
// answered 2020-01-01 where PG answers 2020-01-02).
type tsZoneMode bool

const (
	// tsDiscardZone is the timestamp-without-time-zone / date rule: decode the
	// zone field (so the spelling stays legal input) and then ignore it, keeping
	// the wall clock exactly as written.
	tsDiscardZone tsZoneMode = false
	// tsApplyZone is the timestamptz rule: the offset moves the wall clock onto
	// the UTC line, which is what the KindTime carrier stores.
	tsApplyZone tsZoneMode = true
)

// tsZoneModeForType picks the rule from the SQL type name a text input is being
// read as. Only the with-time-zone spellings keep the offset; `timestamp`,
// `date` and anything else fall on the discard side, as upstream's input
// functions do.
func tsZoneModeForType(typeName string) tsZoneMode {
	if isTimestampTZTypeName(typeName) {
		return tsApplyZone
	}
	return tsDiscardZone
}

// isTimestampTZTypeName reports whether a SQL type name is one of the two
// spellings of `timestamp with time zone`. It backs BOTH halves of the split
// that type makes against plain `timestamp`: the INPUT rule (tsZoneModeForType
// above — the offset moves the wall clock instead of being discarded) and the
// OUTPUT rule (M0119-0006's 40th slice — NewTimestampTZDatum tags the datum so
// timestamptz_out runs instead of timestamp_out). Sharing one predicate is the
// point: the two rules are the same type distinction, and a producer that
// applied the zone on the way in while rendering zone-less on the way out would
// silently relabel the instant — which is exactly the defect that slice fixed.
func isTimestampTZTypeName(typeName string) bool {
	switch strings.ToLower(strings.TrimSpace(typeName)) {
	case "timestamptz", "timestamp with time zone":
		return true
	}
	return false
}

// applyTSZoneMode turns the time.Parse result into the value the target type
// stores. Go hands back the wall clock plus a fixed zone; tsApplyZone converts
// to the UTC instant it denotes, tsDiscardZone re-reads the very same wall-clock
// fields as UTC, which is upstream's "tm2timestamp(tm, fsec, NULL, &result)".
func applyTSZoneMode(ts time.Time, zone tsZoneMode) time.Time {
	if zone == tsApplyZone {
		return ts.UTC()
	}
	return time.Date(ts.Year(), ts.Month(), ts.Day(),
		ts.Hour(), ts.Minute(), ts.Second(), ts.Nanosecond(), time.UTC)
}

// errNoTimestampLayout is parsePGTimestampText's "nothing matched" failure. It
// is a plain syntax miss (22007 at the call sites); a field that parsed but is
// out of range comes back as pgdatetime.ErrFieldOutOfRange instead (22008).
var errNoTimestampLayout = errors.New("no timestamp layout matched")

// parsePGTimestampText is the single text→time.Time entry point behind every
// timestamp/date TEXT input path in goopg: the typed-literal path
// (evalTypedStringLit), the COPY TEXT reader and the cross-kind comparison
// coercion all reach the layout table through here, so a spelling PostgreSQL
// accepts cannot be accepted by one and rejected by another. (Three consecutive
// M0119-0006 slices found exactly that divergence — first the seconds-less
// `HH:MM` form, then the ISO 8601 `T`; sharing the table alone was not enough,
// because each caller still ran its own pre- and post-processing around it.)
//
// The three steps around the table are the ones PostgreSQL's field decoder does
// implicitly and Go's layout parser cannot express at all:
//
//	SplitEra       — the trailing ADBC token ('2020-01-01 BC'), removed before
//	                 the layouts see the string and re-applied to the year after
//	NormalizeInput — unpadded fields, the 'T'/'t' separator, a lone 'Z', an
//	                 absent seconds field
//	ApplyEra       — era → astronomical year, and the no-year-zero range rule
//
// The zone-bearing spellings are read under the caller's tsZoneMode, because
// the layout table alone cannot express the one place the three input functions
// differ (see tsZoneMode). parsePGTimestampText is the timestamptz reading;
// every path that KNOWS its target type must call parsePGTimestampTextZone.
func parsePGTimestampText(s string) (time.Time, error) {
	return parsePGTimestampTextZone(s, tsApplyZone)
}

// parsePGTimestampTextZone is parsePGTimestampText with the target type's zone
// rule made explicit. See tsZoneMode for why the rule is per-type.
func parsePGTimestampTextZone(s string, zone tsZoneMode) (time.Time, error) {
	ts, dayCarry, err := parsePGTimestampTextParts(s, zone)
	if err != nil {
		return time.Time{}, err
	}
	ts = ts.AddDate(0, 0, dayCarry)
	return ts, checkTimeCarrierRange(ts)
}

// parsePGTimestampTextParts is parsePGTimestampTextZone with the whole-day carry
// of an hour-24 / leap-second time of day left UNAPPLIED and returned instead.
//
// The split mirrors upstream's: DecodeDateTime hands back a struct pg_tm whose
// tm_hour may be 24 (or whose tm_sec may be 60), and it is tm2timestamp() — the
// step that COMPOSES date and time as date2j(y,m,d) * USECS_PER_DAY + time — that
// turns that into the next day. date_in() never calls tm2timestamp, so it must
// see the parts, not the composed value: '2020-01-01 24:00:00'::date is
// 2020-01-01 even though the identical text as a timestamp is 2020-01-02 00:00:00.
func parsePGTimestampTextParts(s string, zone tsZoneMode) (time.Time, int, error) {
	body, bc := pgdatetime.SplitEra(s)
	// M0125-0007: PG decodes date/time fields one numeric run at a time, so an
	// unpadded month, day, hour, minute or second is legal input. Normalise
	// before the fixed layouts below, which are not. This is also the coercion
	// used by the cross-kind comparison path (tryParseStringAs), so leaving it
	// out here is what made `d_date = '2002-5-01'` silently match no rows.
	// M0119-0006: this is DecodeDateTime's context, not DecodeTimeOnly's, so a
	// separator-less digit run is a DATE ('20200101', '20200101T040506') — see
	// pgdatetime.NormalizeDateTimeInput. bc must be threaded in because the
	// 2-digit-year windowing is suppressed under an era suffix.
	body = pgdatetime.NormalizeDateTimeInput(body, bc)
	// M0119-0006: a time of day PostgreSQL decodes field-by-field ('10:00 PM',
	// '040506', '10::00', '10:00.5' — see pgdatetime.ParseTimeOfDay) is beyond
	// any layout, but the DATE and ZONE parts around it are not. Canonicalizing
	// the time token in place lets the table below decode the rest unchanged;
	// it is tried only after the plain spelling fails, so the common path costs
	// nothing.
	// M0119-0006: hour 24 and the leap second are legal decoded fields that no
	// Go layout can hold ('24:00:00', '23:59:60'), so the canonicaliser reports
	// the whole day as a carry the caller composes in. An out-of-range token
	// ('25:00:00', '24:00:00.5' — see pgdatetime.TimeOfDay.Overflows) is a field
	// error, not a spelling the layouts should get a second go at.
	canon, canonCarry, canonErr := canonicalizeTimestampTimeToken(body)
	if errors.Is(canonErr, pgdatetime.ErrTimeFieldOverflow) {
		return time.Time{}, 0, pgdatetime.ErrFieldOutOfRange
	}
	// M0119-0006: a month/day ValidateDate() would reject ('20201301',
	// '2020-13-01') matches no layout below and previously fell through to the
	// generic "no timestamp layout matched" 22007 — a syntax error where PG
	// raises 22008, since DecodeDateTime DID recognise the shape. The date
	// token is the same for both candidates (only the time token differs), so
	// one check ahead of the loop covers it.
	if err := validateDateTokenFull(dateTokenPrefix(body), bc); err != nil {
		return time.Time{}, 0, err
	}
	cands := [2]struct {
		text  string
		carry int
	}{{body, 0}, {canon, canonCarry}}
	for _, cand := range cands {
		if cand.text == "" {
			continue
		}
		for _, layout := range pgTimestampLayouts {
			if ts, err := time.Parse(layout, cand.text); err == nil {
				ts, err := pgdatetime.ApplyEra(applyTSZoneMode(ts, zone), bc)
				if err != nil {
					return time.Time{}, 0, err
				}
				return ts, cand.carry, nil
			}
		}
		// M0119-0006 §15.2: the TIMESTAMP half of the BC leap-day race the
		// preceding slice fixed for DATE. validateDateTokenFull above already
		// accepted the date token against the ASTRONOMICAL year, but every
		// layout here still runs time.Parse's own day-in-month check against
		// the token's LITERAL year — so '0001-02-29 10:00:00 BC' (astronomical
		// year 0, leap) matches nothing and reads as a syntax miss (22007)
		// where PG answers a field-range error. Try the proxy-year rebuild
		// before giving up on this candidate, so the hour-24 / leap-second
		// canonical candidate still gets its own turn afterwards.
		if ts, ok := bcLeapTimestampFallback(cand.text, bc, zone); ok {
			return ts, cand.carry, nil
		}
	}
	return time.Time{}, 0, errNoTimestampLayout
}

// bcLeapProxyYear is the year bcLeapTimestampFallback substitutes into the date
// token so the layout table can decode the TIME and ZONE fields around it. It
// only has to be a year every validated month/day fits in — i.e. a leap one,
// since Feb 29 is the whole point — and to be spelled with four digits, which
// is what the layouts' "2006" element expects.
const bcLeapProxyYear = 2000

// bcLeapTimestampFallback is bcLeapDateFallback for a token that also carries a
// time of day. The date half cannot be handed to time.Parse at its own year (see
// the call site), but the time and zone halves must still be decoded by the
// SAME layout table as every other input — hand-rolling a second time-of-day
// parser here is exactly the divergence pgTimestampLayouts exists to prevent.
// So the year is swapped for a leap proxy, the candidate is re-parsed through
// the ordinary table, and the decoded wall clock is then rebuilt at the real
// astronomical year (which also performs ApplyEra's era shift by hand, as the
// DATE fallback does).
//
// The zone rule is applied to the PROXY value, not afterwards, because
// tsApplyZone shifts the wall clock onto the UTC line and that shift can cross
// midnight ('0001-02-29 00:30:00+05:30 BC' is the 28th in UTC). The resulting
// whole-day movement is measured against the proxy date and re-applied to the
// rebuilt one, so the fallback and the ordinary path agree about which day the
// value lands on.
//
// ok is false for everything this does not apply to: a non-BC input (whose
// literal and astronomical years agree, so time.Parse never disagreed), a token
// whose fields do not read back, the no-year-zero refusal, and any candidate
// the proxy rebuild still fails to parse — all of which keep the caller's
// existing errNoTimestampLayout.
func bcLeapTimestampFallback(text string, bc bool, zone tsZoneMode) (time.Time, bool) {
	if !bc {
		return time.Time{}, false
	}
	dateTok := dateTokenPrefix(text)
	month, day, mdok := pgdatetime.DateTokenMonthDay(dateTok)
	year, yok := pgdatetime.DateTokenYear(dateTok)
	if !mdok || !yok {
		return time.Time{}, false
	}
	astroYear, ok := pgdatetime.AstronomicalYear(year, bc)
	if !ok {
		return time.Time{}, false
	}
	probe := fmt.Sprintf("%04d-%02d-%02d", bcLeapProxyYear, month, day) + text[len(dateTok):]
	for _, layout := range pgTimestampLayouts {
		ts, err := time.Parse(layout, probe)
		if err != nil {
			continue
		}
		ts = applyTSZoneMode(ts, zone)
		dayShift := int(time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC).
			Sub(time.Date(bcLeapProxyYear, time.Month(month), day, 0, 0, 0, 0, time.UTC)).Hours() / 24)
		return time.Date(astroYear, time.Month(month), day,
			ts.Hour(), ts.Minute(), ts.Second(), ts.Nanosecond(), time.UTC).
			AddDate(0, 0, dayShift), true
	}
	return time.Time{}, false
}

// dateTokenPrefix returns the leading date token of a normalized "date<sep>
// rest" string — the same split normalizeInput uses to attach the time token
// (first ' ', 'T' or 't'), or the whole string when there is no separator.
func dateTokenPrefix(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == ' ' || c == 'T' || c == 't' {
			return s[:i]
		}
	}
	return s
}

// canonicalizeTimestampTimeToken rewrites the time-of-day token of a
// "YYYY-MM-DD<sep>..." string through pgdatetime.CanonicalizeTimeToken. It
// returns "" when there is nothing to rewrite (no date prefix, or a time token
// the field decoder does not recognise either), the day carry the token implies
// ('24:00:00' / '23:59:60' are the whole day), and ErrTimeFieldOverflow when the
// token decoded but is out of range — which the caller must NOT paper over as a
// syntax miss, since upstream answers 22008 for it.
func canonicalizeTimestampTimeToken(body string) (string, int, error) {
	if len(body) < 12 || body[4] != '-' || body[7] != '-' {
		return "", 0, nil
	}
	if sep := body[10]; sep != ' ' && sep != 'T' {
		return "", 0, nil
	}
	canon, dayCarry, err := pgdatetime.CanonicalizeTimeToken(body[11:])
	if err != nil {
		return "", 0, err
	}
	return body[:11] + canon, dayCarry, nil
}

// goopg's KindTime Datum carries an int64 count of NANOSECONDS since 1970
// (datum.go: NewTimeDatum / TimeValue), which is Go's UnixNano domain and spans
// only 1677-09-21 .. 2262-04-11. PostgreSQL's timestamp is an int64 count of
// MICROSECONDS since 2000-01-01, spanning 4713 BC .. 294276 AD, and its date is
// an int32 day count over the same span.
//
// The mismatch was silent and produced WRONG ANSWERS, not errors: '1000-01-01'
// wrapped to 2169-02-08 and '2300-01-01' to 1715-06-13, because UnixNano
// overflows int64 outside its domain and Go leaves the wrapped value in place.
// Until the carrier moves to microseconds (deferral ledger, M0119-0006 — it is
// the same blocker that keeps a BC date from being STORED after this slice
// taught the input path to READ one), every text-input entry point refuses what
// it cannot represent, so an out-of-range date is a loud 22008 instead of a
// plausible-looking different date.
var (
	timeCarrierMin = time.Unix(0, math.MinInt64).UTC()
	timeCarrierMax = time.Unix(0, math.MaxInt64).UTC()

	errTimeCarrierRange = errors.New("date/time value outside goopg's representable range")
)

// checkTimeCarrierRange reports whether t survives a round trip through the
// KindTime carrier. See the comment on timeCarrierMin above.
func checkTimeCarrierRange(t time.Time) error {
	if t.Before(timeCarrierMin) || t.After(timeCarrierMax) {
		return errTimeCarrierRange
	}
	return nil
}

// dateTimeInputError maps a parsePGDateText / parsePGTimestampText failure onto
// the SQLSTATE PostgreSQL raises for it. Upstream separates the two: a string
// no field decoder recognises is 22007 ("invalid input syntax for type date"),
// while one that decoded into fields that cannot exist — the no-year-zero rule
// ValidateDate() enforces — is 22008 ("date/time field value out of range").
// goopg reported 22007 for both, which mislabels a range error as a typo.
func dateTimeInputError(err error, typeName, input string, pos int) *ExecError {
	if errors.Is(err, errTimeCarrierRange) {
		return &ExecError{Code: "22008", Pos: pos,
			Message: fmt.Sprintf("date/time value out of range for %s: %q "+
				"(goopg stores %s between %s and %s)",
				typeName, input, typeName,
				timeCarrierMin.Format("2006-01-02"), timeCarrierMax.Format("2006-01-02"))}
	}
	if errors.Is(err, pgdatetime.ErrFieldOutOfRange) {
		return &ExecError{Code: "22008", Pos: pos,
			Message: fmt.Sprintf("date/time field value out of range: %q", input)}
	}
	return &ExecError{Code: "22007", Pos: pos,
		Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, input)}
}

// parsePGDateText is the DATE-domain sibling of parsePGTimestampText: the same
// era split and field normalisation in front of the one calendar-only layout
// the typed-literal date arm accepts. It is deliberately narrower than the
// timestamp entry point (a date literal carrying a time of day still goes down
// the cast path, not this one), so the two differ only in the layout set — the
// pre/post steps around it are shared, which is the property that kept breaking.
func parsePGDateText(s string) (time.Time, error) {
	body, bc := pgdatetime.SplitEra(s)
	norm := pgdatetime.NormalizeDateTimeInput(body, bc)
	if err := validateDateTokenFull(norm, bc); err != nil {
		return time.Time{}, err
	}
	t, err := time.Parse("2006-01-02", norm)
	if err != nil {
		// M0119-0006 §14.3: validateDateTokenFull above already ran the full
		// ValidateDate() battery — including day-in-month — against the
		// ASTRONOMICAL year, the only one whose leap-ness matters. A BC date
		// can still lose the race here: time.Parse's OWN day-out-of-range
		// check runs against the token's LITERAL year, and for a BC date
		// that disagrees with the astronomical one ('0001-02-29 BC' is
		// astronomical year 0 — leap, per 0%400==0 — but literal year 1 is
		// not, so time.Parse rejects a day PG accepts). Since the token is
		// already known valid, bypass time.Parse's construction entirely:
		// build the time.Time directly from the validated fields at the
		// astronomical year, which sidesteps ApplyEra's own literal->
		// astronomical shift (already done here) as well as its rejection.
		if t2, ok := bcLeapDateFallback(norm, bc); ok {
			return t2, checkTimeCarrierRange(t2)
		}
		return time.Time{}, err
	}
	t, err = pgdatetime.ApplyEra(t.UTC(), bc)
	if err != nil {
		return time.Time{}, err
	}
	return t, checkTimeCarrierRange(t)
}

// bcLeapDateFallback reconstructs a validated "...-MM-DD" BC date token
// directly via time.Date at the ASTRONOMICAL year, bypassing time.Parse's
// day-out-of-range check — see the M0119-0006 §14.3 comment in
// parsePGDateText above for why that check disagrees with PG for a BC
// February 29. ok is false for anything this fallback does not apply to
// (not BC, or the token/year don't parse — including the no-year-zero
// refusal, left to the caller's original time.Parse error as before).
func bcLeapDateFallback(dateToken string, bc bool) (time.Time, bool) {
	if !bc {
		return time.Time{}, false
	}
	month, day, mdok := pgdatetime.DateTokenMonthDay(dateToken)
	year, yok := pgdatetime.DateTokenYear(dateToken)
	if !mdok || !yok {
		return time.Time{}, false
	}
	astroYear, ok := pgdatetime.AstronomicalYear(year, bc)
	if !ok {
		return time.Time{}, false
	}
	return time.Date(astroYear, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
}

// validateDateTokenFull runs all three ValidateDate() checks (month, day,
// day-in-month) against a normalized "...-MM-DD" date token, using bc to
// compute the astronomical year the day-in-month check needs.
// M0119-0006 §13.3: it must run BEFORE time.Parse, not after — Go's
// time.Parse (unlike time.Date/AddDate) already rejects an impossible
// calendar day itself ("2020-02-30": "day out of range"), but with a bare
// parse error that reads as a 22007 syntax mistake rather than PG's 22008
// field-range one; by the time that error reaches the caller, the token's
// own digits are gone. DateTokenYear/DateTokenMonthDay read the token
// directly, so the astronomical year is available before any Parse call.
func validateDateTokenFull(dateToken string, bc bool) error {
	month, day, haveMD := pgdatetime.DateTokenMonthDay(dateToken)
	if !haveMD {
		return nil
	}
	if err := pgdatetime.ValidateMonthDay(month, day); err != nil {
		return err
	}
	year, haveY := pgdatetime.DateTokenYear(dateToken)
	if !haveY {
		return nil
	}
	astroYear, ok := pgdatetime.AstronomicalYear(year, bc)
	if !ok {
		return nil
	}
	return pgdatetime.ValidateDayOfMonth(astroYear, month, day)
}

// parseDateInputText is date_in()'s reading of a string that may carry a time
// of day: the full timestamp grammar decodes it, but only the year/month/day
// survive. Two things the timestamp reading does must therefore be undone here,
// and both were silently wrong before — upstream's date_in calls neither
// DetermineTimeZoneOffset nor tm2timestamp, it just hands tm_year/mon/mday to
// date2j():
//
//	the zone       — tsDiscardZone, so '2020-01-02 02:00:00+05:30'::date is the
//	                 2nd (fixed in the preceding M0119-0006 slice)
//	the day carry  — '2020-01-01 24:00:00'::date is the 1st, not the 2nd, even
//	                 though the same text AS A TIMESTAMP is 2020-01-02 00:00:00
//
// The returned value still carries the decoded wall clock; callers truncate it
// to midnight as they already did.
func parseDateInputText(s string) (time.Time, error) {
	ts, _, err := parseCopyTimestampZoneParts(s, tsDiscardZone)
	if err != nil {
		return time.Time{}, err
	}
	return ts, checkTimeCarrierRange(ts)
}

// parseCopyTimestamp accepts the layouts upstream's COPY TEXT input
// commonly produces: with or without fractional seconds, with or
// without timezone. It is the timestamptz reading of the input; a caller that
// knows its target type must use parseCopyTimestampZone instead (see
// tsZoneMode — `timestamp` and `date` throw a decoded zone away).
func parseCopyTimestamp(s string) (time.Time, error) {
	return parseCopyTimestampZone(s, tsApplyZone)
}

// parseCopyTimestampZone is parseCopyTimestamp with the target type's zone rule
// made explicit.
func parseCopyTimestampZone(s string, zone tsZoneMode) (time.Time, error) {
	ts, dayCarry, err := parseCopyTimestampZoneParts(s, zone)
	if err != nil {
		return time.Time{}, err
	}
	ts = ts.AddDate(0, 0, dayCarry)
	return ts, checkTimeCarrierRange(ts)
}

// parseCopyTimestampZoneParts is parseCopyTimestampZone with the whole-day carry
// left unapplied; see parsePGTimestampTextParts for why date_in needs it that way.
func parseCopyTimestampZoneParts(s string, zone tsZoneMode) (time.Time, int, error) {
	ts, dayCarry, err := parsePGTimestampTextParts(s, zone)
	if err == nil {
		return ts, dayCarry, nil
	}
	if errors.Is(err, pgdatetime.ErrFieldOutOfRange) || errors.Is(err, errTimeCarrierRange) {
		// A field that decoded but cannot be represented is a range failure, not
		// a syntax miss: do not let the natural-language fallback below have a
		// second go at it and turn a definite answer into "invalid timestamp".
		return time.Time{}, 0, err
	}
	// Try verbose natural-language format used by PostgreSQL's datetime output
	// e.g. "Tuesday, February 22, 2022 2:22:22.00 PM GMT+05:00".
	if ts, err := parseFullTimestamp(pgdatetime.NormalizeDateTimeInput(s, false)); err == nil {
		return ts, 0, nil
	}
	return time.Time{}, 0, fmt.Errorf("invalid timestamp %q", s)
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

// parseDateInfinityLiteral is the date-domain sibling of
// parseTimestampInfinityLiteral: PG's date_in (DecodeDateTime with the same
// DTK_LATE / DTK_EARLY RESERV tokens) maps 'infinity'/'+infinity' →
// DATEVAL_NOEND and '-infinity' → DATEVAL_NOBEGIN. Returns the matching
// ±infinity DATE sentinel Datum (TimeSubDate set) and true, or (zero, false).
// (unimplemented_feat #5(d-iv))
func parseDateInfinityLiteral(s string) (Datum, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "infinity", "+infinity":
		return NewDateInfinity(true), true
	case "-infinity":
		return NewDateInfinity(false), true
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
