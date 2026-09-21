package optimizer

// M0145-0005 — the NOT NULL qual reduction on the jointree arm.
//
// PG applies `restriction_is_always_true`/`restriction_is_always_false` to
// every single-baserel restriction clause (initsplan.c's
// `add_base_clause_to_rel`), and goopg's default arm already does: the rule
// chooser reduces `not_null_col IS NULL` to a CHILDLESS
// `Result / One-Time Filter: false`, with no scan under it, and drops a
// redundant `IS NOT NULL` outright.
//
// Slice 4 routed the jointree arm's single-table+WHERE scopes through the
// GENERIC arm, and the reduction stayed behind in the chooser — so the knob
// arm planned a full scan under a Filter for a predicate PG proves false. The
// pins below are ARM-EQUALITY pins on purpose: the point is not that some
// particular shape appears, it is that flipping `GOOPG_JOINTREE_PIPELINE`
// cannot change this plan, because M0145-0008 flips it for everyone.

import (
	"fmt"
	"testing"
)

// planShapeSpine renders the chain of single-child wrappers down to the first
// node that branches or ends, which is all these cases need.
func planShapeSpine(t *testing.T, n Node) string {
	t.Helper()
	out := ""
	for c := n; c != nil; {
		if out != "" {
			out += " -> "
		}
		out += fmt.Sprintf("%T", c)
		switch x := c.(type) {
		case *Project:
			c = x.Child
		case *Filter:
			c = x.Child
		case *Result:
			// The childless Result is the whole point: say so explicitly.
			out += fmt.Sprintf("{childless=%v}", x.Child == nil)
			c = nil
		default:
			c = nil
		}
	}
	return out
}

// TestNotNullReductionIsArmIndependent pins that the two reductions produce
// the same plan on both pipelines. Before this fix the knob arm produced
// Filter{SeqScan} for every case below.
func TestNotNullReductionIsArmIndependent(t *testing.T) {
	cat, _, _ := ppiCatalog(t)
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{
			// restriction_is_always_false: o_orderkey is NOT NULL, so the
			// predicate can never hold and PG emits no scan at all.
			name: "IS NULL on a NOT NULL column",
			sql:  "select o_orderkey from orders where o_orderkey is null",
			want: "*optimizer.Project -> *optimizer.Result{childless=true}",
		},
		{
			// restriction_is_always_true: the qual is redundant and is
			// dropped, leaving a bare scan with no Filter node.
			name: "IS NOT NULL on a NOT NULL column",
			sql:  "select o_orderkey from orders where o_orderkey is not null",
			want: "*optimizer.Project -> *optimizer.SeqScan",
		},
		{
			// One always-false conjunct makes the whole AND false, so the
			// surviving equality does not save the scan.
			name: "always-false conjunct beside a live one",
			sql:  "select o_orderkey from orders where o_custkey is null and o_orderkey = 5",
			want: "*optimizer.Project -> *optimizer.Result{childless=true}",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var shapes [2]string
			for i, jt := range []bool{false, true} {
				prev := jointreePipeline
				jointreePipeline = jt
				n, err := PlanWithSettings(parseOne(t, c.sql), cat, DefaultPlannerSettings())
				jointreePipeline = prev
				if err != nil {
					t.Fatalf("jointree=%v: %v", jt, err)
				}
				shapes[i] = planShapeSpine(t, n)
			}
			if shapes[0] != shapes[1] {
				t.Fatalf("arms disagree:\n  default  = %s\n  jointree = %s", shapes[0], shapes[1])
			}
			if shapes[0] != c.want {
				t.Fatalf("shape = %s, want %s", shapes[0], c.want)
			}
		})
	}
}
