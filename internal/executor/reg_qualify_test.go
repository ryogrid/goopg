package executor

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0119-0006 (69th slice, ledger row 1304): a reg* object name NOT visible on
// the effective search_path renders schema-qualified — `schema.name` — with the
// name (and schema) quote_identifier'd, exactly as upstream regclassout/
// regprocout/regroleout/regcollationout do via quote_qualified_identifier
// (regproc.c). regOutQualified is the shared rule; RegOut threads the qualify
// flag into it. These tests pin the behavior on the SELECT+COPY choke point.
//
// The qualify flag means "the object's schema is NOT on the session's effective
// search_path" — the COPY path computes it as !regObjectSchemaVisible(ctx,
// "public"), the SELECT path as !publicSchemaVisible(getSetting). pg_catalog is
// always searched implicitly (RelationIsVisible/CollationIsVisible), so an
// object there NEVER qualifies.

func TestRegOutQualifySchemaQualifiesNonPublic(t *testing.T) {
	ctx := regCopyCat(t)
	mytable, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "mytable"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		t.Fatal("mytable not found")
	}
	im := ctx.Catalog.(*catalog.InMemory)
	mycollOID := im.UserCollationOIDByName("mycoll")
	if mycollOID == 0 {
		t.Fatal("mycoll not found")
	}
	userFunc, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:       "my_udf",
		Schema:     "public",
		ReturnType: catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create: %v", err)
	}

	// regclass: mytable lives in public, which qualify=true treats as off the
	// effective search_path → `public.mytable` (not bare `mytable`).
	if got := RegOut("regclass", mytable.OID, ctx.Catalog, true); got != "public.mytable" {
		t.Errorf("regclass(my table) qualify=true = %q, want %q", got, "public.mytable")
	}
	// regcollation: a user collation resolves in public → `public.mycoll`.
	if got := RegOut("regcollation", mycollOID, ctx.Catalog, true); got != "public.mycoll" {
		t.Errorf("regcollation(user) qualify=true = %q, want %q", got, "public.mycoll")
	}
	// regproc: a user routine in public → `public.my_udf` (RegOut resolves it
	// through the live routine registry, not the builtin proc index).
	if got := RegOut("regproc", userFunc.OID, ctx.Catalog, true); got != "public.my_udf" {
		t.Errorf("regproc(user routine) qualify=true = %q, want %q", got, "public.my_udf")
	}
	// The same qualify=false callers used pre-69th keep the BARE name.
	if got := RegOut("regclass", mytable.OID, ctx.Catalog, false); got != "mytable" {
		t.Errorf("regclass(my table) qualify=false = %q, want %q", got, "mytable")
	}
}

// pg_catalog is implicitly searched by every search_path (regproc.c's
// RelationIsVisible/CollationIsVisible/FunctionIsVisible), so an object there
// is NEVER schema-qualified even when qualify=true — the object is visible by
// construction.
func TestRegOutPgCatalogNeverQualifies(t *testing.T) {
	ctx := regCopyCat(t)
	// 1259 = pg_class (the one system catalog the bare InMemory catalog can
	// serve from the VIRTUAL builder) — qualify=true must NOT emit
	// `pg_catalog.pg_class`.
	if got := RegOut("regclass", 1259, ctx.Catalog, true); got != "pg_class" {
		t.Errorf("regclass(1259) qualify=true = %q, want %q", got, "pg_class")
	}
	// A builtin proc resolves in pg_catalog too (regprocout never qualifies it).
	if got := RegOut("regproc", 43, ctx.Catalog, true); got != "int4out" {
		t.Errorf("regproc(43) qualify=true = %q, want %q", got, "int4out")
	}
	// A builtin collation (C, OID 950) likewise; its name is still quoted.
	if got := RegOut("regcollation", 950, ctx.Catalog, true); got != `"C"` {
		t.Errorf("regcollation(950) qualify=true = %q, want %q", got, `"C"`)
	}
}

// reg*out quote_identifier's every emitted name (regproc.c), so a name that is
// not lowercase-alphanumeric-safe — uppercase, a reserved keyword, embedded
// spaces — must come out quoted through the shared pgQuoteIdent guard
// (sqlkeywords.IsReservedForQuoting).
func TestRegOutQuotesIdentifiers(t *testing.T) {
	ctx := regCopyCat(t)
	im := ctx.Catalog.(*catalog.InMemory)

	// A table whose name needs quoting (uppercase + space).
	if err := runDDL(t, ctx, `CREATE TABLE "My Table" (id int)`); err != nil {
		t.Fatalf("CREATE TABLE \"My Table\": %v", err)
	}
	mt, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "My Table"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		t.Fatal(`"My Table" not found`)
	}
	if got := RegOut("regclass", mt.OID, ctx.Catalog, false); got != `"My Table"` {
		t.Errorf("regclass(\"My Table\") = %q, want %q", got, `"My Table"`)
	}
	// And schema-qualified when public is off the path.
	if got := RegOut("regclass", mt.OID, ctx.Catalog, true); got != `public."My Table"` {
		t.Errorf("regclass(\"My Table\") qualify=true = %q, want %q", got, `public."My Table"`)
	}

	// regroleout quote_identifiers the REAL role name: a reserved keyword role
	// name is quoted, a plain lowercase one is not. (A mixed-case role name is
	// unreachable from the catalog — goopg's role store folds every name to
	// lowercase, see the deferral row under 2026-08-14 (69th slice) — but the
	// uppercase quoting itself is proven above by `"My Table"` through the same
	// pgQuoteIdent guard.)
	im.RegisterRoleWithOID("select", 7778)
	if got := RegOut("regrole", 7778, ctx.Catalog, false); got != `"select"` {
		t.Errorf("regrole(keyword) = %q, want %q", got, `"select"`)
	}
	aliceOID, found := im.RoleOID("alice")
	if !found {
		t.Fatal("alice not found")
	}
	if got := RegOut("regrole", aliceOID, ctx.Catalog, false); got != "alice" {
		t.Errorf("regrole(alice) = %q, want %q", got, "alice")
	}
	// A user collation whose name needs quoting survives pgQuoteIdent on both
	// the bare and qualified forms.
	if err := runDDL(t, ctx, `CREATE COLLATION "My Coll" (LOCALE = 'C')`); err != nil {
		t.Fatalf("CREATE COLLATION \"My Coll\": %v", err)
	}
	mycollOID := im.UserCollationOIDByName("My Coll")
	if mycollOID == 0 {
		t.Fatal(`"My Coll" not found`)
	}
	if got := RegOut("regcollation", mycollOID, ctx.Catalog, true); got != `public."My Coll"` {
		t.Errorf("regcollation(\"My Coll\") qualify=true = %q, want %q", got, `public."My Coll"`)
	}
}

// M0119-0006 (70th slice, ledger row 1339): upstream regcollationout
// schema-qualifies a user collation with its ACTUAL namespace
// (regproc.c:1123 get_namespace_name(collnamespace)) when the search path does
// not show it. The 69th slice hardcoded "public" — right for a default-session
// creation schema, wrong for any non-public CREATE COLLATION schema. The
// qualifier is now the collation's own namespace, resolved from NamespaceOID.
func TestRegCollationQualifiesWithActualSchema(t *testing.T) {
	ctx := regCopyCat(t)
	im := ctx.Catalog.(*catalog.InMemory)
	// CREATE SCHEMA other_schema (RegisterSchema is what CREATE SCHEMA's DDL
	// operator ultimately calls), then a collation living there — not in public.
	im.RegisterSchema("other_schema")
	if err := runDDL(t, ctx, `CREATE COLLATION other_schema.othercoll (LOCALE = 'C')`); err != nil {
		t.Fatalf("CREATE COLLATION other_schema.othercoll: %v", err)
	}
	if im.FindCollation("othercoll", "other_schema") == nil {
		t.Fatal("other_schema.othercoll not found (schema fallback to public?)")
	}
	ocOID := im.UserCollationOIDByName("othercoll")
	if ocOID == 0 {
		t.Fatal("othercoll OID not found")
	}
	// qualify=true treats the collation's schema as off the effective
	// search_path → the qualifier is the ACTUAL namespace, not the hardcoded
	// "public" the 69th slice would have emitted.
	if got := RegOut("regcollation", ocOID, ctx.Catalog, true); got != "other_schema.othercoll" {
		t.Errorf("regcollation(other_schema.othercoll) qualify=true = %q, want %q", got, "other_schema.othercoll")
	}
	// A name that needs quoting is quoted under the non-public qualifier too
	// (quote_qualified_identifier quotes both; other_schema is unreserved).
	im.RegisterSchema("quote_schema")
	if err := runDDL(t, ctx, `CREATE COLLATION quote_schema."My Other Coll" (LOCALE = 'C')`); err != nil {
		t.Fatalf(`CREATE COLLATION quote_schema."My Other Coll": %v`, err)
	}
	qc := im.FindCollation("My Other Coll", "quote_schema")
	if qc == nil {
		t.Fatal(`quote_schema."My Other Coll" not found`)
	}
	if got := RegOut("regcollation", qc.OID, ctx.Catalog, true); got != `quote_schema."My Other Coll"` {
		t.Errorf("regcollation(quoted name, non-public schema) qualify=true = %q, want %q", got, `quote_schema."My Other Coll"`)
	}
	// qualify=false keeps the bare quote_identifier'd name (the object is
	// visible), matching the 69th-slice qualify=false behavior.
	if got := RegOut("regcollation", ocOID, ctx.Catalog, false); got != "othercoll" {
		t.Errorf("regcollation(other_schema.othercoll) qualify=false = %q, want %q", got, "othercoll")
	}
	// The 69th slice's measured common case is unchanged: a user collation in
	// public still qualifies with `public`.
	mycollOID := im.UserCollationOIDByName("mycoll")
	if mycollOID == 0 {
		t.Fatal("mycoll not found")
	}
	if got := RegOut("regcollation", mycollOID, ctx.Catalog, true); got != "public.mycoll" {
		t.Errorf("regcollation(mycoll) qualify=true = %q, want %q", got, "public.mycoll")
	}
}

// M0119-0006 (71st slice, deferral row 1338): format_procedure (regproc.c:326)
// schema-qualifies ONLY the routine NAME — quote_qualified_identifier
// (schema,name) when the routine is off the effective search_path, plain
// quote_identifier(name) when visible — and appends the UNQUOTED format_type_be
// arglist. The 69th slice left RegOut's regprocedure arm returning the bare
// signature (`my_udf()` via catalog.RegprocedureName); this closes the gap on
// the shared renderer. The parens must stay unquoted (pgQuoteIdent on
// "int4out(integer)" would wrongly quote them).
func TestRegOutRegprocedureQualifiesNameOnly(t *testing.T) {
	ctx := regCopyCat(t)
	im := ctx.Catalog.(*catalog.InMemory)
	userFunc, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:       "my_udf",
		Schema:     "public",
		ReturnType: catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create: %v", err)
	}
	// qualify=true (the routine's schema off the effective search_path) prefixes
	// the NAME with the schema; the empty arglist is appended unquoted.
	if got := RegOut("regprocedure", userFunc.OID, ctx.Catalog, true); got != "public.my_udf()" {
		t.Errorf("regprocedure(user routine) qualify=true = %q, want %q", got, "public.my_udf()")
	}
	// qualify=false (visible) keeps the bare name.
	if got := RegOut("regprocedure", userFunc.OID, ctx.Catalog, false); got != "my_udf()" {
		t.Errorf("regprocedure(user routine) qualify=false = %q, want %q", got, "my_udf()")
	}

	// With input args, the arglist is the format_type_be display list, still
	// unqualified and unquoted.
	userAdd, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:       "my_add",
		Schema:     "public",
		ArgTypes:   []catalog.Type{{Name: "int4"}, {Name: "int4"}},
		ArgModes:   []string{"i", "i"},
		ReturnType: catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create(my_add): %v", err)
	}
	if got := RegOut("regprocedure", userAdd.OID, ctx.Catalog, true); got != "public.my_add(integer,integer)" {
		t.Errorf("regprocedure(my_add) qualify=true = %q, want %q", got, "public.my_add(integer,integer)")
	}

	// A mixed-case routine name is quote_identifier'd in BOTH arms — the
	// on-path arm quotes it bare, the off-path arm under the schema.
	quoted, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:       "MyFunc",
		Schema:     "public",
		ArgTypes:   []catalog.Type{{Name: "int4"}},
		ReturnType: catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create(MyFunc): %v", err)
	}
	if got := RegOut("regprocedure", quoted.OID, ctx.Catalog, false); got != `"MyFunc"(integer)` {
		t.Errorf("regprocedure(MyFunc) qualify=false = %q, want %q", got, `"MyFunc"(integer)`)
	}
	if got := RegOut("regprocedure", quoted.OID, ctx.Catalog, true); got != `public."MyFunc"(integer)` {
		t.Errorf("regprocedure(MyFunc) qualify=true = %q, want %q", got, `public."MyFunc"(integer)`)
	}

	// A routine in a NON-public schema qualifies with that ACTUAL schema.
	im.RegisterSchema("other_schema")
	other, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:       "other_func",
		Schema:     "other_schema",
		ReturnType: catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create(other_func): %v", err)
	}
	if got := RegOut("regprocedure", other.OID, ctx.Catalog, true); got != "other_schema.other_func()" {
		t.Errorf("regprocedure(other_schema.other_func) qualify=true = %q, want %q", got, "other_schema.other_func()")
	}

	// A builtin resolves in pg_catalog, which every search_path searches
	// implicitly — never qualified, only the bare quoted name.
	if got := RegOut("regprocedure", 43, ctx.Catalog, true); got != "int4out(integer)" {
		t.Errorf("regprocedure(43) qualify=true = %q, want %q", got, "int4out(integer)")
	}
	if got := RegOut("regprocedure", 43, ctx.Catalog, false); got != "int4out(integer)" {
		t.Errorf("regprocedure(43) qualify=false = %q, want %q", got, "int4out(integer)")
	}
}

// M0119-0006 (73rd slice, deferral row 1342): format_procedure_extended passes
// each INPUT arg type through format_type_be (regproc.c:326), which
// schema-qualifies a type whose namespace is off the session's effective
// search_path (`offpath.mytype`), renders a builtin bare via the SQL alias
// (`integer`), and splits/re-appends the array suffix (`offpath.mytype[]`, never
// `offpath."mytype[]"`). regprocedureArglist implements that per-arg rule;
// RegOutArgVisible threads the session's per-schema visibility predicate (the
// base RegOut passes nil → all visible → bare arglist, unchanged for its ~30
// callers). Expected strings are pinned to the §1 oracle table of the 73rd-slice
// design doc.
func TestRegOutRegprocedureQualifiesArgTypes(t *testing.T) {
	ctx := regCopyCat(t)
	// visible is RegObjectSchemaVisible's per-schema rule with offpath NOT on the
	// effective search_path.
	offpathHidden := func(s string) bool { return s != "offpath" }

	// f_offarg(offpath.mytype): the arg's schema is off the path → qualified.
	offArg, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:           "f_offarg",
		Schema:         "public",
		ArgTypes:       []catalog.Type{{Name: "mytype"}},
		ArgModes:       []string{"i"},
		ArgTypeSchemas: []string{"offpath"},
		ReturnType:     catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create(f_offarg): %v", err)
	}
	if got := RegOutArgVisible("regprocedure", offArg.OID, ctx.Catalog, true, offpathHidden); got != "public.f_offarg(offpath.mytype)" {
		t.Errorf("regprocedure(f_offarg) offpath hidden = %q, want %q", got, "public.f_offarg(offpath.mytype)")
	}
	// offpath ON the path → bare, like a builtin (the §1 oracle's search_path row).
	onPath := func(s string) bool { return true }
	if got := RegOutArgVisible("regprocedure", offArg.OID, ctx.Catalog, true, onPath); got != "public.f_offarg(mytype)" {
		t.Errorf("regprocedure(f_offarg) offpath visible = %q, want %q", got, "public.f_offarg(mytype)")
	}

	// f_offarr(offpath.mytype[]): the array suffix is split before quoting and
	// re-appended after — the ELEMENT is quoted, `[]` stays unquoted (reviewer
	// BLOCKER 1).
	offArr, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:           "f_offarr",
		Schema:         "public",
		ArgTypes:       []catalog.Type{{Name: "mytype[]"}},
		ArgModes:       []string{"i"},
		ArgTypeSchemas: []string{"offpath"},
		ReturnType:     catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create(f_offarr): %v", err)
	}
	if got := RegOutArgVisible("regprocedure", offArr.OID, ctx.Catalog, true, offpathHidden); got != "public.f_offarr(offpath.mytype[])" {
		t.Errorf("regprocedure(f_offarr) offpath hidden = %q, want %q", got, "public.f_offarr(offpath.mytype[])")
	}

	// f_builtin(integer): a builtin (pg_catalog) arg renders the bare SQL alias,
	// never qualified, regardless of visibility.
	bi, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:           "f_builtin",
		Schema:         "public",
		ArgTypes:       []catalog.Type{{Name: "int4"}},
		ArgModes:       []string{"i"},
		ArgTypeSchemas: []string{"pg_catalog"},
		ReturnType:     catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create(f_builtin): %v", err)
	}
	if got := RegOutArgVisible("regprocedure", bi.OID, ctx.Catalog, true, offpathHidden); got != "public.f_builtin(integer)" {
		t.Errorf("regprocedure(f_builtin) = %q, want %q", got, "public.f_builtin(integer)")
	}
	// Builtin ARRAY arg: `int[]` → `integer[]` (alias applied to the element).
	biArr, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:           "f_builtin_arr",
		Schema:         "public",
		ArgTypes:       []catalog.Type{{Name: "int[]"}},
		ArgModes:       []string{"i"},
		ArgTypeSchemas: []string{"pg_catalog"},
		ReturnType:     catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create(f_builtin_arr): %v", err)
	}
	if got := RegOutArgVisible("regprocedure", biArr.OID, ctx.Catalog, true, offpathHidden); got != "public.f_builtin_arr(integer[])" {
		t.Errorf("regprocedure(f_builtin_arr) = %q, want %q", got, "public.f_builtin_arr(integer[])")
	}

	// A USER type in a non-pg_catalog schema does NOT get the builtin alias — a
	// user composite named `int` must render `offpath."int"`, never
	// `offpath.integer` (format_type_be's builtin switch applies only to builtin
	// OIDs). PG 18.3 measured: `f_kwint(offpath2."int")` — quote_identifier
	// quotes the keyword `int` inside the qualified name.
	userInt, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:           "f_userint",
		Schema:         "public",
		ArgTypes:       []catalog.Type{{Name: "int"}},
		ArgModes:       []string{"i"},
		ArgTypeSchemas: []string{"offpath"},
		ReturnType:     catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create(f_userint): %v", err)
	}
	if got := RegOutArgVisible("regprocedure", userInt.OID, ctx.Catalog, true, offpathHidden); got != `public.f_userint(offpath."int")` {
		t.Errorf("regprocedure(f_userint) = %q, want %q", got, `public.f_userint(offpath."int")`)
	}

	// Base RegOut (nil predicate) keeps the BARE arglist even for an off-path arg
	// schema — the ~30 existing callers' backward-compatible behavior.
	if got := RegOut("regprocedure", offArg.OID, ctx.Catalog, true); got != "public.f_offarg(mytype)" {
		t.Errorf("regprocedure(f_offarg) base RegOut = %q, want %q", got, "public.f_offarg(mytype)")
	}
}

// A regrole OID not present in the role map is a DANGLING reference: upstream
// regroleout emits the unquoted %u fallback (regproc.c:1609), never a quoted
// name — RoleNameAtOID distinguishes a real role (quoted name) from a dangling
// one (numeric), unlike RoleNameForOID which renders both numerically.
func TestRegOutDanglingRoleUnquotedNumeric(t *testing.T) {
	ctx := regCopyCat(t)
	if got := RegOut("regrole", 999999, ctx.Catalog, false); got != "999999" {
		t.Errorf("regrole(dangling) = %q, want %q", got, "999999")
	}
	if got := RegOut("regrole", 999999, ctx.Catalog, true); got != "999999" {
		t.Errorf("regrole(dangling) qualify=true = %q, want %q", got, "999999")
	}
}

// M0119-0006 (74th slice, deferral rows 1345/1346): ArgTypeDisplayAlias now
// carries format_type_be's varbit→bit varying case and char→"char" default-path
// keyword-quoting (format_type.c: the single-byte char is NOT in the special-case
// switch, so quote_identifier wraps it). The executor's regprocedureArglist
// already split a baked-in [] array suffix before aliasing, so a builtin ARRAY of
// these types must render the element alias re-appended (`"char"[]`,
// `bit varying[]`). Pinned here on the SELECT wire renderer (RegOutArgVisible).
func TestRegOutRegprocedureArgTypesVarbitChar(t *testing.T) {
	ctx := regCopyCat(t)
	allVisible := func(s string) bool { return true }
	cases := []struct {
		name string
		args []catalog.Type
		want string
	}{
		{"f_varbit", []catalog.Type{{Name: "varbit"}}, "public.f_varbit(bit varying)"},
		{"f_char", []catalog.Type{{Name: "char"}}, `public.f_char("char")`},
		{"f_chararr", []catalog.Type{{Name: "char[]"}}, `public.f_chararr("char"[])`},
		{"f_intarr", []catalog.Type{{Name: "int[]"}}, "public.f_intarr(integer[])"},
		{"f_varbitarr", []catalog.Type{{Name: "varbit[]"}}, "public.f_varbitarr(bit varying[])"},
	}
	for _, tc := range cases {
		r, err := ctx.Catalog.Routines().Create(&catalog.Routine{
			Name:           tc.name,
			Schema:         "public",
			ArgTypes:       tc.args,
			ArgModes:       []string{"i"},
			ArgTypeSchemas: []string{"pg_catalog"},
			ReturnType:     catalog.Type{Name: "int4"},
		}, false)
		if err != nil {
			t.Fatalf("Routines().Create(%s): %v", tc.name, err)
		}
		if got := RegOutArgVisible("regprocedure", r.OID, ctx.Catalog, true, allVisible); got != tc.want {
			t.Errorf("regprocedure(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// M0119-0006 (74th slice, rows 1345/1346): the catalog BARE builder
// (RegprocedureName → formatProcedureArglist) and the executor's pg-faithful
// renderer (regprocedureArglist) must emit byte-identical arglists for the same
// []RegprocArg — both split the array suffix and both alias through the shared
// ArgTypeDisplayAlias (Hard-won Rule #2). The name half legitimately differs
// (the bare builder never qualifies it), so this pins the parenthesized arglist
// from each side.
func TestRegprocedureArglistCatalogAndExecutorAgree(t *testing.T) {
	ctx := regCopyCat(t)
	rs := ctx.Catalog.Routines()
	f, err := rs.Create(&catalog.Routine{
		Name:           "f_multi",
		Schema:         "public",
		ArgTypes:       []catalog.Type{{Name: "int[]"}, {Name: "varbit"}, {Name: "char"}, {Name: "char[]"}, {Name: "float8[]"}, {Name: "text"}},
		ArgModes:       []string{"i", "i", "i", "i", "i", "i"},
		ArgTypeSchemas: []string{"pg_catalog", "pg_catalog", "pg_catalog", "pg_catalog", "pg_catalog", "pg_catalog"},
		ReturnType:     catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create(f_multi): %v", err)
	}
	wantArglist := `integer[],bit varying,"char","char"[],double precision[],text`

	catSig, ok := catalog.RegprocedureName(f.OID, rs)
	if !ok {
		t.Fatalf("RegprocedureName(%d) not ok", f.OID)
	}
	if want := "f_multi(" + wantArglist + ")"; catSig != want {
		t.Errorf("catalog bare builder = %q, want %q", catSig, want)
	}

	_, _, argParts, ok := catalog.RegprocedureNameParts(f.OID, rs)
	if !ok {
		t.Fatalf("RegprocedureNameParts(%d) not ok", f.OID)
	}
	if got := regprocedureArglist(argParts, func(s string) bool { return true }); got != wantArglist {
		t.Errorf("executor regprocedureArglist = %q, want %q", got, wantArglist)
	}

	// M0119-0006 (77th slice, deferral row 1351): the regprocedure arglist now
	// carries the resolved arg-type OID (Routine.ArgTypeOIDs → RegprocArg.OID)
	// to disambiguate the ONE ambiguous spelling `char`: a BARE char
	// (OIDBpChar 1042) renders `character` (format_type_be's BPCHAROID switch
	// case), a quoted `"char"` (OIDChar 18) and OID 0 (builtin / pre-change
	// routine) render `"char"`. The array suffix is re-appended AFTER the arm,
	// so `char[]` → `character[]` and `"char"[]` → `"char"[]`. Sibling pin:
	// the catalog bare builder and the executor renderer must agree.
	table := []struct {
		name        string
		argTypes    []catalog.Type
		argTypeOIDs []uint32
		wantArglist string
	}{
		{"f_bare_char", []catalog.Type{{Name: "char"}}, []uint32{catalog.OIDBpChar}, "character"},
		{"f_quoted_char", []catalog.Type{{Name: "char"}}, []uint32{catalog.OIDChar}, `"char"`},
		{"f_zeroid_char", []catalog.Type{{Name: "char"}}, nil, `"char"`},
		{"f_bare_char_arr", []catalog.Type{{Name: "char[]"}}, []uint32{catalog.OIDBpChar}, "character[]"},
		{"f_quoted_char_arr", []catalog.Type{{Name: "char[]"}}, []uint32{catalog.OIDChar}, `"char"[]`},
		// Row 1364: a real CREATE FUNCTION now captures the ARRAY OIDs for the
		// char arrays (char[] → 1014, "char"[] → 1002); the renderers must treat
		// them exactly like the scalar OIDs (1042/18) so the sibling builders
		// stay in agreement on the live capture path too.
		{"f_bare_char_arr_arrOID", []catalog.Type{{Name: "char[]"}}, []uint32{catalog.OIDArrayBpChar}, "character[]"},
		{"f_quoted_char_arr_arrOID", []catalog.Type{{Name: "char[]"}}, []uint32{catalog.OIDArrayChar}, `"char"[]`},
	}
	for _, tc := range table {
		r, err := rs.Create(&catalog.Routine{
			Name:           tc.name,
			Schema:         "public",
			ArgTypes:       tc.argTypes,
			ArgModes:       []string{"i"},
			ArgTypeSchemas: []string{"pg_catalog"},
			ArgTypeOIDs:    tc.argTypeOIDs,
			ReturnType:     catalog.Type{Name: "int4"},
		}, false)
		if err != nil {
			t.Fatalf("Routines().Create(%s): %v", tc.name, err)
		}
		catSig, ok := catalog.RegprocedureName(r.OID, rs)
		if !ok {
			t.Fatalf("RegprocedureName(%s) not ok", tc.name)
		}
		if want := tc.name + "(" + tc.wantArglist + ")"; catSig != want {
			t.Errorf("catalog bare builder (%s) = %q, want %q", tc.name, catSig, want)
		}
		_, _, tcParts, ok := catalog.RegprocedureNameParts(r.OID, rs)
		if !ok {
			t.Fatalf("RegprocedureNameParts(%s) not ok", tc.name)
		}
		if got := regprocedureArglist(tcParts, func(s string) bool { return true }); got != tc.wantArglist {
			t.Errorf("executor regprocedureArglist (%s) = %q, want %q", tc.name, got, tc.wantArglist)
		}
	}
}

func TestRegprocedureArglistQuotesMixedCaseUserType(t *testing.T) {
	ctx := regCopyCat(t)
	allVisible := func(s string) bool { return true }
	offpathHidden := func(s string) bool { return s != "offpath" }

	cases := []struct {
		name       string
		argTypes   []catalog.Type
		argSchemas []string
		visible    func(string) bool
		want       string
	}{
		// Off-path mixed-case user type: both parts quote (quoteQualifiedIdentifier).
		{"f_offpath_mixed", []catalog.Type{{Name: "MyType"}}, []string{"offpath"}, offpathHidden, `public.f_offpath_mixed(offpath."MyType")`},
		// On-path (visible) mixed-case user type: bare name quotes.
		{"f_visible_mixed", []catalog.Type{{Name: "MyType"}}, []string{"offpath"}, allVisible, `public.f_visible_mixed("MyType")`},
		// Lowercase user type stays bare even on-path (quote_identifier no-op).
		{"f_visible_lower", []catalog.Type{{Name: "mytype"}}, []string{"offpath"}, allVisible, "public.f_visible_lower(mytype)"},
		// Builtin unaffected (pg_catalog arm maps to the SQL alias).
		{"f_builtin", []catalog.Type{{Name: "int4"}}, []string{"pg_catalog"}, allVisible, "public.f_builtin(integer)"},
		// Mixed-case user type in a builtin-schema arm is untouched by the quote.
		{"f_offpath_mixed_arr", []catalog.Type{{Name: "MyType[]"}}, []string{"offpath"}, allVisible, `public.f_offpath_mixed_arr("MyType"[])`},
	}
	for _, tc := range cases {
		r, err := ctx.Catalog.Routines().Create(&catalog.Routine{
			Name:           tc.name,
			Schema:         "public",
			ArgTypes:       tc.argTypes,
			ArgModes:       []string{"i"},
			ArgTypeSchemas: tc.argSchemas,
			ReturnType:     catalog.Type{Name: "int4"},
		}, false)
		if err != nil {
			t.Fatalf("Routines().Create(%s): %v", tc.name, err)
		}
		if got := RegOutArgVisible("regprocedure", r.OID, ctx.Catalog, true, tc.visible); got != tc.want {
			t.Errorf("regprocedure(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// M0119-0006 (deferral row L1305): a synthetic TOAST relation OID (parent OID +
// 100M) or TOAST index OID (+200M) — which live only in the virtual pg_class
// builder, never c.tables/c.indexes — must render the schema-qualified
// pg_toast.pg_toast_<parentOID>[_index] name PG's regclassout emits, matching
// the `oid::regclass` CastExpr arm (expr.go:826-828). ToastRelName returns the
// name verbatim (the pg_toast namespace is never on a search_path, so the
// qualify flag is irrelevant — regclassout always schema-qualifies it).
func TestRegOutToastRelnameRendersSchemaQualified(t *testing.T) {
	ctx := regCopyCat(t)
	// No PRIMARY KEY here: regCopyCat's context carries no storage Pool, so an
	// index build would nil-deref; ToastRelName only needs the toastable column.
	if err := runDDL(t, ctx, `CREATE TABLE wide_toast (id int, data text)`); err != nil {
		t.Fatalf("CREATE TABLE wide_toast: %v", err)
	}
	wide, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "wide_toast"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		t.Fatal("wide_toast not found")
	}
	const (
		relOffset = 100_000_000
		idxOffset = 200_000_000
	)
	wantRel := "pg_toast.pg_toast_" + strconv.Itoa(int(wide.OID))
	wantIdx := wantRel + "_index"
	if got := RegOut("regclass", wide.OID+relOffset, ctx.Catalog, false); got != wantRel {
		t.Errorf("regclass(toast relation OID) qualify=false = %q, want %q", got, wantRel)
	}
	// qualify=true (pg_dump-style empty search_path) changes nothing: pg_toast
	// is off every search_path, so the name is always schema-qualified.
	if got := RegOut("regclass", wide.OID+relOffset, ctx.Catalog, true); got != wantRel {
		t.Errorf("regclass(toast relation OID) qualify=true = %q, want %q", got, wantRel)
	}
	if got := RegOut("regclass", wide.OID+idxOffset, ctx.Catalog, false); got != wantIdx {
		t.Errorf("regclass(toast index OID) = %q, want %q", got, wantIdx)
	}
}

// M0119-0006 (deferral row 1347): searchPathSchemas used LookupTable as a
// schema-existence proxy, so a REGISTERED-BUT-EMPTY user schema never appeared
// on the effective search_path — SET search_path = public, offpath with offpath
// empty rendered f_offarg(offpath.mytype) instead of PG's bare f_offarg(mytype).
// The proxy is now the schema registry (SchemaExists, which sees empty schemas
// registered at CREATE SCHEMA), mirroring fetch_search_path's inclusion of empty
// namespaces (postgres/src/backend/catalog/namespace.c:4822-4849) with name→OID
// resolution via get_namespace_oid (namespace.c:3537-3550). This test registers
// an EMPTY offpath (no tables/objects) and asserts RegObjectSchemaVisible sees it
// and the regprocedure arglist renders the offpath arg BARE.
func TestRegObjectSchemaVisibleSeesEmptySchema(t *testing.T) {
	ctx := regCopyCat(t)
	im := ctx.Catalog.(*catalog.InMemory)
	// An EMPTY schema: registered (as CREATE SCHEMA does), but holding no
	// tables/objects — the case the old LookupTable existence proxy could never
	// see (a table literally named "offpath" would have to exist for it to
	// succeed).
	im.RegisterSchema("offpath")

	// The session's effective search_path puts the empty offpath on the path.
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "search_path" {
			return "public, offpath", true
		}
		return "", false
	}

	// The empty schema is now visible on the path even though it holds no tables.
	if got := RegObjectSchemaVisible(ctx, "offpath"); !got {
		t.Errorf("RegObjectSchemaVisible(empty offpath on path) = false, want true")
	}

	// A routine in public whose arg type is the offpath composite mytype: with
	// offpath visible, format_type_be renders the arg BARE (f_offarg(mytype)),
	// not qualified (f_offarg(offpath.mytype)) — the 73rd-slice design doc's §1
	// oracle row `SET search_path = public, offpath; f_offarg → f_offarg(mytype)`.
	offArg, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:           "f_offarg",
		Schema:         "public",
		ArgTypes:       []catalog.Type{{Name: "mytype"}},
		ArgModes:       []string{"i"},
		ArgTypeSchemas: []string{"offpath"},
		ReturnType:     catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create(f_offarg): %v", err)
	}
	visible := func(s string) bool { return RegObjectSchemaVisible(ctx, s) }
	if got := RegOutArgVisible("regprocedure", offArg.OID, ctx.Catalog, true, visible); got != "public.f_offarg(mytype)" {
		t.Errorf("regprocedure(f_offarg) empty offpath on path = %q, want %q", got, "public.f_offarg(mytype)")
	}
}
