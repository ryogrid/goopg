package parser

import "testing"

// M0119-0006 (63rd slice). A column declared `interval year to month` /
// `interval second(2)` / `interval(2)` parses to the packed INTERVAL_TYPMOD in
// Args[0], carrying the FULL range mask so pg_attribute.atttypmod round-trips
// the declared spelling. Before this, `interval year to month` did not parse at
// all (the `year to month` tail was left for the column-def parser to reject)
// and `interval(2)` fell into the generic `(N[,M])` arm, losing the interval
// semantics. Pinned here against PG's intervaltypmodin packing.

func parseIntervalColumnArgs(t *testing.T, ddl string) []int64 {
	t.Helper()
	stmts, err := Parse(ddl)
	if err != nil {
		t.Fatalf("Parse(%q): %v", ddl, err)
	}
	ct, ok := stmts[0].(*CreateTableStmt)
	if !ok {
		t.Fatalf("got %T, want *CreateTableStmt", stmts[0])
	}
	if len(ct.Columns) != 1 {
		t.Fatalf("columns=%d, want 1", len(ct.Columns))
	}
	if ct.Columns[0].Type.Name != "interval" {
		t.Fatalf("type name = %q, want interval", ct.Columns[0].Type.Name)
	}
	return ct.Columns[0].Type.Args
}

func TestParseIntervalColumnTypmod(t *testing.T) {
	cases := []struct {
		ddl  string
		want []int64 // nil means no typmod
	}{
		{"CREATE TABLE t (c interval)", nil},
		// YEAR TO MONTH: INTERVAL_MASK(YEAR)|INTERVAL_MASK(MONTH) = 4|2 = 6,
		// full precision 0xFFFF.
		{"CREATE TABLE t (c interval year to month)", []int64{(6 << 16) | 0xFFFF}},
		{"CREATE TABLE t (c interval year)", []int64{(4 << 16) | 0xFFFF}},
		{"CREATE TABLE t (c interval month)", []int64{(2 << 16) | 0xFFFF}},
		// DAY TO SECOND: 8|1024|2048|4096 = 7176, full precision.
		{"CREATE TABLE t (c interval day to second)", []int64{(7176 << 16) | 0xFFFF}},
		{"CREATE TABLE t (c interval day to minute)", []int64{((1<<3 | 1<<10 | 1<<11) << 16) | 0xFFFF}},
		// SECOND(p): INTERVAL_MASK(SECOND) = 1<<12 = 4096, precision 2.
		{"CREATE TABLE t (c interval second(2))", []int64{(4096 << 16) | 2}},
		// Precision-only interval(p): full range 0x7FFF, precision 2.
		{"CREATE TABLE t (c interval(2))", []int64{(0x7FFF << 16) | 2}},
		// HOUR TO MINUTE: 1024|2048 = 3072.
		{"CREATE TABLE t (c interval hour to minute)", []int64{((1<<10 | 1<<11) << 16) | 0xFFFF}},
	}
	for _, c := range cases {
		t.Run(c.ddl, func(t *testing.T) {
			got := parseIntervalColumnArgs(t, c.ddl)
			if len(got) != len(c.want) {
				t.Fatalf("Args = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("Args = %v, want %v", got, c.want)
				}
			}
		})
	}
}

// An invalid range (`interval year to day`) is a hard error, not a silent
// fall-through to a bare interval.
func TestParseIntervalColumnTypmodInvalidRange(t *testing.T) {
	if _, err := Parse("CREATE TABLE t (c interval year to day)"); err == nil {
		t.Fatal("interval year to day parsed, want error")
	}
}
