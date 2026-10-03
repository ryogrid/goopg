package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0146-0049d1 — ExecHashJoin's empty-inner exit (nodeHashjoin.c,
// HJ_BUILD_HASHTABLE): an empty hash table ends a join that does not emit
// unmatched outer tuples without the outer side ever being scanned. The
// parameterised hash-join inner of a nested loop (TPC-DS Q95) relies on it:
// its build is an index probe that usually finds nothing, and its outer is a
// 1.7M-row CTE scan that would otherwise be read once per outer row.

// M0146-0049d1's other half is the same state's outer prefetch: a join that
// fetches the first outer tuple before building (always when it fills the
// outer side, else by cost) skips the build entirely on an empty outer.

// openCountOp counts the Opens of the operator it wraps.
type openCountOp struct {
	Operator
	opens int
}

func (o *openCountOp) Open(ctx *Context) error {
	o.opens++
	return o.Operator.Open(ctx)
}

func TestHashJoinEmptyBuildSkipsProbe(t *testing.T) {
	const lw, rw = 2, 2
	probe := keyedRows("l", 1, 2, 3)
	for _, tc := range []struct {
		name           string
		jt             optimizer.JoinType
		probe          []Row // nil: the shared three-row probe
		build          []Row
		wantRows       int
		wantOpens      int
		wantBuildOpens int
	}{
		// No unmatched probe row is emitted: the probe is never opened.
		{"inner/empty", optimizer.JoinTypeInner, nil, nil, 0, 0, 1},
		{"semi/empty", optimizer.JoinTypeSemi, nil, nil, 0, 0, 1},
		// RIGHT fills the build side, which is empty.
		{"right/empty", optimizer.JoinTypeRight, nil, nil, 0, 0, 1},
		// The probe side fills (HJ_FILL_OUTER): every probe row still emits.
		{"left/empty", optimizer.JoinTypeLeft, nil, nil, 3, 1, 1},
		{"anti/empty", optimizer.JoinTypeAnti, nil, nil, 3, 1, 1},
		// A non-empty build probes as before.
		{"inner/nonempty", optimizer.JoinTypeInner, nil, keyedRows("r", 2, 9), 1, 1, 1},
		// The outer prefetch (HJ_FILL_OUTER forces it): an empty outer ends
		// the join before the build side is opened.
		{"left/empty-outer", optimizer.JoinTypeLeft, []Row{}, keyedRows("r", 2, 9), 0, 1, 0},
		{"anti/empty-outer", optimizer.JoinTypeAnti, []Row{}, keyedRows("r", 2, 9), 0, 1, 0},
		// A RIGHT join (build-side fill) never prefetches: its unmatched
		// build rows emit whatever the outer holds.
		{"right/empty-outer", optimizer.JoinTypeRight, []Row{}, keyedRows("r", 2, 9), 2, 1, 1},
		// The prefetched tuple is not lost: it is the first probe row.
		{"left/prefetched-row-kept", optimizer.JoinTypeLeft, keyedRows("l", 2), keyedRows("r", 2, 9), 1, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.probe == nil {
				tc.probe = probe
			}
			plan := outerFillPlan(tc.jt, optimizer.JoinAlgoHash, lw, len(tc.probe), len(tc.build))
			left := &openCountOp{Operator: &rowsOp{rows: tc.probe, schema: batchSchema("l", lw)}}
			right := &openCountOp{Operator: &rowsOp{rows: tc.build, schema: batchSchema("r", rw)}}
			o := newJoinOp(plan, left, right)
			if err := o.Open(&Context{}); err != nil {
				t.Fatalf("open join: %v", err)
			}
			rows := 0
			for {
				_, err := o.Next()
				if err == EOF {
					break
				}
				if err != nil {
					t.Fatalf("join Next: %v", err)
				}
				rows++
			}
			if err := o.Close(); err != nil {
				t.Fatalf("close join: %v", err)
			}
			if rows != tc.wantRows {
				t.Errorf("rows = %d, want %d", rows, tc.wantRows)
			}
			if left.opens != tc.wantOpens {
				t.Errorf("probe side opened %d times, want %d", left.opens, tc.wantOpens)
			}
			if right.opens != tc.wantBuildOpens {
				t.Errorf("build side opened %d times, want %d", right.opens, tc.wantBuildOpens)
			}
		})
	}
}
