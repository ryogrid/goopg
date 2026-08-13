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
	for _, name := range []string{"regclass", "regtype", "regprocedure", "cid"} {
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
