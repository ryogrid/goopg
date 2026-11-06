package executor

import (
	"errors"
	"strings"
	"testing"
)

// TestDropTypeDependents pins dependency.c's DROP TYPE behaviour over the
// rebuilt function/aggregate edges (M0148-0001j1): RESTRICT is 2BP01 with
// PG's DETAIL order (descending-OID depth-first walk, reported back to
// front; `st_arr` through the internal array type, the aggregate through its
// final function, not its state type), CASCADE drops them all so the names
// can be created again.
func TestDropTypeDependents(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TYPE st AS (total bigint, cnt bigint)",
		"CREATE FUNCTION st_trans(s st, n int) RETURNS st LANGUAGE sql AS $$ SELECT s $$",
		"CREATE FUNCTION st_final(s st) RETURNS int LANGUAGE sql AS $$ SELECT 1 $$",
		"CREATE FUNCTION st_arr(a st[]) RETURNS int LANGUAGE sql AS $$ SELECT 1 $$",
		"CREATE FUNCTION st_ret() RETURNS st LANGUAGE sql AS $$ SELECT NULL::st $$",
		"CREATE AGGREGATE st_avg(int) (STYPE = st, SFUNC = st_trans, FINALFUNC = st_final)",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	err := runDDL(t, ctx, "DROP TYPE st")
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != "2BP01" {
		t.Fatalf("DROP TYPE st (RESTRICT): err = %v, want 2BP01", err)
	}
	wantDetail := strings.Join([]string{
		"function st_arr(st[]) depends on type st[]",
		"function st_trans(st,integer) depends on type st",
		"function st_final(st) depends on type st",
		"function st_avg(integer) depends on function st_final(st)",
		"function st_ret() depends on type st",
	}, "\n")
	if ee.Detail != wantDetail {
		t.Errorf("DETAIL:\n%s\nwant (PG 18.3):\n%s", ee.Detail, wantDetail)
	}
	ctx.TakeNoticesWithDetail()
	if err := runDDL(t, ctx, "DROP TYPE st CASCADE"); err != nil {
		t.Fatalf("DROP TYPE st CASCADE: %v", err)
	}
	nd := ctx.TakeNoticesWithDetail()
	wantCascade := strings.Join([]string{
		"drop cascades to function st_arr(st[])",
		"drop cascades to function st_trans(st,integer)",
		"drop cascades to function st_final(st)",
		"drop cascades to function st_avg(integer)",
		"drop cascades to function st_ret()",
	}, "\n")
	if len(nd) != 1 || nd[0].Message != "drop cascades to 5 other objects" || nd[0].Detail != wantCascade {
		t.Fatalf("notices %+v, want (PG 18.3) the 5-object cascade:\n%s", nd, wantCascade)
	}
	for _, q := range []string{
		"CREATE TYPE st AS (total bigint, cnt bigint)",
		"CREATE FUNCTION st_final(s st) RETURNS int LANGUAGE sql AS $$ SELECT 1 $$",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("re-create after CASCADE: %s: %v", q, err)
		}
	}
}
