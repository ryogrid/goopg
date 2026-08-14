package executor

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0119-0006 reg* array-element slice (deferral 1306). An array-of-reg* column
// must store 4-byte LE OIDs — resolved through the SAME regIdentifierInput the
// scalar path uses, so the miss SQLSTATEs (42P01 regclass, 42704 regtype/
// regrole/regcollation, 42883 regproc/regprocedure) match — and render them
// back through executor.RegOut, so the array form is byte-identical to the
// scalar form and SELECT/COPY TO agree.

// TestEncodeArrayRegStarStoresOID pins acceptance criterion 2: the blob header
// elemtype is the _reg* type OID (NOT text 25) and each element is the resolved
// 4-byte LE OID. '{mytable}' resolves through the catalog, '{1259}' is a
// numeric OID and '{-}' is OID 0 (parseDashOrOid).
func TestEncodeArrayRegStarStoresOID(t *testing.T) {
	ctx := regCopyCat(t)
	mytable, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "mytable"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		t.Fatal("mytable not found")
	}
	regclassCol := catalog.Column{Name: "r", Type: catalog.Type{Name: "regclass", IsArray: true}}
	im := ctx.Catalog.(*catalog.InMemory)
	mycollOID := im.UserCollationOIDByName("mycoll")
	if mycollOID == 0 {
		t.Fatal("mycoll collation not found")
	}

	blob, err := encodeArrayValuePGCtx(regclassCol.Type, NewStringDatum("{mytable}"), ctx, 0)
	if err != nil {
		t.Fatalf("encode '{mytable}': %v", err)
	}
	if et := binary.LittleEndian.Uint32(blob[12:16]); et != 2205 {
		t.Errorf("elemtype = %d, want 2205 (_regclass), not text 25", et)
	}
	if got := binary.LittleEndian.Uint32(blob[24:28]); got != mytable.OID {
		t.Errorf("element = %d, want %d (mytable)", got, mytable.OID)
	}

	for _, tc := range []struct{ lit string; want uint32 }{
		{"{1259}", 1259}, // numeric OID passes through without the catalog
		{"{-}", 0},       // "-" is InvalidOid
	} {
		blob, err := encodeArrayValuePGCtx(regclassCol.Type, NewStringDatum(tc.lit), ctx, 0)
		if err != nil {
			t.Fatalf("encode %q: %v", tc.lit, err)
		}
		if got := binary.LittleEndian.Uint32(blob[24:28]); got != tc.want {
			t.Errorf("%q element = %d, want %d", tc.lit, got, tc.want)
		}
	}

	// The other five members store their resolved OID too (anchors are the same
	// hardcoded pg_proc/pg_collation OIDs the 68th-slice scalar tests use).
	for _, tc := range []struct {
		typ, lit string
		want     uint32
	}{
		{"regrole", "{postgres}", 10},
		{"regtype", "{integer}", catalog.OIDInt4},
		// eqsel is in the curated builtinProcsByName set (OID 101), which the
		// bare InMemory catalog can resolve; regprocedurein strips the arg list
		// and resolves the same name.
		{"regproc", "{eqsel}", 101},
		{"regprocedure", "{eqsel(int4)}", 101},
		{"regcollation", "{mycoll}", mycollOID},
	} {
		col := catalog.Column{Name: "r", Type: catalog.Type{Name: tc.typ, IsArray: true}}
		blob, err := encodeArrayValuePGCtx(col.Type, NewStringDatum(tc.lit), ctx, 0)
		if err != nil {
			t.Fatalf("encode %s %q: %v", tc.typ, tc.lit, err)
		}
		if et := binary.LittleEndian.Uint32(blob[12:16]); et == 25 {
			t.Errorf("%s %q elemtype = text (25), want the _%s type OID", tc.typ, tc.lit, tc.typ)
		}
		if got := binary.LittleEndian.Uint32(blob[24:28]); got != tc.want {
			t.Errorf("%s %q element = %d, want %d", tc.typ, tc.lit, got, tc.want)
		}
	}
}

// TestEncodeArrayRegStarMissSQLStates pins that a miss inside the array raises
// the ELEMENT type's own SQLSTATE (array_in routes each element through the
// element typinput, regproc.c), NOT a 22P02/22P04 wrap.
func TestEncodeArrayRegStarMissSQLStates(t *testing.T) {
	ctx := regCopyCat(t)
	for _, tc := range []struct {
		typ, lit, code string
	}{
		{"regclass", "{no_such_table}", "42P01"},
		{"regtype", "{no_such_type}", "42704"},
		{"regrole", "{no_such_role}", "42704"},
		{"regcollation", "{no_such_coll}", "42704"},
		{"regproc", "{no_such_fn}", "42883"},
		{"regprocedure", "{no_such_fn()}", "42883"},
	} {
		col := catalog.Column{Name: "r", Type: catalog.Type{Name: tc.typ, IsArray: true}}
		_, err := encodeArrayValuePGCtx(col.Type, NewStringDatum(tc.lit), ctx, 0)
		if err == nil {
			t.Errorf("%s %q: no error, want %s", tc.typ, tc.lit, tc.code)
			continue
		}
		if ee, ok := err.(*ExecError); !ok || ee.Code != tc.code {
			t.Errorf("%s %q err = %v, want code %s", tc.typ, tc.lit, err, tc.code)
		}
	}
}

// TestRegArraySelectCopySiblingAgree is the sibling-agreement test (acceptance
// criterion 3 + Hard-won Rule #2). SELECT and COPY TO both consume the SAME
// decoded array text (the scan operator flattens the array at heap-decode time
// through arrayOutputStyle's bound renderer), so decoding the stored blob with
// the session style IS the agreement point — and each element must be
// byte-identical to the scalar RegOut with the qualify rule COPY TO uses.
func TestRegArraySelectCopySiblingAgree(t *testing.T) {
	ctx := regCopyCat(t)
	mytable, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "mytable"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		t.Fatal("mytable not found")
	}
	col := catalog.Column{Name: "r", Type: catalog.Type{Name: "regclass", IsArray: true}}

	// A name, pg_class's numeric OID, InvalidOid and a dangling OID.
	blob, err := encodeArrayValuePGCtx(col.Type, NewStringDatum("{mytable,1259,-,9999}"), ctx, 0)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// The scalar RegOut each element must equal, with the qualify rule COPY TO
	// uses (arrayRegOutRenderer's own binding).
	qualify := !RegObjectSchemaVisible(ctx, "public")
	dbOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
	scalarMytable := RegOut("regclass", mytable.OID, ctx.Catalog, qualify, dbOid)
	scalarPgClass := RegOut("regclass", 1259, ctx.Catalog, qualify, dbOid)
	scalarDangling := RegOut("regclass", 9999, ctx.Catalog, qualify, dbOid)
	want := fmt.Sprintf("{%s,%s,-,%s}", scalarMytable, scalarPgClass, scalarDangling)
	if scalarPgClass != "pg_class" {
		t.Fatalf("oracle anchor: scalar regclass 1259 = %q, want pg_class", scalarPgClass)
	}

	d, _, err := decodeArrayValuePGStyled(col.Type, blob, arrayOutputStyle(ctx))
	if err != nil {
		t.Fatalf("heap decode (SELECT path): %v", err)
	}
	if got := d.StringValue(); got != want {
		t.Errorf("array decode = %q, want %q (scalar RegOut per element)", got, want)
	}

	// COPY TO consumes the decoded text verbatim (datumToCopyText's IsArray arm
	// passes the KindString through), so it must print the same bytes.
	text, err := EncodeCopyTextRow(nil, Row{d}, []catalog.Column{col}, "ISO", "MDY", "", ctx.Catalog, false)
	if err != nil {
		t.Fatalf("COPY TO: %v", err)
	}
	if wantRow := want + "\n"; string(text) != wantRow {
		t.Errorf("COPY TO = %q, want %q", text, wantRow)
	}
}
