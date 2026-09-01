package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// fkRowMatchesByName is the pre-EO1-11 implementation, kept here so the
// benchmark can show what the per-row name resolution cost. It is the old
// fkRowMatches verbatim.
func fkRowMatchesByName(cols []catalog.Column, fkCols []string, row Row, vals []Datum) bool {
	if len(fkCols) != len(vals) {
		return false
	}
	for i, name := range fkCols {
		found := false
		for j, c := range cols {
			if strings.EqualFold(c.Name, name) {
				if j >= len(row) {
					return false
				}
				if !datumEquals(row[j], vals[i]) {
					return false
				}
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func fkBenchFixture(ncols int) ([]catalog.Column, []string, Row, []Datum) {
	cols := make([]catalog.Column, ncols)
	row := make(Row, ncols)
	for i := range cols {
		cols[i] = catalog.Column{Name: fmt.Sprintf("col%02d", i)}
		row[i] = NewIntDatum(int64(i))
	}
	// Two FK columns near the END of the table, which is what makes the linear
	// name scan expensive.
	fkCols := []string{cols[ncols-2].Name, cols[ncols-1].Name}
	vals := []Datum{NewIntDatum(int64(ncols - 2)), NewIntDatum(int64(ncols - 1))}
	return cols, fkCols, row, vals
}

// BenchmarkFKRowMatch measures review/260831 EO1-11: the FK scan paths resolve
// the FK column names ONCE per scan now (fkColumnIndexes) instead of once per
// row. Each iteration stands for one row of a cascade's table scan.
func BenchmarkFKRowMatch(b *testing.B) {
	const ncols = 16
	cols, fkCols, row, vals := fkBenchFixture(ncols)

	b.Run("resolve-per-row", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if !fkRowMatchesByName(cols, fkCols, row, vals) {
				b.Fatal("expected a match")
			}
		}
	})
	b.Run("precomputed", func(b *testing.B) {
		idx := fkColumnIndexes(cols, fkCols)
		b.ReportAllocs()
		for b.Loop() {
			if !fkRowMatchesAt(idx, row, vals) {
				b.Fatal("expected a match")
			}
		}
	})
}

// TestFKRowMatchAtMatchesByName pins that hoisting the name resolution did not
// change the answer, including the "column name not present" case.
func TestFKRowMatchAtMatchesByName(t *testing.T) {
	cols, fkCols, row, vals := fkBenchFixture(8)
	cases := []struct {
		name    string
		fkCols  []string
		vals    []Datum
		wantHit bool
	}{
		{"match", fkCols, vals, true},
		{"value mismatch", fkCols, []Datum{NewIntDatum(999), vals[1]}, false},
		{"unknown column", []string{"nosuchcol", fkCols[1]}, vals, false},
		{"case-insensitive", []string{strings.ToUpper(fkCols[0]), fkCols[1]}, vals, true},
		{"arity mismatch", fkCols, vals[:1], false},
	}
	for _, c := range cases {
		want := fkRowMatchesByName(cols, c.fkCols, row, c.vals)
		got := fkRowMatchesAt(fkColumnIndexes(cols, c.fkCols), row, c.vals)
		if want != c.wantHit {
			t.Fatalf("%s: the reference implementation says %v, the case expects %v", c.name, want, c.wantHit)
		}
		if got != want {
			t.Errorf("%s: fkRowMatchesAt = %v, name-resolving reference = %v", c.name, got, want)
		}
	}
}
