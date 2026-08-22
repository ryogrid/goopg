package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// CREATE UNLOGGED SEQUENCE / ALTER SEQUENCE ... SET LOGGED|UNLOGGED —
// M0134-0069 (sequence.sql sequence_test_unlogged sub-block). Two distinct
// bugs are pinned here:
//
//  1. CREATE UNLOGGED SEQUENCE was misrouted into the parser's `temp bool`
//     param (internal/parser/ddl.go, parseCreateSequenceTail), which made
//     execCreateSequence call SetSequenceTemporary — wrong on both axes
//     (relpersistence AND session-temp-ness). Unlogged and Temporary are
//     orthogonal: this test asserts the fixed sequence is Unlogged=true
//     (relpersistence 'u') and IsSequenceTemporary()==false.
//  2. ALTER SEQUENCE ... SET LOGGED/UNLOGGED was parsed then discarded
//     (no AST field to carry it). This test asserts it flips
//     catalog.Table.Unlogged both directions.
//
// PG oracle: postgres/src/backend/commands/sequence.c DefineSequence /
// AlterSequence (relpersistence threading); pg_class.relpersistence is the
// observable both \d and pg_dump key off.
// Fixture: postgres/src/test/regress/sql/sequence.sql:276-280.

func TestCreateUnloggedSequence(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE UNLOGGED SEQUENCE seq_unlogged_1"); err != nil {
		t.Fatalf("create unlogged sequence: %v", err)
	}

	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "seq_unlogged_1"}, ctxSeqDBOid(ctx))
	if !ok || tbl == nil {
		t.Fatal("catalog lookup for seq_unlogged_1 failed")
	}
	if !tbl.Unlogged {
		t.Fatal("expected seq_unlogged_1.Unlogged == true (relpersistence 'u'), got false")
	}
	if IsSequenceTemporary("seq_unlogged_1", ctxSeqDBOid(ctx)) {
		t.Fatal("CREATE UNLOGGED SEQUENCE must not register a session-temporary sequence")
	}
}

func TestAlterSequenceSetLoggedUnlogged(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE UNLOGGED SEQUENCE seq_unlogged_2"); err != nil {
		t.Fatalf("create unlogged sequence: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "seq_unlogged_2"}, ctxSeqDBOid(ctx))
	if !ok || tbl == nil {
		t.Fatal("catalog lookup for seq_unlogged_2 failed")
	}
	if !tbl.Unlogged {
		t.Fatal("expected seq_unlogged_2.Unlogged == true before SET LOGGED")
	}

	if err := runDDL(t, ctx, "ALTER SEQUENCE seq_unlogged_2 SET LOGGED"); err != nil {
		t.Fatalf("alter sequence set logged: %v", err)
	}
	if tbl.Unlogged {
		t.Fatal("expected seq_unlogged_2.Unlogged == false after SET LOGGED")
	}

	if err := runDDL(t, ctx, "ALTER SEQUENCE seq_unlogged_2 SET UNLOGGED"); err != nil {
		t.Fatalf("alter sequence set unlogged: %v", err)
	}
	if !tbl.Unlogged {
		t.Fatal("expected seq_unlogged_2.Unlogged == true after SET UNLOGGED")
	}
}
