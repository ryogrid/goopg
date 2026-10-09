package executor

import (
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestSortedSetOpMatchesHashed pins M0146-0005q's SETOP_SORTED executor
// (nodeSetOp.c sorted mode): over inputs sorted on every column it must
// return exactly what the hashed form returns for INTERSECT, INTERSECT ALL,
// EXCEPT and EXCEPT ALL — duplicates, NULL groups (equal in set operations)
// and right-only groups included.
func TestSortedSetOpMatchesHashed(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE sso_t (a int)"); err != nil {
		t.Fatal(err)
	}
	shape := planForTest(t, ctx, "SELECT a FROM sso_t")
	schema := shape.Output()
	null := Datum{Kind: KindNull}
	left := []Row{{NewIntDatum(1)}, {NewIntDatum(1)}, {NewIntDatum(2)}, {NewIntDatum(3)}, {null}, {null}}
	right := []Row{{NewIntDatum(1)}, {NewIntDatum(3)}, {NewIntDatum(3)}, {NewIntDatum(4)}, {null}}
	keys := []optimizer.SortKey{{Expr: &optimizer.ColumnRef{Index: 0, Name: "a", Type: catalog.Type{Name: "int4"}}}}

	run := func(op parser.SetOpType, all bool, merge []optimizer.SortKey) []string {
		plan := &optimizer.SetOp{Left: shape, Right: shape, Op: op, All: all, MergeKeys: merge}
		so := newSetOp(plan, &rowsOp{rows: left, schema: schema}, &rowsOp{rows: right, schema: schema})
		if err := so.Open(ctx); err != nil {
			t.Fatal(err)
		}
		got := readAll(t, so)
		so.Close()
		sort.Strings(got)
		return got
	}
	for _, tc := range []struct {
		name string
		op   parser.SetOpType
		all  bool
		want string
	}{
		{"intersect", parser.SetOpIntersect, false, "1|3|NULL"},
		{"intersect all", parser.SetOpIntersect, true, "1|3|NULL"},
		{"except", parser.SetOpExcept, false, "2"},
		{"except all", parser.SetOpExcept, true, "1|2|NULL"},
	} {
		sorted := run(tc.op, tc.all, keys)
		hashed := run(tc.op, tc.all, nil)
		if strings.Join(sorted, ",") != strings.Join(hashed, ",") {
			t.Errorf("%s: sorted %v != hashed %v", tc.name, sorted, hashed)
		}
		if n := len(strings.Split(tc.want, "|")); len(sorted) != n {
			t.Errorf("%s: got %d rows %v, want %d (%s)", tc.name, len(sorted), sorted, n, tc.want)
		}
	}
}
