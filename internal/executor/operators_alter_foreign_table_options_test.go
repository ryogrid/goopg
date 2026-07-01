package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAlterForeignTableAlterColumnOptionsRoundtrip verifies that
// `ALTER FOREIGN TABLE ... ALTER COLUMN col OPTIONS (ADD|SET|DROP ...)` merges
// onto catalog.Column.FDWOptions exactly like PG's transformGenericOptions:
// ADD appends, SET replaces an existing value, DROP removes. Closes the loop
// #55 deferral-ledger resume point: pg_dump now emits this statement (the
// attfdwoptions round-trip, DU-002 slice 418) but goopg previously could not
// parse it back. DU-002 slice 419.
func TestAlterForeignTableAlterColumnOptionsRoundtrip(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SERVER srv FOREIGN DATA WRAPPER goopg_fdw`); err != nil {
		t.Fatalf("CREATE SERVER: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FOREIGN TABLE ft (c int OPTIONS (column_name 'col1')) SERVER srv`); err != nil {
		t.Fatalf("CREATE FOREIGN TABLE: %v", err)
	}

	col := func() []string {
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: "ft"})
		if !ok {
			t.Fatal("table ft not found")
		}
		for _, c := range tbl.Columns {
			if c.Name == "c" {
				return c.FDWOptions
			}
		}
		t.Fatal("column c not found")
		return nil
	}

	if got, want := col(), []string{"column_name=col1"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("initial FDWOptions = %v, want %v", got, want)
	}

	// ADD appends a new option.
	if err := runDDL(t, ctx, `ALTER FOREIGN TABLE ft ALTER COLUMN c OPTIONS (ADD other 'v2')`); err != nil {
		t.Fatalf("ADD other: %v", err)
	}
	if got, want := col(), []string{"column_name=col1", "other=v2"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("after ADD, FDWOptions = %v, want %v", got, want)
	}

	// SET (with ONLY and bare-form-defaults-to-ADD in the same clause) replaces
	// an existing value and adds a fresh one in one statement.
	if err := runDDL(t, ctx, `ALTER FOREIGN TABLE ONLY ft ALTER COLUMN c OPTIONS (SET column_name 'colX', bare 'v3')`); err != nil {
		t.Fatalf("SET column_name + bare add: %v", err)
	}
	got := col()
	want := []string{"column_name=colX", "other=v2", "bare=v3"}
	if len(got) != len(want) {
		t.Fatalf("after SET+bare-ADD, FDWOptions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("FDWOptions[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}

	// DROP removes an option.
	if err := runDDL(t, ctx, `ALTER FOREIGN TABLE ft ALTER COLUMN c OPTIONS (DROP other)`); err != nil {
		t.Fatalf("DROP other: %v", err)
	}
	if got, want := col(), []string{"column_name=colX", "bare=v3"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("after DROP, FDWOptions = %v, want %v", got, want)
	}
}

// TestAlterForeignTableAlterColumnOptionsErrors pins the SQLSTATEs for the
// invalid forms, mirroring PostgreSQL's ATExecAlterColumnGenericOptions /
// transformGenericOptions (commands/foreigncmds.c): 42809 when the target
// relation is not a foreign table, 42703 for an unknown column, 42710 for an
// ADD of an already-present option, and 42704 for a SET/DROP of a missing
// one. DU-002 slice 419.
func TestAlterForeignTableAlterColumnOptionsErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE plain (c int)`); err != nil {
		t.Fatalf("CREATE TABLE plain: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE SERVER srv FOREIGN DATA WRAPPER goopg_fdw`); err != nil {
		t.Fatalf("CREATE SERVER: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FOREIGN TABLE ft (c int OPTIONS (column_name 'col1')) SERVER srv`); err != nil {
		t.Fatalf("CREATE FOREIGN TABLE: %v", err)
	}

	wantCode := func(t *testing.T, err error, code string) {
		t.Helper()
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != code {
			t.Fatalf("error = %v, want *ExecError %s", err, code)
		}
	}

	wantCode(t, runDDL(t, ctx, `ALTER FOREIGN TABLE plain ALTER COLUMN c OPTIONS (ADD x 'y')`), "42809")
	wantCode(t, runDDL(t, ctx, `ALTER FOREIGN TABLE ft ALTER COLUMN nosuchcol OPTIONS (ADD x 'y')`), "42703")
	wantCode(t, runDDL(t, ctx, `ALTER FOREIGN TABLE ft ALTER COLUMN c OPTIONS (ADD column_name 'dup')`), "42710")
	wantCode(t, runDDL(t, ctx, `ALTER FOREIGN TABLE ft ALTER COLUMN c OPTIONS (SET nosuch 'v')`), "42704")
	wantCode(t, runDDL(t, ctx, `ALTER FOREIGN TABLE ft ALTER COLUMN c OPTIONS (DROP nosuch)`), "42704")
}
