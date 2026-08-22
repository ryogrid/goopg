package executor

import (
	"testing"
)

// TestDefaultPartitionInsertConstraint verifies that a row inserted into (or
// routed to) a DEFAULT partition is rejected with SQLSTATE 23514 when the row's
// partition-key value belongs to a non-default sibling partition — PostgreSQL's
// implicit default-partition constraint enforced by ExecPartitionCheck.
// This is the standalone, non-concurrent core of the partition-concurrent-attach
// piece (c); design 0118-0078.
func TestDefaultPartitionInsertConstraint(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE rp (i int, j text) PARTITION BY RANGE (i)",
		"CREATE TABLE rp_1 PARTITION OF rp FOR VALUES FROM (0) TO (100)",
		"CREATE TABLE rp_def PARTITION OF rp DEFAULT",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	// Value 500 is owned by no non-default sibling → routed insert into the
	// default succeeds.
	if _, err := runQueryWithErr(ctx, "INSERT INTO rp VALUES (500, 'a')"); err != nil {
		t.Fatalf("routed insert of a default-owned value should succeed: %v", err)
	}

	// Direct insert into the default partition of a value owned by rp_1 [0,100)
	// must fail with 23514, naming the default partition.
	_, err := runQueryWithErr(ctx, "INSERT INTO rp_def VALUES (50, 'b')")
	if err == nil {
		t.Fatal("direct insert into default of a sibling-owned value should fail")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23514" {
		t.Fatalf("expected 23514 partition constraint violation, got %v", err)
	}

	// After detaching the sibling that owns 50, the same row becomes valid.
	if err := runDDL(t, ctx, "ALTER TABLE rp DETACH PARTITION rp_1"); err != nil {
		t.Fatalf("detach rp_1: %v", err)
	}
	if _, err := runQueryWithErr(ctx, "INSERT INTO rp_def VALUES (50, 'b')"); err != nil {
		t.Fatalf("after detach, value 50 belongs to the default and should insert: %v", err)
	}
}

// TestDefaultPartitionConstraintDetail verifies the ERROR carries PostgreSQL's
// "Failing row contains (...)" DETAIL clause alongside the partition-constraint
// violation, mirroring execMain.c's ExecConstraints callers, which build
// val_desc via ExecBuildSlotValueDescription and attach it via errdetail().
// M0134-0054 bucket 1.
func TestDefaultPartitionConstraintDetail(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE rpd (i int, j text) PARTITION BY RANGE (i)",
		"CREATE TABLE rpd_1 PARTITION OF rpd FOR VALUES FROM (0) TO (100)",
		"CREATE TABLE rpd_def PARTITION OF rpd DEFAULT",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	_, err := runQueryWithErr(ctx, "INSERT INTO rpd_def VALUES (50, 'b')")
	if err == nil {
		t.Fatal("direct insert into default of a sibling-owned value should fail")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23514" {
		t.Fatalf("expected 23514 partition constraint violation, got %v", err)
	}
	const want = "Failing row contains (50, b)."
	if ee.Detail != want {
		t.Fatalf("Detail = %q, want %q", ee.Detail, want)
	}
}

// TestDefaultPartitionConstraintSubPartitioned exercises the multi-level walk-up:
// the default partition is itself sub-partitioned, so the violating ancestor is
// two levels above the routed leaf (mirrors the partition-concurrent-attach spec's
// tpart → tpart_default → tpart_default_default shape). Design 0118-0078.
func TestDefaultPartitionConstraintSubPartitioned(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE tp (i int, j text) PARTITION BY RANGE (i)",
		"CREATE TABLE tp_1 PARTITION OF tp FOR VALUES FROM (0) TO (100)",
		"CREATE TABLE tp_def (i int, j text) PARTITION BY LIST (j)",
		"CREATE TABLE tp_def_def PARTITION OF tp_def DEFAULT",
		"ALTER TABLE tp ATTACH PARTITION tp_def DEFAULT",
		"CREATE TABLE tp_2 (i int, j text)",
		"ALTER TABLE tp ATTACH PARTITION tp_2 FOR VALUES FROM (100) TO (200)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	// Direct insert into the intermediate default tp_def of i=150 (owned by tp_2
	// [100,200)) must fail, naming tp_def — the violation is detected two levels
	// above the routed leaf tp_def_def.
	_, err := runQueryWithErr(ctx, "INSERT INTO tp_def (i, j) VALUES (150, 'x')")
	if err == nil {
		t.Fatal("direct insert into sub-partitioned default of a sibling-owned value should fail")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23514" {
		t.Fatalf("expected 23514 partition constraint violation, got %v", err)
	}

	// A value owned by no non-default sibling (i=500) routes through the default
	// subtree and succeeds.
	if _, err := runQueryWithErr(ctx, "INSERT INTO tp (i, j) VALUES (500, 'y')"); err != nil {
		t.Fatalf("routed insert of a default-owned value should succeed: %v", err)
	}
}
