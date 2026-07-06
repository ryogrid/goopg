package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestAlterTypeOwnerTo guards the m0097-0017 follow-up (M0122-0005): ALTER
// TYPE ... OWNER TO was a complete no-op (parsed as a stub with no captured
// role, never touching typowner) for both enum and composite types. Mirrors
// TestAlterCollationRenameOwnerRefresh's OWNER TO coverage.
func TestAlterTypeOwnerTo(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TYPE ownertest_enum AS ENUM ('a', 'b')`); err != nil {
		t.Fatalf("CREATE TYPE ... AS ENUM: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TYPE ownertest_comp AS (a int, b text)`); err != nil {
		t.Fatalf("CREATE TYPE ... AS (composite): %v", err)
	}

	et, found := im.LookupEnum("ownertest_enum")
	if !found {
		t.Fatal("enum not found via LookupEnum")
	}
	if et.OwnerOrDefault() != 10 {
		t.Errorf("enum OwnerOrDefault() before OWNER TO = %d, want 10 (bootstrap superuser default)", et.OwnerOrDefault())
	}
	ct := im.LookupCompositeType("ownertest_comp")
	if ct == nil {
		t.Fatal("composite type not found via LookupCompositeType")
	}
	if ct.OwnerOrDefault() != 10 {
		t.Errorf("composite OwnerOrDefault() before OWNER TO = %d, want 10 (bootstrap superuser default)", ct.OwnerOrDefault())
	}

	im.RegisterRole("typeowner")
	wantOID, found := im.RoleOID("typeowner")
	if !found {
		t.Fatal("RoleOID(\"typeowner\") not found after RegisterRole")
	}

	if err := runDDL(t, ctx, `ALTER TYPE ownertest_enum OWNER TO typeowner`); err != nil {
		t.Fatalf("ALTER TYPE ... OWNER TO (enum): %v", err)
	}
	if et.OwnerOrDefault() != wantOID {
		t.Errorf("enum OwnerOrDefault() after OWNER TO = %d, want %d", et.OwnerOrDefault(), wantOID)
	}

	if err := runDDL(t, ctx, `ALTER TYPE ownertest_comp OWNER TO typeowner`); err != nil {
		t.Fatalf("ALTER TYPE ... OWNER TO (composite): %v", err)
	}
	if ct.OwnerOrDefault() != wantOID {
		t.Errorf("composite OwnerOrDefault() after OWNER TO = %d, want %d", ct.OwnerOrDefault(), wantOID)
	}

	// A nonexistent type raises 42704 rather than silently no-op'ing.
	err := runDDL(t, ctx, `ALTER TYPE nosuchtype OWNER TO typeowner`)
	if err == nil {
		t.Fatal("ALTER TYPE ... OWNER TO on an unknown type should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}

	// An unknown role also errors.
	err = runDDL(t, ctx, `ALTER TYPE ownertest_enum OWNER TO nosuchrole`)
	if err == nil {
		t.Fatal("ALTER TYPE ... OWNER TO an unknown role should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}

// TestAlterTypeRenameToComposite guards the same follow-up: ALTER TYPE
// <composite> RENAME TO always called catalog.RenameEnum regardless of the
// target's kind, so renaming a composite type raised a spurious "type does
// not exist" (42710) instead of renaming it.
func TestAlterTypeRenameToComposite(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TYPE renametest_comp AS (x int)`); err != nil {
		t.Fatalf("CREATE TYPE ... AS (composite): %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TYPE renametest_comp RENAME TO renamedcomp`); err != nil {
		t.Fatalf("ALTER TYPE ... RENAME TO (composite): %v", err)
	}
	if ct := im.LookupCompositeType("renametest_comp"); ct != nil {
		t.Error("old composite type name still resolves after RENAME TO")
	}
	ct := im.LookupCompositeType("renamedcomp")
	if ct == nil {
		t.Fatal("renamed composite type not found via LookupCompositeType")
	}
	if len(ct.Fields) != 1 || ct.Fields[0].Name != "x" {
		t.Errorf("Fields after rename = %+v, want [{x ...}] (rename must preserve fields)", ct.Fields)
	}
}

// TestAlterTypeOwnerToRange guards the range-type follow-up to M0122-0005:
// `ALTER TYPE <range> OWNER TO` previously fell through to SetEnumOwner (the
// final fallback in execAlterType's OWNER TO branch), which raised a spurious
// 42704 "type does not exist" for a range type even though it does exist —
// the same dispatch-by-kind gap TestAlterTypeOwnerTo's composite case closed
// earlier. Mirrors TestAlterTypeOwnerTo but for catalog.RangeType.
func TestAlterTypeOwnerToRange(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TYPE ownertest_range AS RANGE (subtype = int4)`); err != nil {
		t.Fatalf("CREATE TYPE ... AS RANGE: %v", err)
	}

	rt, found := im.LookupRangeType("ownertest_range")
	if !found {
		t.Fatal("range type not found via LookupRangeType")
	}
	if rt.OwnerOrDefault() != 10 {
		t.Errorf("range OwnerOrDefault() before OWNER TO = %d, want 10 (bootstrap superuser default)", rt.OwnerOrDefault())
	}

	im.RegisterRole("rangeowner")
	wantOID, found := im.RoleOID("rangeowner")
	if !found {
		t.Fatal("RoleOID(\"rangeowner\") not found after RegisterRole")
	}

	if err := runDDL(t, ctx, `ALTER TYPE ownertest_range OWNER TO rangeowner`); err != nil {
		t.Fatalf("ALTER TYPE ... OWNER TO (range): %v", err)
	}
	if rt.OwnerOrDefault() != wantOID {
		t.Errorf("range OwnerOrDefault() after OWNER TO = %d, want %d", rt.OwnerOrDefault(), wantOID)
	}
}

// TestAlterTypeRenameToRange guards the same range-type follow-up: `ALTER
// TYPE <range> RENAME TO` previously always called catalog.RenameEnum
// regardless of kind, so renaming a range type raised a spurious "type does
// not exist" (42710) instead of renaming it — the same bug class
// TestAlterTypeRenameToComposite closed for composite types.
func TestAlterTypeRenameToRange(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TYPE renametest_range AS RANGE (subtype = int4)`); err != nil {
		t.Fatalf("CREATE TYPE ... AS RANGE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TYPE renametest_range RENAME TO renamedrange`); err != nil {
		t.Fatalf("ALTER TYPE ... RENAME TO (range): %v", err)
	}
	if _, ok := im.LookupRangeType("renametest_range"); ok {
		t.Error("old range type name still resolves after RENAME TO")
	}
	rt, found := im.LookupRangeType("renamedrange")
	if !found {
		t.Fatal("renamed range type not found via LookupRangeType")
	}
	if rt.SubtypeName != "int4" {
		t.Errorf("SubtypeName after rename = %q, want %q (rename must preserve subtype)", rt.SubtypeName, "int4")
	}
}
