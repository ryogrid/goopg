package postmaster

import (
	"io"
	"log/slog"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// TestAppendTypedCellTextBpcharCarriesDeclaredWidth pins the DataRow boundary.
//
// The comment this replaces asserted that "bpcharout (PG) uses bcTruelen which
// trims trailing spaces before sending over the wire". It does not: bpcharout
// is a bare TextDatumGetCString (postgres/src/backend/utils/adt/varchar.c) and
// bcTruelen is used by the COMPARISON and cast-to-text paths, not by output.
// Measured on PG 18.3 with `psql -A -t` piped through `cat -A`: `SELECT c` from
// a `char(10)` holding 'ab' returns ten bytes, where goopg returned two. (The
// trimming that misled the old comment is real but belongs to `c || '|'`, where
// the bpchar->text cast rtrims.)
//
// appendTypedCellText is shared by the simple-query streaming path and the
// extended-query materialising path, so pinning it here covers both protocols.
// The pad itself is catalog.PadBpchar, shared with the COPY and pgoutput
// renderers (.ralph/PROMPT.md hard-won rule #2). M0119-0006 (57th slice).
func TestAppendTypedCellTextBpcharCarriesDeclaredWidth(t *testing.T) {
	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: catalog.NewInMemory(),
	})

	cases := []struct {
		name string
		typ  catalog.Type
		in   string
		want string
	}{
		{"char(10) short", catalog.Type{Name: "char", Args: []int64{10}}, "ab", "ab        "},
		{"char(10) empty", catalog.Type{Name: "char", Args: []int64{10}}, "", "          "},
		{"char(10) exact", catalog.Type{Name: "char", Args: []int64{10}}, "abcdefghij", "abcdefghij"},
		{"bpchar(3)", catalog.Type{Name: "bpchar", Args: []int64{3}}, "x", "x  "},
		{"multibyte by rune count", catalog.Type{Name: "char", Args: []int64{5}}, "あい", "あい   "},
		{"bare char is OID 18", catalog.Type{Name: "char"}, "x", "x"},
		{"varchar never pads", catalog.Type{Name: "varchar", Args: []int64{10}}, "ab", "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(srv.appendTypedCellText(nil, executor.NewStringDatum(tc.in), tc.typ, nil))
			if got != tc.want {
				t.Errorf("appendTypedCellText(%+v, %q) = %q, want %q", tc.typ, tc.in, got, tc.want)
			}
		})
	}
}
