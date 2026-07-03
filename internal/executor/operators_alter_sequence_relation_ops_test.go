package executor

// operators_alter_sequence_relation_ops_test.go pins the DU-002 slice 439 fix:
// `ALTER SEQUENCE name RENAME TO / OWNER TO / SET SCHEMA` previously had no
// parser case at all — the SeqOptList option loop's default case returned
// immediately, leaving the RENAME/OWNER/SET tokens unconsumed and surfacing
// as a bare syntax error at the top-level statement parser. Real PostgreSQL
// treats a sequence as an ordinary relation for these three forms
// (RenameRelation / AlterTableOwner / AlterTableNamespace,
// postgres/src/backend/commands/tablecmds.c + sequence.c), so they now reuse
// the exact executor path ALTER TABLE already uses (AlterTableStmt /
// execAlterTable), including the tbl.IsSequence sequence-registry cascade
// RENAME already had (extended here to SET SCHEMA too, since the registry
// key is schema-qualified).

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func TestAlterSequenceRenameTo(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE SEQUENCE seqx"); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER SEQUENCE seqx RENAME TO seqy"); err != nil {
		t.Fatalf("ALTER SEQUENCE RENAME TO: %v", err)
	}
	if _, ok := cat.LookupTable(parser.ObjectName{Name: "seqy"}); !ok {
		t.Fatalf("catalog has no relation named seqy after rename")
	}
	if _, ok := cat.LookupTable(parser.ObjectName{Name: "seqx"}); ok {
		t.Errorf("catalog still has the old relation name seqx after rename")
	}
	if LookupSequence("seqx") != nil {
		t.Errorf("sequence registry still resolves the old name seqx after rename")
	}
	if LookupSequence("seqy") == nil {
		t.Fatalf("sequence registry does not resolve the new name seqy after rename")
	}
	rows := runQueryRows(t, ctx, "SELECT nextval('seqy')")
	if len(rows) != 1 {
		t.Fatalf("nextval('seqy') row count = %d, want 1", len(rows))
	}
}

func TestAlterSequenceOwnerTo(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE SEQUENCE seqown"); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER SEQUENCE seqown OWNER TO alice"); err != nil {
		t.Fatalf("ALTER SEQUENCE OWNER TO: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "seqown"})
	if !ok {
		t.Fatalf("catalog lost the seqown relation after OWNER TO")
	}
	if tbl.Owner != "alice" {
		t.Errorf("Owner = %q, want %q", tbl.Owner, "alice")
	}
}

func TestAlterSequenceOwnerToCurrentUser(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE SEQUENCE seqown2"); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER SEQUENCE seqown2 OWNER TO CURRENT_USER"); err != nil {
		t.Fatalf("ALTER SEQUENCE OWNER TO CURRENT_USER: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "seqown2"})
	if !ok {
		t.Fatalf("catalog lost the seqown2 relation after OWNER TO CURRENT_USER")
	}
	if tbl.Owner != "" {
		t.Errorf("Owner = %q, want %q (bootstrap superuser sentinel)", tbl.Owner, "")
	}
}

func TestAlterSequenceSetSchema(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE SCHEMA sch1"); err != nil {
		t.Fatalf("CREATE SCHEMA: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE SEQUENCE seqsch"); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER SEQUENCE seqsch SET SCHEMA sch1"); err != nil {
		t.Fatalf("ALTER SEQUENCE SET SCHEMA: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "sch1", Name: "seqsch"})
	if !ok {
		t.Fatalf("catalog lost the seqsch relation after SET SCHEMA")
	}
	if tbl.Schema != "sch1" {
		t.Errorf("Schema = %q, want %q", tbl.Schema, "sch1")
	}
	rows := runQueryRows(t, ctx, "SELECT nextval('sch1.seqsch')")
	if len(rows) != 1 {
		t.Fatalf("nextval('sch1.seqsch') row count = %d, want 1", len(rows))
	}
	if LookupSequence("public.seqsch") != nil {
		t.Errorf("sequence registry still resolves the old schema-qualified name after SET SCHEMA")
	}
}

// TestAlterSequenceRenameOwnerSchemaNotCombinedWithOptions confirms these
// three forms are parsed as their own top-level statement (mirroring PG's
// grammar, where RENAME TO / OWNER TO / SET SCHEMA are distinct productions
// from the ALTER SEQUENCE SeqOptList) rather than folded into the option
// loop — a plain option-only ALTER SEQUENCE must still work unaffected.
func TestAlterSequenceRenameOwnerSchemaNotCombinedWithOptions(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE SEQUENCE seqopt"); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER SEQUENCE seqopt INCREMENT BY 2 RESTART WITH 10"); err != nil {
		t.Fatalf("ALTER SEQUENCE (options only): %v", err)
	}
	if err := runDDL(t, ctx, "ALTER SEQUENCE seqopt SET UNLOGGED"); err != nil {
		t.Fatalf("ALTER SEQUENCE SET UNLOGGED (unrelated SET form must still work): %v", err)
	}
}
