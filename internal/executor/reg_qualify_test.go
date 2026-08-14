package executor

import (
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
