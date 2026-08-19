package executor

import (
	"testing"
)

// TestListPartitionNumericRouting is a targeted regression test for M0134-0012c.
//
// routeToPartitionDepth's "LIST" arm used to stringify the routing key with a
// closed if/else chain covering only KindInt/KindString/KindBool/null, with no
// default arm. A `numeric` (or float/date/uuid/…) LIST key therefore always
// stringified to "", never matched any bound, and — with no DEFAULT partition —
// every insert raised 23514 "no partition of relation ... found for row", even
// though the value plainly matched an attached bound.
//
// Fix: the LIST arm now calls the already-correct partitionKeyDatumToListStr
// helper (previously only used by routePartitionKeyToImmediateChild), which
// falls back to Datum.Format() for kinds it doesn't special-case.
func TestListPartitionNumericRouting(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE list_parted (a numeric, b int, c int8) PARTITION BY LIST (a)",
		"CREATE TABLE list_part1 (a numeric, b int, c int8)",
		"ALTER TABLE list_parted ATTACH PARTITION list_part1 FOR VALUES IN (2,3)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	if _, err := runQueryWithErr(ctx, "INSERT INTO list_parted VALUES (2,5,50)"); err != nil {
		t.Fatalf("INSERT of a numeric LIST key matching an attached bound should succeed: %v", err)
	}

	rows := runQuery(t, ctx, "SELECT tableoid::regclass::text, a, b, c FROM list_parted")
	if len(rows) != 1 {
		t.Fatalf("expected 1 row visible through the parent, got %d", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "list_part1" {
		t.Fatalf("expected row to land in list_part1, got tableoid=%q", got)
	}

	// Direct scan of the child confirms the row physically landed there, not
	// just that tableoid resolved correctly.
	childRows := runQuery(t, ctx, "SELECT a, b, c FROM list_part1")
	if len(childRows) != 1 {
		t.Fatalf("expected 1 row in list_part1 directly, got %d", len(childRows))
	}
}

// TestListPartitionNumericMultiValueBound guards the refuted-but-plausible
// theory that FOR VALUES IN (2,3) only honours the first listed value. Both 2
// and 3 must route to list_part1.
func TestListPartitionNumericMultiValueBound(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE mvlist_parted (a numeric, b int) PARTITION BY LIST (a)",
		"CREATE TABLE mvlist_part1 (a numeric, b int)",
		"ALTER TABLE mvlist_parted ATTACH PARTITION mvlist_part1 FOR VALUES IN (2,3)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	if _, err := runQueryWithErr(ctx, "INSERT INTO mvlist_parted VALUES (2,1)"); err != nil {
		t.Fatalf("INSERT of first bound value (2) should succeed: %v", err)
	}
	if _, err := runQueryWithErr(ctx, "INSERT INTO mvlist_parted VALUES (3,2)"); err != nil {
		t.Fatalf("INSERT of second bound value (3) should succeed: %v", err)
	}

	rows := runQuery(t, ctx, "SELECT a, b FROM mvlist_part1")
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows in mvlist_part1 (both bound values), got %d", len(rows))
	}
}

// TestListPartitionNumericNoMatchingBoundErrors is the negative control: a
// numeric value matching no attached bound, with no DEFAULT partition, must
// still raise 23514 "no partition of relation ... found for row". Without
// this, the fix could turn a real rejection into silent misrouting.
func TestListPartitionNumericNoMatchingBoundErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE nolist_parted (a numeric, b int) PARTITION BY LIST (a)",
		"CREATE TABLE nolist_part1 (a numeric, b int)",
		"ALTER TABLE nolist_parted ATTACH PARTITION nolist_part1 FOR VALUES IN (2,3)",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	_, err := runQueryWithErr(ctx, "INSERT INTO nolist_parted VALUES (99,1)")
	if err == nil {
		t.Fatal("INSERT of a value matching no bound and no DEFAULT partition should fail")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23514" {
		t.Fatalf("expected 23514 no-partition-found error, got %v", err)
	}
}

// TestListPartitionIntStringBoolRegression is a regression guard: existing
// int / text / bool LIST routing (including the short-boolean "t"/"f"
// spelling) must keep working now that the LIST arm delegates to
// partitionKeyDatumToListStr instead of its own if/else chain.
func TestListPartitionIntStringBoolRegression(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE ilist_parted (a int, b text) PARTITION BY LIST (a)",
		"CREATE TABLE ilist_part1 (a int, b text)",
		"ALTER TABLE ilist_parted ATTACH PARTITION ilist_part1 FOR VALUES IN (7,8)",
		"CREATE TABLE slist_parted (a text, b int) PARTITION BY LIST (a)",
		"CREATE TABLE slist_part1 (a text, b int)",
		"ALTER TABLE slist_parted ATTACH PARTITION slist_part1 FOR VALUES IN ('x','y')",
		"CREATE TABLE blist_parted (a bool, b int) PARTITION BY LIST (a)",
		"CREATE TABLE blist_part1 (a bool, b int)",
		"ALTER TABLE blist_parted ATTACH PARTITION blist_part1 FOR VALUES IN ('t')",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	if _, err := runQueryWithErr(ctx, "INSERT INTO ilist_parted VALUES (7,'a')"); err != nil {
		t.Fatalf("int LIST routing regressed: %v", err)
	}
	if _, err := runQueryWithErr(ctx, "INSERT INTO slist_parted VALUES ('x',1)"); err != nil {
		t.Fatalf("text LIST routing regressed: %v", err)
	}
	if _, err := runQueryWithErr(ctx, "INSERT INTO blist_parted VALUES (true,1)"); err != nil {
		t.Fatalf("bool LIST routing (short-boolean bound spelling) regressed: %v", err)
	}

	if rows := runQuery(t, ctx, "SELECT a FROM ilist_part1"); len(rows) != 1 {
		t.Fatalf("expected 1 row in ilist_part1, got %d", len(rows))
	}
	if rows := runQuery(t, ctx, "SELECT a FROM slist_part1"); len(rows) != 1 {
		t.Fatalf("expected 1 row in slist_part1, got %d", len(rows))
	}
	if rows := runQuery(t, ctx, "SELECT a FROM blist_part1"); len(rows) != 1 {
		t.Fatalf("expected 1 row in blist_part1, got %d", len(rows))
	}
}
