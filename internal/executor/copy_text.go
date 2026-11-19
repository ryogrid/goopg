package executor

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/utils/adt/datetime"
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
// for the boot default. byteaMode selects the `bytea_output` GUC's rendering
// for a bytea column ("hex", the boot default, or "escape" —
// M0134-0001 S12); pass "hex" (or "") for the boot default.
func EncodeCopyTextRow(dst []byte, row Row, cols []catalog.Column, dateStyle, dateOrder, timeZone, byteaMode string, cat catalog.Catalog, qualify bool, visible ...func(schema string) bool) ([]byte, error) {
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
		s, err := datumToCopyText(c.Type, d, dateStyle, dateOrder, timeZone, byteaMode, cat, qualify, visible...)
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
//
// timeZone is the session TimeZone GUC ("" = the boot default, UTC); it is the
// read-side counterpart of the timeZone EncodeCopyTextRow already takes, and is
// consumed by the types whose input function reads the GUC when the text itself
// carries no zone field (today: timetz). M0119-0006 (48th slice).
func DecodeCopyTextRow(line []byte, cols []catalog.Column, nullStr string, timeZone string, dateOrder ...string) (Row, error) {
	fields, err := splitCopyTextFields(line, nullStr)
	if err != nil {
		return nil, err
	}
	if len(fields) != len(cols) {
		return nil, fmt.Errorf("COPY: row has %d fields, expected %d", len(fields), len(cols))
	}
	return datumsFromCopyFields(fields, cols, timeZone, dateOrder...)
}

// datumsFromCopyFields converts already-split COPY fields into a Row
// shaped by cols. Shared by the TEXT and CSV readers so the two formats
// cannot drift in how a field's bytes become a Datum (the field SPLIT
// differs between the formats; the per-field input-function call does
// not). Callers check the field count first — the two formats report a
// mismatch with different messages.
func datumsFromCopyFields(fields []copyField, cols []catalog.Column, timeZone string, dateOrder ...string) (Row, error) {
	row := make(Row, len(cols))
	for i, raw := range fields {
		if raw.isNull {
			row[i] = NullDatum
			continue
		}
		d, err := copyTextToDatum(cols[i].Type, raw.bytes, timeZone, dateOrder...)
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
//
// cat/qualify are M0119-0006 (68th slice): the catalog + search-path-qualify
// flag the reg* arms need to render an OID as its name (RegOut), threaded as
// value parameters rather than a *Context so the TEXT and CSV renderers share
// exactly the narrow seam they already did. A nil cat preserves the pre-68th
// numeric rendering.
func datumToCopyText(t catalog.Type, d Datum, dateStyle, dateOrder, timeZone, byteaMode string, cat catalog.Catalog, qualify bool, visible ...func(schema string) bool) (string, error) {
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
	// M0119-0006 (68th slice): reg* columns render through RegOut — the SAME
	// OID→name renderer the SELECT wire path (appendTypedCellText) uses — so
	// COPY TO cannot drift from SELECT. A non-KindInt datum (a KindString
	// already rendered by a `::reg*` cast feeding `COPY (SELECT …) TO`) falls
	// through to the Kind switch below, exactly as the SELECT path's
	// fall-through does. This must sit BEFORE the type-name switch: the switch
	// has no reg* case, so a regrole OID would otherwise hit the default arm
	// and render numerically.
	if isRegIdentifierTypeName(t.Name) && d.Kind == KindInt {
		// visible is the regprocedure arglist's per-arg visibility predicate
		// (73rd slice, deferral row 1342); an empty variadic keeps RegOut's
		// bare-arglist behavior (RegOut == RegOutArgVisible with a nil
		// predicate).
		var argVisible func(s string) bool
		if len(visible) > 0 {
			argVisible = visible[0]
		}
		if argVisible != nil {
			return RegOutVisible(t.Name, uint32(d.Int), cat, argVisible), nil
		}
		return RegOutArgVisible(t.Name, uint32(d.Int), cat, qualify, argVisible), nil
	}
	switch t.Name {
	case "money":
		// cash_out, as the SELECT renderer does (dispatch appendTypedCellText).
		// M0149-0001k1.
		return MoneyText(d), nil
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
		if text, ok := infiniteTimeText(d); ok {
			return text, nil
		}
		return misc.FormatDate(d.TimeValue(), dateStyle, dateOrder), nil
	case "timestamp":
		// DateStyle-aware, mirroring the "date" case above and dispatch.go's
		// appendTypedCellText. M-NIGHTLY (run 20260714-011651) DateStyle
		// output-rendering follow-up.
		if d.Kind != KindTime {
			return "", fmt.Errorf("expected time datum, got kind %d", d.Kind)
		}
		if text, ok := infiniteTimeText(d); ok {
			return text, nil
		}
		return misc.FormatTimestamp(d.TimeValue().UTC(), dateStyle, dateOrder), nil
	case "timestamptz":
		// COPY TO emits the same text timestamptz_out does, so this must track
		// dispatch.go's appendTypedCellText exactly: convert into the session
		// TimeZone and print the zone (`COPY z TO STDOUT` under
		// TimeZone='Asia/Kolkata' is "2020-06-15 15:30:00+05:30" upstream).
		// Split off from the shared "timestamp" case in M0119-0006.
		if d.Kind != KindTime {
			return "", fmt.Errorf("expected time datum, got kind %d", d.Kind)
		}
		if text, ok := infiniteTimeText(d); ok {
			return text, nil
		}
		return misc.FormatTimestampTZ(d.TimeValue(), dateStyle, dateOrder, timeZone), nil
	case "time":
		// M0119-0006 (49th slice): previously unhandled — a `time` column fell
		// through to the default arm, whose Kind switch has no KindTime case, so
		// `COPY <t> TO STDOUT` on any table with a time column hard-errored
		// ("kind 5 cannot encode as time in COPY TEXT") and COPY TO → COPY FROM
		// was not a round trip even though the FROM side has had a "time" arm
		// all along (copyTextToDatum below). Hard-won Rule #2: the two
		// directions of one codec must cover the same type-set.
		//
		// time_out ignores DateStyle entirely (postgres/src/backend/utils/adt/
		// date.c: EncodeTimeOnly is called with USE_ISO_DATES unconditionally),
		// so unlike the date/timestamp arms above this one takes no style
		// argument.
		if d.Kind != KindTime {
			return "", fmt.Errorf("expected time datum for time, got kind %d", d.Kind)
		}
		return datetime.FormatTime(pgTimeMicros(d.TimeValue())), nil
	case "bytea":
		// COPY TO renders bytea per the session's `bytea_output` GUC — the
		// SAME dispatch dispatch.go's appendTypedCellText (SELECT wire) uses —
		// so COPY and SELECT cannot drift on this axis (Hard-won Rule #2).
		// Previously unhandled: bytea fell to the default arm's KindBytes case,
		// which wrote the RAW payload bytes into the text stream instead of an
		// output-function-encoded string. M0134-0001 S12.
		//
		// A KindString datum reaching here is ALREADY output-function text —
		// e.g. string_agg(bytea,...) advertises RetType bytea (pg_proc OID
		// 3545) but returns its accumulated result as a KindString, the same
		// shape dispatch.go's appendTypedCellText (SELECT wire, the sibling
		// this arm must not drift from) already falls through to
		// d.AppendValueText for — so it must pass through verbatim rather
		// than error. Round 2 regression fix: an earlier version of this arm
		// rejected non-KindBytes outright, which turned
		// `COPY (SELECT string_agg(b,','::bytea) FROM zs) TO STDOUT` into a
		// hard 500 instead of the working query it was on HEAD.
		switch d.Kind {
		case KindBytes:
			return byteaOutMode(d.BytesValue(), byteaMode), nil
		case KindString:
			return d.StringValue(), nil
		default:
			return "", fmt.Errorf("expected bytes datum for bytea, got kind %d", d.Kind)
		}
	case "timetz":
		// Sibling of the "time" arm; timetz_out prints the local time of day
		// followed by the UTC offset. pgdatetime.FormatTimeTZ takes PG's own
		// TimeTzADT.zone convention — seconds WEST of UTC — while the Datum
		// stores seconds EAST (Scale, in minutes), hence the negation, exactly
		// as the heap encoder does (codec.go's "timetz" arm). M0119-0006.
		if d.Kind != KindTime {
			return "", fmt.Errorf("expected time datum for timetz, got kind %d", d.Kind)
		}
		return datetime.FormatTimeTZ(pgTimeMicros(d.TimeValue()),
			int32(-d.TimeTZOffsetSecs())), nil
	default:
		switch d.Kind {
		case KindString:
			// catalog.PadBpchar restores the declared width a bpchar column
			// lost when coerceTextLikeDatum trimmed it on the way in; upstream
			// CopyOneRowTo calls bpcharout, which trims nothing, so a
			// `char(10)` holding 'ab' is written as 10 characters (measured
			// against PG 18.3 in both TEXT and CSV). It is a no-op for every
			// other type. M0119-0006 (57th slice).
			return catalog.PadBpchar(t, d.StringValue()), nil
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
		case KindInterval:
			// interval_out under the default 'postgres' IntervalStyle. There
			// was no arm, so COPY TO of any interval column failed "kind 6
			// cannot encode as interval" (M0146-0083).
			return d.Format(), nil
		default:
			return "", fmt.Errorf("kind %d cannot encode as %s in COPY TEXT", d.Kind, t.Name)
		}
	}
}

// copyTextToDatum is the inverse of datumToCopyText. raw is the
// already-unescaped byte representation of one column value. timeZone is the
// session TimeZone GUC ("" = UTC), for the types whose input function consults
// it when the text carries no zone of its own.
func copyTextToDatum(t catalog.Type, raw []byte, timeZone string, dateOrder ...string) (Datum, error) {
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
		//
		// M0134-0026: a zone-less timestamptz value is read as local wall-clock
		// time in the session TimeZone GUC, matching `COPY ... FROM` on real PG
		// (DecodeDateTime, datetime.c:1573-1583) — timeZone is this function's
		// own parameter, already threaded through for the OUTPUT side above.
		in := dateInput{zone: timeZone}
		if len(dateOrder) > 0 {
			in.order = dateOrder[0]
		}
		var ts time.Time
		var err error
		if strings.EqualFold(t.Name, "date") {
			ts, err = decodeDateText(string(raw), in)
		} else {
			ts, _, err = decodeTimestampText(string(raw), tsZoneModeForType(t.Name), in)
		}
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
	case "money":
		// cash_in, as the INSERT path's cast does. M0149-0001k1.
		c, err := moneyCents(NewStringDatum(string(raw)))
		if err != nil {
			return Datum{}, err
		}
		return moneyDatum(c), nil
	case "time":
		ts, err := parseTimeString(string(raw))
		if err != nil {
			return Datum{}, err
		}
		return roundTimeDatumToPrecision(NewTimeDatum(ts), timeColumnPrecision(t)), nil
	case "timetz":
		// M0119-0006 (48th slice): a COPY field with no zone of its own takes the
		// SESSION zone, exactly as the literal path does since the 47th slice —
		// `COPY t FROM STDIN` of "10:00:00" under TimeZone='Asia/Tokyo' is
		// 10:00:00+09 upstream, not +00 (DecodeTimeOnly's `!(fmask & DTK_M(TZ))`
		// arm, datetime.c). Hard-won Rule #2: the INSERT and COPY readers of one
		// type must agree, or a table loaded by COPY differs from the same table
		// loaded by INSERT under the same GUC.
		ts, offsetSecs, err := parseTimeTZString(string(raw), timeZone)
		if err != nil {
			return Datum{}, err
		}
		return roundTimeDatumToPrecision(NewTimeTZDatum(ts, offsetSecs), timeColumnPrecision(t)), nil
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
	case "interval":
		// M0119-0006 (63rd slice): the text-COPY sibling of the interval
		// binary-COPY arm. Parse the field through the SAME
		// parser.ParseIntervalBody tokenizer encodeValuePG and INSERT use (not
		// evalCast's limited `<n> <unit>` arm), then apply the column typmod the
		// way interval_in/AdjustIntervalForTypmod do at input. Before this arm
		// the default handed the raw text back as a KindString and the column
		// typmod was never applied on the load path.
		d, err := roundIntervalDatumToTypmod(NewStringDatum(string(raw)), intervalColumnTypmod(t))
		if err != nil {
			return Datum{}, err
		}
		return d, nil
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
	t, _, err := decodeTimeOnlyText(s, dateInput{}, false)
	return t, err
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
//   - "HH:MM:SS" bare time (offset defaults to the SESSION zone — see below)
//
// Inputs with named timezone in bare time strings (no date) are rejected.
//
// `zone` is the session TimeZone GUC ("" = the boot default, UTC). M0119-0006:
// an input with no zone field of its own does not take +00, it takes the
// session zone's offset — for the date the input itself carries, or for today's
// date when it carries none (DecodeTimeOnly's `!(fmask & DTK_M(TZ))` arm,
// datetime.c). Callers that are deciding a TYPE rather than reading a declared
// timetz pass "" deliberately; see parseTimeTZString's call sites.
func parseTimeTZString(s string, zone string) (time.Time, int, error) {
	return decodeTimeOnlyText(s, dateInput{zone: zone}, true)
}

// wrapTimeTZError converts a time-parsing error into a timetz error.
// Range errors (22008) are preserved with "time with time zone" wording.
// Syntax errors (22007) are re-wrapped with the original full string.
func wrapTimeTZError(err error, orig string) error {
	if ee, ok := err.(*ExecError); ok && ee.Code == "22008" {
		return &ExecError{Code: "22008",
			Message: fmt.Sprintf("date/time field value out of range: %q", orig)}
	}
	// M0119-0006: an unrecognised zone NAME is 22023 and already names the
	// offending zone rather than the input, so re-wrapping it as 22007 would
	// swap PG's error for a different one. It is not a syntax error at all.
	if ee, ok := err.(*ExecError); ok && ee.Code == "22023" {
		return ee
	}
	return &ExecError{Code: "22007",
		Message: fmt.Sprintf("invalid input syntax for type time with time zone: %q", orig)}
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
//
// This is the UTC-session convenience form: callers that cannot reach the
// session TimeZone GUC (or genuinely want the boot default) get it via the
// empty sessionZone, matching this function's pre-M0134-0026 behavior
// exactly. A caller that HAS a session must use parsePGTimestampTextZoneSession
// instead — a zone-less timestamptz literal now depends on it (M0134-0026).
func parsePGTimestampTextZone(s string, zone tsZoneMode) (time.Time, error) {
	ts, _, err := parsePGTimestampTextZoneSession(s, zone, "")
	return ts, err
}

// timestampRangeMin and timestampRangeEnd bound the instants a timestamp or date
// can hold (MIN_TIMESTAMP inclusive, END_TIMESTAMP exclusive; see datum.go).
var (
	timestampRangeMin = timeFromCarrier(pgTimestampMin)
	timestampRangeEnd = timeFromCarrier(pgTimestampEnd)
	dateRangeEnd      = time.Date(5874898, 1, 1, 0, 0, 0, 0, time.UTC)

	errTimeCarrierRange = errors.New("date/time value out of range")
)

// checkTimeCarrierRange reports whether t is inside the timestamp range.
func checkTimeCarrierRange(t time.Time) error {
	if t.Before(timestampRangeMin) || !t.Before(timestampRangeEnd) {
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
		kind := "timestamp"
		if strings.EqualFold(typeName, "date") {
			kind = "date"
		}
		return &ExecError{Code: "22008", Pos: pos,
			Message: fmt.Sprintf("%s out of range: %q", kind, input)}
	}
	var badZone *datetime.BadTimezoneError
	switch {
	case errors.As(err, &badZone):
		return &ExecError{Code: "22023", Pos: pos, Message: badZone.Error()}
	case errors.Is(err, datetime.ErrTZDispOverflow):
		return &ExecError{Code: "22009", Pos: pos,
			Message: fmt.Sprintf("time zone displacement out of range: %q", input)}
	case errors.Is(err, datetime.ErrMDFieldOverflow):
		return &ExecError{Code: "22008", Pos: pos,
			Message: fmt.Sprintf("date/time field value out of range: %q", input),
			Hint:    `Perhaps you need a different "DateStyle" setting.`}
	case errors.Is(err, datetime.ErrFieldOutOfRange):
		return &ExecError{Code: "22008", Pos: pos,
			Message: fmt.Sprintf("date/time field value out of range: %q", input)}
	}
	return &ExecError{Code: "22007", Pos: pos,
		Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, input)}
}

// timestampInputTypeName is the type name PG's input errors use.
func timestampInputTypeName(withZone bool) string {
	if withZone {
		return "timestamp with time zone"
	}
	return "timestamp"
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
	return parseCopyTimestampZoneSession(s, zone, "")
}

// parseTimestampSpecialLiteral recognises PostgreSQL's RESERV keyword
// spellings for timestamp/timestamptz input (DecodeDateTime, datetime.c's
// datetbl): 'infinity' / '+infinity' (DTK_LATE) and '-infinity' (DTK_EARLY)
// have no finite time.Time representation; 'now' (DTK_NOW), 'today'
// (DTK_TODAY), 'tomorrow' (DTK_TOMORROW), 'yesterday' (DTK_YESTERDAY) and
// 'epoch' (DTK_EPOCH) resolve against now, the caller's captured statement
// time (ctx.Now — see nowFromCtx) — matching real PG's GetCurrentTimeUsec/
// GetCurrentDateTime, which read the transaction's `now()` rather than the
// live wall clock. 'today'/'tomorrow'/'yesterday' truncate to midnight in
// UTC, the same simplification current_date's own RESERV-less arm already
// makes (expr.go's "current_date" case) — session-TimeZone-aware midnight
// is a pre-existing, separately-tracked gap, not one this literal path
// introduces. Matching is case-insensitive on the trimmed text. tz tags the
// result TimeSubTimestampTZ (NewTimestampTZDatum) vs plain (NewTimeDatum),
// mirroring every other timestamp-producing site's convention
// (isTimestampTZTypeName). Returns the resolved Datum and true, or (zero,
// false) when s matches none of the RESERV spellings. (unimplemented_feat
// #5(d-iv), M0134-0182)
func parseTimestampSpecialLiteral(s string, now time.Time, tz bool) (Datum, bool) {
	mk := NewTimeDatum
	if tz {
		mk = NewTimestampTZDatum
	}
	midnight := func(t time.Time) Datum {
		t = t.UTC()
		return mk(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC))
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "infinity", "+infinity":
		return NewTimestampInfinity(true), true
	case "-infinity":
		return NewTimestampInfinity(false), true
	case "now":
		return mk(now), true
	case "epoch":
		return mk(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)), true
	case "today":
		return midnight(now), true
	case "tomorrow":
		return midnight(now.AddDate(0, 0, 1)), true
	case "yesterday":
		return midnight(now.AddDate(0, 0, -1)), true
	}
	return Datum{}, false
}

// parseDateSpecialLiteral is the date-domain sibling of
// parseTimestampSpecialLiteral: PG's date_in shares DecodeDateTime with the
// same RESERV tokens, then discards the time-of-day. 'now' and 'today' are
// therefore the same DATE for date_in (both fold to midnight of the current
// day; the distinction only matters for the timestamp domain, where 'now'
// keeps the time-of-day). See parseTimestampSpecialLiteral for the shared
// now/UTC-midnight semantics and the current_date TimeZone caveat.
// (unimplemented_feat #5(d-iv), M0134-0182)
func parseDateSpecialLiteral(s string, now time.Time) (Datum, bool) {
	midnight := func(t time.Time) Datum {
		t = t.UTC()
		return NewDateDatum(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC))
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "infinity", "+infinity":
		return NewDateInfinity(true), true
	case "-infinity":
		return NewDateInfinity(false), true
	case "now", "today":
		return midnight(now), true
	case "tomorrow":
		return midnight(now.AddDate(0, 0, 1)), true
	case "yesterday":
		return midnight(now.AddDate(0, 0, -1)), true
	case "epoch":
		return NewDateDatum(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)), true
	}
	return Datum{}, false
}

// parseTimeSpecialLiteral recognises 'now', the only RESERV token
// DecodeTimeOnly accepts (datetime.c) for `time` input: the caller's
// captured statement time-of-day, date discarded. Mirrors current_time's
// existing convention (expr.go's "current_time" FuncCall arm) of reading
// ctx.Now.UTC() directly rather than converting through the session
// TimeZone — `time`/`time with time zone`'s own zone handling is a
// pre-existing, separately-tracked simplification this literal path does
// not change. now is nowFromCtx(ctx). (M0134-0182)
func parseTimeSpecialLiteral(s string, now time.Time) (Datum, bool) {
	if !strings.EqualFold(strings.TrimSpace(s), "now") {
		return Datum{}, false
	}
	t := now.UTC()
	return NewTimeDatum(time.Date(1970, 1, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)), true
}

// parseTimeTZSpecialLiteral is the timetz-domain sibling of
// parseTimeSpecialLiteral. Unlike plain `time`, `time with time zone` has an
// established "no zone in the input" fallback already (parseTimeTZString's
// own tail: misc.TimeTZSessionOffset(zone, ...) supplies the session zone's
// current offset) — 'now' reuses that exact mechanism rather than adding a
// second, divergent one. zone is the session TimeZone GUC (timeZoneFromCtx);
// an empty zone (no GetSetting wired) leaves the offset at 0, same as every
// other timetz path's nil-ctx fallback.
func parseTimeTZSpecialLiteral(s string, now time.Time, zone string) (Datum, bool) {
	if !strings.EqualFold(strings.TrimSpace(s), "now") {
		return Datum{}, false
	}
	t := now.UTC()
	tt := time.Date(1970, 1, 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	offsetSecs := 0
	if zone != "" {
		offsetSecs = misc.TimeTZSessionOffset(zone, t.Hour(), t.Minute(), t.Second())
	}
	return NewTimeTZDatum(tt, offsetSecs), true
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

// infiniteTimeText is the output of a date or timestamp infinity.
func infiniteTimeText(d Datum) (string, bool) {
	switch {
	case d.IsTimestampPosInf():
		return "infinity", true
	case d.IsTimestampNegInf():
		return "-infinity", true
	}
	return "", false
}

// dateInput is the session state date and timestamp input depends on. The zero
// value means UTC, the MDY field order and the current time.
type dateInput struct {
	zone  string
	order string
	now   time.Time
}

// dateInputFromCtx reads the TimeZone and DateStyle settings and statement time.
func dateInputFromCtx(ctx *Context) dateInput {
	_, order := dateStyleFromCtx(ctx)
	return dateInput{zone: timeZoneFromCtx(ctx), order: order, now: nowFromCtx(ctx)}
}

// decodeTimeOnlyText is time_in, or timetz_in for withZone: PostgreSQL's
// ParseDateTime and DecodeTimeOnly over the shared decoder. The time of day is
// returned on 1970-01-01 UTC, 24:00:00 as the next midnight, with the zone's
// offset east of UTC for timetz.
func decodeTimeOnlyText(s string, in dateInput, withZone bool) (time.Time, int, error) {
	typeName := "time"
	if withZone {
		typeName = "time with time zone"
	}
	env := decodeEnvFor(in)
	// time_in also passes tzp, so zone fields parse for time too and are
	// dropped; only timetz keeps the offset.
	d, err := datetime.DecodeTimeOnlyInput(s, &env, true)
	if err != nil {
		return time.Time{}, 0, dateTimeInputError(err, typeName, s, 0)
	}
	micros := ((int64(d.Hour)*60+int64(d.Min))*60+int64(d.Sec))*1_000_000 + int64(d.Usec)
	return pgTimeFromMicros(micros), -d.TZWest, nil
}

// decodeDateTimeText runs PostgreSQL's date and time input parser.
func decodeDateTimeText(s string, in dateInput) (datetime.Decoded, error) {
	env := decodeEnvFor(in)
	return datetime.DecodeDateTimeInput(s, &env)
}

// decodeEnvFor is the decoder's view of the session settings.
func decodeEnvFor(in dateInput) datetime.DecodeEnv {
	env := datetime.DecodeEnv{
		Order:      datetime.DateOrderMDY,
		Now:        in.now,
		Session:    misc.SessionLocation(in.zone),
		LookupZone: misc.ZoneByName,
		WestOffset: misc.WallClockWestOffset,
	}
	switch in.order {
	case "DMY":
		env.Order = datetime.DateOrderDMY
	case "YMD":
		env.Order = datetime.DateOrderYMD
	}
	if env.Now.IsZero() {
		env.Now = time.Now()
	}
	return env
}

// decodeTimestampText is timestamp_in, or timestamptz_in for tsApplyZone.
// usedSession reports that the result depends on session settings beyond the text.
func decodeTimestampText(s string, zone tsZoneMode, in dateInput) (t time.Time, usedSession bool, err error) {
	d, err := decodeDateTimeText(s, in)
	if err != nil {
		return time.Time{}, false, err
	}
	switch d.Type {
	case datetime.DecodedEpoch:
		return time.Unix(0, 0).UTC(), false, nil
	case datetime.DecodedLate:
		return timeCarrierPosInfInstant, false, nil
	case datetime.DecodedEarly:
		return timeCarrierNegInfInstant, false, nil
	}
	t = d.Wall()
	if zone == tsApplyZone {
		t = t.Add(time.Duration(d.TZWest) * time.Second)
	}
	return t, d.UsedSession, checkTimeCarrierRange(t)
}

// decodeDateText is date_in: the date fields as written, whatever the time of day
// and zone say.
func decodeDateText(s string, in dateInput) (time.Time, error) {
	t, _, err := decodeDateTextSession(s, in)
	return t, err
}

// decodeDateTextSession also reports whether the result depends on session settings.
func decodeDateTextSession(s string, in dateInput) (time.Time, bool, error) {
	d, err := decodeDateTimeText(s, in)
	if err != nil {
		return time.Time{}, false, err
	}
	switch d.Type {
	case datetime.DecodedEpoch:
		return time.Unix(0, 0).UTC(), false, nil
	case datetime.DecodedLate:
		return timeCarrierPosInfInstant, false, nil
	case datetime.DecodedEarly:
		return timeCarrierNegInfInstant, false, nil
	}
	t := time.Date(d.Year, time.Month(d.Mon), d.Mday, 0, 0, 0, 0, time.UTC)
	if t.Before(timestampRangeMin) || !t.Before(dateRangeEnd) {
		return time.Time{}, false, errTimeCarrierRange
	}
	return t, d.UsedSession, nil
}

// parsePGDateText reads a date with default session settings.
func parsePGDateText(s string) (time.Time, error) { return decodeDateText(s, dateInput{}) }

// parseDateInputText is parsePGDateText under its older name.
func parseDateInputText(s string) (time.Time, error) { return decodeDateText(s, dateInput{}) }

// parsePGTimestampTextZoneSession reads a timestamp with the given session zone.
func parsePGTimestampTextZoneSession(s string, zone tsZoneMode, sessionZone string) (time.Time, bool, error) {
	return decodeTimestampText(s, zone, dateInput{zone: sessionZone})
}

// parseCopyTimestampZoneSession reads a timestamp with the given session zone.
func parseCopyTimestampZoneSession(s string, zone tsZoneMode, sessionZone string) (time.Time, error) {
	t, _, err := decodeTimestampText(s, zone, dateInput{zone: sessionZone})
	return t, err
}
