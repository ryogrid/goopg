package testport

// TestPort_NotNullDiamondConinhcount and its siblings guard M0134-0005p:
// the `coninhcount`/`conislocal`/`convalidated` counter ARITHMETIC on a
// pg_constraint contype='n' row, distinct from 0005i's mere presence/absence
// of the cascade. Four defects fixed here (report.md §2, brief §Scope):
//   - A: `ALTER TABLE ... SET NOT NULL` on a column that already carries a
//     not-null constraint must merge in place (conislocal / validate) and
//     NOT recurse to children a second time
//     (postgres/src/backend/commands/tablecmds.c:7913 ATExecSetNotNull,
//     existing-constraint branch :7950-8010).
//   - B1: a cascaded child constraint must inherit the SOURCE constraint's
//     NotValid instead of always convalidated=true
//     (postgres/src/backend/catalog/heap.c:2385 AddRelationNewConstraints).
//   - B2: a diamond-inherited descendant must get coninhcount incremented
//     once PER DISTINCT PARENT EDGE, not once total
//     (postgres/src/backend/catalog/pg_constraint.c:742
//     AdjustNotNullInheritance).
//   - C: a freshly created `PARTITION OF` child always cooks
//     convalidated=true regardless of the parent's own NOT VALID state.

import (
	"testing"
)

// TestPort_NotNullDiamondConinhcount is acceptance criterion 1: a 3-level
// INHERITS diamond (`grand INHERITS (parent, child)`, `child INHERITS
// (parent)`) — SET NOT NULL on parent must leave grand's coninhcount at 2
// (one increment per distinct parent edge: parent->grand and child->grand),
// not 1.
func TestPort_NotNullDiamondConinhcount(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-diamond-coninhcount")

	for _, stmt := range []string{
		"CREATE TABLE dparent (a int, b int)",
		"CREATE TABLE dchild (a int, b int) INHERITS (dparent)",
		"CREATE TABLE dgrand () INHERITS (dparent, dchild)",
		"ALTER TABLE dparent ALTER COLUMN a SET NOT NULL",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if got := queryScalar(t, c,
		"SELECT coninhcount FROM pg_constraint WHERE contype = 'n' AND conrelid = 'dgrand'::regclass"); got != "2" {
		t.Fatalf("dgrand's NOT NULL coninhcount = %q, want 2 (diamond: one increment per distinct parent edge)", got)
	}
}

// TestPort_NotNullSetNotNullOnExistingDoesNotDoubleCascade is acceptance
// criterion 2: when a child already has the constraint (from an earlier
// cascade) and the parent's SET NOT NULL runs again, the child's
// coninhcount must NOT be bumped a second time, and an already-local NOT
// VALID constraint flips to validated (convalidated=t) instead of being
// left alone.
func TestPort_NotNullSetNotNullOnExistingDoesNotDoubleCascade(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-setnn-no-doublecascade")

	for _, stmt := range []string{
		"CREATE TABLE ep (a int, b int)",
		"CREATE TABLE ec () INHERITS (ep)",
		"ALTER TABLE ep ALTER COLUMN a SET NOT NULL",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	if got := queryScalar(t, c,
		"SELECT coninhcount FROM pg_constraint WHERE contype = 'n' AND conrelid = 'ec'::regclass"); got != "1" {
		t.Fatalf("ec's NOT NULL coninhcount after first SET NOT NULL = %q, want 1", got)
	}

	// Re-running SET NOT NULL on the parent (idempotent, merges into the
	// already-local constraint) must not re-cascade to the child.
	if err := runSQLSimple(t, c, "ALTER TABLE ep ALTER COLUMN a SET NOT NULL"); err != nil {
		t.Fatalf("second SET NOT NULL on ep: %v", err)
	}
	if got := queryScalar(t, c,
		"SELECT coninhcount FROM pg_constraint WHERE contype = 'n' AND conrelid = 'ec'::regclass"); got != "1" {
		t.Fatalf("ec's NOT NULL coninhcount after SECOND parent SET NOT NULL = %q, want still 1 (must not double-cascade)", got)
	}

	// Now the local-NOT VALID -> validate branch: an already-local NOT VALID
	// constraint on ep itself flips convalidated true on a repeat SET NOT
	// NULL, without touching ec.
	for _, stmt := range []string{
		"CREATE TABLE nv (x int)",
		"ALTER TABLE nv ADD CONSTRAINT nv_x_nn NOT NULL x NOT VALID",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	if got := queryScalar(t, c,
		"SELECT convalidated FROM pg_constraint WHERE conname = 'nv_x_nn' AND conrelid = 'nv'::regclass"); got != "false" {
		t.Fatalf("nv_x_nn convalidated before SET NOT NULL = %q, want false", got)
	}
	if err := runSQLSimple(t, c, "ALTER TABLE nv ALTER COLUMN x SET NOT NULL"); err != nil {
		t.Fatalf("ALTER TABLE nv ALTER COLUMN x SET NOT NULL (validate existing local NOT VALID): %v", err)
	}
	if got := queryScalar(t, c,
		"SELECT convalidated FROM pg_constraint WHERE conname = 'nv_x_nn' AND conrelid = 'nv'::regclass"); got != "true" {
		t.Fatalf("nv_x_nn convalidated after SET NOT NULL = %q, want true (validate-in-place branch)", got)
	}
}

// TestPort_NotNullAddConstraintNotValidCascadesNotValid is acceptance
// criterion 3: `ADD CONSTRAINT ... NOT NULL ... NOT VALID` on a parent with
// an existing child propagates convalidated=f to the CHILD's newly created
// row too (not hardcoded true).
func TestPort_NotNullAddConstraintNotValidCascadesNotValid(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-addconstraint-notvalid-cascade")

	for _, stmt := range []string{
		"CREATE TABLE vp (a int, b int)",
		"CREATE TABLE vc () INHERITS (vp)",
		"ALTER TABLE vp ADD CONSTRAINT vp_a_nn NOT NULL a NOT VALID",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if got := queryScalar(t, c,
		"SELECT convalidated FROM pg_constraint WHERE contype = 'n' AND conrelid = 'vc'::regclass"); got != "false" {
		t.Fatalf("vc's cascaded NOT NULL convalidated = %q, want false (NOT VALID must propagate to the child row)", got)
	}
}

// TestPort_NotNullPartitionOfAlwaysValidated is acceptance criterion 4: a
// freshly created `PARTITION OF` child always gets convalidated=t on the
// inherited-column not-null row, even when the parent's own constraint is
// NOT VALID (a new empty partition has no rows to violate it).
func TestPort_NotNullPartitionOfAlwaysValidated(t *testing.T) {
	c := startNotNullCascadeCluster(t, "notnull-partitionof-validated")

	for _, stmt := range []string{
		"CREATE TABLE pp (a int, b int) PARTITION BY LIST (a)",
		"ALTER TABLE pp ADD CONSTRAINT pp_b_nn NOT NULL b NOT VALID",
		"CREATE TABLE pp1 PARTITION OF pp FOR VALUES IN (1)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if got := queryScalar(t, c,
		"SELECT convalidated FROM pg_constraint WHERE contype = 'n' AND conrelid = 'pp1'::regclass AND conname = 'pp_b_nn'"); got != "true" {
		t.Fatalf("pp1's inherited NOT NULL (from parent's NOT VALID constraint) convalidated = %q, want true (a fresh empty partition is always cooked validated)", got)
	}
}
