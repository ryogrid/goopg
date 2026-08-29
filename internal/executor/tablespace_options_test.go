package executor

import "testing"

// spcoptionsOf reads a tablespace's spcoptions straight off the registry, which
// is what pg_tablespace's virtual row builder renders.
func spcoptionsOf(t *testing.T, ctx *Context, name string) []string {
	t.Helper()
	opts, ok := ctx.Catalog.TablespaceOptions(name)
	if !ok {
		t.Fatalf("tablespace %q not registered", name)
	}
	return opts
}

// TestCreateTablespaceRejectsUnknownOption is the regression guard for the bug
// M0134-0176 fixed: the WITH clause was parsed into a token list nobody read,
// so an unrecognized storage parameter was SILENTLY ACCEPTED. The assertion
// that no tablespace is left behind is the load-bearing half — upstream
// validates before inserting the pg_tablespace tuple (tablespace.c:359), and a
// tablespace created by a statement that should have failed turns every later
// negative test into a spurious "already exists".
func TestCreateTablespaceRejectsUnknownOption(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION '' WITH (some_nonexistent_parameter = true)")
	if err == nil {
		t.Fatal("CREATE TABLESPACE with a bogus option: want error, got nil")
	}
	if got := execErrCode(err); got != "22023" {
		t.Errorf("SQLSTATE = %q, want 22023 (%v)", got, err)
	}
	if _, found := ctx.Catalog.LookupTablespaceOID("ts1"); found {
		t.Error("rejected CREATE TABLESPACE left the tablespace behind")
	}
	// The relation options are a DIFFERENT kind: fillfactor is admissible on a
	// heap but not on a tablespace, and the four tablespace options are not
	// admissible anywhere else. A registry that merged the two kinds would
	// accept this.
	if err := runDDL(t, ctx, "CREATE TABLESPACE ts2 LOCATION '' WITH (fillfactor = 50)"); err == nil {
		t.Error("CREATE TABLESPACE WITH (fillfactor): want error, got nil")
	}
}

// TestCreateTablespaceStoresOptions pins that a recognized parameter actually
// lands in spcoptions, in PG's own `name=value` element form.
func TestCreateTablespaceStoresOptions(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION '' WITH (random_page_cost = 3.0)"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	got := spcoptionsOf(t, ctx, "ts1")
	if len(got) != 1 || got[0] != "random_page_cost=3.0" {
		t.Errorf("spcoptions = %v, want [random_page_cost=3.0]", got)
	}
	// A tablespace with no WITH clause keeps spcoptions NULL, not `{}`.
	if err := runDDL(t, ctx, "CREATE TABLESPACE ts2 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	if got := spcoptionsOf(t, ctx, "ts2"); len(got) != 0 {
		t.Errorf("spcoptions of an optionless tablespace = %v, want empty", got)
	}
}

// TestAlterTablespaceSetResetMatchesOracle walks the exact sequence
// tablespace.sql runs, whose every answer was verified against a live PG 18.3
// server. Before M0134-0176 all four statements were syntax errors.
func TestAlterTablespaceSetResetMatchesOracle(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLESPACE ts SET (random_page_cost = 1.0, seq_page_cost = 1.1)"); err != nil {
		t.Fatalf("ALTER ... SET: %v", err)
	}
	got := spcoptionsOf(t, ctx, "ts")
	if len(got) != 2 || got[0] != "random_page_cost=1.0" || got[1] != "seq_page_cost=1.1" {
		t.Fatalf("spcoptions = %v, want [random_page_cost=1.0 seq_page_cost=1.1]", got)
	}
	// An unrecognized name on the way IN is rejected, and nothing changes.
	err := runDDL(t, ctx, "ALTER TABLESPACE ts SET (some_nonexistent_parameter = true)")
	if execErrCode(err) != "22023" {
		t.Errorf("SET bogus: SQLSTATE %q want 22023 (%v)", execErrCode(err), err)
	}
	// RESET (name = value) is a SYNTAX error, not a parameter error — upstream
	// raises it in transformRelOptions because the grammar accepts the form
	// (reloptions.c:1238-1243).
	err = runDDL(t, ctx, "ALTER TABLESPACE ts RESET (random_page_cost = 2.0)")
	if execErrCode(err) != "42601" {
		t.Errorf("RESET with a value: SQLSTATE %q want 42601 (%v)", execErrCode(err), err)
	}
	if got := spcoptionsOf(t, ctx, "ts"); len(got) != 2 {
		t.Errorf("a rejected ALTER changed spcoptions: %v", got)
	}
	// RESET removes the named options and IGNORES a name that was never set —
	// upstream validates the SURVIVING array, so a bogus name on the way OUT
	// cannot be rejected. Verified against the 18.3 oracle.
	if err := runDDL(t, ctx, "ALTER TABLESPACE ts RESET (random_page_cost, effective_io_concurrency)"); err != nil {
		t.Fatalf("RESET: %v", err)
	}
	if got := spcoptionsOf(t, ctx, "ts"); len(got) != 1 || got[0] != "seq_page_cost=1.1" {
		t.Fatalf("spcoptions after RESET = %v, want [seq_page_cost=1.1]", got)
	}
	if err := runDDL(t, ctx, "ALTER TABLESPACE ts RESET (bogus_never_set)"); err != nil {
		t.Errorf("RESET of a never-set name: want success, got %v", err)
	}
	// Emptying the array returns spcoptions to NULL, never `{}`.
	if err := runDDL(t, ctx, "ALTER TABLESPACE ts RESET (seq_page_cost)"); err != nil {
		t.Fatalf("RESET: %v", err)
	}
	if got := spcoptionsOf(t, ctx, "ts"); got != nil {
		t.Errorf("spcoptions after the last RESET = %v, want nil (SQL NULL)", got)
	}
}

// TestAlterTablespaceSetMergesWithExisting pins transformRelOptions' merge
// order (reloptions.c:1180-1245): surviving old elements keep their original
// order, then the new elements are appended in source order. Replacing an
// option therefore MOVES it to the end.
func TestAlterTablespaceSetMergesWithExisting(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts LOCATION '' WITH (random_page_cost = 1.0, seq_page_cost = 2.0)"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLESPACE ts SET (random_page_cost = 9.0)"); err != nil {
		t.Fatalf("ALTER ... SET: %v", err)
	}
	got := spcoptionsOf(t, ctx, "ts")
	if len(got) != 2 || got[0] != "seq_page_cost=2.0" || got[1] != "random_page_cost=9.0" {
		t.Errorf("spcoptions = %v, want [seq_page_cost=2.0 random_page_cost=9.0]", got)
	}
}

// TestAlterTablespaceRenameAndOwner covers the two non-option forms plus the
// three errors they share with upstream.
func TestAlterTablespaceRenameAndOwner(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts LOCATION '' WITH (seq_page_cost = 2.0)"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	oid, _ := ctx.Catalog.LookupTablespaceOID("ts")

	if err := runDDL(t, ctx, "ALTER TABLESPACE nosuchts SET (seq_page_cost = 1)"); execErrCode(err) != "42704" {
		t.Errorf("ALTER of a missing tablespace: SQLSTATE %q want 42704 (%v)", execErrCode(err), err)
	}
	if err := runDDL(t, ctx, "ALTER TABLESPACE ts RENAME TO pg_bogus"); execErrCode(err) != "42939" {
		t.Errorf("RENAME to a pg_ name: SQLSTATE %q want 42939 (%v)", execErrCode(err), err)
	}
	if err := runDDL(t, ctx, "ALTER TABLESPACE ts RENAME TO ts2"); err != nil {
		t.Fatalf("RENAME: %v", err)
	}
	// The OID survives a rename — it is the pg_tblspc/<oid> directory name and
	// every reltablespace pointing at this tablespace, so a rename that minted
	// a new OID would orphan the data. PG only rewrites spcname.
	newOID, found := ctx.Catalog.LookupTablespaceOID("ts2")
	if !found || newOID != oid {
		t.Errorf("after RENAME: oid=%d found=%v, want oid=%d", newOID, found, oid)
	}
	if _, stillOld := ctx.Catalog.LookupTablespaceOID("ts"); stillOld {
		t.Error("the old tablespace name is still resolvable after RENAME")
	}
	if got := spcoptionsOf(t, ctx, "ts2"); len(got) != 1 || got[0] != "seq_page_cost=2.0" {
		t.Errorf("RENAME lost spcoptions: %v", got)
	}
	if err := runDDL(t, ctx, "CREATE TABLESPACE ts3 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLESPACE ts2 RENAME TO ts3"); execErrCode(err) != "42710" {
		t.Errorf("RENAME onto a taken name: SQLSTATE %q want 42710 (%v)", execErrCode(err), err)
	}
	if err := runDDL(t, ctx, "ALTER TABLESPACE ts2 OWNER TO nosuchrole"); execErrCode(err) != "42704" {
		t.Errorf("OWNER TO an unknown role: SQLSTATE %q want 42704 (%v)", execErrCode(err), err)
	}
	ctx.Catalog.RegisterRole("alice")
	if err := runDDL(t, ctx, "ALTER TABLESPACE ts2 OWNER TO alice"); err != nil {
		t.Fatalf("OWNER TO: %v", err)
	}
}
