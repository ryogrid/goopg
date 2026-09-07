package executor

import (
	"fmt"
	"strings"
	"testing"
)

// pqCompositeFixture builds a fact/dimension pair joined on TWO columns, so the
// join takes the composite-key lane (join_composite_key.go) rather than the
// single-key int64 or string map.
//
// The dimension rows carry a payload column that no join key references. That
// is deliberate: the failure mode this family of tests exists for is a right
// row COUNT with a NULL payload (5bf764520 / the EX1-02 deform bound), and only
// a column outside the key can witness it.
func pqCompositeFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	fail := func(err error, what string) {
		cleanup()
		t.Fatalf("%s: %v", what, err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE pc_dim (k1 int, k2 int, dname text)"); err != nil {
		fail(err, "create dim")
	}
	if err := runDDL(t, ctx, "CREATE TABLE pc_fact (fid int, k1 int, k2 int)"); err != nil {
		fail(err, "create fact")
	}
	// 40 dimension rows over a 5x8 key grid, one with a NULL key component so
	// the composite lane's NULL-key path (recordBuildNullKey) is exercised.
	for i := 0; i < 40; i++ {
		k1 := fmt.Sprintf("%d", i%5)
		k2 := fmt.Sprintf("%d", i%8)
		if i == 11 {
			k2 = "NULL"
		}
		if err := runDDL(t, ctx,
			fmt.Sprintf("INSERT INTO pc_dim VALUES (%s, %s, 'd-%d')", k1, k2, i)); err != nil {
			fail(err, "insert dim")
		}
	}
	// 400 fact rows; keys 5..9 on k1 have no dimension row.
	for i := 0; i < 400; i++ {
		if err := runDDL(t, ctx,
			fmt.Sprintf("INSERT INTO pc_fact VALUES (%d, %d, %d)", i, i%10, i%8)); err != nil {
			fail(err, "insert fact")
		}
	}
	return ctx, cleanup
}

const pcCompositeSQL = "SELECT f.fid, d.dname FROM pc_fact f JOIN pc_dim d " +
	"ON f.k1 = d.k1 AND f.k2 = d.k2 WHERE d.k1 >= 0"

// pcExpectedRows is the serial answer, computed once by the serial build so the
// parallel sweep has something to be identical TO rather than a hand-count that
// could itself be wrong.
func pcExpectedRows(t *testing.T) []string {
	t.Helper()
	ctx, cleanup := pqCompositeFixture(t)
	defer cleanup()
	// MinParallelTableScanBlocks above any table here ⇒ Rule 4 declines and
	// the build runs serially.
	ctx.MinParallelTableScanBlocks = 1 << 30
	rows, err := runQueryWithErr(ctx, pcCompositeSQL)
	if err != nil {
		t.Fatalf("serial reference: %v", err)
	}
	out := renderRows(rows)
	if len(out) == 0 {
		t.Fatal("serial reference produced no rows — the fixture joins nothing")
	}
	return out
}

// TestCoopParallelHashBuildComposite is E-18 slice 3's "the newly-reachable
// path actually fires, and it is right" assertion.
//
// Before slice 3, parallelBuildEligible declined every multi-column key
// ("composite keys have a different insertion path that the channel-source
// pattern doesn't reach"). It does reach it: fileCompositeBuildRow is called
// from inside buildLoopRight/buildLoopLeft, which is exactly what the
// cooperative consumer runs, off a channelSource.
//
// The sweep over work_mem is not decoration. The sibling regression
// (TestCoopParallelHashBuildValuesAcrossWorkMem) failed at 25 of 32 work_mem
// values with a CORRECT row count and a NULL payload, and was correct in a
// narrow band a spot check would have landed in. Composite builds additionally
// change lane with work_mem (batching routes rows to files instead of the map),
// so a single setting exercises one of two insertion paths.
func TestCoopParallelHashBuildComposite(t *testing.T) {
	want := pcExpectedRows(t)

	workMems := []int64{0}
	for e := 0; e <= 30; e++ {
		workMems = append(workMems, int64(1)<<uint(e))
	}

	before := CoopCompositeBuildCount()

	for _, wm := range workMems {
		wm := wm
		t.Run(workMemName(wm), func(t *testing.T) {
			ctx, cleanup := pqCompositeFixture(t)
			defer cleanup()
			ctx.WorkMem = wm

			rows, err := runQueryWithErr(ctx, pcCompositeSQL)
			if err != nil {
				t.Fatalf("work_mem=%d: %v", wm, err)
			}
			got := renderRows(rows)

			if len(got) != len(want) {
				t.Fatalf("work_mem=%d: %d rows, want %d", wm, len(got), len(want))
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("work_mem=%d: row %d = %q, want %q — the cooperative "+
						"composite build does not agree with the serial one",
						wm, i, got[i], want[i])
				}
			}
			// Row counts sail past payload corruption; assert the payload
			// column directly as well.
			for _, r := range got {
				if strings.HasSuffix(r, "|NULL") {
					t.Fatalf("work_mem=%d: NULL d.dname payload in %q — the "+
						"cooperative composite build lost its payload columns", wm, r)
				}
			}
		})
	}

	if got := CoopCompositeBuildCount(); got == before {
		t.Fatalf("cooperative COMPOSITE build count did not move (%d): the path "+
			"E-18 slice 3 opened was never taken, so this test proves nothing "+
			"about it. Either the fixture no longer takes the composite lane or "+
			"eligibility declines it for another reason", got)
	}
}
