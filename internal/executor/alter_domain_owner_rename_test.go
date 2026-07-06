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
