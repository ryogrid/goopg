package executor

import (
	"strings"
	"testing"
)

// TestAttachPartitionRejectsDefaultConflict verifies that ALTER TABLE … ATTACH
// PARTITION rejects the attach when the parent's existing DEFAULT partition
// already holds rows that the new partition would claim — PostgreSQL's
// ATExecAttachPartition → check_default_partition_contents (SQLSTATE 23P01).
//
// Before this fix the default-conflict check ran only on the CREATE TABLE …
// PARTITION OF path (validatePartitionChild); ALTER TABLE … ATTACH PARTITION
// silently allowed the attach, leaving rows in the default that violate its
// updated partition constraint. This is the standalone, non-concurrent core of
// the partition-concurrent-attach isolation spec (M0118-0008).
func TestAttachPartitionRejectsDefaultConflict(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE tp (i int, j text) PARTITION BY RANGE (i)",
		"CREATE TABLE tp_def PARTITION OF tp DEFAULT",
		"CREATE TABLE tp_2 (LIKE tp)",
		// A row that lives in the default and falls in [100,200) — the range the
		// new partition will claim.
		"INSERT INTO tp VALUES (110, 'xxx')",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	err := runDDL(t, ctx, "ALTER TABLE tp ATTACH PARTITION tp_2 FOR VALUES FROM (100) TO (200)")
	if err == nil {
		t.Fatalf("ATTACH PARTITION over a conflicting default row should fail, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type = %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "23P01" {
		t.Fatalf("SQLSTATE = %q, want 23P01: %v", ee.Code, ee)
	}
	if !strings.Contains(ee.Message, `default partition "tp_def"`) {
		t.Fatalf("message = %q, want it to name default partition tp_def", ee.Message)
	}
}

// TestAttachPartitionNoConflictSucceeds is the negative control: when the
// default holds no row the new partition would claim, the attach succeeds.
func TestAttachPartitionNoConflictSucceeds(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE tp (i int, j text) PARTITION BY RANGE (i)",
		"CREATE TABLE tp_def PARTITION OF tp DEFAULT",
		"CREATE TABLE tp_2 (LIKE tp)",
		// A row outside [100,200) stays valid in the default after the attach.
		"INSERT INTO tp VALUES (250, 'zzz')",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	if err := runDDL(t, ctx, "ALTER TABLE tp ATTACH PARTITION tp_2 FOR VALUES FROM (100) TO (200)"); err != nil {
		t.Fatalf("ATTACH PARTITION with no default conflict should succeed: %v", err)
	}
}

// TestAttachPartitionNestedDefaultNamesLeaf mirrors the partition-concurrent-attach
// spec's sub-partitioned default: tpart_default is itself partitioned and its own
// default (tpart_default_default) holds the offending rows. PostgreSQL names the
// LEAF default partition in the error, not the intermediate one.
func TestAttachPartitionNestedDefaultNamesLeaf(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE tpart (i int, j text) PARTITION BY RANGE (i)",
		"CREATE TABLE tpart_2 (LIKE tpart)",
		"CREATE TABLE tpart_default (j text, i int) PARTITION BY LIST (j)",
		"CREATE TABLE tpart_default_default (i int, j text)",
		"ALTER TABLE tpart_default ATTACH PARTITION tpart_default_default DEFAULT",
		"ALTER TABLE tpart ATTACH PARTITION tpart_default DEFAULT",
		"ALTER TABLE tpart ATTACH PARTITION tpart_1 FOR VALUES FROM (0) TO (100)",
		// Row routes through tpart_default to the leaf tpart_default_default.
		"INSERT INTO tpart VALUES (110, 'xxx')",
	} {
		if s == "ALTER TABLE tpart ATTACH PARTITION tpart_1 FOR VALUES FROM (0) TO (100)" {
			// tpart_1 was never created above; create it just-in-time.
			if err := runDDL(t, ctx, "CREATE TABLE tpart_1 (LIKE tpart)"); err != nil {
				t.Fatalf("create tpart_1: %v", err)
			}
		}
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}

	err := runDDL(t, ctx, "ALTER TABLE tpart ATTACH PARTITION tpart_2 FOR VALUES FROM (100) TO (200)")
	if err == nil {
		t.Fatalf("ATTACH over nested-default conflict should fail, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type = %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "23P01" {
		t.Fatalf("SQLSTATE = %q, want 23P01: %v", ee.Code, ee)
	}
	if !strings.Contains(ee.Message, `default partition "tpart_default_default"`) {
		t.Fatalf("message = %q, want it to name the LEAF default tpart_default_default", ee.Message)
	}
}
