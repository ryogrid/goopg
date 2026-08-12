package executor

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// The expectations below were measured on the PG 18.3 oracle (port
// 65432, 2026-08-13) with a two-column `text` table, not derived from
// reading copyfromparse.c:
//
//	COPY zz_csv FROM STDIN WITH (FORMAT csv);
//	plain,7                          -> 'plain'    , '7'
//	"quoted","has,comma"             -> 'quoted'   , 'has,comma'
//	"embedded\nnewline",x            -> 'embedded\nnewline', 'x'
//	"dbl""quote",                    -> 'dbl"quote', NULL
//	,unquoted-empty-is-null          -> NULL       , 'unquoted-…'
//	"",quoted-empty-is-not-null      -> ''         , 'quoted-…'
func TestParseCopyCsvFields(t *testing.T) {
	csv := copyToFormatFromOptions([]parser.CopyOption{{Name: "format", Value: "csv"}})

	type want struct {
		text   string
		isNull bool
	}
	cases := []struct {
		name string
		line string
		f    copyToFormat
		want []want
	}{
		{name: "plain", line: "plain,7", f: csv,
			want: []want{{text: "plain"}, {text: "7"}}},
		{name: "quoted and embedded delimiter", line: `"quoted","has,comma"`, f: csv,
			want: []want{{text: "quoted"}, {text: "has,comma"}}},
		{name: "doubled quote", line: `"dbl""quote",`, f: csv,
			want: []want{{text: `dbl"quote`}, {isNull: true}}},
		{name: "unquoted empty is null", line: ",unquoted", f: csv,
			want: []want{{isNull: true}, {text: "unquoted"}}},
		{name: "quoted empty is not null", line: `"",quoted`, f: csv,
			want: []want{{text: ""}, {text: "quoted"}}},
		{name: "quoted section can end mid-field", line: `"ab"cd,x`, f: csv,
			want: []want{{text: "abcd"}, {text: "x"}}},
		{name: "backslash is not an escape in csv", line: `a\tb,c\nd`, f: csv,
			want: []want{{text: `a\tb`}, {text: `c\nd`}}},
		{
			name: "custom delimiter, quote and null string",
			line: "~a;b~;NUL",
			f: copyToFormatFromOptions([]parser.CopyOption{
				{Name: "format", Value: "csv"},
				{Name: "delimiter", Value: ";"},
				{Name: "quote", Value: "~"},
				{Name: "null", Value: "NUL"},
			}),
			want: []want{{text: "a;b"}, {isNull: true}},
		},
		{
			name: "explicit escape character",
			line: `"a\"b",x`,
			f: copyToFormatFromOptions([]parser.CopyOption{
				{Name: "format", Value: "csv"},
				{Name: "escape", Value: `\`},
			}),
			want: []want{{text: `a"b`}, {text: "x"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCopyCsvFields([]byte(tc.line), tc.f)
			if err != nil {
				t.Fatalf("parseCopyCsvFields(%q): %v", tc.line, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("fields=%d want %d (%+v)", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if got[i].isNull != w.isNull {
					t.Errorf("field %d isNull=%v want %v", i, got[i].isNull, w.isNull)
				}
				if !w.isNull && string(got[i].bytes) != w.text {
					t.Errorf("field %d = %q want %q", i, got[i].bytes, w.text)
				}
			}
		})
	}
}

// A record whose quoted field is still open reports errCsvIncompleteRecord
// so the caller can re-join it with the next physical line; on the oracle
// the same input reaching end-of-stream is `unterminated CSV quoted field`.
func TestParseCopyCsvFieldsIncompleteRecord(t *testing.T) {
	csv := copyToFormatFromOptions([]parser.CopyOption{{Name: "format", Value: "csv"}})
	if _, err := parseCopyCsvFields([]byte(`"unterminated`), csv); !errors.Is(err, errCsvIncompleteRecord) {
		t.Fatalf("err=%v want errCsvIncompleteRecord", err)
	}
	// Re-joined with its continuation the record parses, and the newline
	// the wire layer removed is restored inside the field.
	got, err := parseCopyCsvFields([]byte("\"embedded\nnewline\",x"), csv)
	if err != nil {
		t.Fatalf("re-joined record: %v", err)
	}
	if len(got) != 2 || string(got[0].bytes) != "embedded\nnewline" {
		t.Fatalf("fields=%+v", got)
	}
}

// Field-count mismatches carry upstream's two distinct messages.
func TestDecodeCopyCsvRowFieldCount(t *testing.T) {
	csv := copyToFormatFromOptions([]parser.CopyOption{{Name: "format", Value: "csv"}})
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "text"}},
		{Name: "b", Type: catalog.Type{Name: "text"}},
	}
	if _, err := DecodeCopyCsvRow([]byte("x,y,z"), cols, csv, ""); err == nil ||
		err.Error() != "extra data after last expected column" {
		t.Fatalf("extra-column err=%v", err)
	}
	if _, err := DecodeCopyCsvRow([]byte("onlyone"), cols, csv, ""); err == nil ||
		err.Error() != `missing data for column "b"` {
		t.Fatalf("missing-column err=%v", err)
	}
}
