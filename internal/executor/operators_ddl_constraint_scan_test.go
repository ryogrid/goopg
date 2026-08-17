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

// ---- C3 slice 2: ADD PK / UNIQUE index-build path ----
// PG raises the duplicate-key 23505 from the tuplesort compare callback
// (comparetup_index_btree, tuplesortvariants.c:1686-1693) with a DETAIL built
// by BuildIndexValueDescription (genam.c:178-276): "Key (a, b)=(1, 2) is
// duplicated." and NO errposition (Pos stays 0 — psql prints no LINE 1 caret).

// TestAlterTableAddPrimaryKeyDuplicateKeyDetail verifies the single-column
// ADD PRIMARY KEY 23505 carries PG's exact DETAIL + Pos=0.
func TestAlterTableAddPrimaryKeyDuplicateKeyDetail(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE pk_dup (a int)`); err != nil {
		t.Fatalf("CREATE TABLE pk_dup: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO pk_dup VALUES (1), (1)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE pk_dup ADD PRIMARY KEY (a)`),
		"23505", `could not create unique index "pk_dup_pkey"`)
	if ee.Pos != 0 {
		t.Errorf("Pos = %d, want 0 (PG emits no errposition on this refusal)", ee.Pos)
	}
	if ee.Detail != "Key (a)=(1) is duplicated." {
		t.Errorf("Detail = %q, want %q", ee.Detail, "Key (a)=(1) is duplicated.")
	}
}

// TestAlterTableAddPrimaryKeyMultiColumnDuplicateDetail verifies the
// multi-column rendering "Key (a, b)=(1, 2)" (comma-space separated names and
// values, ruleutils.c:1235).
func TestAlterTableAddPrimaryKeyMultiColumnDuplicateDetail(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE pk_dup2 (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE pk_dup2: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO pk_dup2 VALUES (1, 2), (1, 2)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE pk_dup2 ADD PRIMARY KEY (a, b)`),
		"23505", `could not create unique index "pk_dup2_pkey"`)
	if ee.Pos != 0 {
		t.Errorf("Pos = %d, want 0", ee.Pos)
	}
	if ee.Detail != "Key (a, b)=(1, 2) is duplicated." {
		t.Errorf("Detail = %q, want %q", ee.Detail, "Key (a, b)=(1, 2) is duplicated.")
	}
}

// TestAlterTableAddPrimaryKeyNullScan verifies the genuinely-missing 23502
// ADD-PK-over-NULL scan (ATRewriteTable verify_new_notnull, tablecmds.c:6456-6462):
// the FIRST NULL in a PK column raises `column "%s" of relation "%s" contains
// null values`, Pos=0.
func TestAlterTableAddPrimaryKeyNullScan(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE pk_null (a int)`); err != nil {
		t.Fatalf("CREATE TABLE pk_null: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO pk_null VALUES (1), (NULL)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE pk_null ADD PRIMARY KEY (a)`),
		"23502", `column "a" of relation "pk_null" contains null values`)
	if ee.Pos != 0 {
		t.Errorf("Pos = %d, want 0", ee.Pos)
	}
}

// TestAlterTableAddPrimaryKeyDuplicateWinsOverNull verifies PG's pass ordering:
// the index build (AT_PASS_ADD_INDEX, pass 3 — the 23505 dup check) runs BEFORE
// the phase-2 verify scan, so a table with BOTH a duplicate and a NULL reports
// the 23505, never the 23502.
func TestAlterTableAddPrimaryKeyDuplicateWinsOverNull(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE pk_both (a int)`); err != nil {
		t.Fatalf("CREATE TABLE pk_both: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO pk_both VALUES (1), (1), (NULL)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE pk_both ADD PRIMARY KEY (a)`),
		"23505", `could not create unique index "pk_both_pkey"`)
	if ee.Pos != 0 {
		t.Errorf("Pos = %d, want 0", ee.Pos)
	}
}

// TestAlterTableAddUniqueDuplicateDetail verifies the ADD UNIQUE twin raises the
// same 23505 + DETAIL over a duplicate.
func TestAlterTableAddUniqueDuplicateDetail(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE uq_dup (a int)`); err != nil {
		t.Fatalf("CREATE TABLE uq_dup: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO uq_dup VALUES (1), (1)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE uq_dup ADD UNIQUE (a)`),
		"23505", `could not create unique index`)
	if ee.Pos != 0 {
		t.Errorf("Pos = %d, want 0", ee.Pos)
	}
	if ee.Detail != "Key (a)=(1) is duplicated." {
		t.Errorf("Detail = %q, want %q", ee.Detail, "Key (a)=(1) is duplicated.")
	}
}

// TestAlterTableAddUniqueNullsAllowed verifies ADD UNIQUE does NOT run the 23502
// null scan: NULLs are distinct by default (NULLS DISTINCT), so duplicate NULL
// rows build fine — and unlike ADD PRIMARY KEY, no verify scan follows.
func TestAlterTableAddUniqueNullsAllowed(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE uq_null (a int)`); err != nil {
		t.Fatalf("CREATE TABLE uq_null: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO uq_null VALUES (NULL), (NULL)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE uq_null ADD UNIQUE (a)`); err != nil {
		t.Fatalf("ADD UNIQUE over NULLs should succeed (NULLS DISTINCT default): %v", err)
	}
}

// TestCreateUniqueIndexNullsNotDistinctDuplicateDetail verifies the NULLS NOT
// DISTINCT build's duplicate-NULL 23505 renders the key with NULL as the literal
// "null" (genam.c:246-247) and Pos=0.
func TestCreateUniqueIndexNullsNotDistinctDuplicateDetail(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nnd_dup (a int)`); err != nil {
		t.Fatalf("CREATE TABLE nnd_dup: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO nnd_dup VALUES (NULL), (NULL)`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	ee := requireExecError(t, runDDL(t, ctx, `CREATE UNIQUE INDEX nnd_dup_a ON nnd_dup (a) NULLS NOT DISTINCT`),
		"23505", `could not create unique index "nnd_dup_a"`)
	if ee.Pos != 0 {
		t.Errorf("Pos = %d, want 0", ee.Pos)
	}
	if ee.Detail != "Key (a)=(null) is duplicated." {
		t.Errorf("Detail = %q, want %q", ee.Detail, "Key (a)=(null) is duplicated.")
	}
}
