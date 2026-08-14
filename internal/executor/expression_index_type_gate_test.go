package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

// boxKeyExpr is the smallest expression whose planner.ExprResultType names box
// — a bare ColumnRef of that type. It mirrors floatKeyExprOf / enumKeyExpr and
// keeps the test independent of which box-returning overloads the pg_proc seed
// happens to carry.
func boxKeyExpr() planner.Expr {
	return &planner.ColumnRef{Type: catalog.Type{Name: "box"}}
}

// TestExpressionIndexKeyRejectsBoxAndInt4Range is the DDL-gate regression
// witness for M0119-0006 expression-key type validation.
//
// The bug: createBTreeIndex's expression-key branch (name == "") skipped the
// isSupportedBTreeKeyType gate the named-column branch applies, so
// CREATE INDEX ON t ((b)) on a box column silently built a B-tree index that
// encoded the box TEXT in varchar byte order (encodeArbiterExprKey's KindString
// arm). PG 18.3 rejects a btree index on a box expression with 42704 "data type
// box has no default operator class for access method \"btree\"" because box
// has no btree opclass (indexcmds.c ResolveOpClass → GetDefaultOpClass →
// InvalidOid); int4range PG *accepts* (range_ops is the default via the
// binary-coercible-to-anyrange path), but goopg has no range value model, so it
// must reject honestly. Both go through the SAME 0A000 error the named-column
// path already uses, keeping the two branches consistent (the 42704 polish for
// box is a separate deferred slice).
func TestExpressionIndexKeyRejectsBoxAndInt4Range(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	cases := []struct {
		name    string
		table   string
		index   string
		typ     string
		keyExpr planner.Expr
		datum   Datum
	}{
		{
			name:    "box",
			table:   "CREATE TABLE exprgate_box_t (b box)",
			index:   "CREATE INDEX exprgate_box_idx ON exprgate_box_t ((b))",
			typ:     "box",
			keyExpr: boxKeyExpr(),
			datum:   NewStringDatum("(1,1),(2,2)"),
		},
		{
			name:  "int4range",
			table: "CREATE TABLE exprgate_range_t (r int4range)",
			index: "CREATE INDEX exprgate_range_idx ON exprgate_range_t ((r))",
			typ:   "int4range",
		},
	}
	for _, tc := range cases {
		if err := runDDL(t, ctx, tc.table); err != nil {
			t.Fatalf("%s: %v", tc.table, err)
		}
		err := runDDL(t, ctx, tc.index)
		if err == nil {
			t.Fatalf("%s expression index should have been rejected (PG has no "+
				"btree key for %q), got nil", tc.name, tc.typ)
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Fatalf("want *ExecError, got %T: %v", err, err)
		}
		if ee.Code != "0A000" {
			t.Errorf("%s: Code=%q want 0A000", tc.name, ee.Code)
		}
		if ee.Message != `btree v0 only supports int4 / numeric keys, got "`+tc.typ+`"` {
			t.Errorf("%s: Message=%q", tc.name, ee.Message)
		}

		// Mutation witness (not-vacuous check, same pattern as the float/enum
		// encoder tests): the DDL gate is the ONLY thing rejecting the box
		// expression — the shared expression-key encoder would happily build a
		// varchar key for a box text datum (the pre-fix silent-build bug). If a
		// future edit bypasses the gate, this assertion still proves the encoder
		// alone does not decline.
		if tc.keyExpr != nil {
			if got := encodeArbiterExprKey(ctx, tc.datum, tc.keyExpr, 0); got == nil {
				t.Errorf("%s: encodeArbiterExprKey returns nil for a %s text datum — "+
					"if the encoder declined, the gate's rejection is no longer the "+
					"only guard", tc.name, tc.typ)
			}
		}
	}
}

// TestExpressionIndexKeyStillAllowsFloatEnumText pins the non-regression half
// of the gate: expression keys whose result type IS btree-legal in PG must
// still build. float8 and text are supported by isSupportedBTreeKeyType; a user
// enum is admitted via the LookupEnum exception (M0097-0022) — exactly the
// named-column branch's behaviour, mirrored onto the expression branch.
func TestExpressionIndexKeyStillAllowsFloatEnumText(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	for _, sql := range []string{
		"CREATE TYPE exprgate_mood AS ENUM ('sad', 'ok', 'happy')",
		"CREATE TABLE exprgate_ok_t (a int4, f float8, m exprgate_mood, n text)",
		// float expression — existing expression_index_float_key_test.go's shape.
		"CREATE INDEX exprgate_float_idx ON exprgate_ok_t ((f * 2))",
		// enum expression — existing expression_index_enum_key_test.go's shape
		// (a bare column reference would be an ordinary column key, not an
		// expression column).
		"CREATE INDEX exprgate_enum_idx ON exprgate_ok_t ((CASE WHEN a > 0 THEN m ELSE m END))",
		// text expression.
		"CREATE INDEX exprgate_text_idx ON exprgate_ok_t ((lower(n)))",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
}
