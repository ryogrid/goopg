package executor

// column_collation_validation_test.go pins the M0134-0101 fix: an inline
// `COLLATE` clause on a column whose type has no typcollation (int, bool,
// numeric, …) must raise 42804 "collations are not supported by type %s",
// matching PostgreSQL's transformColumnType
// (postgres/src/backend/parser/parse_utilcmd.c:4044-4067). Before this fix
// goopg silently accepted the clause and created the table anyway, leaving a
// phantom relation the PG 18.3 oracle never creates — the widest-cascading
// divergence in collate.sql's 558-line diff.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCreateTableRejectsCollateOnNonCollatableType covers the base case from
// collate.sql: a plain `int` column with an inline COLLATE clause.
func TestCreateTableRejectsCollateOnNonCollatableType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, `CREATE TABLE collate_test_fail (a int COLLATE "C", b text)`)
	if err == nil {
		t.Fatal("expected 42804: COLLATE is not supported on an integer column")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %#v", err)
	}
	if ee.Code != "42804" {
		t.Fatalf("Code = %q, want 42804", ee.Code)
	}
	const want = "collations are not supported by type integer"
	if ee.Message != want {
		t.Fatalf("Message = %q, want %q", ee.Message, want)
	}

	if _, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "collate_test_fail"}); ok {
		t.Fatal("table must not exist after a rejected CREATE TABLE — PG aborts the whole statement")
	}
}

// TestCreateTableAllowsCollateOnCollatableTypes covers the positive cases:
// text/varchar/bpchar/name (and an array over a collatable element type) all
// accept an inline COLLATE clause with no error.
func TestCreateTableAllowsCollateOnCollatableTypes(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	stmts := []string{
		`CREATE TABLE coll_text (a text COLLATE "C")`,
		`CREATE TABLE coll_varchar (a varchar(10) COLLATE "POSIX")`,
		`CREATE TABLE coll_bpchar (a char(3) COLLATE "C")`,
		`CREATE TABLE coll_array (a text[] COLLATE "C")`,
	}
	for _, s := range stmts {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("%s: unexpected error: %v", s, err)
		}
	}
}

// TestCreateTableRejectsCollateOnDomainOverNonCollatableType covers a domain
// whose base type is non-collatable: the check must run on the
// domain-resolved base type, not the domain's own (unregistered) type name.
func TestCreateTableRejectsCollateOnDomainOverNonCollatableType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE DOMAIN intdom AS int`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	err := runDDL(t, ctx, `CREATE TABLE coll_domain (a intdom COLLATE "C")`)
	if err == nil {
		t.Fatal("expected 42804: intdom's base type (integer) is not collatable")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42804" {
		t.Fatalf("expected 42804 *ExecError, got %#v", err)
	}
}
