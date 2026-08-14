package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0119-0006 (75th slice, deferral ledger row 1348): the regproc/
// regprocedure NAME→OID INPUT half (regIdentifierInput AND the expr.go
// `::regproc`/`::regprocedure` cast siblings) used to call
// Routines.LookupByName with NO dbOid, which resolved DefaultDBOid
// regardless of the connection's actual database. A LIVE-created routine —
// registered under its real dbOid by the 4e-series dbOid-keyed routine
// registry (Create keys by `routineKey(NamespaceDBOid(ctx.CurrentDatabaseOid), …)`)
// — was therefore invisible to the name→OID lookup from that same connection:
// `'g_offarg'::regprocedure` failed `function "g_offarg" does not exist` while
// an initdb-reloaded (DefaultDBOid) routine resolved. This mirrors the 33
// VirtualRows/cast-scoping follow-ups (TestRegclassCastScopedToConnectionDBOid
// is the closest sibling — regclass ALREADY threads connDBOid) and closes the
// one remaining unscoped reg* arm: regproc/regprocedure.
//
// The two lookups are sibling paths (Hard-won Rule #2): regIdentifierInput
// feeds COPY FROM coercion + constraint checks, the expr.go cast feeds
// `'name'::regproc`/`'name'::regprocedure` in expressions. Both resolve
// through the same Routines.LookupByName — this test pins BOTH so one cannot
// drift.

// TestRegProcInputScopedToConnectionDBOid is the failing shape from row 1348:
// two IDENTICALLY-named routines created under two distinct dbOids must each
// resolve to their OWN database's routine through the regproc/regprocedure
// INPUT half. Pre-fix, LookupByName resolved DefaultDBOid, so from the
// distinct-dbOid connection `'shared_fn'::regproc` either missed (42883) or
// resolved the other database's routine (a cross-dbOid leak).
func TestRegProcInputScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7020
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	// Create the SAME-named routine under two distinct dbOids.
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, `CREATE FUNCTION shared_fn(a int4) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("CREATE FUNCTION shared_fn (default): %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, `CREATE FUNCTION shared_fn(a int4) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("CREATE FUNCTION shared_fn (other): %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}
	rs := im.Routines()
	defRoutine := rs.LookupByName(parser.ObjectName{Name: "shared_fn"}, catalog.DefaultDBOid)
	if len(defRoutine) == 0 {
		t.Fatal("shared_fn not found under DefaultDBOid")
	}
	otherRoutine := rs.LookupByName(parser.ObjectName{Name: "shared_fn"}, otherDBOid)
	if len(otherRoutine) == 0 {
		t.Fatal("shared_fn not found under otherDBOid")
	}
	if defRoutine[0].OID == otherRoutine[0].OID {
		t.Fatalf("test setup: shared_fn got the same OID (%d) in both databases", defRoutine[0].OID)
	}

	// 'name'::regproc from otherDBOid's own connection resolves to its OWN
	// routine's OID — the row-1348 failing shape (pre-fix it missed or leaked).
	ctx.CurrentDatabaseOid = otherDBOid
	rows := runQueryUnderDBOid(t, ctx, "SELECT 'shared_fn'::regproc")
	if got := uint32(rows[0][0].Int); got != otherRoutine[0].OID {
		t.Errorf("otherDBOid: 'shared_fn'::regproc = %d, want its own %d (got DefaultDBOid's %d instead?)", got, otherRoutine[0].OID, defRoutine[0].OID)
	}
	// regprocedure resolves the name the same way (arg list stripped first).
	rows = runQueryUnderDBOid(t, ctx, "SELECT 'shared_fn(int4)'::regprocedure")
	if got := uint32(rows[0][0].Int); got != otherRoutine[0].OID {
		t.Errorf("otherDBOid: 'shared_fn(int4)'::regprocedure = %d, want its own %d", got, otherRoutine[0].OID)
	}

	// DefaultDBOid's own connection resolves its own routine — the reverse
	// direction, pins the always-worked path against a regression.
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	rows = runQueryUnderDBOid(t, ctx, "SELECT 'shared_fn'::regproc")
	if got := uint32(rows[0][0].Int); got != defRoutine[0].OID {
		t.Errorf("DefaultDBOid: 'shared_fn'::regproc = %d, want its own %d", got, defRoutine[0].OID)
	}
	rows = runQueryUnderDBOid(t, ctx, "SELECT 'shared_fn(int4)'::regprocedure")
	if got := uint32(rows[0][0].Int); got != defRoutine[0].OID {
		t.Errorf("DefaultDBOid: 'shared_fn(int4)'::regprocedure = %d, want its own %d", got, defRoutine[0].OID)
	}

	// regIdentifierInput is the sibling path (COPY FROM coercion / constraint
	// checks): the same name must resolve to the connection's OWN routine.
	ctx.CurrentDatabaseOid = otherDBOid
	got, err := regIdentifierInput(NewStringDatum("shared_fn"), "regproc", ctx, 0)
	if err != nil {
		t.Fatalf("otherDBOid: regIdentifierInput(shared_fn, regproc): %v", err)
	}
	if got.Kind != KindInt || uint32(got.Int) != otherRoutine[0].OID {
		t.Errorf("otherDBOid: regIdentifierInput(shared_fn) = kind %d %d, want %d", got.Kind, got.Int, otherRoutine[0].OID)
	}
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	got, err = regIdentifierInput(NewStringDatum("shared_fn"), "regproc", ctx, 0)
	if err != nil {
		t.Fatalf("DefaultDBOid: regIdentifierInput(shared_fn, regproc): %v", err)
	}
	if got.Kind != KindInt || uint32(got.Int) != defRoutine[0].OID {
		t.Errorf("DefaultDBOid: regIdentifierInput(shared_fn) = kind %d %d, want %d", got.Kind, got.Int, defRoutine[0].OID)
	}
}

// TestRegProcInputSchemaQualifiedScopedToConnectionDBOid pins the
// schema-qualified form of the same fix: a routine created under a distinct
// dbOid resolves by `schema.name` from that connection's name→OID lookup, and
// an identical name under the same schema of a DIFFERENT dbOid does not leak.
func TestRegProcInputSchemaQualifiedScopedToConnectionDBOid(t *testing.T) {
	const otherDBOid = 7021
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, `CREATE SCHEMA apps`); err != nil {
		t.Fatalf("CREATE SCHEMA apps (default): %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION apps.route(a int4) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("CREATE FUNCTION apps.route (default): %v", err)
	}
	ctx.CurrentDatabaseOid = otherDBOid
	if err := runDDL(t, ctx, `CREATE SCHEMA apps`); err != nil {
		t.Fatalf("CREATE SCHEMA apps (other): %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION apps.route(a int4) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("CREATE FUNCTION apps.route (other): %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}
	defRoutine := im.Routines().LookupByName(parser.ObjectName{Schema: "apps", Name: "route"}, catalog.DefaultDBOid)
	if len(defRoutine) == 0 {
		t.Fatal("apps.route not found under DefaultDBOid")
	}
	otherRoutine := im.Routines().LookupByName(parser.ObjectName{Schema: "apps", Name: "route"}, otherDBOid)
	if len(otherRoutine) == 0 {
		t.Fatal("apps.route not found under otherDBOid")
	}
	if defRoutine[0].OID == otherRoutine[0].OID {
		t.Fatalf("test setup: apps.route got the same OID (%d) in both databases", defRoutine[0].OID)
	}

	ctx.CurrentDatabaseOid = otherDBOid
	rows := runQueryUnderDBOid(t, ctx, "SELECT 'apps.route'::regproc")
	if got := uint32(rows[0][0].Int); got != otherRoutine[0].OID {
		t.Errorf("otherDBOid: 'apps.route'::regproc = %d, want its own %d", got, otherRoutine[0].OID)
	}
	// The other database's SAME-schema-same-name routine must not resolve here.
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	rows = runQueryUnderDBOid(t, ctx, "SELECT 'apps.route'::regproc")
	if got := uint32(rows[0][0].Int); got != defRoutine[0].OID {
		t.Errorf("DefaultDBOid: 'apps.route'::regproc = %d, want its own %d", got, defRoutine[0].OID)
	}
}

// TestRegProcInputDistinctDBOidMissIsNotDefaultLeak pins the isolation
// boundary the fix must NOT cross: from a distinct-dbOid connection, a routine
// that exists ONLY under DefaultDBOid must raise the 42883 miss (never resolve
// to another database's routine by name), mirroring regclass's "the fallback
// must not leak DefaultDBOid's real user tables into an unrelated database's
// connection" guard.
func TestRegProcInputDistinctDBOidMissIsNotDefaultLeak(t *testing.T) {
	const otherDBOid = 7022
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	// The routine exists ONLY under DefaultDBOid.
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid
	if err := runDDL(t, ctx, `CREATE FUNCTION only_default_fn(a int4) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("CREATE FUNCTION only_default_fn: %v", err)
	}

	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("ctx.Catalog is %T, want *catalog.InMemory", ctx.Catalog)
	}
	defRoutine := im.Routines().LookupByName(parser.ObjectName{Name: "only_default_fn"}, catalog.DefaultDBOid)
	if len(defRoutine) == 0 {
		t.Fatal("only_default_fn not found under DefaultDBOid")
	}

	ctx.CurrentDatabaseOid = otherDBOid
	_, err := regIdentifierInput(NewStringDatum("only_default_fn"), "regproc", ctx, 0)
	execErr, isErr := err.(*ExecError)
	if !isErr || execErr.Code != "42883" {
		t.Fatalf("otherDBOid: regIdentifierInput(only_default_fn) = %v, want 42883 (must not resolve DefaultDBOid's routine)", err)
	}
	_ = fmt.Sprintf("%d", defRoutine[0].OID) // pin the OID so the miss is not a "resolve to default" success
}
