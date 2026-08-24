package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCreateForeignDataWrapperDuplicateErrors pins the M0134-0124 fix to
// CREATE FOREIGN DATA WRAPPER's missing already-exists check
// (postgres/src/backend/commands/foreigncmds.c ~596-603): a second
// `CREATE FOREIGN DATA WRAPPER <same name>` must raise 42710
// "foreign-data wrapper %q already exists" instead of silently
// re-registering (the registry's own RegisterForeignDataWrapper is
// deliberately idempotent for internal/recovery callers, so the guard has
// to live at the exec call site).
func TestCreateForeignDataWrapperDuplicateErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FOREIGN DATA WRAPPER dupfdw`); err != nil {
		t.Fatalf("first CREATE FOREIGN DATA WRAPPER: %v", err)
	}
	err := runDDL(t, ctx, `CREATE FOREIGN DATA WRAPPER dupfdw`)
	if err == nil {
		t.Fatal("expected duplicate CREATE FOREIGN DATA WRAPPER to error")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "42710" {
		t.Errorf("Code = %q, want 42710", ee.Code)
	}
	if want := `foreign-data wrapper "dupfdw" already exists`; ee.Message != want {
		t.Errorf("Message = %q, want %q", ee.Message, want)
	}
}

// TestCreateForeignDataWrapperSuperuserCheck pins the M0134-0124 fix adding
// CreateForeignDataWrapper's missing superuser check
// (postgres/src/backend/commands/foreigncmds.c ~586-591). Unlike the
// LEAKPROOF/event-trigger sibling checks (which treat any non-"postgres"
// NonSuperuserRole as non-superuser — a known convention gap tracked in the
// M0134-0124 deferral-ledger row), this checks the role's actual SUPERUSER
// attribute via catalog.IsSuperuser, so a `SET SESSION AUTHORIZATION` to a
// role created with `CREATE ROLE ... SUPERUSER` is correctly still
// permitted, matching PG's own semantics (only a non-superuser role is
// denied).
func TestCreateForeignDataWrapperSuperuserCheck(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("expected *catalog.InMemory")
	}

	im.RegisterRole("fdw_super")
	im.SetRoleAttrs("fdw_super", catalog.RoleAttrs{Superuser: true})
	im.RegisterRole("fdw_nonsuper")

	// A CREATE-ROLE-...-SUPERUSER role (not the literal "postgres") must
	// still be allowed to create an FDW.
	ctx.NonSuperuserRole = "fdw_super"
	if err := runDDL(t, ctx, `CREATE FOREIGN DATA WRAPPER superfdw`); err != nil {
		t.Fatalf("CREATE FOREIGN DATA WRAPPER as SUPERUSER role: %v", err)
	}
	if _, found := im.LookupForeignDataWrapper("superfdw"); !found {
		t.Fatal("superfdw not registered")
	}

	// A genuinely non-superuser role must be denied with 42501 + HINT.
	ctx.NonSuperuserRole = "fdw_nonsuper"
	err := runDDL(t, ctx, `CREATE FOREIGN DATA WRAPPER nonsuperfdw`)
	if err == nil {
		t.Fatal("expected permission-denied error for non-superuser role")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "42501" {
		t.Errorf("Code = %q, want 42501", ee.Code)
	}
	if want := `permission denied to create foreign-data wrapper "nonsuperfdw"`; ee.Message != want {
		t.Errorf("Message = %q, want %q", ee.Message, want)
	}
	if ee.Hint != "Must be superuser to create a foreign-data wrapper." {
		t.Errorf("Hint = %q", ee.Hint)
	}
	if _, found := im.LookupForeignDataWrapper("nonsuperfdw"); found {
		t.Fatal("nonsuperfdw must not have been registered")
	}
}
