package executor

// pg_get_partition_constraintdef_test.go — pins pg_get_partition_constraintdef
// (regclass) → text (M0134-0005ag), the `\d+` builtin regress `constraints.sql`
// hunk #13 (notnull_tbl6_1) needs. Design 0134-0005ag.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// scalarText evaluates a single-row, single-column SELECT and returns its
// text form, or ("", true) if the value is SQL NULL.
func scalarText(t *testing.T, ctx *Context, sql string) (string, bool) {
	t.Helper()
	rows := runQueryRows(t, ctx, sql)
	if len(rows) != 1 {
		t.Fatalf("query %q: row count = %d, want 1", sql, len(rows))
	}
	if rows[0][0].IsNull() {
		return "", true
	}
	return rows[0][0].StringValue(), false
}

// TestPgGetPartitionConstraintdefListSingleValue pins the exact target case
// from constraints.sql (notnull_tbl6/notnull_tbl6_1, expected/constraints.out
// :1367-1368): a single-value LIST partition's constraint deparses as
// `((a IS NOT NULL) AND (a = 1))`, byte-for-byte, double-paren included.
func TestPgGetPartitionConstraintdefListSingleValue(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE notnull_tbl6 (a int, b int) PARTITION BY LIST (a)",
		"CREATE TABLE notnull_tbl6_1 PARTITION OF notnull_tbl6 FOR VALUES IN (1)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	got, isNull := scalarText(t, ctx, "SELECT pg_get_partition_constraintdef('notnull_tbl6_1'::regclass)")
	if isNull {
		t.Fatalf("got NULL, want a constraint def string")
	}
	want := "((a IS NOT NULL) AND (a = 1))"
	if got != want {
		t.Errorf("pg_get_partition_constraintdef = %q, want %q", got, want)
	}
}

// TestPgGetPartitionConstraintdefNonPartition pins the not-a-partition
// contract: a plain (non-partitioned, non-partition) relation returns SQL
// NULL, never an error and never an empty string (get_partition_qual_relid,
// partcache.c:299 — guards on !get_rel_relispartition).
func TestPgGetPartitionConstraintdefNonPartition(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE plain_tbl (a int)"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, isNull := scalarText(t, ctx, "SELECT pg_get_partition_constraintdef('plain_tbl'::regclass)")
	if !isNull {
		t.Errorf("pg_get_partition_constraintdef on a non-partition relation: want NULL")
	}
}

// TestPgGetPartitionConstraintdefUnknownOID pins the unknown-OID contract:
// an OID that resolves to no relation at all returns NULL, matching
// pg_get_constraintdef's existing miss behavior (never an error).
func TestPgGetPartitionConstraintdefUnknownOID(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	_, isNull := scalarText(t, ctx, "SELECT pg_get_partition_constraintdef(999999999)")
	if !isNull {
		t.Errorf("pg_get_partition_constraintdef on an unknown OID: want NULL")
	}
}

// TestPgGetPartitionConstraintdefDefaultPartition pins the DEFAULT-partition
// deferral: PG's real answer is a negation over every sibling partition's own
// bound (get_qual_for_list, partbounds.c:4099-4225), which this slice does not
// attempt. Returning NULL (rather than a wrong-but-plausible qual) is
// deliberate per the design's return contract.
func TestPgGetPartitionConstraintdefDefaultPartition(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE dp (a int) PARTITION BY LIST (a)",
		"CREATE TABLE dp_1 PARTITION OF dp FOR VALUES IN (1)",
		"CREATE TABLE dp_def PARTITION OF dp DEFAULT",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	_, isNull := scalarText(t, ctx, "SELECT pg_get_partition_constraintdef('dp_def'::regclass)")
	if !isNull {
		t.Errorf("pg_get_partition_constraintdef on a DEFAULT partition: want NULL (deferred)")
	}
}

// TestPgGetPartitionConstraintdefRangeSingleColumn pins the RANGE strategy's
// common single-column-key shape: `(a IS NOT NULL) AND (a >= 0) AND (a < 100)`,
// following get_qual_for_range (partbounds.c:4274).
func TestPgGetPartitionConstraintdefRangeSingleColumn(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE rp (a int) PARTITION BY RANGE (a)",
		"CREATE TABLE rp_1 PARTITION OF rp FOR VALUES FROM (0) TO (100)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	got, isNull := scalarText(t, ctx, "SELECT pg_get_partition_constraintdef('rp_1'::regclass)")
	if isNull {
		t.Fatalf("got NULL, want a constraint def string")
	}
	want := "((a IS NOT NULL) AND (a >= 0) AND (a < 100))"
	if got != want {
		t.Errorf("pg_get_partition_constraintdef = %q, want %q", got, want)
	}
}

// TestPgGetPartitionConstraintdefHashSingleColumn pins the HASH strategy's
// rendered form: a bare call to the built-in satisfies_hash_partition,
// following get_qual_for_hash (partbounds.c:3982).
func TestPgGetPartitionConstraintdefHashSingleColumn(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE hp (a int) PARTITION BY HASH (a)",
		"CREATE TABLE hp_1 PARTITION OF hp FOR VALUES WITH (MODULUS 4, REMAINDER 0)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "hp"})
	if !ok {
		t.Fatalf("table %q not found", "hp")
	}
	parentOID := tbl.OID

	got, isNull := scalarText(t, ctx, "SELECT pg_get_partition_constraintdef('hp_1'::regclass)")
	if isNull {
		t.Fatalf("got NULL, want a constraint def string")
	}
	want := fmt.Sprintf("satisfies_hash_partition(%d, 4, 0, a)", parentOID)
	if got != want {
		t.Errorf("pg_get_partition_constraintdef = %q, want %q", got, want)
	}
}

// TestPgGetPartitionConstraintdefListMultiValue pins the multi-value LIST
// `= ANY (ARRAY[...])` form (get_qual_for_list's make_partition_op_expr,
// partbounds.c:3868, nelems > 1 branch).
func TestPgGetPartitionConstraintdefListMultiValue(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE lp (a int) PARTITION BY LIST (a)",
		"CREATE TABLE lp_1 PARTITION OF lp FOR VALUES IN (1, 2, 3)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	got, isNull := scalarText(t, ctx, "SELECT pg_get_partition_constraintdef('lp_1'::regclass)")
	if isNull {
		t.Fatalf("got NULL, want a constraint def string")
	}
	want := "((a IS NOT NULL) AND (a = ANY (ARRAY[1, 2, 3])))"
	if got != want {
		t.Errorf("pg_get_partition_constraintdef = %q, want %q", got, want)
	}
}
