package executor

import (
	"strings"
	"testing"
)

// M0146-0015h: a non-LATERAL derived table (or VALUES) inside a
// correlated subquery resolves outer-QUERY-level column references —
// PG's parse_subquery links the parent ParseState regardless of
// rte->lateral; only same-level FROM siblings stay invisible.
// goopg planned the `lateralCtx == nil` arm (first FROM item, and every
// non-LATERAL join right side) with a nil parent, so `x.h = a.h` inside
// the derived body died 42703 while the same ref in the subquery's own
// WHERE worked.
//
// The join-right-side arm is the subtle one: the enclosing Join flips
// Lateral on the resolved OuterColumnRef and openLateral pushes the
// left row, so outer refs there must resolve at level 2 — the nil
// marker link (`&resolveContext{parent: planParent}`) supplies that
// extra hop without exposing sibling bindings.
//
// Expected values below were captured live on PostgreSQL 18.3 over a
// 100-row tk (u1=i, u2=i, h=i%10, t=i%5).
func TestDerivedTableOuterReference(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, d := range []string{
		"CREATE TABLE tk (u1 int4, u2 int4, h int4, t int4)",
		"INSERT INTO tk SELECT i, i, i % 10, i % 5 FROM generate_series(0, 99) i",
	} {
		if err := runDDL(t, ctx, d); err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	cases := []struct {
		name, q, want string
	}{
		// The filed shape: first-FROM-item derived table whose WHERE
		// correlates to the grandparent level.
		{"derived-where", "SELECT a.u1, (SELECT count(*) FROM (SELECT x.u1 FROM tk x WHERE x.h = a.h) s) FROM tk a WHERE a.u1 < 3 ORDER BY a.u1", "0|10,1|10,2|10"},
		// Same, with an unqualified ref to the derived table's own column.
		{"derived-unqualified", "SELECT a.u1, (SELECT count(*) FROM (SELECT x.u1 FROM tk x WHERE h = a.h) s) FROM tk a WHERE a.u1 < 3 ORDER BY a.u1", "0|10,1|10,2|10"},
		// Outer ref in the derived body's TARGET LIST.
		{"derived-target", "SELECT a.u1, (SELECT count(*) FROM (SELECT a.h + x.u1 AS u1 FROM tk x) s) FROM tk a WHERE a.u1 < 3 ORDER BY a.u1", "0|100,1|100,2|100"},
		// ORDER BY/LIMIT inside the derived body.
		{"derived-limit", "SELECT a.u1, (SELECT count(*) FROM (SELECT x.u1 FROM tk x WHERE x.h = a.h ORDER BY x.u1 LIMIT 2) s) FROM tk a WHERE a.u1 < 3 ORDER BY a.u1", "0|2,1|2,2|2"},
		// Non-LATERAL JOIN right side: outer ref must resolve at level 2
		// (the Join marks Lateral on the OuterColumnRef and openLateral
		// pushes the left row — count is o(100) × s(10)).
		{"join-right-outer", "SELECT a.u1, (SELECT count(*) FROM tk o JOIN (SELECT x.u1 FROM tk x WHERE x.h = a.h) s ON true) FROM tk a WHERE a.u1 < 3 ORDER BY a.u1", "0|1000,1|1000,2|1000"},
		// Derived table inside a correlated EXISTS.
		{"exists", "SELECT count(*) FROM tk a WHERE EXISTS (SELECT 1 FROM (SELECT x.u1 FROM tk x WHERE x.h = a.h) s WHERE s.u1 = a.u1)", "100"},
		// VALUES in the same first-item position.
		{"values", "SELECT a.u1, (SELECT count(*) FROM (VALUES (a.h)) v) FROM tk a WHERE a.u1 < 3 ORDER BY a.u1", "0|1,1|1,2|1"},
		// VALUES on a non-LATERAL join right side (level-2 arm).
		{"values-join-right", "SELECT a.u1, (SELECT count(*) FROM tk o JOIN (VALUES (a.h)) v(c1) ON true) FROM tk a WHERE a.u1 < 3 ORDER BY a.u1", "0|100,1|100,2|100"},
		// Non-correlated control: an ordinary derived table still works.
		{"control", "SELECT count(*) FROM (SELECT x.u1 FROM tk x) s, tk a", "10000"},
	}
	for _, tc := range cases {
		rows, err := runQueryWithErr(ctx, tc.q)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		var cells []string
		for _, r := range rows {
			parts := make([]string, 0, len(r))
			for _, d := range r {
				parts = append(parts, d.Format())
			}
			cells = append(cells, strings.Join(parts, "|"))
		}
		if got := strings.Join(cells, ","); got != tc.want {
			t.Errorf("%s\n got  %s\n want %s (PG 18.3)", tc.name, got, tc.want)
		}
	}

	// Non-LATERAL must still hide same-level FROM siblings: a ref to the
	// join's left side inside the right subquery errors on both engines
	// (PG 42P01 "invalid reference to FROM-clause entry").
	if _, err := runQueryWithErr(ctx, "SELECT a.u1, (SELECT count(*) FROM tk o JOIN (SELECT x.u1 FROM tk x WHERE x.h = o.h) s ON true) FROM tk a WHERE a.u1 < 3"); err == nil {
		t.Errorf("non-lateral join right side referenced a sibling: expected error, got rows")
	}
	// LATERAL still sees the sibling (the opposite arm must keep working).
	if _, err := runQueryWithErr(ctx, "SELECT a.u1, (SELECT count(*) FROM tk o JOIN LATERAL (SELECT x.u1 FROM tk x WHERE x.h = o.h) s ON true) FROM tk a WHERE a.u1 < 3"); err != nil {
		t.Errorf("lateral join right side lost sibling visibility: %v", err)
	}
}
