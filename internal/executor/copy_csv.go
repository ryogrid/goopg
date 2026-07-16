package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// copyToFormat captures the COPY TO output knobs derived from the option
// list. Only the text/CSV-relevant settings are interpreted here; binary
// format is handled separately by IsBinaryFormat. The zero value is the
// default TEXT format (tab delimiter, "\N" NULL, no header). M0097-0024.
type copyToFormat struct {
	csv     bool
	header  bool
	delim   byte
	quote   byte
	escape  byte
	nullStr string
	// forceQuote selects the columns (by name) that are always quoted in
	// CSV output. forceQuoteAll is the `FORCE QUOTE *` form.
	forceQuote    map[string]bool
	forceQuoteAll bool
}

// copyToFormatFromOptions interprets the parsed COPY options into a
// copyToFormat. It assumes binary format has already been ruled out by
// the caller (RunCopyTo). Unknown options are ignored here — the planner's
// validateCopyOptions already rejected anything unsupported. M0097-0024.
func copyToFormatFromOptions(opts []parser.CopyOption) copyToFormat {
	f := copyToFormat{
		delim:   '\t',
		quote:   '"',
		escape:  '"',
		nullStr: `\N`,
	}
	for _, o := range opts {
		switch strings.ToLower(o.Name) {
		case "format":
			if strings.EqualFold(o.Value, "csv") {
				f.csv = true
			}
		case "csv":
			if o.Bool {
				f.csv = true
			}
		case "header":
			// Bare HEADER, or HEADER true/on. "match" is an input-only
			// directive; for output it behaves as a header line too.
			f.header = o.Bool || copyOptionTruthy(o.Value)
		case "delimiter":
			if len(o.Value) > 0 {
				f.delim = o.Value[0]
			}
		case "quote":
			if len(o.Value) > 0 {
				f.quote = o.Value[0]
			}
		case "escape":
			if len(o.Value) > 0 {
				f.escape = o.Value[0]
			}
		case "null":
			f.nullStr = o.Value
		case "force_quote":
			if o.Star {
				f.forceQuoteAll = true
			} else {
				if f.forceQuote == nil {
					f.forceQuote = make(map[string]bool, len(o.Cols))
				}
				for _, c := range o.Cols {
					f.forceQuote[c] = true
				}
			}
		}
	}
	// CSV defaults differ from TEXT: comma delimiter, empty NULL string.
	if f.csv {
		if f.delim == '\t' {
			f.delim = ','
		}
		if f.nullStr == `\N` {
			f.nullStr = ""
		}
	}
	return f
}

func copyOptionTruthy(v string) bool {
	switch strings.ToLower(v) {
	case "true", "on", "match":
		return true
	}
	return false
}

// hasHeader reports whether a header line should be emitted.
func (f copyToFormat) hasHeader() bool { return f.header }

// appendHeader writes the column-name header line. CSV column names follow
// CSV quoting rules (but never force-quote); TEXT names use text escaping.
func (f copyToFormat) appendHeader(dst []byte, cols []catalog.Column) []byte {
	for i, c := range cols {
		if i > 0 {
			dst = append(dst, f.delim)
		}
		if f.csv {
			dst = appendCsvField(dst, c.Name, false, f.delim, f.quote, f.escape)
		} else {
			dst = appendCopyTextEscaped(dst, c.Name)
		}
	}
	return append(dst, '\n')
}

// EncodeCopyCsvRow renders one row in CSV format per the supplied format
// settings. NULL fields are written as the (unquoted) null string; all
// other fields are quoted when they need it or when force-quote applies.
// dateStyle/dateOrder select DATE column rendering, same as EncodeCopyTextRow.
func EncodeCopyCsvRow(dst []byte, row Row, cols []catalog.Column, f copyToFormat, dateStyle, dateOrder string) ([]byte, error) {
	for i, c := range cols {
		if i > 0 {
			dst = append(dst, f.delim)
		}
		d := row[i]
		if d.IsNull() {
			dst = append(dst, f.nullStr...)
			continue
		}
		s, err := datumToCopyText(c.Type, d, dateStyle, dateOrder)
		if err != nil {
			return nil, err
		}
		fq := f.forceQuoteAll || f.forceQuote[c.Name]
		dst = appendCsvField(dst, s, fq, f.delim, f.quote, f.escape)
	}
	return append(dst, '\n'), nil
}

// appendCsvField writes a single CSV field, quoting it when forced or when
// it contains the delimiter, the quote character, or a newline/carriage
// return. Embedded quote and escape characters are prefixed by the escape
// character (which defaults to the quote character, yielding doubled
// quotes). Mirrors PostgreSQL's CopyAttributeOutCSV. M0097-0024.
func appendCsvField(dst []byte, s string, force bool, delim, quote, escape byte) []byte {
	needQuote := force
	if !needQuote {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == delim || c == quote || c == '\n' || c == '\r' {
				needQuote = true
				break
			}
		}
	}
	if !needQuote {
		return append(dst, s...)
	}
	dst = append(dst, quote)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == quote || c == escape {
			dst = append(dst, escape)
		}
		dst = append(dst, c)
	}
	return append(dst, quote)
}
