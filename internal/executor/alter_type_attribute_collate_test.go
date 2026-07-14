package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestAlterTypeAlterAttributeCollateApplied guards the M0110-0001
// unimplemented_feat.json entry (verified stale/resolved 2026-07-10,
// M0122-0022): "COLLATE and USING clauses in ALTER TYPE … ALTER ATTRIBUTE
// are accepted by the parser but their effects are ignored during
// execution." COLLATE is in fact applied — both the single-subcommand path
// (execAlterType, operators_ddl.go ~18776) and the multi-subcommand path
// (execAlterTypeAttrCmds, operators_ddl.go ~18990) copy AlterAttrCollation
// into the composite field's Collation, which buildUserPGAttributeRowForCompositeField
// then writes into pg_attribute.attcollation for pg_dump round-trip. USING is
// not part of this statement's real PG grammar at all (only COLLATE and
// CASCADE|RESTRICT — see postgres/src/backend/parser/gram.y's
// `ALTER ATTRIBUTE ColId opt_set_data TYPE_P Typename opt_collate_clause
// opt_drop_behavior`), so there is nothing to "ignore" there.
func TestAlterTypeAlterAttributeCollateApplied(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TYPE aac_single AS (a int, b text COLLATE "C")`); err != nil {
		t.Fatalf("CREATE TYPE: %v", err)
	}

	// Single-subcommand form: re-type with an explicit COLLATE.
	if err := runDDL(t, ctx, `ALTER TYPE aac_single ALTER ATTRIBUTE b TYPE varchar(8) COLLATE "POSIX"`); err != nil {
		t.Fatalf("ALTER TYPE ... ALTER ATTRIBUTE ... COLLATE: %v", err)
	}
	ct := im.LookupCompositeType("aac_single")
	if ct == nil {
		t.Fatal("composite type not found via LookupCompositeType")
	}
	if got := ct.Fields[1].Collation; got != "POSIX" {
		t.Errorf("field[1].Collation after ALTER ATTRIBUTE ... COLLATE \"POSIX\" = %q, want \"POSIX\"", got)
	}

	// Re-typing without an explicit COLLATE resets to the new type's default
	// (empty), matching the code's documented behavior: "the prior collation
	// no longer applies".
	if err := runDDL(t, ctx, `ALTER TYPE aac_single ALTER ATTRIBUTE b TYPE text`); err != nil {
		t.Fatalf("ALTER TYPE ... ALTER ATTRIBUTE (no COLLATE): %v", err)
	}
	ct = im.LookupCompositeType("aac_single")
	if got := ct.Fields[1].Collation; got != "" {
		t.Errorf("field[1].Collation after re-type without COLLATE = %q, want \"\" (reset)", got)
	}

	// Multi-subcommand form exercises the sibling execAlterTypeAttrCmds path.
	if err := runDDL(t, ctx, `CREATE TYPE aac_multi AS (a int, b text)`); err != nil {
		t.Fatalf("CREATE TYPE (multi): %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TYPE aac_multi ADD ATTRIBUTE c text, ALTER ATTRIBUTE b TYPE varchar(16) COLLATE "C"`); err != nil {
		t.Fatalf("ALTER TYPE (multi-subcommand) ... COLLATE: %v", err)
	}
	ct = im.LookupCompositeType("aac_multi")
	if ct == nil {
		t.Fatal("composite type not found via LookupCompositeType (multi)")
	}
	if got := ct.Fields[1].Collation; got != "C" {
		t.Errorf("multi-subcommand field[1].Collation = %q, want \"C\"", got)
	}
}
