package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestReduceUniqueSemijoinsBaseTable pins M0145-0008ae's RTE_RELATION arm of
// PG's reduce_unique_semijoins (analyzejoins.c:844): a pulled semijoin whose
// single-rel RHS has a UNIQUE index covered by the equated columns is planned
// as an inner join, and one without that proof keeps its semi join.
//
// The negative rows guard the value risk: reducing a semijoin whose RHS is
// NOT unique would return one output row per RHS match instead of one per
// outer row.
func TestReduceUniqueSemijoinsBaseTable(t *testing.T) {
	cases := []struct {
		name   string
		unique bool
		cols   []string
		sql    string
		semi   bool
	}{
		{"in-over-unique-key", true, []string{"j"}, `select tag from jtp_o where k in (select j from jtp_i)`, false},
		{"exists-over-unique-key", true, []string{"j"}, `select tag from jtp_o where exists (select 1 from jtp_i where j = k)`, false},
		{"no-index", false, nil, `select tag from jtp_o where k in (select j from jtp_i)`, true},
		{"non-unique-index", false, []string{"j"}, `select tag from jtp_o where k in (select j from jtp_i)`, true},
		{"unique-key-not-covered", true, []string{"j", "v"}, `select tag from jtp_o where k in (select j from jtp_i)`, true},
		{"unique-on-other-column", true, []string{"v"}, `select tag from jtp_o where k in (select j from jtp_i)`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := jtpCatalog(t)
			if tc.cols != nil {
				tbl, ok := cat.LookupTable(parser.ObjectName{Name: "jtp_i"})
				if !ok {
					t.Fatal("jtp_i not found")
				}
				if _, err := cat.CreateIndex(parser.ObjectName{Name: "jtp_i_idx"}, tbl, tc.cols, tc.unique, "btree", false); err != nil {
					t.Fatal(err)
				}
			}
			delete(sublinkRouteCounts, spineRouteJointree)
			node := planOnPipeline(t, tc.sql, cat)
			if n := sublinkRouteCounts[spineRouteJointree]; n != 1 {
				t.Fatalf("jointree pull-up engaged %d times, want 1; tree: %s", n, describePlanTree(node))
			}
			j := findSemiOrAntiJoin(node)
			if tc.semi && (j == nil || j.Type != JoinTypeSemi) {
				t.Fatalf("semijoin without a uniqueness proof was reduced; tree: %s", describePlanTree(node))
			}
			if !tc.semi && j != nil {
				t.Fatalf("semijoin over a unique key kept its %v join; tree: %s", j.Type, describePlanTree(node))
			}
		})
	}
}
