package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestAlterForeignDataWrapperOptionsRoundtrip verifies that
// `ALTER FOREIGN DATA WRAPPER name OPTIONS ([ADD|SET|DROP] name ['value'], …)`
// merges onto the registered ForeignDataWrapper.Options exactly like PG's
// transformGenericOptions: ADD appends, SET replaces an existing value, DROP
// removes, mirroring the identical ALTER FOREIGN TABLE ... OPTIONS (...)
// mechanism. Also confirms a `NO HANDLER` clause (no matching function to
// resolve) is accepted without disturbing the options — see
// TestAlterForeignDataWrapperHandlerValidatorSetAndClear for HANDLER/
// VALIDATOR resolution itself. Closes the loop #57 deferral-ledger resume
// point ("ALTER FOREIGN DATA WRAPPER remains entirely unparseable").
// DU-002 slice 421.
func TestAlterForeignDataWrapperOptionsRoundtrip(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("expected *catalog.InMemory")
	}

	if err := runDDL(t, ctx, `CREATE FOREIGN DATA WRAPPER fdw1 OPTIONS (host 'a')`); err != nil {
		t.Fatalf("CREATE FOREIGN DATA WRAPPER: %v", err)
	}

	opts := func() []string {
		fdw, found := im.LookupForeignDataWrapper("fdw1")
		if !found {
			t.Fatal("fdw fdw1 not found")
		}
		return fdw.Options
	}

	if got, want := opts(), []string{"host=a"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("initial Options = %v, want %v", got, want)
	}

	// ADD appends a new option; a preceding NO HANDLER/VALIDATOR clause is
	// accepted and discarded.
	if err := runDDL(t, ctx, `ALTER FOREIGN DATA WRAPPER fdw1 NO HANDLER OPTIONS (ADD port '5432')`); err != nil {
		t.Fatalf("ADD port: %v", err)
	}
	if got, want := opts(), []string{"host=a", "port=5432"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("after ADD, Options = %v, want %v", got, want)
	}

	// SET replaces an existing value and a bare (verb-unspecified) entry
	// defaults to ADD, in the same clause.
	if err := runDDL(t, ctx, `ALTER FOREIGN DATA WRAPPER fdw1 OPTIONS (SET host 'b', bare 'v3')`); err != nil {
		t.Fatalf("SET host + bare add: %v", err)
	}
	got := opts()
	want := []string{"host=b", "port=5432", "bare=v3"}
	if len(got) != len(want) {
		t.Fatalf("after SET+bare-ADD, Options = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Options[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}

	// DROP removes an option.
	if err := runDDL(t, ctx, `ALTER FOREIGN DATA WRAPPER fdw1 OPTIONS (DROP port)`); err != nil {
		t.Fatalf("DROP port: %v", err)
	}
	if got, want := opts(), []string{"host=b", "bare=v3"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("after DROP, Options = %v, want %v", got, want)
	}
}

// TestAlterForeignDataWrapperOptionsErrors pins the SQLSTATEs for the invalid
// forms: 42704 undefined_object when the target FDW does not exist at all,
// 42710 for an ADD of an already-present option, and 42704 for a SET/DROP of
// a missing one — mirroring the ALTER FOREIGN TABLE OPTIONS error tests.
// DU-002 slice 421.
func TestAlterForeignDataWrapperOptionsErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FOREIGN DATA WRAPPER fdw1 OPTIONS (host 'a')`); err != nil {
		t.Fatalf("CREATE FOREIGN DATA WRAPPER: %v", err)
	}

	wantCode := func(t *testing.T, err error, code string) {
		t.Helper()
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != code {
			t.Fatalf("error = %v, want *ExecError %s", err, code)
		}
	}

	wantCode(t, runDDL(t, ctx, `ALTER FOREIGN DATA WRAPPER nosuch OPTIONS (ADD x 'y')`), "42704")
	wantCode(t, runDDL(t, ctx, `ALTER FOREIGN DATA WRAPPER fdw1 OPTIONS (ADD host 'dup')`), "42710")
	wantCode(t, runDDL(t, ctx, `ALTER FOREIGN DATA WRAPPER fdw1 OPTIONS (SET nosuch 'v')`), "42704")
	wantCode(t, runDDL(t, ctx, `ALTER FOREIGN DATA WRAPPER fdw1 OPTIONS (DROP nosuch)`), "42704")
}
