package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// requireExecError asserts err is a non-nil *ExecError carrying wantCode and a
// message containing wantSubstr, and returns it. M0134-0002 C3 slice 1.
func requireExecError(t *testing.T, err error, wantCode, wantSubstr string) *ExecError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error (SQLSTATE %s), got nil", wantCode)
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != wantCode {
		t.Errorf("Code = %q, want %q (err=%v)", ee.Code, wantCode, err)
	}
	if !strings.Contains(ee.Message, wantSubstr) {
		t.Errorf("Message = %q, want it to contain %q", ee.Message, wantSubstr)
	}
	return ee
}

// TestAlterTableAddCheckScansExistingRows verifies M0134-0002 C3 slice 1: an
// `ALTER TABLE ... ADD CONSTRAINT name CHECK (expr)` scans existing rows and
// raises PG's exact 23514 when a row evaluates the expression to a definite
// FALSE (ATAddCheckNNConstraint queues the constraint for ATRewriteTable's
// phase-3 scan when !skip_validation, tablecmds.c:9956; the per-row refusal is
// tablecmds.c:6493-6498). The scan runs before registration, so a failed
// statement leaves no in-memory constraint behind.
func TestAlterTableAddCheckScansExistingRows(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE chk_scan (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE chk_scan: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO chk_scan VALUES (1, 5), (2, 20)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TABLE chk_scan ADD CONSTRAINT chk_b CHECK (b > 10)`)
	requireExecError(t, err, "23514",
		`check constraint "chk_b" of relation "chk_scan" is violated by some row`)

	// The failed ADD CHECK must not leave a ghost constraint (mirrors PG,
	// where the whole statement rolls back).
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "chk_scan"})
	if !ok {
		t.Fatal("chk_scan table not found")
	}
	if len(tbl.NamedChecks) != 0 {
		t.Errorf("NamedChecks after failed ADD CHECK = %+v, want none", tbl.NamedChecks)
	}
}

// TestAlterTableAddCheckAnonymousNameInMessage verifies the 23514 message for
// an anonymous ADD CHECK (no CONSTRAINT name) renders the auto-generated
// constraint name — ChooseConstraintName-derived "<table>_<col>_check" for a
// single-column expression (autoCheckName, operators_ddl.go:4088), matching
// PG's refusal text for the regress sql:388-style anonymous CHECK.
func TestAlterTableAddCheckAnonymousNameInMessage(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE chk_anon (a int)`); err != nil {
		t.Fatalf("CREATE TABLE chk_anon: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO chk_anon VALUES (0)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TABLE chk_anon ADD CHECK (a > 0)`)
	requireExecError(t, err, "23514",
		`check constraint "chk_anon_a_check" of relation "chk_anon" is violated by some row`)
}

// TestAlterTableAddCheckNullPassesScan verifies SQL 3-valued logic: a row
// where the CHECK expression evaluates to NULL passes the scan (only a
// definite boolean FALSE violates — ExecCheck, tablecmds.c:6492).
func TestAlterTableAddCheckNullPassesScan(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE chk_null (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE chk_null: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO chk_null VALUES (1, NULL), (2, 20)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE chk_null ADD CONSTRAINT chk_b CHECK (b > 10)`); err != nil {
		t.Fatalf("ADD CHECK over a NULL-bearing row should pass: %v", err)
	}
}

// TestAlterTableAddCheckNotValidSkipsScan verifies the NOT VALID trailer
// (skip_validation): existing rows are NOT scanned, so a violating row is
// accepted and the constraint is registered with convalidated='f'.
func TestAlterTableAddCheckNotValidSkipsScan(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE chk_nv (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE chk_nv: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO chk_nv VALUES (1, 5)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE chk_nv ADD CONSTRAINT chk_b CHECK (b > 10) NOT VALID`); err != nil {
		t.Fatalf("ADD CHECK NOT VALID should not scan: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "chk_nv"})
	if !ok {
		t.Fatal("chk_nv table not found")
	}
	if len(tbl.NamedChecks) != 1 || tbl.NamedChecks[0].Name != "chk_b" {
		t.Fatalf("expected 1 named check chk_b, got %+v", tbl.NamedChecks)
	}
	if !tbl.NamedChecks[0].NotValid {
		t.Errorf("expected NamedChecks[0].NotValid=true after NOT VALID, got %+v", tbl.NamedChecks[0])
	}
}

// TestAlterTableSetNotNullScansExistingRows verifies the SET NOT NULL 23502
// scan (ATExecSetNotNull → phase-3 verify_new_notnull, tablecmds.c:8057 /
// ATRewriteTable :6450-6463): the FIRST NULL raises a single
// `column "%s" of relation "%s" contains null values`. The scan runs before
// the in-memory flag is set, so a failed statement leaves the column nullable.
func TestAlterTableSetNotNullScansExistingRows(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nn_scan (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE nn_scan: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO nn_scan VALUES (NULL, 1), (2, NULL)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TABLE nn_scan ALTER a SET NOT NULL`)
	requireExecError(t, err, "23502",
		`column "a" of relation "nn_scan" contains null values`)

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "nn_scan"})
	if !ok {
		t.Fatal("nn_scan table not found")
	}
	if tbl.Columns[0].NotNull {
		t.Errorf("column a NotNull = true after failed SET NOT NULL, want false")
	}
}

// TestAlterTableAddNotNullScansExistingRows verifies the named twin
// (`ADD CONSTRAINT name NOT NULL col`) runs the same 23502 scan.
func TestAlterTableAddNotNullScansExistingRows(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nn_add (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE nn_add: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO nn_add VALUES (NULL, 1)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TABLE nn_add ADD CONSTRAINT nn_a NOT NULL a`)
	requireExecError(t, err, "23502",
		`column "a" of relation "nn_add" contains null values`)

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "nn_add"})
	if !ok {
		t.Fatal("nn_add table not found")
	}
	if tbl.Columns[0].NotNull {
		t.Errorf("column a NotNull = true after failed ADD NOT NULL, want false")
	}
	if len(tbl.NotNullConstraints) != 0 {
		t.Errorf("NotNullConstraints after failed ADD NOT NULL = %+v, want none", tbl.NotNullConstraints)
	}
}

// TestAlterTableValidateCheckScansAndFlipsConvalidated verifies VALIDATE
// CONSTRAINT on a CHECK: re-runs the 23514 scan (QueueCheckConstraintValidation
// re-reads conbin, tablecmds.c:13116), refuses NOT ENFORCED with 55000, and on
// success flips convalidated 'f'→'t' (a repeated VALIDATE on an already-valid
// constraint is a no-op — ATExecValidateConstraint gates on !convalidated,
// tablecmds.c:12960).
func TestAlterTableValidateCheckScansAndFlipsConvalidated(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE vc (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE vc: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO vc VALUES (1, 5), (2, 20)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE vc ADD CONSTRAINT chk_b CHECK (b > 10) NOT VALID`); err != nil {
		t.Fatalf("ADD CHECK NOT VALID: %v", err)
	}

	pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	convalidated := func() string {
		for _, r := range pgcon.VirtualRows() {
			if r[3] == "c" && r[1] == "chk_b" {
				return r[6]
			}
		}
		return ""
	}
	if got := convalidated(); got != "f" {
		t.Errorf("convalidated after ADD ... NOT VALID = %q, want f", got)
	}

	// A still-violating row makes VALIDATE fail with the same 23514 scan.
	err := runDDL(t, ctx, `ALTER TABLE vc VALIDATE CONSTRAINT chk_b`)
	requireExecError(t, err, "23514",
		`check constraint "chk_b" of relation "vc" is violated by some row`)
	if got := convalidated(); got != "f" {
		t.Errorf("convalidated after failed VALIDATE = %q, want still f", got)
	}

	// Remove the violating row, then VALIDATE succeeds and flips convalidated.
	if err := runDDL(t, ctx, `DELETE FROM vc WHERE NOT b > 10`); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE vc VALIDATE CONSTRAINT chk_b`); err != nil {
		t.Fatalf("VALIDATE CONSTRAINT after cleanup: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "vc"})
	if !ok {
		t.Fatal("vc table not found")
	}
	if len(tbl.NamedChecks) != 1 || tbl.NamedChecks[0].Name != "chk_b" {
		t.Fatalf("expected 1 named check chk_b, got %+v", tbl.NamedChecks)
	}
	if tbl.NamedChecks[0].NotValid {
		t.Errorf("NamedChecks[0].NotValid still true after VALIDATE CONSTRAINT")
	}
	if got := convalidated(); got != "t" {
		t.Errorf("convalidated after VALIDATE = %q, want t", got)
	}

	// Repeated VALIDATE on an already-valid constraint is a no-op.
	if err := runDDL(t, ctx, `ALTER TABLE vc VALIDATE CONSTRAINT chk_b`); err != nil {
		t.Fatalf("repeated VALIDATE CONSTRAINT should be a no-op: %v", err)
	}
}

// TestAlterTableValidateCheckNotEnforced verifies VALIDATE CONSTRAINT on a
// NOT ENFORCED CHECK raises PG's 55000 (ATExecValidateConstraint,
// tablecmds.c:12955-12958).
func TestAlterTableValidateCheckNotEnforced(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE vc_ne (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE vc_ne: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE vc_ne ADD CONSTRAINT chk_b CHECK (b > 10) NOT ENFORCED`); err != nil {
		t.Fatalf("ADD CHECK NOT ENFORCED: %v", err)
	}

	pgcon, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_constraint"})
	if !ok || pgcon.VirtualRows == nil {
		t.Fatal("pg_constraint virtual table not found")
	}
	convalidated := func() string {
		for _, r := range pgcon.VirtualRows() {
			if r[3] == "c" && r[1] == "chk_b" {
				return r[6]
			}
		}
		return ""
	}
	// NOT ENFORCED implies unvalidated (convalidated='f') from registration.
	if got := convalidated(); got != "f" {
		t.Errorf("convalidated after ADD ... NOT ENFORCED = %q, want f", got)
	}

	err := runDDL(t, ctx, `ALTER TABLE vc_ne VALIDATE CONSTRAINT chk_b`)
	requireExecError(t, err, "55000", "cannot validate NOT ENFORCED constraint")

	// The failed VALIDATE must not flip convalidated.
	if got := convalidated(); got != "f" {
		t.Errorf("convalidated after failed VALIDATE = %q, want still f", got)
	}
}
