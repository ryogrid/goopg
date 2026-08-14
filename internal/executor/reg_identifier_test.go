package executor

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0119-0006 (reg* family + cid 4-byte storage). Before this slice the heap
// codec recognised only `oid` and `regproc` as 4-byte identifiers; `regclass`,
// `regtype`, `regprocedure` and `cid` fell through encodeValuePG's default and
// were stored as varlena TEXT, so a hosted PG reading the column saw text where
// it expects a 4-byte identifier (regclasssend/regtypesend/regproceduresend/
// cidsend are all pq_sendint32). These tests pin the storage width/alignment and
// the name→OID input resolution.

func TestRegIdentifierColumnStoresFourBytesNotText(t *testing.T) {
	for _, name := range []string{"regclass", "regtype", "regprocedure", "regrole", "regcollation", "cid"} {
		col := catalog.Type{Name: name}
		heap, err := encodeValuePG(col, NewIntDatum(16422))
		if err != nil {
			t.Fatalf("%s: encodeValuePG: %v", name, err)
		}
		if len(heap) != 4 {
			t.Fatalf("%s: heap width = %d bytes, want 4 (not varlena text)", name, len(heap))
		}
		if binary.LittleEndian.Uint32(heap) != 16422 {
			t.Fatalf("%s: heap bytes = %#x, want OID 16422", name, heap)
		}
		if pgPhysicalTypeIsVarlena(col) {
			t.Fatalf("%s: pgPhysicalTypeIsVarlena = true, want false (typbyval)", name)
		}
		if got := physicalPGTypeAlign(col); got != 4 {
			t.Fatalf("%s: physicalPGTypeAlign = %d, want 4 (typalign 'i')", name, got)
		}
		// Decode twin must reproduce the OID, not a text Datum.
		back, n, err := decodePhysicalPGValueMctx(col, heap, nil)
		if err != nil {
			t.Fatalf("%s: decodePhysicalPGValueMctx: %v", name, err)
		}
		if n != 4 || back.Kind != KindInt || back.Int != 16422 {
			t.Fatalf("%s: round-trip = kind %d %v after %d bytes, want int 16422 after 4", name, back.Kind, back.Int, n)
		}
	}
}

func TestRegIdentifierInputResolvesRegclassName(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE TABLE mytable (id int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	connDBOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "mytable"}, connDBOid)
	if !ok {
		t.Fatal("mytable not found in catalog")
	}

	got, err := regIdentifierInput(NewStringDatum("mytable"), "regclass", ctx, 0)
	if err != nil {
		t.Fatalf("regIdentifierInput(mytable): %v", err)
	}
	if got.Kind != KindInt || got.Int != int64(tbl.OID) {
		t.Fatalf("regIdentifierInput(mytable) = kind %d %v, want int OID %d", got.Kind, got.Int, tbl.OID)
	}

	// A miss raises the regclassin undefined-object error (42P01), not a numeric
	// parse failure — the whole point of resolving before the heap arm.
	if _, err := regIdentifierInput(NewStringDatum("no_such_table"), "regclass", ctx, 0); err == nil {
		t.Fatal("regIdentifierInput(no_such_table) should raise 42P01")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42P01" {
		t.Fatalf("regIdentifierInput(no_such_table) err = %v, want 42P01", err)
	}

	// A numeric OID datum passes through unchanged (already an OID).
	passthrough, err := regIdentifierInput(NewIntDatum(int64(tbl.OID)), "regclass", ctx, 0)
	if err != nil {
		t.Fatalf("regIdentifierInput(KindInt): %v", err)
	}
	if passthrough.Kind != KindInt || passthrough.Int != int64(tbl.OID) {
		t.Fatalf("regIdentifierInput(KindInt) = %v, want pass-through %d", passthrough, tbl.OID)
	}
}

func TestRegIdentifierInputResolvesRegtypeName(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()

	// A builtin type name resolves through the static OID table.
	got, err := regIdentifierInput(NewStringDatum("int4"), "regtype", ctx, 0)
	if err != nil {
		t.Fatalf("regIdentifierInput(int4): %v", err)
	}
	if got.Kind != KindInt || got.Int != int64(catalog.OIDInt4) {
		t.Fatalf("regIdentifierInput(int4) = %v, want OID %d", got, catalog.OIDInt4)
	}

	// A miss raises the regtypein undefined-object error (42704).
	if _, err := regIdentifierInput(NewStringDatum("no_such_type"), "regtype", ctx, 0); err == nil {
		t.Fatal("regIdentifierInput(no_such_type) should raise 42704")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Fatalf("regIdentifierInput(no_such_type) err = %v, want 42704", err)
	}
}

// M0119-0006 (67th slice — regrole/regcollation 4-byte storage). regrolein
// (regproc.c:1541) resolves a single-identifier role name through get_role_oid;
// a qualified name (list_length != 1) is 42602 invalid name syntax, and a miss
// is 42704 `role "%s" does not exist`.
func TestRegIdentifierInputResolvesRegroleName(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	// Register a user role directly (CREATE ROLE is outside this parser path);
	// 7777 is a stand-in for an initdb/nextOID-minted OID.
	im := ctx.Catalog.(*catalog.InMemory)
	im.RegisterRoleWithOID("alice", 7777)

	// The bootstrap superuser resolves to OID 10 (BOOTSTRAP_SUPERUSERID).
	got, err := regIdentifierInput(NewStringDatum("postgres"), "regrole", ctx, 0)
	if err != nil {
		t.Fatalf("regIdentifierInput(postgres): %v", err)
	}
	if got.Kind != KindInt || got.Int != 10 {
		t.Fatalf("regIdentifierInput(postgres) = %v, want int 10", got)
	}

	// A user-created role resolves through the live role map.
	aliceOID, ok := im.RoleOID("alice")
	if !ok {
		t.Fatal("alice not found via RoleOID")
	}
	got, err = regIdentifierInput(NewStringDatum("alice"), "regrole", ctx, 0)
	if err != nil {
		t.Fatalf("regIdentifierInput(alice): %v", err)
	}
	if got.Kind != KindInt || got.Int != int64(aliceOID) {
		t.Fatalf("regIdentifierInput(alice) = %v, want int %d", got, aliceOID)
	}

	// A miss raises the regrolein undefined-object error (42704).
	if _, err := regIdentifierInput(NewStringDatum("no_such_role"), "regrole", ctx, 0); err == nil {
		t.Fatal("regIdentifierInput(no_such_role) should raise 42704")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Fatalf("regIdentifierInput(no_such_role) err = %v, want 42704", err)
	}

	// A qualified role name is 42602 — roles are never schema-qualified.
	if _, err := regIdentifierInput(NewStringDatum("public.alice"), "regrole", ctx, 0); err == nil {
		t.Fatal("regIdentifierInput(public.alice) should raise 42602")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42602" {
		t.Fatalf("regIdentifierInput(public.alice) err = %v, want 42602", err)
	}
}

// regcollationin (regproc.c:1026) resolves a (possibly qualified) collation
// name through get_collation_oid; a miss is 42704
// `collation "%s" for encoding "%s" does not exist` (goopg is UTF-8 only).
func TestRegIdentifierInputResolvesRegcollationName(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE COLLATION mycoll (LOCALE = 'C')`); err != nil {
		t.Fatalf("CREATE COLLATION: %v", err)
	}

	// Builtin collations resolve through the BKI-pinned table (builtin-then-user
	// order in CollationOIDByName). The store is case-sensitive with
	// PG-identifier semantics, so an UNQUOTED `C`/`POSIX` downcasts to
	// `c`/`posix` and MISSES (PG 18.3: `'C'::regcollation` → 42704; only the
	// quoted `'"C"'` resolves to 950) — the 72nd slice made the input path
	// follow SplitIdentifierString's downcasing, closing the old leniency.
	for _, tc := range []struct {
		name string
		want uint32
	}{
		{"default", 100},
		{"DEFAULT", 100}, // downcast to "default"
		{`"C"`, 950},     // quoted — keeps the exact case
		{`"POSIX"`, 951}, // quoted — keeps the exact case
	} {
		got, err := regIdentifierInput(NewStringDatum(tc.name), "regcollation", ctx, 0)
		if err != nil {
			t.Fatalf("regIdentifierInput(%s): %v", tc.name, err)
		}
		if got.Kind != KindInt || got.Int != int64(tc.want) {
			t.Fatalf("regIdentifierInput(%s) = %v, want int %d", tc.name, got, tc.want)
		}
	}

	// An unquoted mixed-case builtin collation name downcasts and misses exactly
	// like PG 18.3 (`'C'::regcollation` → 42704 "collation "c" ... does not
	// exist").
	if _, err := regIdentifierInput(NewStringDatum("C"), "regcollation", ctx, 0); err == nil {
		t.Fatal("regIdentifierInput(C) should raise 42704 (downcast to c, PG-faithful)")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Fatalf("regIdentifierInput(C) err = %v, want 42704", err)
	}

	// A user-created collation resolves through the live registry.
	im := ctx.Catalog.(*catalog.InMemory)
	mycollOID := im.UserCollationOIDByName("mycoll")
	if mycollOID == 0 {
		t.Fatal("mycoll not found via UserCollationOIDByName")
	}
	got, err := regIdentifierInput(NewStringDatum("mycoll"), "regcollation", ctx, 0)
	if err != nil {
		t.Fatalf("regIdentifierInput(mycoll): %v", err)
	}
	if got.Kind != KindInt || got.Int != int64(mycollOID) {
		t.Fatalf("regIdentifierInput(mycoll) = %v, want int %d", got, mycollOID)
	}

	// A miss raises the regcollationin undefined-object error (42704).
	if _, err := regIdentifierInput(NewStringDatum("no_such_coll"), "regcollation", ctx, 0); err == nil {
		t.Fatal("regIdentifierInput(no_such_coll) should raise 42704")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Fatalf("regIdentifierInput(no_such_coll) err = %v, want 42704", err)
	}
}

// parseDashOrOid (regproc.c) runs before ANY name resolution for every reg*
// type — the family-wide latent gap the 66th slice left: '-' is InvalidOid (0)
// and a pure-digit string is a numeric OID via oidin (never a name).
func TestRegIdentifierInputAcceptsDashAndNumericOid(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()

	for _, name := range []string{"regclass", "regtype", "regproc", "regrole", "regcollation"} {
		got, err := regIdentifierInput(NewStringDatum("-"), name, ctx, 0)
		if err != nil {
			t.Fatalf("regIdentifierInput(-) %s: %v", name, err)
		}
		if got.Kind != KindInt || got.Int != 0 {
			t.Fatalf("regIdentifierInput(-) %s = %v, want int 0 (InvalidOid)", name, got)
		}
	}

	got, err := regIdentifierInput(NewStringDatum("16422"), "regrole", ctx, 0)
	if err != nil {
		t.Fatalf("regIdentifierInput(16422): %v", err)
	}
	if got.Kind != KindInt || got.Int != 16422 {
		t.Fatalf("regIdentifierInput(16422) = %v, want int 16422", got)
	}

	// A pure-digit string beyond uint32 raises oidin's 22003 (parseDashOrOid →
	// oidin_cstr), never falling through to name resolution.
	if _, err := regIdentifierInput(NewStringDatum("99999999999999"), "regrole", ctx, 0); err == nil {
		t.Fatal("regIdentifierInput(overflow) should raise 22003")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "22003" {
		t.Fatalf("regIdentifierInput(overflow) err = %v, want 22003", err)
	}
}

// INSERT: coerceRowForConstraintChecks is the single coercion point every new
// row passes through; a reg* column must resolve a bare quoted name literal to
// its OID there (regclassin/regtypein), so the heap arm stores the 4-byte
// identifier instead of the name as text. The reverse is a miss-path check: an
// unresolvable name raises the reg*in undefined-object error, not a silent text
// store.
func TestCoerceRowForConstraintChecksResolvesRegIdentifier(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE TABLE mytable (id int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	connDBOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "mytable"}, connDBOid)
	if !ok {
		t.Fatal("mytable not found in catalog")
	}

	cols := []catalog.Column{{Name: "r", Type: catalog.Type{Name: "regclass"}}}
	row := Row{NewStringDatum("mytable")}
	if err := coerceRowForConstraintChecks(cols, row, func(int) bool { return true }, ctx, 0); err != nil {
		t.Fatalf("coerceRowForConstraintChecks(regclass): %v", err)
	}
	if row[0].Kind != KindInt || row[0].Int != int64(tbl.OID) {
		t.Fatalf("regclass column coerced to kind %d %v, want int OID %d", row[0].Kind, row[0].Int, tbl.OID)
	}
	// The coerced OID must survive the heap encode as 4 bytes — the whole point
	// of resolving before the oid arm.
	heap, err := encodeValuePG(catalog.Type{Name: "regclass"}, row[0])
	if err != nil {
		t.Fatalf("encodeValuePG(regclass OID): %v", err)
	}
	if len(heap) != 4 || binary.LittleEndian.Uint32(heap) != tbl.OID {
		t.Fatalf("regclass heap = %x (%d bytes), want 4-byte OID %d", heap, len(heap), tbl.OID)
	}

	// A miss raises 42P01 through the choke point, not a silent text store.
	if err := coerceRowForConstraintChecks(cols, Row{NewStringDatum("no_such_table")}, func(int) bool { return true }, ctx, 0); err == nil {
		t.Fatal("coerceRowForConstraintChecks(no_such_table) should raise 42P01")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42P01" {
		t.Fatalf("coerceRowForConstraintChecks(no_such_table) err = %v, want 42P01", err)
	}
}

// M0119-0006 (67th slice): the same choke point resolves 'postgres'/'C' for
// regrole/regcollation columns, and the coerced OIDs encode to 4 bytes. A miss
// raises the type's own 42704 instead of a silent text store.
func TestCoerceRowForConstraintChecksResolvesRegRoleAndCollation(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()

	roleCols := []catalog.Column{{Name: "r", Type: catalog.Type{Name: "regrole"}}}
	roleRow := Row{NewStringDatum("postgres")}
	if err := coerceRowForConstraintChecks(roleCols, roleRow, func(int) bool { return true }, ctx, 0); err != nil {
		t.Fatalf("coerceRowForConstraintChecks(regrole): %v", err)
	}
	if roleRow[0].Kind != KindInt || roleRow[0].Int != 10 {
		t.Fatalf("regrole column coerced to kind %d %v, want int 10 (postgres)", roleRow[0].Kind, roleRow[0].Int)
	}
	heap, err := encodeValuePG(catalog.Type{Name: "regrole"}, roleRow[0])
	if err != nil {
		t.Fatalf("encodeValuePG(regrole OID): %v", err)
	}
	if len(heap) != 4 || binary.LittleEndian.Uint32(heap) != 10 {
		t.Fatalf("regrole heap = %x (%d bytes), want 4-byte OID 10", heap, len(heap))
	}
	if err := coerceRowForConstraintChecks(roleCols, Row{NewStringDatum("no_such_role")}, func(int) bool { return true }, ctx, 0); err == nil {
		t.Fatal("coerceRowForConstraintChecks(no_such_role) should raise 42704")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Fatalf("coerceRowForConstraintChecks(no_such_role) err = %v, want 42704", err)
	}

	collCols := []catalog.Column{{Name: "c", Type: catalog.Type{Name: "regcollation"}}}
	collRow := Row{NewStringDatum(`"C"`)} // quoted — the unquoted `C` downcasts to `c` and misses (PG-faithful)
	if err := coerceRowForConstraintChecks(collCols, collRow, func(int) bool { return true }, ctx, 0); err != nil {
		t.Fatalf("coerceRowForConstraintChecks(regcollation): %v", err)
	}
	if collRow[0].Kind != KindInt || collRow[0].Int != 950 {
		t.Fatalf("regcollation column coerced to kind %d %v, want int 950 (quoted \"C\")", collRow[0].Kind, collRow[0].Int)
	}
	heap, err = encodeValuePG(catalog.Type{Name: "regcollation"}, collRow[0])
	if err != nil {
		t.Fatalf("encodeValuePG(regcollation OID): %v", err)
	}
	if len(heap) != 4 || binary.LittleEndian.Uint32(heap) != 950 {
		t.Fatalf("regcollation heap = %x (%d bytes), want 4-byte OID 950", heap, len(heap))
	}
	if err := coerceRowForConstraintChecks(collCols, Row{NewStringDatum("no_such_coll")}, func(int) bool { return true }, ctx, 0); err == nil {
		t.Fatal("coerceRowForConstraintChecks(no_such_coll) should raise 42704")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Fatalf("coerceRowForConstraintChecks(no_such_coll) err = %v, want 42704", err)
	}
}
