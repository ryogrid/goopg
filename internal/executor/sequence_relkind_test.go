package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// Sequence wrong-relkind guard — M0134-0069 bucket 6 item 5. PostgreSQL
// distinguishes "relation doesn't exist" (42P01) from "relation exists but
// isn't a sequence" (42809) for nextval/currval/setval/lastval/ALTER
// SEQUENCE/DROP SEQUENCE. goopg previously misrouted an existing non-sequence
// relation into the generic not-found path (or, for setval, silently
// fabricated a phantom sequence). Two distinct PG message templates:
//
//   - nextval/currval/setval/ALTER SEQUENCE: sequence_open ->
//     validate_relation_kind, ERRCODE_WRONG_OBJECT_TYPE (42809),
//     errmsg("cannot open relation \"%s\"", name),
//     errdetail_relkind_not_supported(relkind) for DETAIL.
//     postgres/src/backend/access/sequence/sequence.c:37-46,66-77
//     postgres/src/backend/catalog/pg_class.c:24-52
//   - DROP SEQUENCE: get_object_address_relobject, OBJECT_SEQUENCE case,
//     ERRCODE_WRONG_OBJECT_TYPE (42809), errmsg("\"%s\" is not a sequence",
//     name), no DETAIL. postgres/src/backend/catalog/objectaddress.c:1362-1368
//
// Fixture: postgres/src/test/regress/sql/sequence.sql line 176
// ("ALTER SEQUENCE serialTest1 CYCLE;  -- error, not a sequence").

func TestNextvalOnTableWrongRelkind(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE seq_relkind_tbl1 (id int)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	_, err := runSQLCtxErr(t, ctx, "SELECT nextval('seq_relkind_tbl1')")
	if err == nil {
		t.Fatal("expected 42809, got nil")
	}
	ee := mustExecErr(t, err)
	if ee.Code != "42809" {
		t.Fatalf("expected 42809, got %s (%v)", ee.Code, ee)
	}
	if want := `cannot open relation "seq_relkind_tbl1"`; ee.Message != want {
		t.Fatalf("expected message %q, got %q", want, ee.Message)
	}
	if want := "This operation is not supported for tables."; ee.Detail != want {
		t.Fatalf("expected detail %q, got %q", want, ee.Detail)
	}
}

func TestCurrvalOnTableWrongRelkind(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE seq_relkind_tbl2 (id int)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	_, err := runSQLCtxErr(t, ctx, "SELECT currval('seq_relkind_tbl2')")
	if err == nil {
		t.Fatal("expected 42809, got nil")
	}
	ee := mustExecErr(t, err)
	if ee.Code != "42809" {
		t.Fatalf("expected 42809, got %s (%v)", ee.Code, ee)
	}
	if want := `cannot open relation "seq_relkind_tbl2"`; ee.Message != want {
		t.Fatalf("expected message %q, got %q", want, ee.Message)
	}
	if want := "This operation is not supported for tables."; ee.Detail != want {
		t.Fatalf("expected detail %q, got %q", want, ee.Detail)
	}
}

// TestSetvalOnTableWrongRelkind additionally asserts no phantom sequence is
// registered afterward — the pre-fix behavior silently called
// RegisterSequence on LookupSequence miss, BEFORE the relkind check existed.
func TestSetvalOnTableWrongRelkind(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE seq_relkind_tbl3 (id int)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	_, err := runSQLCtxErr(t, ctx, "SELECT setval('seq_relkind_tbl3', 5)")
	if err == nil {
		t.Fatal("expected 42809, got nil")
	}
	ee := mustExecErr(t, err)
	if ee.Code != "42809" {
		t.Fatalf("expected 42809, got %s (%v)", ee.Code, ee)
	}
	if want := `cannot open relation "seq_relkind_tbl3"`; ee.Message != want {
		t.Fatalf("expected message %q, got %q", want, ee.Message)
	}
	if want := "This operation is not supported for tables."; ee.Detail != want {
		t.Fatalf("expected detail %q, got %q", want, ee.Detail)
	}

	dbOid := ctxSeqDBOid(ctx)
	if s := LookupSequence("seq_relkind_tbl3", dbOid); s != nil {
		t.Fatalf("setval must not fabricate a phantom sequence for a wrong-relkind name, but LookupSequence found one")
	}
}

func TestAlterSequenceOnTableWrongRelkind(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE seq_relkind_tbl4 (id int)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	err := runDDL(t, ctx, "ALTER SEQUENCE seq_relkind_tbl4 CYCLE")
	if err == nil {
		t.Fatal("expected 42809, got nil")
	}
	ee := mustExecErr(t, err)
	if ee.Code != "42809" {
		t.Fatalf("expected 42809, got %s (%v)", ee.Code, ee)
	}
	if want := `cannot open relation "seq_relkind_tbl4"`; ee.Message != want {
		t.Fatalf("expected message %q, got %q", want, ee.Message)
	}
	if want := "This operation is not supported for tables."; ee.Detail != want {
		t.Fatalf("expected detail %q, got %q", want, ee.Detail)
	}
}

func TestDropSequenceOnTableWrongRelkind(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE seq_relkind_tbl5 (id int)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	err := runDDL(t, ctx, "DROP SEQUENCE seq_relkind_tbl5")
	if err == nil {
		t.Fatal("expected 42809, got nil")
	}
	ee := mustExecErr(t, err)
	if ee.Code != "42809" {
		t.Fatalf("expected 42809, got %s (%v)", ee.Code, ee)
	}
	if want := `"seq_relkind_tbl5" is not a sequence`; ee.Message != want {
		t.Fatalf("expected message %q, got %q", want, ee.Message)
	}
	if ee.Detail != "" {
		t.Fatalf("expected no DETAIL, got %q", ee.Detail)
	}

	// The table must survive — DROP SEQUENCE on a wrong-relkind name must
	// not drop the table either.
	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "seq_relkind_tbl5"}); !ok {
		t.Fatal("table was dropped by DROP SEQUENCE on wrong relkind")
	}
}

// TestSequenceRelkindGenuineNotFoundUnchanged pins that a name resolving to
// nothing at all (neither catalog nor sequence registry) still returns
// 42P01 / the plain DROP SEQUENCE miss path, unaffected by the relkind
// guard added above.
func TestSequenceRelkindGenuineNotFoundUnchanged(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	_, err := runSQLCtxErr(t, ctx, "SELECT nextval('seq_relkind_does_not_exist')")
	if err == nil {
		t.Fatal("expected 42P01, got nil")
	}
	if ee := mustExecErr(t, err); ee.Code != "42P01" {
		t.Fatalf("expected 42P01, got %s (%v)", ee.Code, ee)
	}

	err = runDDL(t, ctx, "ALTER SEQUENCE seq_relkind_does_not_exist CYCLE")
	if err == nil {
		t.Fatal("expected 42P01, got nil")
	}
	if ee := mustExecErr(t, err); ee.Code != "42P01" {
		t.Fatalf("expected 42P01, got %s (%v)", ee.Code, ee)
	}

	err = runDDL(t, ctx, "DROP SEQUENCE seq_relkind_does_not_exist")
	if err == nil {
		t.Fatal("expected 42704, got nil")
	}
	if ee := mustExecErr(t, err); ee.Code != "42704" {
		t.Fatalf("expected 42704, got %s (%v)", ee.Code, ee)
	}
}
