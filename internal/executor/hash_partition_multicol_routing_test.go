package executor

import (
	"fmt"
	"testing"
)

// TestHashPartitionMultiColumnRouting is a targeted regression test for
// M0134-0053. routeToPartitionDepth's "HASH" case used to resolve and hash
// only the FIRST partition key column (resolvePartitionKeyDatum(0)), so a
// table declared PARTITION BY HASH (a, b) silently ignored b when routing
// rows: two rows sharing column a but differing in column b would always
// collide into the same partition, regardless of PG's actual
// satisfies_hash_partition()/compute_partition_hash_value() algorithm (which
// folds the hash of every partition-key column via hash_combine64 before the
// modulus/remainder match — postgres/src/backend/partitioning/partbounds.c).
//
// Fix: routeToPartitionDepth's HASH case now calls the shared
// computeHashPartitionRowHash helper (also used by the satisfies_hash_partition
// builtin in expr.go) to fold ALL partition-key columns, then routes via
// FindHashPartitionByHash instead of the single-column, string-based
// FindHashPartitionForValue.
//
// Expected routing below was independently derived by computing
// pgHashCombine64(pgHashUint32Extended(a, seed), pgHashUint32Extended(b, seed))
// for (a=1, b=1) and (a=1, b=3): rowHash%4 == 2 for (1,1) and rowHash%4 == 3
// for (1,3) — i.e. two DIFFERENT partitions despite sharing column a, exactly
// what a correct multi-column HASH fold must produce and what the pre-fix
// single-column code could never produce (it would always route both rows to
// whichever partition matches hash(a) alone).
func TestHashPartitionMultiColumnRouting(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE hp2 (a int, b int) PARTITION BY HASH (a, b)",
		"CREATE TABLE hp2_0 PARTITION OF hp2 FOR VALUES WITH (MODULUS 4, REMAINDER 0)",
		"CREATE TABLE hp2_1 PARTITION OF hp2 FOR VALUES WITH (MODULUS 4, REMAINDER 1)",
		"CREATE TABLE hp2_2 PARTITION OF hp2 FOR VALUES WITH (MODULUS 4, REMAINDER 2)",
		"CREATE TABLE hp2_3 PARTITION OF hp2 FOR VALUES WITH (MODULUS 4, REMAINDER 3)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	// (a=1, b=1) → satisfies_hash_partition(hp2, 4, 2, 1, 1) is true (remainder 2).
	if _, err := runQueryWithErr(ctx, "INSERT INTO hp2 VALUES (1, 1)"); err != nil {
		t.Fatalf("INSERT (1,1) should succeed: %v", err)
	}
	// (a=1, b=3) → satisfies_hash_partition(hp2, 4, 3, 1, 3) is true (remainder 3).
	if _, err := runQueryWithErr(ctx, "INSERT INTO hp2 VALUES (1, 3)"); err != nil {
		t.Fatalf("INSERT (1,3) should succeed: %v", err)
	}

	// Sanity: satisfies_hash_partition itself agrees with these expectations
	// (guards the test's own arithmetic, independent of routing).
	rowsA := runQueryRows(t, ctx, "SELECT satisfies_hash_partition('hp2'::regclass, 4, 2, 1, 1)")
	if len(rowsA) != 1 || rowsA[0][0].IsNull() || rowsA[0][0].Int == 0 {
		t.Fatalf("satisfies_hash_partition(hp2,4,2,1,1) = %v, want true", rowsA)
	}
	rowsB := runQueryRows(t, ctx, "SELECT satisfies_hash_partition('hp2'::regclass, 4, 3, 1, 3)")
	if len(rowsB) != 1 || rowsB[0][0].IsNull() || rowsB[0][0].Int == 0 {
		t.Fatalf("satisfies_hash_partition(hp2,4,3,1,3) = %v, want true", rowsB)
	}

	rows := runQuery(t, ctx, "SELECT tableoid::regclass::text, a, b FROM hp2 ORDER BY b")
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows visible through the parent, got %d", len(rows))
	}
	got0 := rows[0][0].StringValue()
	got1 := rows[1][0].StringValue()
	if got0 != "hp2_2" {
		t.Errorf("row (1,1): expected tableoid=hp2_2, got %q", got0)
	}
	if got1 != "hp2_3" {
		t.Errorf("row (1,3): expected tableoid=hp2_3, got %q", got1)
	}
	if got0 == got1 {
		t.Fatalf("rows sharing column a but differing in b landed in the SAME partition (%q) — column b was ignored by routing", got0)
	}

	// Direct scans of the children confirm the rows physically landed there.
	if childRows := runQuery(t, ctx, "SELECT a, b FROM hp2_2"); len(childRows) != 1 {
		t.Fatalf("expected 1 row in hp2_2 directly, got %d", len(childRows))
	}
	if childRows := runQuery(t, ctx, "SELECT a, b FROM hp2_3"); len(childRows) != 1 {
		t.Fatalf("expected 1 row in hp2_3 directly, got %d", len(childRows))
	}
}

// TestHashPartitionSingleColumnRoutingRegression is a regression guard: the
// original single-column HASH routing path (still the common case) must keep
// working after the multi-column fix. M0134-0053.
func TestHashPartitionSingleColumnRoutingRegression(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE hp1 (a int) PARTITION BY HASH (a)",
		"CREATE TABLE hp1_0 PARTITION OF hp1 FOR VALUES WITH (MODULUS 4, REMAINDER 0)",
		"CREATE TABLE hp1_1 PARTITION OF hp1 FOR VALUES WITH (MODULUS 4, REMAINDER 1)",
		"CREATE TABLE hp1_2 PARTITION OF hp1 FOR VALUES WITH (MODULUS 4, REMAINDER 2)",
		"CREATE TABLE hp1_3 PARTITION OF hp1 FOR VALUES WITH (MODULUS 4, REMAINDER 3)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	for _, v := range []int{1, 5, 42, 99, 7} {
		sql := fmt.Sprintf("INSERT INTO hp1 VALUES (%d)", v)
		if _, err := runQueryWithErr(ctx, sql); err != nil {
			t.Fatalf("INSERT (%d) should succeed: %v", v, err)
		}
	}

	rows := runQuery(t, ctx, "SELECT a FROM hp1")
	if len(rows) != 5 {
		t.Fatalf("expected all 5 rows to land somewhere under hp1, got %d", len(rows))
	}
}
