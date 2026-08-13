package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0119-0006 (68th slice): TEXT/CSV COPY of a reg* column renders its NAME, not
// its numeric OID. Before this slice datumToCopyText had no reg* arm, so a
// regrole column copied OUT as `10` where regroleout emits `postgres`, and the
// COPY FROM row reached encodeValuePG as a KindString that the numeric oid arm
// misparsed. The renderer is now RegOut — the same OID→name switch the SELECT
// wire path (appendTypedCellText) uses — threaded through the renderers as
// (cat, qualify); the COPY FROM row is routed through the INSERT path's
// coerceRowForConstraintChecks with a reg*-only include filter.

// regCopyCat returns a Context whose InMemory catalog carries a user table
// (mytable), a user role (alice, OID 7777) and a user collation (mycoll), so
// every reg* family has a non-builtin hit to resolve.
func regCopyCat(t *testing.T) *Context {
	t.Helper()
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE TABLE mytable (id int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	ctx.Catalog.(*catalog.InMemory).RegisterRoleWithOID("alice", 7777)
	if err := runDDL(t, ctx, `CREATE COLLATION mycoll (LOCALE = 'C')`); err != nil {
		t.Fatalf("CREATE COLLATION: %v", err)
	}
	return ctx
}

func TestRegCopyToRendersName(t *testing.T) {
	ctx := regCopyCat(t)
	im := ctx.Catalog.(*catalog.InMemory)
	mytable, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "mytable"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		t.Fatal("mytable not found")
	}
	mycollOID := im.UserCollationOIDByName("mycoll")
	if mycollOID == 0 {
		t.Fatal("mycoll not found")
	}
	aliceOID, found := im.RoleOID("alice")
	if !found {
		t.Fatal("alice not found")
	}
	cases := []struct {
		typ  string
		oid  int64
		want string
	}{
		{"regrole", 10, "postgres"},
		{"regrole", int64(aliceOID), "alice"},
		// pg_class is OID 1259 — the one system-catalog regclass resolution a
		// bare InMemory catalog can serve (pg_class comes from the VIRTUAL
		// builder; the heap-backed catalogs like pg_type are not loaded into
		// the OID→table map without initdb).
		{"regclass", 1259, "pg_class"},
		{"regclass", int64(mytable.OID), "mytable"},
		{"regtype", int64(catalog.OIDInt4), "integer"},
		{"regtype", int64(catalog.OIDText), "text"},
		// The same hardcoded pg_proc OIDs the server sibling test uses (regproc
		// Output, OID 43 = int4out, 42 = int4in): the hand-curated
		// builtinProcsByName set does not hold the operator functions, but
		// RegprocName/RegprocedureName resolve against the live InMemory proc
		// registry that initdb's BKI populates.
		{"regproc", 43, "int4out"},
		{"regprocedure", 43, "int4out(integer)"},
		{"regcollation", 950, "C"},
		{"regcollation", int64(mycollOID), "mycoll"},
	}
	csvFmt := copyToFormatFromOptions([]parser.CopyOption{{Name: "format", Value: "csv"}})
	for _, tc := range cases {
		cols := []catalog.Column{{Name: "c", Type: catalog.Type{Name: tc.typ}}}
		row := Row{NewIntDatum(tc.oid)}
		text, err := EncodeCopyTextRow(nil, row, cols, "ISO", "MDY", "", ctx.Catalog, false)
		if err != nil {
			t.Fatalf("TEXT %s: %v", tc.typ, err)
		}
		if want := tc.want + "\n"; string(text) != want {
			t.Errorf("TEXT %s(oid %d) = %q, want %q", tc.typ, tc.oid, text, want)
		}
		csv, err := EncodeCopyCsvRow(nil, row, cols, csvFmt, "ISO", "MDY", "", ctx.Catalog, false)
		if err != nil {
			t.Fatalf("CSV %s: %v", tc.typ, err)
		}
		if want := tc.want + "\n"; string(csv) != want {
			t.Errorf("CSV %s(oid %d) = %q, want %q", tc.typ, tc.oid, csv, want)
		}
	}
}

// Every reg*out renders OID 0 (InvalidOid) as "-" (regproc.c:949-953 etc.); the
// pre-68th regclass SELECT case instead matched an OID-0 information_schema
// virtual table and rendered a nondeterministic name, which is the behavioral
// fix this slice makes both SELECT and COPY agree on.
func TestRegCopyToInvalidOidIsDash(t *testing.T) {
	ctx := regCopyCat(t)
	csvFmt := copyToFormatFromOptions([]parser.CopyOption{{Name: "format", Value: "csv"}})
	for _, typ := range []string{"regclass", "regproc", "regprocedure", "regtype", "regrole", "regcollation"} {
		cols := []catalog.Column{{Name: "c", Type: catalog.Type{Name: typ}}}
		row := Row{NewIntDatum(0)}
		text, err := EncodeCopyTextRow(nil, row, cols, "ISO", "MDY", "", ctx.Catalog, false)
		if err != nil {
			t.Fatalf("TEXT %s: %v", typ, err)
		}
		if want := "-\n"; string(text) != want {
			t.Errorf("TEXT %s(oid 0) = %q, want %q", typ, text, want)
		}
		csv, err := EncodeCopyCsvRow(nil, row, cols, csvFmt, "ISO", "MDY", "", ctx.Catalog, false)
		if err != nil {
			t.Fatalf("CSV %s: %v", typ, err)
		}
		if want := "-\n"; string(csv) != want {
			t.Errorf("CSV %s(oid 0) = %q, want %q", typ, csv, want)
		}
	}
}

// A KindString datum in a reg* column (a `::reg*` cast result feeding
// `COPY (SELECT ...) TO`) is already rendered text and must pass through — the
// same fall-through the SELECT path uses for a non-int datum.
func TestRegCopyToKindStringPassthrough(t *testing.T) {
	ctx := regCopyCat(t)
	cols := []catalog.Column{{Name: "c", Type: catalog.Type{Name: "regclass"}}}
	row := Row{NewStringDatum("pg_type")}
	got, err := EncodeCopyTextRow(nil, row, cols, "ISO", "MDY", "", ctx.Catalog, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "pg_type\n"; string(got) != want {
		t.Errorf("KindString regclass = %q, want %q", got, want)
	}
}

// COPY FROM: a name field into a reg* column resolves through the SAME
// coerceRowForConstraintChecks the INSERT path uses, with a reg*-only include
// filter (the wiring insertSourceRow now performs). "-" becomes OID 0, a
// pure-digit field a numeric OID, and a miss raises the family's OWN SQLSTATE
// unwrapped (42P01 for regclass, 42704 for regrole) — not a 22P04 wrap.
func TestRegCopyFromResolvesName(t *testing.T) {
	ctx := regCopyCat(t)
	im := ctx.Catalog.(*catalog.InMemory)
	mytable, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "mytable"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		t.Fatal("mytable not found")
	}

	regclassCols := []catalog.Column{{Name: "r", Type: catalog.Type{Name: "regclass"}}}
	decoded, err := DecodeCopyTextRow([]byte("mytable"), regclassCols, `\N`, "")
	if err != nil {
		t.Fatalf("DecodeCopyTextRow: %v", err)
	}
	if decoded[0].Kind != KindString {
		t.Fatalf("decoded regclass field = kind %d, want KindString (unresolved)", decoded[0].Kind)
	}
	if err := coerceRowForConstraintChecks(regclassCols, decoded, func(i int) bool {
		return isRegIdentifierTypeName(regclassCols[i].Type.Name)
	}, ctx, 0); err != nil {
		t.Fatalf("coerce(regclass name): %v", err)
	}
	if decoded[0].Kind != KindInt || decoded[0].Int != int64(mytable.OID) {
		t.Fatalf("regclass name coerced to kind %d %v, want int OID %d", decoded[0].Kind, decoded[0].Int, mytable.OID)
	}

	roleCols := []catalog.Column{{Name: "r", Type: catalog.Type{Name: "regrole"}}}
	roleRow, err := DecodeCopyTextRow([]byte("postgres"), roleCols, `\N`, "")
	if err != nil {
		t.Fatalf("DecodeCopyTextRow(regrole): %v", err)
	}
	if err := coerceRowForConstraintChecks(roleCols, roleRow, func(i int) bool {
		return isRegIdentifierTypeName(roleCols[i].Type.Name)
	}, ctx, 0); err != nil {
		t.Fatalf("coerce(regrole name): %v", err)
	}
	if roleRow[0].Kind != KindInt || roleRow[0].Int != 10 {
		t.Fatalf("regrole name coerced to kind %d %v, want int 10 (postgres)", roleRow[0].Kind, roleRow[0].Int)
	}
	// "alice" resolves through the live role map too.
	aliceOID, _ := im.RoleOID("alice")
	aliceRow, err := DecodeCopyTextRow([]byte("alice"), roleCols, `\N`, "")
	if err != nil {
		t.Fatalf("DecodeCopyTextRow(regrole alice): %v", err)
	}
	if err := coerceRowForConstraintChecks(roleCols, aliceRow, func(i int) bool {
		return isRegIdentifierTypeName(roleCols[i].Type.Name)
	}, ctx, 0); err != nil {
		t.Fatalf("coerce(regrole alice): %v", err)
	}
	if aliceRow[0].Kind != KindInt || aliceRow[0].Int != int64(aliceOID) {
		t.Fatalf("regrole alice coerced to kind %d %v, want int %d", aliceRow[0].Kind, aliceRow[0].Int, aliceOID)
	}

	// A miss surfaces the family's OWN error code, unwrapped.
	miss, err := DecodeCopyTextRow([]byte("no_such_table"), regclassCols, `\N`, "")
	if err != nil {
		t.Fatalf("DecodeCopyTextRow(miss): %v", err)
	}
	if err := coerceRowForConstraintChecks(regclassCols, miss, func(i int) bool {
		return isRegIdentifierTypeName(regclassCols[i].Type.Name)
	}, ctx, 0); err == nil {
		t.Fatal("coerce(no_such_table) should raise 42P01")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42P01" {
		t.Fatalf("coerce(no_such_table) err = %v, want 42P01", err)
	}
	if err := coerceRowForConstraintChecks(roleCols, Row{NewStringDatum("no_such_role")}, func(i int) bool {
		return isRegIdentifierTypeName(roleCols[i].Type.Name)
	}, ctx, 0); err == nil {
		t.Fatal("coerce(no_such_role) should raise 42704")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Fatalf("coerce(no_such_role) err = %v, want 42704", err)
	}
}

// parseRegDashOrOid on the FROM path: "-" is OID 0 (InvalidOid) and a
// pure-digit field is a numeric OID, never a name to resolve.
func TestRegCopyFromNumericAndDash(t *testing.T) {
	ctx := regCopyCat(t)
	cols := []catalog.Column{{Name: "r", Type: catalog.Type{Name: "regrole"}}}
	for _, tc := range []struct {
		field string
		want  int64
	}{
		{"-", 0},
		{"10", 10},
		{"16422", 16422},
	} {
		row, err := DecodeCopyTextRow([]byte(tc.field), cols, `\N`, "")
		if err != nil {
			t.Fatalf("DecodeCopyTextRow(%q): %v", tc.field, err)
		}
		if err := coerceRowForConstraintChecks(cols, row, func(i int) bool {
			return isRegIdentifierTypeName(cols[i].Type.Name)
		}, ctx, 0); err != nil {
			t.Fatalf("coerce(%q): %v", tc.field, err)
		}
		if row[0].Kind != KindInt || row[0].Int != tc.want {
			t.Errorf("field %q coerced to kind %d %v, want int %d", tc.field, row[0].Kind, row[0].Int, tc.want)
		}
	}
}

// The include filter must admit ONLY the reg* family: a non-reg* column (int4
// here) that copyTextToDatum already typed is left untouched — re-coercing it
// is exactly the drift the filter exists to prevent.
func TestRegCopyFromFilterLeavesNonRegTypesUntouched(t *testing.T) {
	ctx := regCopyCat(t)
	mytable, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "mytable"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		t.Fatal("mytable not found")
	}
	cols := []catalog.Column{
		{Name: "r", Type: catalog.Type{Name: "regclass"}},
		{Name: "n", Type: catalog.Type{Name: "int4"}},
	}
	row := Row{NewStringDatum("mytable"), NewIntDatum(5)}
	if err := coerceRowForConstraintChecks(cols, row, func(i int) bool {
		return isRegIdentifierTypeName(cols[i].Type.Name)
	}, ctx, 0); err != nil {
		t.Fatalf("coerce(mixed row): %v", err)
	}
	if row[0].Kind != KindInt || row[0].Int != int64(mytable.OID) {
		t.Errorf("regclass col = kind %d %v, want int OID %d", row[0].Kind, row[0].Int, mytable.OID)
	}
	if row[1].Kind != KindInt || row[1].Int != 5 {
		t.Errorf("int4 col = kind %d %v, want untouched int 5", row[1].Kind, row[1].Int)
	}
}

// The family predicate: exactly the six reg* types with a name→OID seam. oid
// and cid are numeric-only and must be excluded (the encode/align arms keep
// their own wider list because they also cover those two). Matching is
// case-insensitive (the pervasive strings.ToLower idiom), and the reg* family
// are fixed builtin type names, so mixed case is admitted the same way every
// other type-name switch in the codebase admits it.
func TestIsRegIdentifierTypeName(t *testing.T) {
	for _, name := range []string{"regproc", "regprocedure", "regclass", "regtype", "regrole", "regcollation"} {
		if !isRegIdentifierTypeName(name) {
			t.Errorf("isRegIdentifierTypeName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"RegClass", "REGROLE", "RegProcedure"} {
		if !isRegIdentifierTypeName(name) {
			t.Errorf("isRegIdentifierTypeName(%q) = false, want true (case-insensitive)", name)
		}
	}
	for _, name := range []string{"oid", "cid", "int4", "text", ""} {
		if isRegIdentifierTypeName(name) {
			t.Errorf("isRegIdentifierTypeName(%q) = true, want false", name)
		}
	}
}
