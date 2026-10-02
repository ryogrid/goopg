package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestRestrictionSkipScanMatchesPG pins M0146-0005dg: PG 18 binds a
// `col = const` on a NON-leading btree column as an index qual and skips the
// unbound leading column (`_bt_skiparray`), so `WHERE b = 77` on (a, b)
// plans `Index Scan ... Index Cond: (b = 77)` instead of a Seq Scan. goopg
// had the parameterised skip arm only (M0146-0005v). The test drives the
// planner (sequential and bitmap scans off, so the skip path is the one
// index answer) and checks the lowered node and its rows; a nullable leading
// column must keep the skip off (the byte-key btree stores no NULL-keyed
// entry), and an index-only promotion must not drop SkipPrefix (it re-aimed
// the probe at the leading column).
func TestRestrictionSkipScanMatchesPG(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE sk (a int NOT NULL, b int NOT NULL, c int)")
	runSQL(t, ctx, "INSERT INTO sk SELECT g % 5, g, g * 10 FROM generate_series(1, 20000) g")
	runSQL(t, ctx, "CREATE INDEX sk_ab ON sk (a, b)")
	runSQL(t, ctx, "CREATE TABLE skn (a int, b int NOT NULL)")
	runSQL(t, ctx, "INSERT INTO skn SELECT g % 5, g FROM generate_series(1, 20000) g")
	runSQL(t, ctx, "CREATE INDEX skn_ab ON skn (a, b)")
	runSQL(t, ctx, "ANALYZE sk")
	runSQL(t, ctx, "ANALYZE skn")

	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	ps.EnableSeqScan = false
	ps.EnableBitmapScan = false

	findScan := func(n optimizer.Node) (*optimizer.IndexScan, bool) {
		var hit *optimizer.IndexScan
		ios := false
		var walk func(optimizer.Node)
		walk = func(n optimizer.Node) {
			switch x := n.(type) {
			case *optimizer.IndexScan:
				hit = x
			case *optimizer.IndexOnlyScan:
				ios = true
			}
			for _, k := range planChildren(n) {
				walk(k)
			}
		}
		walk(n)
		return hit, ios
	}

	for _, c := range []struct {
		sql  string
		want string
	}{
		{"SELECT a, b, c FROM sk WHERE b = 77", "[2 77 770]"},
		// Covering target list: the promotion to an index-only scan must
		// not lose the skip (PG would print Index Only Scan; ledgered).
		{"SELECT b FROM sk WHERE b = 77", "[77]"},
	} {
		plan := planWithSettings(t, ctx, c.sql, ps)
		is, ios := findScan(plan)
		if is == nil || is.SkipPrefix != 1 || len(is.Keys) != 1 || ios {
			t.Errorf("%s: want an Index Scan with SkipPrefix 1 and one key, got %+v (index-only=%v)", c.sql, is, ios)
		}
		rows := drainPlanRows(t, ctx, plan)
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows, want 1", c.sql, len(rows))
		}
		got := make([]int64, len(rows[0]))
		for i, d := range rows[0] {
			got[i] = d.Int
		}
		if fmt.Sprint(got) != c.want {
			t.Errorf("%s: row %v, want %s", c.sql, got, c.want)
		}
	}

	// A nullable skipped column: no skip probe.
	plan := planWithSettings(t, ctx, "SELECT a, b FROM skn WHERE b = 77", ps)
	if is, _ := findScan(plan); is != nil && is.SkipPrefix > 0 {
		t.Errorf("nullable leading column: got a skip probe %+v", is)
	}
	if rows := drainPlanRows(t, ctx, plan); len(rows) != 1 {
		t.Errorf("nullable leading column: %d rows, want 1", len(rows))
	}
}
