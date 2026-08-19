package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0134-0005ar: AddCheckInherited (internal/catalog/catalog.go) never carried
// a parent CHECK constraint's NotValid/NotEnforced flags onto
// inherited/partition children, and the three call sites (execCreateTable's
// INHERITS loop, execCreatePartitionChild, AlterTableAddCheck's partition
// cascade) follow an ASYMMETRIC PG rule that must not be "symmetrized":
//
//   - CREATE-time (INHERITS / PARTITION OF): MergeCheckConstraint
//     (tablecmds.c:3167-3222) derives the child's validity PURELY from
//     enforcement (`skip_validation = !is_enforced`) — never from the
//     parent's own convalidated. NotValid mirrors NotEnforced.
//   - ALTER-time cascade (ATAddCheckNNConstraint, tablecmds.c:9912-10049):
//     PG reuses the SAME Constraint node for every child, so each child gets
//     the user's literal NOT VALID / NOT ENFORCED clauses UNCHANGED.
//
// Closes deferral ledger rows .ralph/deferral_ledger.md:1540 (0005z
// NotEnforced half) and :1573 (0005an NotValid half).

// TestCheckInheritFlagsPortedAlterTableInheritedNotValid is a literal port of
// alter_table.sql:397-406 (attmp3/attmp6/attmp7, constraint b_le_20): a CHECK
// added NOT VALID via ALTER TABLE on a parent that already has plain INHERITS
// children must FAIL VALIDATE CONSTRAINT while a child row violates it, and
// only succeed once the offending row is deleted. NOTE: this scenario's
// scan (forEachLiveRow walking descendant storage, gated only on the
// PARENT's own NamedChecks[i].NotValid) is independent of this brief's fix —
// AlterTableAddCheck's cascade loop (operators_ddl.go:~8751) only walks
// PartitionChildren, never plain InheritanceChildren, so attmp6/attmp7-style
// children never receive a NamedChecks entry for a check ADDED after they
// already exist (a separate, already-ledgered gap — see report.md). This
// subtest is a no-regression pin, not a FAIL-pre proof for this brief's
// specific edit; subtests b-e below are.
func TestCheckInheritFlagsPortedAlterTableInheritedNotValid(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE cif_attmp3 (a int, b int)`,
		`CREATE TABLE cif_attmp6 () INHERITS (cif_attmp3)`,
		`CREATE TABLE cif_attmp7 () INHERITS (cif_attmp3)`,
		`INSERT INTO cif_attmp6 VALUES (6, 30), (7, 16)`,
		`ALTER TABLE cif_attmp3 ADD CONSTRAINT b_le_20 CHECK (b <= 20) NOT VALID`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runDDL(t, ctx, `ALTER TABLE cif_attmp3 VALIDATE CONSTRAINT b_le_20`)
	requireExecError(t, err, "23514", `check constraint "b_le_20"`)

	if err := runDDL(t, ctx, `DELETE FROM cif_attmp6 WHERE b > 20`); err != nil {
		t.Fatalf("DELETE FROM cif_attmp6 WHERE b > 20: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TABLE cif_attmp3 VALIDATE CONSTRAINT b_le_20`); err != nil {
		t.Fatalf("ALTER TABLE cif_attmp3 VALIDATE CONSTRAINT b_le_20 (post-delete): %v", err)
	}

	parent, ok := cat.LookupTable(parser.ObjectName{Name: "cif_attmp3"})
	if !ok {
		t.Fatal("cif_attmp3 not found")
	}
	if idx := findNamedCheck(t, parent, "b_le_20"); parent.NamedChecks[idx].NotValid {
		t.Errorf("cif_attmp3's b_le_20.NotValid = true after VALIDATE CONSTRAINT succeeded, want false")
	}
}

// TestCheckInheritFlagsAlterCascadeNotEnforced pins the ALTER-time cascade
// site (AlterTableAddCheck's PartitionChildren loop, operators_ddl.go:~8765):
// `ADD CONSTRAINT ... CHECK (...) NOT ENFORCED` on a partitioned parent must
// propagate NotEnforced=true onto each existing partition child's own
// NamedChecks entry, so a violating INSERT directly into the child succeeds
// (an unenforced constraint is a documentation-only annotation — PG never
// checks it).
func TestCheckInheritFlagsAlterCascadeNotEnforced(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE cif_altp (a int, b int) PARTITION BY LIST (a)`,
		`CREATE TABLE cif_altp_1 PARTITION OF cif_altp FOR VALUES IN (1,2)`,
		`ALTER TABLE cif_altp ADD CONSTRAINT b_le_20 CHECK (b <= 20) NOT ENFORCED`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "cif_altp_1"})
	if !ok {
		t.Fatal("cif_altp_1 not found")
	}
	idx := findNamedCheck(t, child, "b_le_20")
	if !child.NamedChecks[idx].NotEnforced {
		t.Errorf("cif_altp_1's b_le_20.NotEnforced = false after parent ADD CONSTRAINT ... NOT ENFORCED, want true")
	}

	// A violating row inserted directly into the child must succeed since the
	// constraint is not enforced on it.
	if err := runDDL(t, ctx, `INSERT INTO cif_altp_1 VALUES (1, 999)`); err != nil {
		t.Errorf("INSERT of a violating row into cif_altp_1 failed despite NOT ENFORCED: %v", err)
	}
}

// TestCheckInheritFlagsCreateInheritsNotEnforced covers the drifted
// execCreateTable INHERITS loop (operators_ddl.go:~4030, the site that used
// to stamp NotEnforced post-hoc via a separate M0134-0005z patch, now folded
// into AddCheckInherited itself): a `NOT ENFORCED` parent CHECK must produce
// a child entry with BOTH NotEnforced=true and NotValid=true (CREATE-time
// rule: NotValid mirrors NotEnforced, per MergeCheckConstraint).
func TestCheckInheritFlagsCreateInheritsNotEnforced(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE cif_cip (a int, b int)`,
		`ALTER TABLE cif_cip ADD CONSTRAINT b_le_20 CHECK (b <= 20) NOT ENFORCED`,
		`CREATE TABLE cif_cic () INHERITS (cif_cip)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "cif_cic"})
	if !ok {
		t.Fatal("cif_cic not found")
	}
	idx := findNamedCheck(t, child, "b_le_20")
	if !child.NamedChecks[idx].NotEnforced {
		t.Errorf("cif_cic's b_le_20.NotEnforced = false after CREATE TABLE ... INHERITS a NOT ENFORCED parent, want true")
	}
	if !child.NamedChecks[idx].NotValid {
		t.Errorf("cif_cic's b_le_20.NotValid = false after CREATE TABLE ... INHERITS a NOT ENFORCED parent, want true (CREATE-time rule: NotValid mirrors NotEnforced)")
	}
}

// TestCheckInheritFlagsCreatePartitionOfNotEnforced is the PARTITION OF twin
// of TestCheckInheritFlagsCreateInheritsNotEnforced, pinning
// execCreatePartitionChild (operators_ddl.go:~5175) — the site that had NO
// NotEnforced/NotValid stamp at all before this brief, despite the comment
// at operators_ddl.go:~4026 claiming the two sites mirror each other.
func TestCheckInheritFlagsCreatePartitionOfNotEnforced(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE cif_pop (a int, b int) PARTITION BY LIST (a)`,
		`ALTER TABLE cif_pop ADD CONSTRAINT b_le_20 CHECK (b <= 20) NOT ENFORCED`,
		`CREATE TABLE cif_pop_1 PARTITION OF cif_pop FOR VALUES IN (1,2)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "cif_pop_1"})
	if !ok {
		t.Fatal("cif_pop_1 not found")
	}
	idx := findNamedCheck(t, child, "b_le_20")
	if !child.NamedChecks[idx].NotEnforced {
		t.Errorf("cif_pop_1's b_le_20.NotEnforced = false after CREATE TABLE ... PARTITION OF a NOT ENFORCED parent, want true")
	}
	if !child.NamedChecks[idx].NotValid {
		t.Errorf("cif_pop_1's b_le_20.NotValid = false after CREATE TABLE ... PARTITION OF a NOT ENFORCED parent, want true (CREATE-time rule: NotValid mirrors NotEnforced)")
	}
}

// TestCheckInheritFlagsCreateTimeAntiSymmetryNotValid is the negative /
// anti-symmetry guard the brief calls out explicitly: a parent CHECK that is
// NOT VALID but still ENFORCED must NOT propagate NotValid=true to a freshly
// created CREATE-time child — PG treats a fresh empty child as trivially
// valid (MergeCheckConstraint derives validity from enforcement alone, never
// from the parent's own convalidated). This is what stops a future loop from
// "symmetrizing" the three call sites by propagating the parent's NotValid
// directly.
func TestCheckInheritFlagsCreateTimeAntiSymmetryNotValid(t *testing.T) {
	t.Run("INHERITS", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		for _, stmt := range []string{
			`CREATE TABLE cif_asip (a int, b int)`,
			`ALTER TABLE cif_asip ADD CONSTRAINT b_le_20 CHECK (b <= 20) NOT VALID`,
			`CREATE TABLE cif_asic () INHERITS (cif_asip)`,
		} {
			if err := runDDL(t, ctx, stmt); err != nil {
				t.Fatalf("setup %q: %v", stmt, err)
			}
		}

		child, ok := cat.LookupTable(parser.ObjectName{Name: "cif_asic"})
		if !ok {
			t.Fatal("cif_asic not found")
		}
		idx := findNamedCheck(t, child, "b_le_20")
		if child.NamedChecks[idx].NotEnforced {
			t.Errorf("cif_asic's b_le_20.NotEnforced = true, want false (parent constraint is enforced)")
		}
		if child.NamedChecks[idx].NotValid {
			t.Errorf("cif_asic's b_le_20.NotValid = true for a fresh CREATE-time INHERITS child of an ENFORCED-but-NOT-VALID parent, want false (a fresh empty child is trivially valid)")
		}
	})

	t.Run("PARTITION OF", func(t *testing.T) {
		ctx, cat, cleanup := newDDLFixture(t)
		defer cleanup()

		for _, stmt := range []string{
			`CREATE TABLE cif_asop (a int, b int) PARTITION BY LIST (a)`,
			`ALTER TABLE cif_asop ADD CONSTRAINT b_le_20 CHECK (b <= 20) NOT VALID`,
			`CREATE TABLE cif_asop_1 PARTITION OF cif_asop FOR VALUES IN (1,2)`,
		} {
			if err := runDDL(t, ctx, stmt); err != nil {
				t.Fatalf("setup %q: %v", stmt, err)
			}
		}

		child, ok := cat.LookupTable(parser.ObjectName{Name: "cif_asop_1"})
		if !ok {
			t.Fatal("cif_asop_1 not found")
		}
		idx := findNamedCheck(t, child, "b_le_20")
		if child.NamedChecks[idx].NotEnforced {
			t.Errorf("cif_asop_1's b_le_20.NotEnforced = true, want false (parent constraint is enforced)")
		}
		if child.NamedChecks[idx].NotValid {
			t.Errorf("cif_asop_1's b_le_20.NotValid = true for a fresh CREATE-time PARTITION OF child of an ENFORCED-but-NOT-VALID parent, want false (a fresh empty child is trivially valid)")
		}
	})
}
