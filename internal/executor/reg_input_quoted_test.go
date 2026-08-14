package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0119-0006 (72nd slice, deferral ledger row 1341): the reg* INPUT half of
// the name↔OID contract now honors double-quoted identifiers. Upstream feeds
// every reg*in string through stringToQualifiedNameList → SplitIdentifierString
// (varlena.c:3581): a `"…"` segment keeps its exact case with `""` collapsing
// to a literal `"`, an unquoted segment is downcased, whitespace around
// segments is skipped, and `.` inside quotes is not a separator. Previously
// goopg ran the whole candidate through strings.ToLower + a dumb first-dot
// split, so `'"MyFunc"'::regproc` reached LookupByName with the literal quotes
// → 42883, while PG 18.3 resolves the quoted name. The shared parser
// (splitRegQualifiedName in reg_identifier.go) is used by every reg* name→OID
// arm of regIdentifierInput AND the ::reg* cast siblings in expr.go — the
// input counterpart of RegOut's quote-emission (69th/70th/71st slices);
// sibling renderers must agree (Hard-won Rule #2).
//
// The catalog lookups fold case, so the resolution half is untouched —
// quote-stripping alone makes quoted names resolve. The unquoted leniency
// (`'myfunc'` matching a `MyFunc` routine where PG raises 42883) is the
// pre-existing case-insensitive name model, deliberately out of scope (see the
// design doc).

// TestRegProcInputResolvesQuotedIdentifier is the exact failing shape from row
// 1341: a quoted mixed-case routine name resolves through BOTH the `::regproc`
// cast and the `::regprocedure` cast (which strips the arg list first). Pre-fix
// each was 42883.
func TestRegProcInputResolvesQuotedIdentifier(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION "MyFunc"(a int4) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create quoted-name function: %v", err)
	}
	cands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: "MyFunc"})
	if len(cands) == 0 {
		t.Fatal(`"MyFunc" routine not found`)
	}
	udfOID := cands[0].OID

	if got := runQuery(t, ctx, `SELECT '"MyFunc"'::regproc`)[0][0].Int; got != int64(udfOID) {
		t.Errorf(`'"MyFunc"'::regproc = %d, want %d`, got, udfOID)
	}
	// regprocedurein (regproc.c:244) splits the arg list off the name
	// (parseNameAndArgTypes); goopg resolves the NAME only (the arg list is a
	// recorded gap), so `(integer)` must not break the quoted-name resolution.
	if got := runQuery(t, ctx, `SELECT '"MyFunc"(integer)'::regprocedure`)[0][0].Int; got != int64(udfOID) {
		t.Errorf(`'"MyFunc"(integer)'::regprocedure = %d, want %d`, got, udfOID)
	}
	// A quoted BUILTIN resolves through the builtin proc index too.
	unquoted := runQuery(t, ctx, `SELECT 'int4eq'::regproc`)[0][0].Int
	if got := runQuery(t, ctx, `SELECT '"int4eq"'::regproc`)[0][0].Int; got != unquoted {
		t.Errorf(`'"int4eq"'::regproc = %d, want %d`, got, unquoted)
	}
}

// TestRegInputQuotedSchemaAndCollapsedQuotes pins the parser's two trickiest
// rules against real catalog objects: a dot inside a QUOTED schema is part of
// the identifier (not a separator), a quoted NAME under an unquoted schema
// resolves, and an adjacent `""` inside a quoted name collapses to a literal
// quote (upstream's memmove loop in SplitIdentifierString).
func TestRegInputQuotedSchemaAndCollapsedQuotes(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// A schema whose name contains a dot.
	if err := runDDL(t, ctx, `CREATE SCHEMA "my.schema"`); err != nil {
		t.Fatalf("create dotted schema: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION "my.schema"."dot fn"(a int4) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create dotted-schema function: %v", err)
	}
	cands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Schema: "my.schema", Name: "dot fn"})
	if len(cands) == 0 {
		t.Fatal(`"my.schema"."dot fn" routine not found`)
	}
	if got := runQuery(t, ctx, `SELECT '"my.schema"."dot fn"'::regproc`)[0][0].Int; got != int64(cands[0].OID) {
		t.Errorf(`'"my.schema"."dot fn"'::regproc = %d, want %d`, got, cands[0].OID)
	}

	// A quoted NAME under an unquoted schema.
	if err := runDDL(t, ctx, `CREATE SCHEMA ragout`); err != nil {
		t.Fatalf("create ragout schema: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION ragout."Quoted Other"(a int4) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create ragout quoted function: %v", err)
	}
	rc := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Schema: "ragout", Name: "Quoted Other"})
	if len(rc) == 0 {
		t.Fatal(`ragout."Quoted Other" routine not found`)
	}
	if got := runQuery(t, ctx, `SELECT 'ragout."Quoted Other"'::regproc`)[0][0].Int; got != int64(rc[0].OID) {
		t.Errorf(`'ragout."Quoted Other"'::regproc = %d, want %d`, got, rc[0].OID)
	}
	// Whitespace around the separator resolves too (PG 18.3 measured 16455 for
	// `' ragout71 . other_func '`).
	if got := runQuery(t, ctx, `SELECT ' ragout . "Quoted Other" '::regproc`)[0][0].Int; got != int64(rc[0].OID) {
		t.Errorf(`' ragout . "Quoted Other" '::regproc = %d, want %d`, got, rc[0].OID)
	}

	// Quote-quote collapse: the identifier literally contains a quote. Created
	// via the live registry (DDL CREATE FUNCTION with an embedded quote is out
	// of the test's scope); `'"Weird""Quote"'` must collapse `""` → `"` and
	// resolve.
	if _, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:       `Weird"Quote`,
		Schema:     "public",
		ReturnType: catalog.Type{Name: "int4"},
	}, false); err != nil {
		t.Fatalf("Routines().Create(Weird\"Quote): %v", err)
	}
	wc := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: `Weird"Quote`})
	if len(wc) == 0 {
		t.Fatal(`Weird"Quote routine not found`)
	}
	if got := runQuery(t, ctx, `SELECT '"Weird""Quote"'::regproc`)[0][0].Int; got != int64(wc[0].OID) {
		t.Errorf(`'"Weird""Quote"'::regproc = %d, want %d`, got, wc[0].OID)
	}
}

// TestRegInputQuotedFamilySiblings pins the same quote-stripping on the reg*
// family's OTHER name→OID inputs: regclass (a quoted table name), regcollation
// (a quoted user collation), regrole (a quoted role name — a dot inside the
// quotes is NOT a qualification, unlike the old strings.Contains check), and
// regtype (a quoted builtin).
func TestRegInputQuotedFamilySiblings(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	im := ctx.Catalog.(*catalog.InMemory)

	// regclass: quoted table name.
	if err := runDDL(t, ctx, `CREATE TABLE "My Table" (id int)`); err != nil {
		t.Fatalf("create quoted table: %v", err)
	}
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "My Table"}, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		t.Fatal(`"My Table" not found`)
	}
	if got := runQuery(t, ctx, `SELECT '"My Table"'::regclass`)[0][0].Int; got != int64(tbl.OID) {
		t.Errorf(`'"My Table"'::regclass = %d, want %d`, got, tbl.OID)
	}

	// regcollation: quoted USER collation (the store is case-sensitive, so a
	// quoted mixed-case collation must keep its case to resolve). The
	// name→OID half here is regIdentifierInput (the coercion path): the
	// ::regcollation CAST is a pre-existing no-op (no expr.go arm exists — see
	// the design doc's scope exclusions), so the quote-stripping is asserted
	// through the input function itself.
	if err := runDDL(t, ctx, `CREATE COLLATION "My Coll" (LOCALE = 'C')`); err != nil {
		t.Fatalf("create quoted collation: %v", err)
	}
	collOID := im.UserCollationOIDByName("My Coll")
	if collOID == 0 {
		t.Fatal(`"My Coll" not found`)
	}
	if got, err := regIdentifierInput(NewStringDatum(`"My Coll"`), "regcollation", ctx, 0); err != nil {
		t.Fatalf(`regIdentifierInput("My Coll", regcollation): %v`, err)
	} else if got.Kind != KindInt || got.Int != int64(collOID) {
		t.Errorf(`regIdentifierInput("My Coll", regcollation) = kind %d %v, want int %d`, got.Kind, got.Int, collOID)
	}

	// regrole: quoted role name with a dot INSIDE the quotes — a single
	// identifier (regrolein list_length == 1), resolving through the
	// case-insensitive role store; the old `strings.Contains(name, ".")` would
	// have false-flagged it as qualified → 42602. Same coercion-path note as
	// regcollation.
	im.RegisterRoleWithOID("alice.bob", 7777)
	if got, err := regIdentifierInput(NewStringDatum(`"Alice.Bob"`), "regrole", ctx, 0); err != nil {
		t.Fatalf(`regIdentifierInput("Alice.Bob", regrole): %v`, err)
	} else if got.Kind != KindInt || got.Int != 7777 {
		t.Errorf(`regIdentifierInput("Alice.Bob", regrole) = kind %d %v, want int 7777`, got.Kind, got.Int)
	}

	// regtype: quoted builtin keeps its OID (TypeNameToOID folds case, so the
	// quoted spelling resolves exactly like the bare one).
	unquoted := runQuery(t, ctx, `SELECT 'int4'::regtype`)[0][0].Int
	if got := runQuery(t, ctx, `SELECT '"int4"'::regtype`)[0][0].Int; got != unquoted {
		t.Errorf(`'"int4"'::regtype = %d, want %d`, got, unquoted)
	}
}

// TestRegInputSyntaxErrors: a syntax error in the name — a mismatched quote or
// an empty segment — raises 42602 "invalid name syntax" (stringToQualifiedNameList's
// ereturn) on BOTH the ::reg* cast path and the regIdentifierInput coercion
// path, never a silent fall-through or a bogus 42883.
func TestRegInputSyntaxErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, tc := range []struct {
		sql  string
		desc string
	}{
		{`SELECT '"MyFunc'::regproc`, "mismatched quote"},
		{`SELECT 'a..b'::regproc`, "empty unquoted segment"},
		{`SELECT '.b'::regproc`, "leading empty segment"},
		{`SELECT 'a.'::regproc`, "trailing empty segment"},
		{`SELECT 'a b'::regproc`, "unquoted whitespace gap"},
	} {
		if _, err := runQueryErr(t, ctx, tc.sql); err == nil {
			t.Errorf("%s: %s should raise 42602", tc.desc, tc.sql)
		} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42602" {
			t.Errorf("%s: %s err = %v, want 42602", tc.desc, tc.sql, err)
		}
	}

	// The same rejections on the regIdentifierInput coercion path.
	for _, tc := range []struct {
		input string
		typ   string
		desc  string
	}{
		{`"MyFunc`, "regproc", "mismatched quote"},
		{"a..b", "regclass", "empty unquoted segment"},
		{"a b", "regproc", "unquoted whitespace gap"},
	} {
		if _, err := regIdentifierInput(NewStringDatum(tc.input), tc.typ, ctx, 0); err == nil {
			t.Errorf("%s: regIdentifierInput(%s, %s) should raise 42602", tc.desc, tc.input, tc.typ)
		} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42602" {
			t.Errorf("%s: regIdentifierInput(%s, %s) err = %v, want 42602", tc.desc, tc.input, tc.typ, err)
		}
	}

	// The regprocedure arm strips `(…)` FIRST, so a mismatched quote inside the
	// NAME part is still 42602 — the strip must not hide it.
	if _, err := regIdentifierInput(NewStringDatum(`"MyFunc(integer)`), "regprocedure", ctx, 0); err == nil {
		t.Error("regIdentifierInput(\"MyFunc(integer), regprocedure) should raise 42602")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42602" {
		t.Errorf("regIdentifierInput(\"MyFunc(integer), regprocedure) err = %v, want 42602", err)
	}
}

// TestRegIdentifierInputQuotedCoercion: the INSERT/DEFAULT choke point
// (coerceRowForConstraintChecks) resolves a quoted regproc name through the
// shared parser, so the heap stores the 4-byte OID — not the name as text.
func TestRegIdentifierInputQuotedCoercion(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE FUNCTION "MyFunc"(a int4) RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create quoted-name function: %v", err)
	}
	cands := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: "MyFunc"})
	if len(cands) == 0 {
		t.Fatal(`"MyFunc" routine not found`)
	}

	cols := []catalog.Column{{Name: "r", Type: catalog.Type{Name: "regproc"}}}
	row := Row{NewStringDatum(`"MyFunc"`)}
	if err := coerceRowForConstraintChecks(cols, row, func(int) bool { return true }, ctx, 0); err != nil {
		t.Fatalf("coerceRowForConstraintChecks(quoted regproc): %v", err)
	}
	if row[0].Kind != KindInt || row[0].Int != int64(cands[0].OID) {
		t.Fatalf("regproc column coerced to kind %d %v, want int OID %d", row[0].Kind, row[0].Int, cands[0].OID)
	}
}

// TestRegInputQuotedMissMessages: a quoted-name MISS renders with the quotes
// STRIPPED (NameListToString) for regclass/regrole/regcollation/regtype, and
// with the RAW input for regproc/regprocedure — exactly the shapes measured on
// PG 18.3.
func TestRegInputQuotedMissMessages(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// regclass: relation "No Such Table" (stripped), not the raw quotes.
	if _, err := regIdentifierInput(NewStringDatum(`"No Such Table"`), "regclass", ctx, 0); err == nil {
		t.Fatal(`regIdentifierInput("No Such Table", regclass) should raise 42P01`)
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42P01" {
		t.Fatalf("regclass quoted miss err = %v, want 42P01", err)
	} else if !strings.Contains(ee.Message, `relation "No Such Table" does not exist`) {
		t.Errorf("regclass quoted miss message = %q, want stripped form", ee.Message)
	}

	// regrole: role "No Such Role" (stripped single segment).
	if _, err := regIdentifierInput(NewStringDatum(`"No Such Role"`), "regrole", ctx, 0); err == nil {
		t.Fatal(`regIdentifierInput("No Such Role", regrole) should raise 42704`)
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Fatalf("regrole quoted miss err = %v, want 42704", err)
	} else if !strings.Contains(ee.Message, `role "No Such Role" does not exist`) {
		t.Errorf("regrole quoted miss message = %q, want stripped form", ee.Message)
	}

	// regproc: function "\"No Such Func\"" (RAW input, quotes included).
	if _, err := regIdentifierInput(NewStringDatum(`"No Such Func"`), "regproc", ctx, 0); err == nil {
		t.Fatal(`regIdentifierInput("No Such Func", regproc) should raise 42883`)
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42883" {
		t.Fatalf("regproc quoted miss err = %v, want 42883", err)
	} else if !strings.Contains(ee.Message, `function "\"No Such Func\"" does not exist`) {
		t.Errorf("regproc quoted miss message = %q, want RAW input", ee.Message)
	}

	// regcollation: collation "No Such Coll" for encoding "UTF8" (stripped).
	if _, err := regIdentifierInput(NewStringDatum(`"No Such Coll"`), "regcollation", ctx, 0); err == nil {
		t.Fatal(`regIdentifierInput("No Such Coll", regcollation) should raise 42704`)
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Fatalf("regcollation quoted miss err = %v, want 42704", err)
	} else if !strings.Contains(ee.Message, `collation "No Such Coll" for encoding "UTF8" does not exist`) {
		t.Errorf("regcollation quoted miss message = %q, want stripped form", ee.Message)
	}

	// regtype: type "No Such Type" (stripped).
	if _, err := regIdentifierInput(NewStringDatum(`"No Such Type"`), "regtype", ctx, 0); err == nil {
		t.Fatal(`regIdentifierInput("No Such Type", regtype) should raise 42704`)
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Fatalf("regtype quoted miss err = %v, want 42704", err)
	} else if !strings.Contains(ee.Message, `type "No Such Type" does not exist`) {
		t.Errorf("regtype quoted miss message = %q, want stripped form", ee.Message)
	}
}
