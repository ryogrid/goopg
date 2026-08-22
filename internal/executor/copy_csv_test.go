package executor

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestCopyCsvForceQuoteHeader reproduces the copyselect regress shape
//
//	copy (select t from test1 where id = 1) to stdout csv header force quote t
//
// whose expected output is the header line `t` followed by the
// force-quoted value `"a"`. M0097-0024.
func TestCopyCsvForceQuoteHeader(t *testing.T) {
	opts := []parser.CopyOption{
		{Name: "csv", Bool: true},
		{Name: "header", Bool: true},
		{Name: "force_quote", Cols: []string{"t"}},
	}
	f := copyToFormatFromOptions(opts)
	if !f.csv || !f.header || !f.forceQuote["t"] {
		t.Fatalf("format not parsed: %+v", f)
	}
	cols := []catalog.Column{{Name: "t", Type: catalog.Type{Name: "text"}}}

	hdr := f.appendHeader(nil, cols)
	if string(hdr) != "t\n" {
		t.Errorf("header got %q, want %q", hdr, "t\n")
	}

	row := Row{NewStringDatum("a")}
	got, err := EncodeCopyCsvRow(nil, row, cols, f, "ISO", "MDY", "UTC", "hex", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\"a\"\n" {
		t.Errorf("row got %q, want %q", got, "\"a\"\n")
	}
}

// TestCopyCsvDefaultsAndQuoting verifies CSV defaults (comma delimiter,
// empty NULL string) and the auto-quote rules: a field is quoted only
// when it contains the delimiter, the quote character, or a line break,
// and embedded quotes are doubled.
func TestCopyCsvDefaultsAndQuoting(t *testing.T) {
	f := copyToFormatFromOptions([]parser.CopyOption{{Name: "format", Value: "csv"}})
	if f.delim != ',' || f.quote != '"' || f.nullStr != "" {
		t.Fatalf("csv defaults wrong: %+v", f)
	}
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "text"}},
		{Name: "b", Type: catalog.Type{Name: "text"}},
		{Name: "c", Type: catalog.Type{Name: "text"}},
	}
	// a: contains a comma → quoted; b: NULL → empty unquoted; c: contains
	// a quote → quoted with the quote doubled.
	row := Row{NewStringDatum("x,y"), NullDatum, NewStringDatum(`he"llo`)}
	got, err := EncodeCopyCsvRow(nil, row, cols, f, "ISO", "MDY", "UTC", "hex", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "\"x,y\",,\"he\"\"llo\"\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestCopyCsvForceQuoteNullAndEofMarker covers the two extra force-quote
// rules from PostgreSQL's CopyAttributeOutCSV
// (postgres/src/backend/commands/copyto.c): a field is force-quoted when
// its rendered text equals the configured NULL string (disambiguating an
// actual empty/matching string from SQL NULL) or, on a single-column row,
// when it equals the literal `\.` end-of-data marker. M0134-0031.
func TestCopyCsvForceQuoteNullAndEofMarker(t *testing.T) {
	// Case 1: empty string on a default-NULL-string ("") CSV format must
	// be quoted, not emitted as bare nothing (which means NULL).
	t.Run("empty string forces quote", func(t *testing.T) {
		f := copyToFormatFromOptions([]parser.CopyOption{{Name: "format", Value: "csv"}})
		cols := []catalog.Column{{Name: "t", Type: catalog.Type{Name: "text"}}}
		row := Row{NewStringDatum("")}
		got, err := EncodeCopyCsvRow(nil, row, cols, f, "ISO", "MDY", "UTC", "hex", nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "\"\"\n" {
			t.Errorf("got %q, want %q", got, "\"\"\n")
		}
	})

	// Case 3: a lone `\.` value on a single-column row must be quoted so
	// it isn't misread as the COPY end-of-data marker.
	t.Run("single-column backslash-dot forces quote", func(t *testing.T) {
		f := copyToFormatFromOptions([]parser.CopyOption{{Name: "format", Value: "csv"}})
		cols := []catalog.Column{{Name: "t", Type: catalog.Type{Name: "text"}}}
		row := Row{NewStringDatum(`\.`)}
		got, err := EncodeCopyCsvRow(nil, row, cols, f, "ISO", "MDY", "UTC", "hex", nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "\"\\.\"\n" {
			t.Errorf("got %q, want %q", got, "\"\\.\"\n")
		}
	})

	// Case 4: the lone-`\.` rule only applies to single-column rows; a
	// two-column row with one field equal to `\.` must NOT force-quote it.
	t.Run("multi-column backslash-dot not forced", func(t *testing.T) {
		f := copyToFormatFromOptions([]parser.CopyOption{{Name: "format", Value: "csv"}})
		cols := []catalog.Column{
			{Name: "a", Type: catalog.Type{Name: "text"}},
			{Name: "b", Type: catalog.Type{Name: "text"}},
		}
		row := Row{NewStringDatum(`\.`), NewStringDatum("y")}
		got, err := EncodeCopyCsvRow(nil, row, cols, f, "ISO", "MDY", "UTC", "hex", nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "\\.,y\n" {
			t.Errorf("got %q, want %q", got, "\\.,y\n")
		}
	})

	// Case 5: a custom NULL option string must be used (not a hardcoded
	// ""), so a field whose text matches it is quoted to disambiguate.
	t.Run("custom null string forces quote", func(t *testing.T) {
		f := copyToFormatFromOptions([]parser.CopyOption{
			{Name: "format", Value: "csv"},
			{Name: "null", Value: "MYNULL"},
		})
		cols := []catalog.Column{{Name: "t", Type: catalog.Type{Name: "text"}}}
		row := Row{NewStringDatum("MYNULL")}
		got, err := EncodeCopyCsvRow(nil, row, cols, f, "ISO", "MDY", "UTC", "hex", nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "\"MYNULL\"\n" {
			t.Errorf("got %q, want %q", got, "\"MYNULL\"\n")
		}
	})
}

// TestCopyTextHeaderUnaffectedByCsv confirms HEADER works for the TEXT
// format too (tab-delimited column names) and that the default format
// stays TEXT when no CSV flag is present.
func TestCopyTextHeaderUnaffectedByCsv(t *testing.T) {
	f := copyToFormatFromOptions([]parser.CopyOption{{Name: "header", Bool: true}})
	if f.csv {
		t.Fatalf("expected TEXT format, got csv")
	}
	cols := []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "text"}},
	}
	hdr := f.appendHeader(nil, cols)
	if !bytes.Equal(hdr, []byte("id\tv\n")) {
		t.Errorf("text header got %q, want %q", hdr, "id\tv\n")
	}
}
