package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestAlterDomainOwnerTo guards the M0122-0005 domain follow-up: `ALTER
// DOMAIN ... OWNER TO` was previously wholly unparsed — the statement fell
// through to a discarded compat no-op, so typowner never changed and pg_dump
// always rendered the bootstrap superuser as owner. Mirrors
// TestAlterTypeOwnerTo/TestAlterTypeOwnerToRange.
func TestAlterDomainOwnerTo(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE DOMAIN ownertest_domain AS int`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}

	d, found := im.LookupDomain("ownertest_domain")
	if !found {
		t.Fatal("domain not found via LookupDomain")
	}
	if d.OwnerOrDefault() != 10 {
		t.Errorf("domain OwnerOrDefault() before OWNER TO = %d, want 10 (bootstrap superuser default)", d.OwnerOrDefault())
	}

	im.RegisterRole("domainowner")
	wantOID, found := im.RoleOID("domainowner")
	if !found {
		t.Fatal("RoleOID(\"domainowner\") not found after RegisterRole")
	}

	if err := runDDL(t, ctx, `ALTER DOMAIN ownertest_domain OWNER TO domainowner`); err != nil {
		t.Fatalf("ALTER DOMAIN ... OWNER TO: %v", err)
	}
	if d.OwnerOrDefault() != wantOID {
		t.Errorf("domain OwnerOrDefault() after OWNER TO = %d, want %d", d.OwnerOrDefault(), wantOID)
	}

	// A nonexistent domain raises 42704 rather than silently no-op'ing.
	err := runDDL(t, ctx, `ALTER DOMAIN nosuchdomain OWNER TO domainowner`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... OWNER TO on an unknown domain should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}

	// An unknown role also errors.
	err = runDDL(t, ctx, `ALTER DOMAIN ownertest_domain OWNER TO nosuchrole`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... OWNER TO an unknown role should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}

// TestAlterDomainRenameTo guards the same follow-up for RENAME TO: the
// domain's Base/Checks must survive the rename (RegisterDomain isn't
// re-invoked; RenameDomain re-keys the existing *Domain in place).
func TestAlterDomainRenameTo(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE DOMAIN renametest_domain AS text CHECK (VALUE <> '')`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER DOMAIN renametest_domain RENAME TO renameddomain`); err != nil {
		t.Fatalf("ALTER DOMAIN ... RENAME TO: %v", err)
	}
	if _, ok := im.LookupDomain("renametest_domain"); ok {
		t.Error("old domain name still resolves after RENAME TO")
	}
	d, found := im.LookupDomain("renameddomain")
	if !found {
		t.Fatal("renamed domain not found via LookupDomain")
	}
	if d.Base.Name != "text" {
		t.Errorf("Base.Name after rename = %q, want %q (rename must preserve base type)", d.Base.Name, "text")
	}
	if len(d.Checks) != 1 {
		t.Errorf("Checks after rename = %+v, want 1 CHECK preserved", d.Checks)
	}

	// A nonexistent domain raises 42710, mirroring RenameRangeType/RenameEnum.
	err := runDDL(t, ctx, `ALTER DOMAIN nosuchdomain RENAME TO whatever`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... RENAME TO on an unknown domain should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42710" {
		t.Errorf("err = %v, want *ExecError{Code: 42710}", err)
	}
}

// TestAlterDomainRenameConstraint guards the RENAME CONSTRAINT follow-up:
// `ALTER DOMAIN name RENAME CONSTRAINT old TO new` was previously wholly
// unparsed (fell into the same discarded compat no-op as RENAME TO/OWNER TO
// once had). Mirrors real PG's error codes: 42704 (undefined constraint,
// including an unknown domain) and 42710 (name collision with another CHECK
// on the same domain) — see rename_constraint_internal/RenameConstraintById
// in postgres/src/backend/commands/tablecmds.c and pg_constraint.c.
func TestAlterDomainRenameConstraint(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	// Two named CHECKs declared at CREATE time (multi-CHECK, DU-002 slice
	// 385) — ALTER DOMAIN ADD CONSTRAINT is a separate, not-yet-implemented
	// sub-form (see the deferral ledger row this loop closes against), so
	// the collision-target constraint must come from CREATE DOMAIN itself.
	if err := runDDL(t, ctx, `CREATE DOMAIN renamecontest_domain AS int CONSTRAINT positive_check CHECK (VALUE > 0) CONSTRAINT second_check CHECK (VALUE < 1000)`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	d, found := im.LookupDomain("renamecontest_domain")
	if !found {
		t.Fatal("domain not found via LookupDomain")
	}
	if len(d.Checks) != 2 {
		t.Fatalf("Checks after CREATE DOMAIN = %+v, want exactly 2", d.Checks)
	}
	origExpr := d.Checks[0].Expr

	if err := runDDL(t, ctx, `ALTER DOMAIN renamecontest_domain RENAME CONSTRAINT positive_check TO renamed_check`); err != nil {
		t.Fatalf("ALTER DOMAIN ... RENAME CONSTRAINT: %v", err)
	}
	if d.Checks[0].Name != "renamed_check" {
		t.Errorf("Checks after rename = %+v, want first CHECK named renamed_check", d.Checks)
	}
	if d.Checks[0].Expr != origExpr {
		t.Errorf("CHECK expression changed across constraint rename: got %q, want %q", d.Checks[0].Expr, origExpr)
	}
	if d.Checks[1].Name != "second_check" {
		t.Errorf("Checks after rename = %+v, sibling CHECK must be untouched", d.Checks)
	}

	// A nonexistent constraint on an existing domain raises 42704.
	err := runDDL(t, ctx, `ALTER DOMAIN renamecontest_domain RENAME CONSTRAINT nosuchcheck TO whatever`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... RENAME CONSTRAINT on an unknown constraint should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}

	// A nonexistent domain also raises 42704.
	err = runDDL(t, ctx, `ALTER DOMAIN nosuchdomain RENAME CONSTRAINT foo TO bar`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... RENAME CONSTRAINT on an unknown domain should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}

	// Renaming to an already-used name on the same domain raises 42710.
	err = runDDL(t, ctx, `ALTER DOMAIN renamecontest_domain RENAME CONSTRAINT second_check TO renamed_check`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... RENAME CONSTRAINT to a colliding name should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42710" {
		t.Errorf("err = %v, want *ExecError{Code: 42710}", err)
	}
}

// TestAlterDomainAddConstraint guards the ADD CONSTRAINT follow-up named in
// TestAlterDomainRenameConstraint's comment above: `ALTER DOMAIN name ADD
// [CONSTRAINT name] CHECK (expr)` previously fell into the discarded
// compat no-op alongside every other unmodelled ALTER DOMAIN form. Mirrors
// real PG's AlterDomainAddConstraint/domainAddCheckConstraint: an explicit
// name colliding with an existing CHECK on the same domain is 42710; an
// unknown domain is 42704.
func TestAlterDomainAddConstraint(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE DOMAIN addconstest_domain AS int`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	d, found := im.LookupDomain("addconstest_domain")
	if !found {
		t.Fatal("domain not found via LookupDomain")
	}
	if len(d.Checks) != 0 {
		t.Fatalf("Checks before ADD CONSTRAINT = %+v, want none", d.Checks)
	}

	// Named CHECK.
	if err := runDDL(t, ctx, `ALTER DOMAIN addconstest_domain ADD CONSTRAINT positive_check CHECK (VALUE > 0)`); err != nil {
		t.Fatalf("ALTER DOMAIN ... ADD CONSTRAINT: %v", err)
	}
	if len(d.Checks) != 1 || d.Checks[0].Name != "positive_check" || d.Checks[0].Expr != "VALUE > 0" {
		t.Errorf("Checks after named ADD CONSTRAINT = %+v, want [{positive_check VALUE > 0}]", d.Checks)
	}

	// Unnamed CHECK gets PG's auto-generated `<domain>_check` name.
	if err := runDDL(t, ctx, `ALTER DOMAIN addconstest_domain ADD CHECK (VALUE < 1000)`); err != nil {
		t.Fatalf("ALTER DOMAIN ... ADD CHECK: %v", err)
	}
	if len(d.Checks) != 2 || d.Checks[1].Name != "addconstest_domain_check" {
		t.Errorf("Checks after unnamed ADD CHECK = %+v, want second entry named addconstest_domain_check", d.Checks)
	}

	// CHECK (VALUE IN (...)) shortcut form synthesizes the ScalarArrayOpExpr
	// text, same as CREATE DOMAIN's own IN-list handling.
	if err := runDDL(t, ctx, `ALTER DOMAIN addconstest_domain ADD CONSTRAINT allowed_check CHECK (VALUE IN (1, 2, 3))`); err != nil {
		t.Fatalf("ALTER DOMAIN ... ADD CONSTRAINT ... IN: %v", err)
	}
	if len(d.Checks) != 3 || d.Checks[2].Expr != "VALUE = ANY (ARRAY[1, 2, 3])" {
		t.Errorf("Checks after IN-list ADD CONSTRAINT = %+v, want third entry's Expr = VALUE = ANY (ARRAY[1, 2, 3])", d.Checks)
	}

	// NOT VALID is parsed and discarded (no existing-data validation either way).
	if err := runDDL(t, ctx, `ALTER DOMAIN addconstest_domain ADD CONSTRAINT skip_check CHECK (VALUE <> 0) NOT VALID`); err != nil {
		t.Fatalf("ALTER DOMAIN ... ADD CONSTRAINT ... NOT VALID: %v", err)
	}
	if len(d.Checks) != 4 || d.Checks[3].Name != "skip_check" {
		t.Errorf("Checks after NOT VALID ADD CONSTRAINT = %+v, want fourth entry named skip_check", d.Checks)
	}

	// A colliding explicit name raises 42710.
	err := runDDL(t, ctx, `ALTER DOMAIN addconstest_domain ADD CONSTRAINT positive_check CHECK (VALUE > 5)`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... ADD CONSTRAINT with a colliding name should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42710" {
		t.Errorf("err = %v, want *ExecError{Code: 42710}", err)
	}

	// An unknown domain raises 42704.
	err = runDDL(t, ctx, `ALTER DOMAIN nosuchdomain ADD CONSTRAINT c CHECK (VALUE > 0)`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... ADD CONSTRAINT on an unknown domain should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}

// TestAlterDomainDropConstraint guards the DROP CONSTRAINT follow-up:
// `ALTER DOMAIN name DROP CONSTRAINT [IF EXISTS] name [RESTRICT|CASCADE]`.
// Mirrors real PG's AlterDomainDropConstraint: an unknown constraint without
// IF EXISTS is 42704; IF EXISTS silently no-ops instead.
func TestAlterDomainDropConstraint(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE DOMAIN dropconstest_domain AS int CONSTRAINT positive_check CHECK (VALUE > 0) CONSTRAINT second_check CHECK (VALUE < 1000)`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	d, found := im.LookupDomain("dropconstest_domain")
	if !found {
		t.Fatal("domain not found via LookupDomain")
	}
	if len(d.Checks) != 2 {
		t.Fatalf("Checks after CREATE DOMAIN = %+v, want exactly 2", d.Checks)
	}

	if err := runDDL(t, ctx, `ALTER DOMAIN dropconstest_domain DROP CONSTRAINT positive_check`); err != nil {
		t.Fatalf("ALTER DOMAIN ... DROP CONSTRAINT: %v", err)
	}
	if len(d.Checks) != 1 || d.Checks[0].Name != "second_check" {
		t.Errorf("Checks after DROP CONSTRAINT = %+v, want only second_check left", d.Checks)
	}

	// RESTRICT/CASCADE trailer is accepted.
	if err := runDDL(t, ctx, `ALTER DOMAIN dropconstest_domain DROP CONSTRAINT second_check RESTRICT`); err != nil {
		t.Fatalf("ALTER DOMAIN ... DROP CONSTRAINT ... RESTRICT: %v", err)
	}
	if len(d.Checks) != 0 {
		t.Errorf("Checks after second DROP CONSTRAINT = %+v, want none left", d.Checks)
	}

	// An unknown constraint without IF EXISTS raises 42704.
	err := runDDL(t, ctx, `ALTER DOMAIN dropconstest_domain DROP CONSTRAINT nosuchcheck`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... DROP CONSTRAINT on an unknown constraint should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}

	// IF EXISTS silently no-ops instead.
	if err := runDDL(t, ctx, `ALTER DOMAIN dropconstest_domain DROP CONSTRAINT IF EXISTS nosuchcheck`); err != nil {
		t.Errorf("ALTER DOMAIN ... DROP CONSTRAINT IF EXISTS on an unknown constraint should not error: %v", err)
	}

	// An unknown domain raises 42704 regardless of IF EXISTS (mirrors real PG:
	// missing_ok only covers the named constraint, not the domain lookup).
	err = runDDL(t, ctx, `ALTER DOMAIN nosuchdomain DROP CONSTRAINT IF EXISTS c`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... DROP CONSTRAINT on an unknown domain should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}

// TestAlterDomainSetDropDefault guards the SET/DROP DEFAULT follow-up: `ALTER
// DOMAIN name SET DEFAULT expr` / `ALTER DOMAIN name DROP DEFAULT`. Mirrors
// CREATE DOMAIN's own DEFAULT clause (same parseExpr + DefaultBin render
// path), and TestAlterDomainAddConstraint/TestAlterDomainDropConstraint's
// error-code conventions.
func TestAlterDomainSetDropDefault(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE DOMAIN setdefaulttest_domain AS int`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	d, found := im.LookupDomain("setdefaulttest_domain")
	if !found {
		t.Fatal("domain not found via LookupDomain")
	}
	if d.Default != nil {
		t.Fatalf("Default before SET DEFAULT = %v, want nil", d.Default)
	}

	if err := runDDL(t, ctx, `ALTER DOMAIN setdefaulttest_domain SET DEFAULT 42`); err != nil {
		t.Fatalf("ALTER DOMAIN ... SET DEFAULT: %v", err)
	}
	if d.Default == nil || d.DefaultBin() != "42" {
		t.Errorf("DefaultBin() after SET DEFAULT = %q, want 42", d.DefaultBin())
	}

	// A later SET DEFAULT replaces the prior expression outright.
	if err := runDDL(t, ctx, `ALTER DOMAIN setdefaulttest_domain SET DEFAULT 7`); err != nil {
		t.Fatalf("ALTER DOMAIN ... SET DEFAULT (replace): %v", err)
	}
	if d.DefaultBin() != "7" {
		t.Errorf("DefaultBin() after replacing SET DEFAULT = %q, want 7", d.DefaultBin())
	}

	if err := runDDL(t, ctx, `ALTER DOMAIN setdefaulttest_domain DROP DEFAULT`); err != nil {
		t.Fatalf("ALTER DOMAIN ... DROP DEFAULT: %v", err)
	}
	if d.Default != nil {
		t.Errorf("Default after DROP DEFAULT = %v, want nil", d.Default)
	}

	// An unknown domain raises 42704 for both forms.
	err := runDDL(t, ctx, `ALTER DOMAIN nosuchdomain SET DEFAULT 1`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... SET DEFAULT on an unknown domain should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
	err = runDDL(t, ctx, `ALTER DOMAIN nosuchdomain DROP DEFAULT`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... DROP DEFAULT on an unknown domain should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}

// TestAlterDomainUnmodelledFormsAreNoop guards a regression discovered while
// implementing SET/DROP DEFAULT: the parser's shared "unmodelled ALTER
// DOMAIN form" fallback (SET SCHEMA — the last form goopg doesn't track yet)
// used to return a bare, nameless *parser.AlterTableStmt, which the
// executor's generic ALTER TABLE path tried to resolve as a relation lookup
// and rejected with a spurious 42P01 "relation \"\" does not exist" — even
// though the statement was meant to be a harmless no-op, same as every other
// not-yet-modelled ALTER ... tail in this file. The same fallback shape
// (return &AlterTableStmt{pos} with no Name) existed at 12 sites across
// internal/parser/ddl.go for other ALTER object kinds; this test pins the
// ALTER DOMAIN instance of the fix (routing through CompatNoopStmt instead,
// which short-circuits before any lookup). SET/DROP NOT NULL used to be
// covered here too, until the SET/DROP NOT NULL follow-up gave them real
// behavior — see TestAlterDomainSetDropNotNull.
func TestAlterDomainUnmodelledFormsAreNoop(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if _, ok := cat.(*catalog.InMemory); !ok {
		t.Fatal("catalog is not *InMemory")
	}
	if err := runDDL(t, ctx, `CREATE DOMAIN unmodelledtest_domain AS int`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}

	for _, sql := range []string{
		`ALTER DOMAIN unmodelledtest_domain SET SCHEMA other_schema`,
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Errorf("%s: got error %v, want no-op success", sql, err)
		}
	}
}

// TestAlterDomainSetDropNotNull guards the SET/DROP NOT NULL follow-up:
// `ALTER DOMAIN name SET NOT NULL` / `ALTER DOMAIN name DROP NOT NULL`.
// Mirrors real PG's AlterDomainNotNull (toggles typnotnull) and
// TestAlterDomainSetDropDefault's error-code conventions. Unlike real PG,
// SET NOT NULL does not scan existing table columns of this domain type for
// already-present NULL values — see SetDomainNotNull's doc comment — so this
// test only asserts the flag toggle and its effect on freshly-inserted rows,
// not a validation scan over pre-existing data.
func TestAlterDomainSetDropNotNull(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE DOMAIN setnotnulltest_domain AS int`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	d, found := im.LookupDomain("setnotnulltest_domain")
	if !found {
		t.Fatal("domain not found via LookupDomain")
	}
	if d.NotNull {
		t.Fatal("NotNull before SET NOT NULL = true, want false")
	}

	if err := runDDL(t, ctx, `ALTER DOMAIN setnotnulltest_domain SET NOT NULL`); err != nil {
		t.Fatalf("ALTER DOMAIN ... SET NOT NULL: %v", err)
	}
	if !d.NotNull {
		t.Error("NotNull after SET NOT NULL = false, want true")
	}

	if err := runDDL(t, ctx, `ALTER DOMAIN setnotnulltest_domain DROP NOT NULL`); err != nil {
		t.Fatalf("ALTER DOMAIN ... DROP NOT NULL: %v", err)
	}
	if d.NotNull {
		t.Error("NotNull after DROP NOT NULL = true, want false")
	}

	// DROP NOT NULL on a domain that isn't NOT NULL is a harmless no-op.
	if err := runDDL(t, ctx, `ALTER DOMAIN setnotnulltest_domain DROP NOT NULL`); err != nil {
		t.Errorf("ALTER DOMAIN ... DROP NOT NULL (already false) should not error: %v", err)
	}

	// An unknown domain raises 42704 for both forms.
	err := runDDL(t, ctx, `ALTER DOMAIN nosuchdomain SET NOT NULL`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... SET NOT NULL on an unknown domain should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
	err = runDDL(t, ctx, `ALTER DOMAIN nosuchdomain DROP NOT NULL`)
	if err == nil {
		t.Fatal("ALTER DOMAIN ... DROP NOT NULL on an unknown domain should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}
