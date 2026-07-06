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
