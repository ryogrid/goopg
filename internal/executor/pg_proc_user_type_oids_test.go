package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPGProcUserTypeOIDs pins M0148-0001j4 item (2): pg_proc's proargtypes /
// proallargtypes / prorettype carry the OID LookupTypeName found at CREATE
// FUNCTION (ProcedureCreate). goopg resolved built-in names only, and
// catalog.TypeNameToOID's text fallback wrote 25 for every user composite,
// enum, domain or range type (the virtual pg_proc view wrote 0). The heap row
// builder and the (proname, proargtypes, pronamespace) index key share
// pgProcInputArgOIDs, so both must see the user OIDs, arrays included.
func TestPGProcUserTypeOIDs(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TYPE st AS (a int, b text)",
		"CREATE TYPE en AS ENUM ('x')",
		"CREATE DOMAIN dm AS int",
		"CREATE FUNCTION f3(st, st[], en, en[], dm, int[], text) RETURNS st[] LANGUAGE sql AS 'SELECT NULL::st[]'",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	im := ctx.Catalog.(*catalog.InMemory)
	db := ctx.CurrentDatabaseOid
	ct := im.LookupCompositeType("st", db)
	et, _ := im.LookupEnum("en", db)
	d, _ := im.LookupDomain("dm", db)
	if ct == nil || et == nil || d == nil {
		t.Fatalf("fixture types missing: st=%v en=%v dm=%v", ct, et, d)
	}
	rs := routineRegistry(ctx)
	rr := rs.LookupByName(parser.ObjectName{Name: "f3"})
	if len(rr) != 1 {
		t.Fatalf("LookupByName(f3) = %d routines", len(rr))
	}
	r := rr[0]
	want := []uint32{ct.OID, ct.ArrayOID, et.OID, et.ArrayOID, d.OID, 1007, catalog.OIDText}
	got := pgProcInputArgOIDs(ctx.Catalog, r)
	if len(got) != len(want) {
		t.Fatalf("proargtypes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("proargtypes[%d] (%s) = %d, want %d", i, r.ArgTypes[i].Name, got[i], want[i])
		}
	}
	if ret := PGProcTypeOID(ctx.Catalog, r.ReturnType.Name, r.DBOid); ret != ct.ArrayOID {
		t.Errorf("prorettype = %d, want st[] %d", ret, ct.ArrayOID)
	}
	if o := PGProcTypeOID(ctx.Catalog, "no_such_type", r.DBOid); o != catalog.OIDText {
		t.Errorf("unresolved name = %d, want the text fallback %d", o, catalog.OIDText)
	}
}
