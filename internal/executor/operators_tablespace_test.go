package executor

import (
	"os"
	"path/filepath"
	"testing"
)

// tablespaceFixture builds a VM fixture with a real data directory and a
// configurable allow_in_place_tablespaces GUC, then returns the context plus a
// pointer the test flips to toggle the GUC. M0095-0003.
func tablespaceFixture(t *testing.T) (*Context, *bool, func()) {
	t.Helper()
	ctx, cleanup := newVMFixture(t)
	ctx.DataDir = t.TempDir()
	allow := true
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "allow_in_place_tablespaces" {
			if allow {
				return "on", true
			}
			return "off", true
		}
		return "", false
	}
	return ctx, &allow, cleanup
}

func tblspcEntries(t *testing.T, ctx *Context) []string {
	t.Helper()
	dir := filepath.Join(ctx.DataDir, "pg_tblspc")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func execErrCode(err error) string {
	if e, ok := err.(*ExecError); ok {
		return e.Code
	}
	return ""
}

// TestCreateInPlaceTablespace covers the happy path: an in-place tablespace
// creates exactly one pg_tblspc/<oid> directory, and DROP removes it.
func TestCreateInPlaceTablespace(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	ents := tblspcEntries(t, ctx)
	if len(ents) != 1 {
		t.Fatalf("expected 1 pg_tblspc entry, got %v", ents)
	}
	// The entry is the numeric OID directory.
	info, err := os.Stat(filepath.Join(ctx.DataDir, "pg_tblspc", ents[0]))
	if err != nil || !info.IsDir() {
		t.Fatalf("pg_tblspc/%s is not a directory: %v", ents[0], err)
	}

	if err := runDDL(t, ctx, "DROP TABLESPACE ts1"); err != nil {
		t.Fatalf("DROP TABLESPACE: %v", err)
	}
	if ents := tblspcEntries(t, ctx); len(ents) != 0 {
		t.Fatalf("expected pg_tblspc empty after DROP, got %v", ents)
	}
}

// TestCreateTablespaceDuplicate — a second CREATE of the same name errors 42710.
func TestCreateTablespaceDuplicate(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE dup LOCATION ''"); err != nil {
		t.Fatalf("first CREATE: %v", err)
	}
	err := runDDL(t, ctx, "CREATE TABLESPACE dup LOCATION ''")
	if got := execErrCode(err); got != "42710" {
		t.Fatalf("duplicate CREATE: want 42710, got %q (%v)", got, err)
	}
	// No second directory should have been created.
	if ents := tblspcEntries(t, ctx); len(ents) != 1 {
		t.Fatalf("expected 1 entry after failed duplicate, got %v", ents)
	}
}

// TestDropTablespaceMissing — DROP of an absent tablespace errors 42704 unless
// IF EXISTS is given.
func TestDropTablespaceMissing(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, "DROP TABLESPACE nope")
	if got := execErrCode(err); got != "42704" {
		t.Fatalf("DROP missing: want 42704, got %q (%v)", got, err)
	}
	if err := runDDL(t, ctx, "DROP TABLESPACE IF EXISTS nope"); err != nil {
		t.Fatalf("DROP IF EXISTS missing: want nil, got %v", err)
	}
}

// TestCreateTablespaceGUCOff — with allow_in_place_tablespaces off, an empty
// LOCATION takes PG's non-in-place path and errors "must be an absolute path".
func TestCreateTablespaceGUCOff(t *testing.T) {
	ctx, allow, cleanup := tablespaceFixture(t)
	defer cleanup()
	*allow = false

	err := runDDL(t, ctx, "CREATE TABLESPACE ts2 LOCATION ''")
	if got := execErrCode(err); got != "42P17" {
		t.Fatalf("GUC off empty LOCATION: want 42P17, got %q (%v)", got, err)
	}
	if ents := tblspcEntries(t, ctx); len(ents) != 0 {
		t.Fatalf("expected no directory on rejected CREATE, got %v", ents)
	}
}

// TestCreateTablespaceReservedName — a "pg_"-prefixed name errors 42939.
func TestCreateTablespaceReservedName(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, "CREATE TABLESPACE pg_evil LOCATION ''")
	if got := execErrCode(err); got != "42939" {
		t.Fatalf("reserved name: want 42939, got %q (%v)", got, err)
	}
}

// TestCreateTablespaceExternalLocation — an absolute external location is valid
// in PG but unsupported in goopg (no relfile relocation) → 0A000.
func TestCreateTablespaceExternalLocation(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, "CREATE TABLESPACE ts3 LOCATION '/var/lib/goopg/ts3'")
	if got := execErrCode(err); got != "0A000" {
		t.Fatalf("external LOCATION: want 0A000, got %q (%v)", got, err)
	}
}

// TestDropTablespaceRejectsWhenTableStillReferencesIt guards the M0122-0007
// physical-relocation safety fix: since CREATE TABLE/ALTER ... SET
// TABLESPACE now place a table's real data file under pg_tblspc/<oid>/...,
// DROP TABLESPACE must refuse (55000, mirroring upstream's "tablespace %q is
// not empty") rather than os.RemoveAll the directory out from under a live
// table — before this guard existed, that call would have destroyed real
// user data instead of merely leaving a harmless dangling registry entry
// (the old, catalog-metadata-only behavior's worst case).
func TestDropTablespaceRejectsWhenTableStillReferencesIt(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int) TABLESPACE ts1"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	err := runDDL(t, ctx, "DROP TABLESPACE ts1")
	if got := execErrCode(err); got != "55000" {
		t.Fatalf("DROP TABLESPACE with a live table: want 55000, got %q (%v)", got, err)
	}
	// The tablespace registry entry and directory must both survive the
	// rejected DROP.
	if _, ok := ctx.Catalog.LookupTablespaceOID("ts1"); !ok {
		t.Fatal("ts1 should still be registered after a rejected DROP")
	}
	if ents := tblspcEntries(t, ctx); len(ents) != 1 {
		t.Fatalf("expected pg_tblspc entry to survive rejected DROP, got %v", ents)
	}

	// Once the table is moved back to the default tablespace, DROP succeeds.
	if err := runDDL(t, ctx, "ALTER TABLE t1 SET TABLESPACE pg_default"); err != nil {
		t.Fatalf("ALTER TABLE SET TABLESPACE pg_default: %v", err)
	}
	if err := runDDL(t, ctx, "DROP TABLESPACE ts1"); err != nil {
		t.Fatalf("DROP TABLESPACE after table moved out: %v", err)
	}
}

// TestDropTablespaceRejectsWhenIndexStillReferencesIt mirrors the table
// case for an index.
func TestDropTablespaceRejectsWhenIndexStillReferencesIt(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLESPACE ts1 LOCATION ''"); err != nil {
		t.Fatalf("CREATE TABLESPACE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE t1 (a int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX idx1 ON t1 (a) TABLESPACE ts1"); err != nil {
		t.Fatalf("CREATE INDEX ... TABLESPACE ts1: %v", err)
	}

	err := runDDL(t, ctx, "DROP TABLESPACE ts1")
	if got := execErrCode(err); got != "55000" {
		t.Fatalf("DROP TABLESPACE with a live index: want 55000, got %q (%v)", got, err)
	}
}

// TestCreateTablespaceQuoteInLocation — a single quote in the location errors
// 42602, mirroring PG's CREATE-DATABASE-safety check.
func TestCreateTablespaceQuoteInLocation(t *testing.T) {
	ctx, _, cleanup := tablespaceFixture(t)
	defer cleanup()

	// SQL '''' → a one-character location consisting of a single quote.
	err := runDDL(t, ctx, "CREATE TABLESPACE ts4 LOCATION ''''")
	if got := execErrCode(err); got != "42602" {
		t.Fatalf("quote in LOCATION: want 42602, got %q (%v)", got, err)
	}
}
