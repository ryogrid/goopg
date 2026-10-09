package optimizer

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestOrderWindowDefsLikePG pins M0146-0005dm against plans read off PG 18.3:
// select_active_windows' common_prefix_cmp decides the WindowAgg stack, the
// first window being the lowest. The windows are passed in the order their
// calls appear, as buildWindowStage collects them; want is that list
// reordered bottom-up.
func TestOrderWindowDefsLikePG(t *testing.T) {
	cases := []struct {
		sql  string
		want string // indexes of the windows in first-appearance order, bottom-up
	}{
		// GROUP BY gives a,b,c refs 1-3; the ORDER BY window's sum(d) takes 4,
		// so (PARTITION BY a, b ORDER BY c, sum(d)) sorts first.
		{"SELECT sum(sum(d)) OVER (PARTITION BY a, b, c), rank() OVER (PARTITION BY a, b ORDER BY c, sum(d)) FROM wt GROUP BY a, b, c", "[1 0]"},
		// A common prefix: the longer list goes first.
		{"SELECT avg(d) OVER (PARTITION BY a), rank() OVER (PARTITION BY a ORDER BY b) FROM wt", "[1 0]"},
		// ORDER BY items take refs before PARTITION BY items: b=1, a=2.
		{"SELECT rank() OVER (PARTITION BY a ORDER BY b), avg(d) OVER (PARTITION BY b) FROM wt", "[0 1]"},
		// The statement ORDER BY c takes ref 1; b DESC outranks b ASC.
		{"SELECT rank() OVER (ORDER BY b DESC), rank() OVER (ORDER BY b), rank() OVER (ORDER BY c) FROM wt ORDER BY c", "[0 1 2]"},
	}
	for _, c := range cases {
		stmts, err := parser.Parse(c.sql)
		if err != nil {
			t.Fatalf("parse %q: %v", c.sql, err)
		}
		s := stmts[0].(*parser.SelectStmt)
		calls, err := collectWindowCalls(s)
		if err != nil {
			t.Fatal(err)
		}
		var defs []*parser.WindowDef
		seen := map[string]bool{}
		for _, fc := range calls {
			if k := windowSpecKey(fc.Over); !seen[k] {
				seen[k] = true
				defs = append(defs, fc.Over)
			}
		}
		if got := fmt.Sprint(orderWindowDefsLikePG(s, defs)); got != c.want {
			t.Errorf("%s:\n got %s, want %s", c.sql, got, c.want)
		}
	}
}
