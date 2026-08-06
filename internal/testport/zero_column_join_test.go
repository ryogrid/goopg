package testport

// zero_column_join_test.go — a join whose OWN output row is zero-column must
// return one row, not kill the backend.
//
// `Row(nil)` doubles as "a row with no columns" and "no row at all". Both join
// streams signal absence with EOF (an error), so every `(row, nil)` return is a
// real tuple — but when both inputs are zero-column the concatenated pair has
// width 0, `cloneRow` of that is nil, and `asSlot` mapped the nil Row back to a
// nil TupleSlot. `internal/server/dispatch.go`'s simple-query result loop then
// read the nil error as "row present" and dereferenced the slot, taking the
// whole backend down and truncating the rest of the session.
//
// PG oracle: postgres/src/test/regress/expected/select.out:520-522 —
// `SELECT * FROM nocols n, LATERAL (VALUES(n.*)) v;` emits one 0-column row.
// That is regress case `select` line 149; the crash truncated the case's
// remaining 352 lines, which is how the nightly saw it
// (AI-20260806-011323-018).
//
// WHY THIS LIVES HERE AND NOT IN internal/executor. The natural home would be
// an executor unit test over newDDLFixture, and one was written first — it
// passed against the UNFIXED code. That fixture's scan yields a non-nil,
// zero-length Row where a real heap scan yields nil, so the pair never becomes
// nil and the seam is never reached. Identical plans (`Nested Loop (CROSS)`
// over two width=0 Seq Scans) on both sides; only the Row's nil-ness differs.
// A guard that cannot fail on the broken code is worse than no guard, so this
// one runs against a live cluster, which is the level that reproduces.
// Verified by A/B of two binaries differing only in the two seam lines: the
// pre-fix binary loses the connection on all three shapes below, the fixed one
// returns one row each.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_ZeroColumnJoinDoesNotCrashBackend pins every shape that reaches a
// join with zero-column output. Each must return exactly one row and leave the
// connection alive; before the fix each closed the connection.
func TestPort_ZeroColumnJoinDoesNotCrashBackend(t *testing.T) {
	c, err := cluster.New("zero-column-join", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// `CREATE TABLE t()` is legal PG and the only way to reach a join whose
	// output width is genuinely zero — a projection above the join cannot
	// stand in, because it supplies its own non-nil row.
	for _, stmt := range []string{
		"CREATE TABLE zc_a ()",
		"CREATE TABLE zc_b ()",
		"INSERT INTO zc_a DEFAULT VALUES",
		"INSERT INTO zc_b DEFAULT VALUES",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}

	cases := []struct {
		name string
		sql  string
	}{
		// The plain streaming nested loop (joinOp.nextNL).
		{"cross", "SELECT * FROM zc_a a, zc_b b"},
		// The null-extending arm, whose unmatched path builds its row through
		// concatRows rather than cloneRow — a different producer, same seam.
		{"left", "SELECT * FROM zc_a a LEFT JOIN zc_b b ON true"},
		// No star to expand, so a projection cannot be what supplies the row.
		{"empty_target_list", "SELECT FROM zc_a a, zc_b b"},
		// The upstream regress statement itself: `n.*` on a zero-column table
		// expands to zero expressions, so the LATERAL VALUES row is zero-column
		// too and the join takes nextLateral rather than nextNL.
		{"lateral_values", "SELECT * FROM zc_a n, LATERAL (VALUES(n.*)) v"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			rows, err := c.Query(ctx, tc.sql)
			if err != nil {
				// The pre-fix failure mode: the backend panicked on the nil
				// slot and the connection went away mid-result.
				t.Fatalf("%s: %v (backend must not die on a zero-column join)", tc.sql, err)
			}
			if len(rows) != 1 {
				t.Fatalf("%s: got %d rows, want 1 (PG emits one 0-column row)", tc.sql, len(rows))
			}
			if len(rows[0]) != 0 {
				t.Fatalf("%s: row has %d columns, want 0", tc.sql, len(rows[0]))
			}
		})
	}

	// The connection surviving each statement is the point; prove the cluster
	// is still serving afterwards rather than inferring it.
	if got := queryScalar(t, c, "SELECT 1"); got != "1" {
		t.Fatalf("cluster unusable after the zero-column joins: SELECT 1 = %q", got)
	}
}
