package executor

// operators_vacuum_analyze_permission_test.go pins M0134-0021b: VACUUM /
// ANALYZE of a partitioned table run by a role that owns neither the parent
// nor (some of) the children must emit ONE
// `permission denied to vacuum/analyze "<rel>", skipping it` WARNING per
// skipped relation — the parent AND each expanded partition child
// independently — while still processing every relation the caller IS
// permitted to touch. Before this fix, maintenancePermitted() was checked
// only against the explicitly-named relation in expandVacuumTargets /
// expandAnalyzeTargets, so every expanded partition child was
// vacuumed/analyzed unconditionally and its WARNING never fired
// (postgres/src/test/regress/expected/vacuum.out:593-684). PG's
// expand_vacuum_rel() deliberately skips the ownership check at expansion
// time (vacuum.c:1003-1005); the check happens later, once per flattened
// target, in vacuum_rel() / analyze_rel(). Design doc:
// docs/design/m0134-0021-vacuum-partition-child-permission.md.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// setupVacuumPermissionPartitions creates a partitioned parent "vparent"
// with two leaf children "vchild1" (a in [0,100)) and "vchild2" (a in
// [100,200)), all created (and therefore owned, per
// TestCreateTablePartitionOfStampsCreatingRoleAsOwner) by ownerRole.
func setupVacuumPermissionPartitions(t *testing.T, ctx *Context, cat catalog.Catalog, ownerRole string) (parent, child1, child2 *catalog.Table) {
	t.Helper()
	ctx.NonSuperuserRole = ownerRole
	if err := runDDL(t, ctx, "CREATE TABLE vparent (a int) PARTITION BY RANGE (a)"); err != nil {
		t.Fatalf("CREATE TABLE vparent PARTITION BY: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE vchild1 PARTITION OF vparent FOR VALUES FROM (0) TO (100)"); err != nil {
		t.Fatalf("CREATE TABLE vchild1 PARTITION OF: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE vchild2 PARTITION OF vparent FOR VALUES FROM (100) TO (200)"); err != nil {
		t.Fatalf("CREATE TABLE vchild2 PARTITION OF: %v", err)
	}
	parent, ok := cat.LookupTable(parser.ObjectName{Name: "vparent"})
	if !ok {
		t.Fatalf("catalog lost vparent")
	}
	child1, ok = cat.LookupTable(parser.ObjectName{Name: "vchild1"})
	if !ok {
		t.Fatalf("catalog lost vchild1")
	}
	child2, ok = cat.LookupTable(parser.ObjectName{Name: "vchild2"})
	if !ok {
		t.Fatalf("catalog lost vchild2")
	}
	if parent.Owner != ownerRole || child1.Owner != ownerRole || child2.Owner != ownerRole {
		t.Fatalf("setup: owners = %q/%q/%q, want all %q", parent.Owner, child1.Owner, child2.Owner, ownerRole)
	}
	return parent, child1, child2
}

// TestVacuumPartitionChildDeniedEmitsWarningPerRelation covers the fully-
// denied case: a caller that owns neither the parent nor either child gets
// exactly THREE warnings, one per relation, parent first then children in
// expansion order, with PG's exact wording. Confirmed FAILING before the
// M0134-0021b fix (only the explicitly-named "vparent" warned; both children
// were silently vacuumed).
func TestVacuumPartitionChildDeniedEmitsWarningPerRelation(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	im.RegisterRole("owner1")
	im.RegisterRole("outsider")

	setupVacuumPermissionPartitions(t, ctx, cat, "owner1")

	ctx.NonSuperuserRole = "outsider"
	ctx.Warnings = nil
	if err := runDDL(t, ctx, "VACUUM vparent"); err != nil {
		t.Fatalf("VACUUM vparent: %v", err)
	}
	want := []string{
		`permission denied to vacuum "vparent", skipping it`,
		`permission denied to vacuum "vchild1", skipping it`,
		`permission denied to vacuum "vchild2", skipping it`,
	}
	assertWarningsEqual(t, ctx.Warnings, want)

	ctx.Warnings = nil
	if err := runDDL(t, ctx, "ANALYZE vparent"); err != nil {
		t.Fatalf("ANALYZE vparent: %v", err)
	}
	wantAnalyze := []string{
		`permission denied to analyze "vparent", skipping it`,
		`permission denied to analyze "vchild1", skipping it`,
		`permission denied to analyze "vchild2", skipping it`,
	}
	assertWarningsEqual(t, ctx.Warnings, wantAnalyze)
}

// TestVacuumPartitionChildMixedOwnershipSkipsOnlyDenied covers the mixed-
// ownership case: the caller owns the parent and vchild1 but not vchild2 —
// exactly ONE warning fires (for vchild2), and the parent + vchild1 are
// actually processed (not skipped alongside it).
func TestVacuumPartitionChildMixedOwnershipSkipsOnlyDenied(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	im.RegisterRole("owner1")
	im.RegisterRole("other")

	parent, child1, child2 := setupVacuumPermissionPartitions(t, ctx, cat, "owner1")
	// Reassign vchild2's ownership away from owner1 so owner1 no longer owns
	// it — mirrors ALTER TABLE vchild2 OWNER TO other, done directly on the
	// catalog handle for test brevity.
	child2.Owner = "other"

	ctx.NonSuperuserRole = "owner1"
	ctx.Warnings = nil
	if err := runDDL(t, ctx, "VACUUM vparent"); err != nil {
		t.Fatalf("VACUUM vparent: %v", err)
	}
	assertWarningsEqual(t, ctx.Warnings, []string{
		`permission denied to vacuum "vchild2", skipping it`,
	})

	ctx.Warnings = nil
	if err := runDDL(t, ctx, "ANALYZE vparent"); err != nil {
		t.Fatalf("ANALYZE vparent: %v", err)
	}
	assertWarningsEqual(t, ctx.Warnings, []string{
		`permission denied to analyze "vchild2", skipping it`,
	})

	// vchild2 was skipped: ANALYZE never touched its Stats.
	if child2.Stats != nil && child2.Stats.Analyzed {
		t.Fatalf("vchild2 was ANALYZEd despite the permission denial — Stats=%+v", child2.Stats)
	}
	// The parent and vchild1 — both permitted — WERE processed.
	if child1.Stats == nil || !child1.Stats.Analyzed {
		t.Fatalf("vchild1 (owned by owner1) was NOT analyzed alongside the denied sibling — Stats=%+v", child1.Stats)
	}
	if parent.PartitionMethod == "" {
		t.Fatalf("setup regression: vparent lost its PartitionMethod")
	}
}

// TestVacuumExplicitTargetGetsExactlyOneWarning guards against the loop
// check double-firing alongside the pre-existing explicit-target check: a
// single, non-partitioned relation named directly and denied must produce
// exactly one warning, not two.
func TestVacuumExplicitTargetGetsExactlyOneWarning(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	im.RegisterRole("owner1")
	im.RegisterRole("outsider")

	ctx.NonSuperuserRole = "owner1"
	if err := runDDL(t, ctx, "CREATE TABLE plaintbl (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "plaintbl"})
	if !ok {
		t.Fatalf("catalog lost plaintbl")
	}
	if tbl.Owner != "owner1" {
		t.Fatalf("Owner = %q, want owner1", tbl.Owner)
	}

	ctx.NonSuperuserRole = "outsider"
	ctx.Warnings = nil
	if err := runDDL(t, ctx, "VACUUM plaintbl"); err != nil {
		t.Fatalf("VACUUM plaintbl: %v", err)
	}
	assertWarningsEqual(t, ctx.Warnings, []string{
		`permission denied to vacuum "plaintbl", skipping it`,
	})

	ctx.Warnings = nil
	if err := runDDL(t, ctx, "ANALYZE plaintbl"); err != nil {
		t.Fatalf("ANALYZE plaintbl: %v", err)
	}
	assertWarningsEqual(t, ctx.Warnings, []string{
		`permission denied to analyze "plaintbl", skipping it`,
	})
}

func assertWarningsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("warnings = %#v (len %d), want %#v (len %d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("warnings[%d] = %q, want %q (full: got=%#v want=%#v)", i, got[i], want[i], got, want)
		}
	}
}
