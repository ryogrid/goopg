package executor

// hypothetical_set_agg_errpos_test.go — M0134-0001 S21: a hypothetical-set
// aggregate whose direct argument goopg coerces at runtime (buildAggregateCall,
// internal/optimizer/planner.go:8374-8379) must carry a nonzero error
// position, so psql renders `LINE 1:`/`^` the way PG does. PG oracle:
// postgres/src/backend/parser/parse_coerce.c:coerce_type (:294-304) keeps the
// original literal's location when it folds a string Const through its input
// function at parse-analysis time; goopg defers that same coercion to a
// runtime CastExpr instead, but the CastExpr must still carry the original
// literal's position for its own ExecError.Pos convention
// (internal/executor/operators_ddl.go:3207,10043 — Pos==0 means "suppress
// LINE 1", enforced by operators_ddl_system_column_test.go:34) to fire.
//
// The sibling collation-mismatch error (42P21, planner.go:8286-8302) is a
// PlanError set at PLAN time and already carried its position correctly
// before this fix; it is asserted alongside so the pair stays paired (per
// aggregates.out around line 2283, where both queries sit two lines apart).

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestHypotheticalSetAggRuntimeCastErrorCarriesPosition is the guard for
// M0134-0001 S21: before the fix, the CastExpr built at planner.go:8376 had
// pos==0 (Go zero value, never set), so evalCast's string→int4 branch
// (internal/executor/expr.go:3536-3543) produced an ExecError with Pos==0 —
// a real error, but one that renders no LINE 1:/^ in psql. FAIL-pre / PASS-post.
func TestHypotheticalSetAggRuntimeCastErrorCarriesPosition(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	sql := "select rank('fred') within group (order by x) from generate_series(1,5) x"
	_, err := runQueryErr(t, ctx, sql)
	if err == nil {
		t.Fatalf("%s: got no error, want invalid input syntax for type integer", sql)
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("%s: err = %v (%T), want *ExecError", sql, err, err)
	}
	if ee.Pos == 0 {
		t.Errorf("%s: Pos = 0, want NONZERO (pos==0 suppresses LINE 1: rendering; "+
			"before M0134-0001 S21 the CastExpr wrapping the direct arg never had "+
			"its pos set)", sql)
	}
	if wantSub := `invalid input syntax for type integer`; !strings.Contains(ee.Message, wantSub) {
		t.Errorf("%s: Message = %q, want substring %q", sql, ee.Message, wantSub)
	}
}

// TestHypotheticalSetAggCollationMismatchCarriesPosition pins the sibling
// 42P21 error (planner.go:8286-8302) so it stays paired with the case above —
// both queries sit two lines apart in aggregates.out and this one already
// worked before S21 (it is a PlanError set at plan time, not a runtime
// CastExpr). Regressing it would be an S21 scope violation.
func TestHypotheticalSetAggCollationMismatchCarriesPosition(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE hsa_v (x text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO hsa_v VALUES ('fred'), ('jim')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	sql := `select rank('adam'::text collate "C") within group (order by x collate "POSIX") from hsa_v`
	_, err := runQueryErr(t, ctx, sql)
	if err == nil {
		t.Fatalf("%s: got no error, want collation mismatch", sql)
	}
	pe, ok := err.(*optimizer.PlanError)
	if !ok {
		t.Fatalf("%s: err = %v (%T), want *optimizer.PlanError", sql, err, err)
	}
	if pe.Code != "42P21" {
		t.Errorf("%s: Code = %q, want 42P21", sql, pe.Code)
	}
	if pe.Pos == 0 {
		t.Errorf("%s: Pos = 0, want NONZERO — the sibling of the runtime-cast "+
			"case above; both must carry a position for LINE 1:/^ to render", sql)
	}
}
