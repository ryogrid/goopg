package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestToHexTwosComplementWidth pins to_hex(int)'s two's-complement
// zero-extension semantics for negative arguments: the int4 overload
// (to_hex32) zero-extends the 32-bit pattern (8 hex chars), while the int8
// overload (to_hex64) zero-extends the 64-bit pattern (16 hex chars).
// Oracle: postgres/src/backend/utils/adt/varlena.c:5254-5267
// (to_hex32/to_hex64, both via convert_to_base at varlena.c:5190-5210).
// Expected strings pinned from postgres/src/test/regress/expected/
// strings.out:2306-2326. M0134-0070.
func TestToHexTwosComplementWidth(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want string
	}{
		{sql: `select to_hex(-1234)`, want: "fffffb2e"},
		{sql: `select to_hex(-1234::bigint)`, want: "fffffffffffffb2e"},
		{sql: `select to_hex(256*256*256 - 1)`, want: "ffffff"},
		{sql: `select to_hex(256::bigint*256::bigint*256::bigint*256::bigint - 1)`, want: "ffffffff"},
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
