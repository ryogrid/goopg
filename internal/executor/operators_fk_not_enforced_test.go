package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestFKNotEnforcedAlterTable verifies DU-002 slice 431: a FOREIGN KEY
// constraint added via `ALTER TABLE ... ADD CONSTRAINT ... FOREIGN KEY (...)
// REFERENCES ... NOT ENFORCED` (PG18) is captured on
// catalog.ForeignKey.NotEnforced (previously a hard parse error), surfaces as
// conenforced='f' AND convalidated='f' in pg_constraint (mirroring the CHECK
// precedent, DU-002 slice 430 — NOT ENFORCED implies unvalidated too), and
// pg_get_constraintdef renders the trailing ` NOT ENFORCED` — NOT the
// ` NOT VALID` tail, per ruleutils.c's conenforced-first precedence.
func TestFKNotEnforcedAlterTable(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nenf_parent (id integer)`); err != nil {
		t.Fatalf("CREATE TABLE nenf_parent: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE nenf_child (id integer, pid integer)`); err != nil {
		t.Fatalf("CREATE TABLE nenf_child: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE nenf_child ADD CONSTRAINT nenf_fk FOREIGN KEY (pid) REFERENCES nenf_parent(id) NOT ENFORCED`); err != nil {
		t.Fatalf("ALTER TABLE nenf_child ADD CONSTRAINT: %v", err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "nenf_child"})
	if !ok {
		t.Fatal("nenf_child table not found")
	}
	if len(tbl.ForeignKeys) != 1 || tbl.ForeignKeys[0].Name != "nenf_fk" {
		t.Fatalf("expected 1 foreign key nenf_fk, got %+v", tbl.ForeignKeys)
	}
	if !tbl.ForeignKeys[0].NotEnforced {
		t.Fatalf("expected ForeignKeys[0].NotEnforced=true, got %+v", tbl.ForeignKeys[0])
	}

	pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	var row []string
	for _, r := range pgcon.VirtualRows() {
		if r[3] == "f" && r[1] == "nenf_fk" {
			row = r
		}
	}
	if row == nil {
		t.Fatal("no pg_constraint row for nenf_fk")
	}
	if row[6] != "f" {
		t.Errorf("convalidated = %q, want f (NOT ENFORCED implies unvalidated)", row[6])
	}
	if row[25] != "f" {
		t.Errorf("conenforced = %q, want f", row[25])
	}

	rows := runQuery(t, ctx, `SELECT pg_get_constraintdef(`+row[0]+`::oid)`)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row from pg_get_constraintdef, got %d", len(rows))
	}
	def := rows[0][0].StringValue()
	if !strings.Contains(def, "NOT ENFORCED") {
		t.Errorf("pg_get_constraintdef = %q, want it to contain NOT ENFORCED", def)
	}
	if strings.Contains(def, "NOT VALID") {
		t.Errorf("pg_get_constraintdef = %q, must NOT also print NOT VALID (conenforced takes precedence per ruleutils.c)", def)
	}
}

// TestFKNotEnforcedPlainNotValidStillRendersNotValid guards against a
// regression where the new NOT ENFORCED precedence check accidentally
// swallows the pre-existing NOT VALID rendering (DU-002 slice 307) for an FK
// that is only NOT VALID, not NOT ENFORCED.
func TestFKNotEnforcedPlainNotValidStillRendersNotValid(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nv_parent (id integer)`); err != nil {
		t.Fatalf("CREATE TABLE nv_parent: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE nv_child (id integer, pid integer)`); err != nil {
		t.Fatalf("CREATE TABLE nv_child: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE nv_child ADD CONSTRAINT nv_fk FOREIGN KEY (pid) REFERENCES nv_parent(id) NOT VALID`); err != nil {
		t.Fatalf("ALTER TABLE nv_child ADD CONSTRAINT: %v", err)
	}

	pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	var oid string
	for _, r := range pgcon.VirtualRows() {
		if r[3] == "f" && r[1] == "nv_fk" {
			oid = r[0]
			if r[25] != "t" {
				t.Errorf("conenforced = %q, want t for a plain NOT VALID FK", r[25])
			}
		}
	}
	if oid == "" {
		t.Fatal("no pg_constraint row for nv_fk")
	}

	rows := runQuery(t, ctx, `SELECT pg_get_constraintdef(`+oid+`::oid)`)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	def := rows[0][0].StringValue()
	if !strings.Contains(def, "NOT VALID") {
		t.Errorf("pg_get_constraintdef = %q, want it to contain NOT VALID", def)
	}
	if strings.Contains(def, "NOT ENFORCED") {
		t.Errorf("pg_get_constraintdef = %q, must not print NOT ENFORCED for a plain NOT VALID FK", def)
	}
}

// TestFKNotEnforcedSkipsRuntimeCheck verifies the behavioral half of DU-002
// slice 431: real PostgreSQL creates NO RI check/action triggers at all for a
// NOT ENFORCED FK (addFkRecurseReferencing's `if (fkconstraint->is_enforced)`
// gate, tablecmds.c:11065), so an INSERT with a dangling reference must
// succeed silently — never a 23503 — precisely because the constraint isn't
// merely deferred but entirely unenforced. A control case on the same schema
// without NOT ENFORCED confirms the fixture would otherwise 23503.
func TestFKNotEnforcedSkipsRuntimeCheck(t *testing.T) {
	t.Run("NotEnforcedAllowsDanglingReference", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE rt_parent (id integer)`); err != nil {
			t.Fatalf("CREATE TABLE rt_parent: %v", err)
		}
		if err := runDDL(t, ctx, `CREATE TABLE rt_child (id integer, pid integer)`); err != nil {
			t.Fatalf("CREATE TABLE rt_child: %v", err)
		}
		if err := runDDL(t, ctx, `ALTER TABLE rt_child ADD CONSTRAINT rt_fk FOREIGN KEY (pid) REFERENCES rt_parent(id) NOT ENFORCED`); err != nil {
			t.Fatalf("ALTER TABLE rt_child ADD CONSTRAINT: %v", err)
		}
		if err := runDDL(t, ctx, `INSERT INTO rt_child VALUES (1, 999)`); err != nil {
			t.Fatalf("INSERT with dangling FK reference under NOT ENFORCED should succeed, got: %v", err)
		}
	})
	t.Run("EnforcedControlRejectsDanglingReference", func(t *testing.T) {
		ctx, _, cleanup := newDDLFixture(t)
		defer cleanup()

		if err := runDDL(t, ctx, `CREATE TABLE rt2_parent (id integer)`); err != nil {
			t.Fatalf("CREATE TABLE rt2_parent: %v", err)
		}
		if err := runDDL(t, ctx, `CREATE TABLE rt2_child (id integer, pid integer)`); err != nil {
			t.Fatalf("CREATE TABLE rt2_child: %v", err)
		}
		if err := runDDL(t, ctx, `ALTER TABLE rt2_child ADD CONSTRAINT rt2_fk FOREIGN KEY (pid) REFERENCES rt2_parent(id)`); err != nil {
			t.Fatalf("ALTER TABLE rt2_child ADD CONSTRAINT: %v", err)
		}
		err := runDDL(t, ctx, `INSERT INTO rt2_child VALUES (1, 999)`)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "23503" {
			t.Fatalf("expected 23503 foreign_key_violation on the enforced control, got: %v", err)
		}
	})
}

// TestFKNotEnforcedValidateConstraintErrors verifies that VALIDATE CONSTRAINT
// on a NOT ENFORCED FK is rejected (real PG's ATExecValidateConstraint:
// `if (!con->conenforced) ereport(ERROR, ... "cannot validate NOT ENFORCED
// constraint")`, SQLSTATE 55000) rather than silently flipping convalidated
// to true.
func TestFKNotEnforcedValidateConstraintErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE vc_parent (id integer)`); err != nil {
		t.Fatalf("CREATE TABLE vc_parent: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE vc_child (id integer, pid integer)`); err != nil {
		t.Fatalf("CREATE TABLE vc_child: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE vc_child ADD CONSTRAINT vc_fk FOREIGN KEY (pid) REFERENCES vc_parent(id) NOT ENFORCED`); err != nil {
		t.Fatalf("ALTER TABLE vc_child ADD CONSTRAINT: %v", err)
	}
	err := runDDL(t, ctx, `ALTER TABLE vc_child VALIDATE CONSTRAINT vc_fk`)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "55000" {
		t.Fatalf("expected 55000 on VALIDATE CONSTRAINT of a NOT ENFORCED FK, got: %v", err)
	}
}
