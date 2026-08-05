package executor

// M0127-P3.1 — build-side table presizing (design leftdeep-joins/06 §2.1).
//
// presizeLazyHash is the executor's first consumer of the shared
// planner↔executor sizing rule (internal/hashsize). The geometry arithmetic is
// tested in that package; what needs pinning HERE is the wiring, because the
// two failure modes are silent:
//
//   - allocating the WRONG map. The build commits to one representation
//     (o.lazyHashIsInt) before the first row arrives; presizing the other one
//     would either strand the allocation or, worse, look to the insert path
//     like a table that already exists.
//   - allocating on no information. planner.EstimateRows returns 0 for any
//     relation that has never been ANALYZEd — the common case in goopg, since
//     stats are per-connection — and hashsize floors an unknown build at 1024
//     buckets. Presizing on that floor would reserve a table for every join in
//     every query, including the three-row ones.
//
// Neither is visible in a row count, and Go gives no way to read a map's
// capacity back, so the assertions are about which map exists.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

// valuesNode returns a plan node whose EstimateRows is exactly n — Values is
// the one node type the planner estimates exactly rather than statistically.
func valuesNode(n int) *planner.Values {
	rows := make([][]planner.Expr, n)
	for i := range rows {
		rows[i] = []planner.Expr{&planner.ColumnRef{Index: 0, Type: catalog.Type{Name: "int4"}}}
	}
	return &planner.Values{Rows: rows}
}

func TestPresizeLazyHashChoosesTheLaneTheBuildCommittedTo(t *testing.T) {
	t.Run("string lane", func(t *testing.T) {
		o := &joinOp{plan: twoKeyJoinPlan(2)}
		o.lazyHashIsInt = false
		o.presizeLazyHash(nil, valuesNode(5000), 4, false)
		if o.lazyHash == nil {
			t.Fatalf("string table was not presized")
		}
		if o.lazyIntHash != nil {
			t.Fatalf("int table allocated for a string-lane build")
		}
	})
	t.Run("int lane", func(t *testing.T) {
		o := &joinOp{plan: twoKeyJoinPlan(2)}
		o.lazyHashIsInt = true
		o.presizeLazyHash(nil, valuesNode(5000), 4, false)
		if o.lazyIntHash == nil {
			t.Fatalf("int table was not presized")
		}
		if o.lazyHash != nil {
			t.Fatalf("string table allocated for an int-lane build")
		}
	})
}

// A build the planner cannot size — or sizes at three rows — must allocate
// nothing: hashsize's 1024-bucket floor means "no information", and every
// unestimated relation in the query would otherwise reserve a table.
func TestPresizeLazyHashSkipsWhenSizeIsUnknownOrTiny(t *testing.T) {
	cases := []struct {
		name string
		node planner.Node
	}{
		{"no node", nil},
		{"no estimate", valuesNode(0)},
		{"tiny build", valuesNode(3)},
		{"at the bucket floor", valuesNode(1024)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := &joinOp{plan: twoKeyJoinPlan(2)}
			o.presizeLazyHash(nil, c.node, 4, false)
			if o.lazyHash != nil || o.lazyIntHash != nil {
				t.Fatalf("presized a table for %q (lazyHash=%v lazyIntHash=%v)",
					c.name, o.lazyHash != nil, o.lazyIntHash != nil)
			}
		})
	}
}

// prebuildSharedHashJoins can reach the build phase without the operator ever
// being opened, and buildLazyHashTable is documented as idempotent. Presizing
// must therefore never discard rows an earlier pass already filed.
func TestPresizeLazyHashKeepsAnExistingTable(t *testing.T) {
	o := &joinOp{plan: twoKeyJoinPlan(2)}
	o.lazyHash = map[string][]Row{"k": {Row{NewIntDatum(7)}}}
	o.presizeLazyHash(nil, valuesNode(5000), 4, false)
	if len(o.lazyHash) != 1 || len(o.lazyHash["k"]) != 1 {
		t.Fatalf("presize replaced a populated table: %v", o.lazyHash)
	}
}

// The int lane's safety net (demoteIntHash) reads o.lazyIntHash directly, and
// its "nothing to re-key" guard is a nil check — which a presized-but-empty
// table no longer satisfies. Pin that a build which presizes the int table and
// then meets a non-integer key still lands every row in the string table.
func TestPresizedIntTableStillDemotes(t *testing.T) {
	o := &joinOp{plan: twoKeyJoinPlan(2)}
	o.lazyHashIsInt = true
	o.presizeLazyHash(nil, valuesNode(5000), 1, false)
	if o.lazyIntHash == nil {
		t.Fatalf("precondition: int table was not presized")
	}
	o.lazyHashInsertDatum(NewIntDatum(42), Row{NewIntDatum(42)})
	o.lazyHashInsertDatum(NewStringDatum("abc"), Row{NewStringDatum("abc")})
	if o.lazyIntHash != nil {
		t.Fatalf("int table survived the demotion")
	}
	if got := len(o.lazyHash); got != 2 {
		t.Fatalf("string table holds %d keys after demotion, want 2 (42 re-keyed + abc)", got)
	}
}
