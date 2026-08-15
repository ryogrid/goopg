package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCheckConstraintNotEnforcedAlterTable verifies DU-002 slice 430: a CHECK
// constraint added via `ALTER TABLE ... ADD CONSTRAINT ... CHECK (...) NOT
// ENFORCED` (PG18) is captured on catalog.NamedCheckConstraint.NotEnforced
// (previously discarded at parse time — pg_constraint.conenforced was
// hardcoded 't' always), surfaces as conenforced='f' AND convalidated='f' in
// pg_constraint (real PG's ATAddCheckNNConstraint sets
// skip_validation=!is_enforced, so an unenforced constraint is implicitly
// unvalidated too), and pg_get_constraintdef renders the trailing
// ` NOT ENFORCED` — NOT the ` NOT VALID` tail, per ruleutils.c's
// conenforced-first precedence.
func TestCheckConstraintNotEnforcedAlterTable(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nenf_t (id integer, val integer)`); err != nil {
		t.Fatalf("CREATE TABLE nenf_t: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE nenf_t ADD CONSTRAINT nenf_chk CHECK (val > 0) NOT ENFORCED`); err != nil {
		t.Fatalf("ALTER TABLE nenf_t ADD CONSTRAINT: %v", err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "nenf_t"})
	if !ok {
		t.Fatal("nenf_t table not found")
	}
	if len(tbl.NamedChecks) != 1 || tbl.NamedChecks[0].Name != "nenf_chk" {
		t.Fatalf("expected 1 named check nenf_chk, got %+v", tbl.NamedChecks)
	}
	if !tbl.NamedChecks[0].NotEnforced {
		t.Fatalf("expected NamedChecks[0].NotEnforced=true, got %+v", tbl.NamedChecks[0])
	}

	pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	var row []string
	for _, r := range pgcon.VirtualRows() {
		if r[3] == "c" && r[1] == "nenf_chk" {
			row = r
		}
	}
	if row == nil {
		t.Fatal("no pg_constraint row for nenf_chk")
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

// TestCheckConstraintNotEnforcedCreateTableForms verifies the three
// CREATE-TABLE-time NOT ENFORCED forms (anonymous table-level, named
// table-level, inline column-level) all thread the flag onto
// catalog.NamedCheckConstraint.NotEnforced. DU-002 slice 430.
func TestCheckConstraintNotEnforcedCreateTableForms(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nenf_anon (a integer, CHECK (a > 0) NOT ENFORCED)`); err != nil {
		t.Fatalf("CREATE TABLE nenf_anon: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE nenf_named (a integer, CONSTRAINT nenf_named_chk CHECK (a > 0) NOT ENFORCED)`); err != nil {
		t.Fatalf("CREATE TABLE nenf_named: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE nenf_col (a integer CHECK (a > 0) NOT ENFORCED)`); err != nil {
		t.Fatalf("CREATE TABLE nenf_col: %v", err)
	}

	for _, tc := range []struct {
		table, checkName string
	}{
		{"nenf_anon", "nenf_anon_a_check"},
		{"nenf_named", "nenf_named_chk"},
		{"nenf_col", "nenf_col_a_check"},
	} {
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: tc.table})
		if !ok {
			t.Fatalf("%s table not found", tc.table)
		}
		var found bool
		for _, nc := range tbl.NamedChecks {
			if nc.Name == tc.checkName {
				found = true
				if !nc.NotEnforced {
					t.Errorf("%s: NamedChecks[%q].NotEnforced = false, want true", tc.table, tc.checkName)
				}
			}
		}
		if !found {
			t.Errorf("%s: no NamedChecks entry named %q, got %+v", tc.table, tc.checkName, tbl.NamedChecks)
		}
	}
}

// TestCheckConstraintPlainNotValidStillRendersNotValid guards against a
// regression where the new NOT ENFORCED precedence check accidentally
// swallows the pre-existing NOT VALID rendering (DU-002 slice 308) for a
// constraint that is only NOT VALID, not NOT ENFORCED.
func TestCheckConstraintPlainNotValidStillRendersNotValid(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nv_t (id integer, val integer)`); err != nil {
		t.Fatalf("CREATE TABLE nv_t: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE nv_t ADD CONSTRAINT nv_chk CHECK (val > 0) NOT VALID`); err != nil {
		t.Fatalf("ALTER TABLE nv_t ADD CONSTRAINT: %v", err)
	}

	pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	var oid string
	for _, r := range pgcon.VirtualRows() {
		if r[3] == "c" && r[1] == "nv_chk" {
			oid = r[0]
			if r[25] != "t" {
				t.Errorf("conenforced = %q, want t for a plain NOT VALID constraint", r[25])
			}
		}
	}
	if oid == "" {
		t.Fatal("no pg_constraint row for nv_chk")
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
		t.Errorf("pg_get_constraintdef = %q, must not print NOT ENFORCED for a plain NOT VALID constraint", def)
	}
}

// TestCheckConstraintNoInheritAlterTable verifies M0134-0002 C2: an `ALTER
// TABLE ... ADD [CONSTRAINT name] CHECK (...) NO INHERIT` is threaded onto
// catalog.NamedCheckConstraint.NoInherit (AddCheckFull already stores the
// flag, so no new catalog field), surfaces as pg_constraint.connoinherit='t'
// and a pg_get_constraintdef ` NO INHERIT` tail, and is rejected on a
// partitioned table with PG's exact 42P16 error ("cannot add NO INHERIT
// constraint to partitioned table", tablecmds.c ATAddCheckConstraint).
func TestCheckConstraintNoInheritAlterTable(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	// (a) Non-partitioned target: NO INHERIT succeeds and is recorded.
	if err := runDDL(t, ctx, `CREATE TABLE ni_t (id integer, val integer)`); err != nil {
		t.Fatalf("CREATE TABLE ni_t: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE ni_t ADD CONSTRAINT ni_chk CHECK (val > 0) NO INHERIT`); err != nil {
		t.Fatalf("ALTER TABLE ni_t ADD CONSTRAINT: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "ni_t"})
	if !ok {
		t.Fatal("ni_t table not found")
	}
	if len(tbl.NamedChecks) != 1 || tbl.NamedChecks[0].Name != "ni_chk" {
		t.Fatalf("expected 1 named check ni_chk, got %+v", tbl.NamedChecks)
	}
	if !tbl.NamedChecks[0].NoInherit {
		t.Fatalf("expected NamedChecks[0].NoInherit=true, got %+v", tbl.NamedChecks[0])
	}

	pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	var oid string
	for _, r := range pgcon.VirtualRows() {
		if r[3] == "c" && r[1] == "ni_chk" {
			oid = r[0]
			if r[17] != "t" {
				t.Errorf("connoinherit = %q, want t", r[17])
			}
		}
	}
	if oid == "" {
		t.Fatal("no pg_constraint row for ni_chk")
	}
	rows := runQuery(t, ctx, `SELECT pg_get_constraintdef(`+oid+`::oid)`)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row from pg_get_constraintdef, got %d", len(rows))
	}
	if def := rows[0][0].StringValue(); !strings.Contains(def, "NO INHERIT") {
		t.Errorf("pg_get_constraintdef = %q, want it to contain NO INHERIT", def)
	}

	// (b) Partitioned target: rejected with PG's exact 42P16 error.
	if err := runDDL(t, ctx, `CREATE TABLE ni_pt (id integer, val integer) PARTITION BY RANGE (id)`); err != nil {
		t.Fatalf("CREATE TABLE ni_pt: %v", err)
	}
	err := runDDL(t, ctx, `ALTER TABLE ni_pt ADD CONSTRAINT ni_pt_chk CHECK (val > 0) NO INHERIT`)
	if err == nil {
		t.Fatal("NO INHERIT CHECK on a partitioned table should fail")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "42P16" {
		t.Errorf("Code = %q, want 42P16", ee.Code)
	}
	if want := `cannot add NO INHERIT constraint to partitioned table "ni_pt"`; ee.Message != want {
		t.Errorf("Message = %q, want %q", ee.Message, want)
	}
}
