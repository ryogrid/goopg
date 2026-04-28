package executor

import (
	"fmt"
	"strconv"
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
func DecodeCopyTextRow(line []byte, cols []catalog.Column) (Row, error) {
	fields, err := splitCopyTextFields(line)
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
func splitCopyTextFields(line []byte) ([]copyField, error) {
	var (
		out      []copyField
		field    []byte
		isNull   bool
		anyChars bool // tracks "did we see any non-`\N` bytes in this field?"
	)
	flush := func() {
		if isNull && !anyChars {
			out = append(out, copyField{isNull: true})
		} else {
			cp := make([]byte, len(field))
			copy(cp, field)
			out = append(out, copyField{bytes: cp})
		}
		field = field[:0]
		isNull = false
		anyChars = false
	}
	i := 0
	for i < len(line) {
		b := line[i]
		switch b {
		case '\t':
			flush()
			i++
		case '\\':
			if i+1 >= len(line) {
				return nil, fmt.Errorf("COPY: trailing backslash")
			}
			c := line[i+1]
			i += 2
			switch c {
			case 'N':
				if !anyChars && !isNull {
					isNull = true
					continue
				}
				// `\N` mid-string folds to literal "N" per upstream.
				field = append(field, 'N')
				anyChars = true
			case 'b':
				field = append(field, 0x08)
				anyChars = true
			case 'f':
				field = append(field, 0x0c)
				anyChars = true
			case 'n':
				field = append(field, '\n')
				anyChars = true
			case 'r':
				field = append(field, '\r')
				anyChars = true
			case 't':
				field = append(field, '\t')
				anyChars = true
			case 'v':
				field = append(field, 0x0b)
				anyChars = true
			case '\\':
				field = append(field, '\\')
				anyChars = true
			case 'x', 'X':
				v, n := decodeHexEscape(line[i:])
				if n == 0 {
					field = append(field, c)
				} else {
					field = append(field, v)
					i += n
				}
				anyChars = true
			case '0', '1', '2', '3', '4', '5', '6', '7':
				v := c - '0'
				extra := 0
				for extra < 2 && i+extra < len(line) {
					nc := line[i+extra]
					if nc < '0' || nc > '7' {
						break
					}
					v = v<<3 | (nc - '0')
					extra++
				}
				i += extra
				field = append(field, v)
				anyChars = true
			default:
				field = append(field, c)
				anyChars = true
			}
		default:
			field = append(field, b)
			anyChars = true
			i++
		}
	}
	flush()
	return out, nil
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
		if d.Bool {
			return "t", nil
		}
		return "f", nil
	case "timestamp", "timestamptz":
		if d.Kind != KindTime {
			return "", fmt.Errorf("expected time datum, got kind %d", d.Kind)
		}
		return d.Time.UTC().Format("2006-01-02 15:04:05.000000"), nil
	default:
		switch d.Kind {
		case KindString:
			return d.String, nil
		case KindBytes:
			return string(d.Bytes), nil
		case KindInt:
			return strconv.FormatInt(d.Int, 10), nil
		case KindBool:
			if d.Bool {
				return "t", nil
			}
			return "f", nil
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
			return Datum{Kind: KindBool, Bool: true}, nil
		case "f", "false", "F", "FALSE", "n", "N", "no", "0":
			return Datum{Kind: KindBool, Bool: false}, nil
		default:
			return Datum{}, fmt.Errorf("invalid boolean %q", string(raw))
		}
	case "timestamp", "timestamptz":
		ts, err := parseCopyTimestamp(string(raw))
		if err != nil {
			return Datum{}, err
		}
		return Datum{Kind: KindTime, Time: ts}, nil
	default:
		// text / varchar / char / unknown — keep as String.
		return Datum{Kind: KindString, String: string(raw)}, nil
	}
}

// parseCopyTimestamp accepts the layouts upstream's COPY TEXT input
// commonly produces: with or without fractional seconds, with or
// without timezone.
func parseCopyTimestamp(s string) (time.Time, error) {
	layouts := []string{
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
	return time.Time{}, fmt.Errorf("invalid timestamp %q", s)
}
