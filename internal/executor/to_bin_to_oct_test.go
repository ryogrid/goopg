package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestToBinToOctTwosComplementWidth pins to_bin(int)/to_oct(int)'s
// two's-complement zero-extension semantics for negative arguments: the int4
// overload (to_bin32/to_oct32) zero-extends the 32-bit pattern, while the
// int8 overload (to_bin64/to_oct64) zero-extends the 64-bit pattern. Bare
// untyped integer literals (e.g. `to_bin(-1234)`, no cast) resolve to the
// int4/32-bit overload, mirroring to_hex's literal carve-out (M0134-0070,
// see internal/optimizer/planner.go's shared to_hex/to_bin/to_oct
// ArgWidth-stamping intercept).
// Oracle: postgres/src/backend/utils/adt/varlena.c:5190-5248 (convert_to_base,
// to_bin32, to_bin64, to_oct32, to_oct64).
func TestToBinToOctTwosComplementWidth(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want string
	}{
		{sql: `select to_bin(52)`, want: "110100"},
		{sql: `select to_bin(-52)`, want: "11111111111111111111111111001100"},
		{sql: `select to_bin(-52::bigint)`, want: "1111111111111111111111111111111111111111111111111111111111001100"},
		// Bare untyped literal carve-out: exprType would otherwise type an
		// untyped integer literal as int8, mis-resolving this into the
		// int8/64-bit path.
		{sql: `select to_bin(-1234)`, want: "11111111111111111111101100101110"},
		{sql: `select to_oct(52)`, want: "64"},
		{sql: `select to_oct(-52)`, want: "37777777714"},
		{sql: `select to_oct(-52::bigint)`, want: "1777777777777777777714"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			advanceStmtCounter(ctx)
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
			if err != nil {
				t.Fatalf("Plan(%q): %v", tc.sql, err)
			}
			op, err := Build(plan)
			if err != nil {
				t.Fatalf("Build(%q): %v", tc.sql, err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("Open(%q): %v", tc.sql, err)
			}
			rows, err := drainScan(op)
			_ = op.Close()
			if err != nil {
				t.Fatalf("exec(%q): %v", tc.sql, err)
			}
			if len(rows) != 1 || len(rows[0]) != 1 {
				t.Fatalf("%q: want 1x1 result, got %d rows", tc.sql, len(rows))
			}
			d := rows[0][0]
			if d.Kind != KindString {
				t.Fatalf("%q: Kind = %d, want KindString", tc.sql, d.Kind)
			}
			if got := d.StringValue(); got != tc.want {
				t.Errorf("%q: got %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}
