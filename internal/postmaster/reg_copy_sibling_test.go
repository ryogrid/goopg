package postmaster

import (
	"io"
	"log/slog"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// createSiblingTable creates a user table in the catalog through the same
// planner→Build pipeline the executor package's runDDL test helper uses, so a
// sibling renderer test can exercise a real user object without a server.
func createSiblingTable(t *testing.T, cat *catalog.InMemory, sql string) error {
	t.Helper()
	ctx := executor.NewContext()
	ctx.Catalog = cat
	stmts, err := parser.Parse(sql)
	if err != nil {
		return err
	}
	plan, err := optimizer.Plan(stmts[0], cat)
	if err != nil {
		return err
	}
	op, err := executor.Build(plan)
	if err != nil {
		return err
	}
	if err := op.Open(ctx); err != nil {
		return err
	}
	if _, err := op.Next(); err != executor.EOF {
		return err
	}
	return op.Close()
}

// M0119-0006 (68th slice): the TEXT/CSV COPY renderer and the SELECT wire
// renderer must agree on a reg* column (Hard-won Rule #2). Both now dispatch
// through executor.RegOut — COPY via datumToCopyText, SELECT via
// appendTypedCellText's reg* case — and this test pins the sibling agreement
// structurally: for a battery of (type, OID) pairs the two produce
// byte-identical text. getSetting=nil makes appendTypedCellText's qualify
// default to !visible("$user", public) = false, the same value the COPY path
// computes for a default search_path, so the comparison is apples-to-apples.
//
// OID 0 is the behavioral fix worth pinning: pre-68th, the SELECT regclass case
// resolved OID 0 against an information_schema virtual table and rendered a
// nondeterministic name, while regprocout/regroleout/regcollationout already
// rendered "-"; RegOut gives every family the regproc.c "-" for InvalidOid.
func TestRegCopyAndSelectSiblingRenderersAgree(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.RegisterRoleWithOID("alice", 7777)

	// A toast-bearing table so the harness can pin synthetic TOAST relation/index
	// OIDs (parent + 100M / +200M), which live only in the virtual pg_class
	// builder and resolve through InMemory.ToastRelName — not c.tables/indexes.
	// M0119-0006 (deferral row L1305).
	if err := createSiblingTable(t, cat, `CREATE TABLE wide_toast (id int, data text)`); err != nil {
		t.Fatalf("CREATE TABLE wide_toast: %v", err)
	}
	wideTbl, ok := cat.LookupTable(parser.ObjectName{Name: "wide_toast"})
	if !ok {
		t.Fatal("wide_toast not found")
	}

	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: cat,
	})

	tests := []struct {
		typ      string
		oid      int64
		wantPin  string // "" means no pin; the agreement itself is the assertion
	}{
		{"regclass", 1259, "pg_class"},
		{"regclass", 0, "-"},
		{"regproc", 42, "int4in"},
		{"regproc", 43, "int4out"},
		{"regproc", 0, "-"},
		{"regprocedure", 43, "int4out(integer)"},
		{"regtype", 23, "integer"},
		{"regtype", 25, "text"},
		{"regrole", 10, "postgres"},
		{"regrole", 7777, "alice"},
		{"regrole", 0, "-"},
		// regcollationout quote_identifiers every name, so "C" and "default"
		// (uppercase / reserved keyword) render with the quotes PG adds.
		{"regcollation", 950, `"C"`},
		{"regcollation", 100, `"default"`},
		{"regcollation", 0, "-"},
		// A synthetic TOAST relation/index OID (parent + 100M / +200M) renders
		// the schema-qualified pg_toast.pg_toast_<parentOID>[_index] name in
		// BOTH renderers via InMemory.ToastRelName (M0119-0006, deferral row
		// L1305) — identical to the `oid::regclass` CastExpr arm's output. The
		// pg_toast namespace is never on a search_path, so qualify=false still
		// yields the qualified name.
		{"regclass", int64(wideTbl.OID) + 100_000_000,
			"pg_toast.pg_toast_" + strconv.Itoa(int(wideTbl.OID))},
		{"regclass", int64(wideTbl.OID) + 200_000_000,
			"pg_toast.pg_toast_" + strconv.Itoa(int(wideTbl.OID)) + "_index"},
		// An OID unresolvable by either source falls back to the raw numeric
		// text in both renderers.
		{"regtype", 999999999, "999999999"},
	}
	for _, tc := range tests {
		typ := catalog.Type{Name: tc.typ}
		sel := string(srv.appendTypedCellText(nil, executor.NewIntDatum(tc.oid), typ, nil))
		copyText := executor.RegOut(tc.typ, uint32(tc.oid), cat, false)
		if sel != copyText {
			t.Errorf("%s(oid %d): SELECT=%q COPY=%q — renderers diverged",
				tc.typ, tc.oid, sel, copyText)
			continue
		}
		if tc.wantPin != "" && sel != tc.wantPin {
			t.Errorf("%s(oid %d) = %q, want %q", tc.typ, tc.oid, sel, tc.wantPin)
		}
	}
}

// The qualify=true path (relation not visible in the search_path) must also
// agree between the two renderers. appendTypedCellText derives qualify from the
// search_path GUC, so a search_path that excludes public makes it render
// schema-qualified names; RegOut takes the same flag as an explicit parameter.
// The pre-69th version of this test only exercised 1259 (pg_catalog), which
// NEVER qualifies — so a disagreement between the SELECT path's
// publicSchemaVisible and the COPY path's regObjectSchemaVisible would have
// gone undetected. A user table in public (created through the same
// planner→Build pipeline the executor's own tests use) makes the qualify flag
// observable: both renderers must emit `public.qmt`, and both must agree.
func TestRegCopyAndSelectSiblingQualifyAgree(t *testing.T) {
	cat := catalog.NewInMemory()

	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: cat,
	})

	// A search_path that names no public schema (e.g. pg_dump's search_path='')
	// must make both renderers qualify. The empty effective path makes
	// publicSchemaVisible and searchPathSchemas agree (neither sees public).
	noPublic := func(name string) (string, bool) { return "", true }

	// A user table in public — the one object that qualifies when public is off
	// the path.
	if err := createSiblingTable(t, cat, `CREATE TABLE qmt (id int)`); err != nil {
		t.Fatalf("CREATE TABLE qmt: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "qmt"},
		catalog.NamespaceDBOid(0))
	if !ok {
		t.Fatal("qmt not found")
	}

	typ := catalog.Type{Name: "regclass"}
	sel := string(srv.appendTypedCellText(nil, executor.NewIntDatum(int64(tbl.OID)), typ, noPublic))
	copyText := executor.RegOut("regclass", tbl.OID, cat, true)
	if sel != copyText {
		t.Errorf("regclass(qmt) qualify=true: SELECT=%q COPY=%q — renderers diverged", sel, copyText)
	}
	// And the value is the schema-qualified, quoted name PG emits (regproc.c's
	// quote_qualified_identifier through the 69th slice).
	if sel != "public.qmt" {
		t.Errorf("regclass(qmt) qualify=true = %q, want %q", sel, "public.qmt")
	}

	// A user collation in a NON-public schema must qualify with that schema,
	// and both renderers must agree (70th slice, deferral row 1339 — the 69th
	// slice hardcoded "public", which is wrong for any non-public CREATE
	// COLLATION schema).
	cat.RegisterSchema("other_schema")
	if err := createSiblingTable(t, cat, `CREATE COLLATION other_schema.oc (LOCALE = 'C')`); err != nil {
		t.Fatalf("CREATE COLLATION other_schema.oc: %v", err)
	}
	oc := cat.FindCollation("oc", "other_schema")
	if oc == nil {
		t.Fatal("other_schema.oc not found")
	}
	colTyp := catalog.Type{Name: "regcollation"}
	sel = string(srv.appendTypedCellText(nil, executor.NewIntDatum(int64(oc.OID)), colTyp, noPublic))
	copyText = executor.RegOut("regcollation", oc.OID, cat, true)
	if sel != copyText {
		t.Errorf("regcollation(other_schema.oc) qualify=true: SELECT=%q COPY=%q — renderers diverged", sel, copyText)
	}
	if sel != "other_schema.oc" {
		t.Errorf("regcollation(other_schema.oc) qualify=true = %q, want %q", sel, "other_schema.oc")
	}

	// A user routine in public, off a search_path that excludes it, must
	// qualify its NAME (format_procedure qualifies only the name; the arglist
	// stays unquoted) in both renderers (71st slice, deferral row 1338).
	routine, err := cat.Routines().Create(&catalog.Routine{
		Name:       "regproc_sibling_udf",
		Schema:     "public",
		ReturnType: catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create: %v", err)
	}
	procTyp := catalog.Type{Name: "regprocedure"}
	sel = string(srv.appendTypedCellText(nil, executor.NewIntDatum(int64(routine.OID)), procTyp, noPublic))
	copyText = executor.RegOut("regprocedure", routine.OID, cat, true)
	if sel != copyText {
		t.Errorf("regprocedure(routine) qualify=true: SELECT=%q COPY=%q — renderers diverged", sel, copyText)
	}
	if sel != "public.regproc_sibling_udf()" {
		t.Errorf("regprocedure(routine) qualify=true = %q, want %q", sel, "public.regproc_sibling_udf()")
	}

	// pg_catalog objects are implicitly visible and never qualify even when the
	// search path is empty — both renderers agree on the bare name.
	sel = string(srv.appendTypedCellText(nil, executor.NewIntDatum(1259), typ, noPublic))
	copyText = executor.RegOut("regclass", 1259, cat, true)
	if sel != copyText {
		t.Errorf("regclass(1259) qualify=true: SELECT=%q COPY=%q — renderers diverged", sel, copyText)
	}
	if sel != "pg_class" {
		t.Errorf("regclass(1259) qualify=true = %q, want %q", sel, "pg_class")
	}
}

// M0119-0006 (73rd slice, deferral row 1342): a raw regprocedure-typed column
// whose OID is a routine with an OFF-PATH arg type renders the arglist
// schema-qualified on BOTH wire paths — the SELECT simple-query renderer
// (appendTypedCellText with the per-arg RegObjectSchemaVisible predicate) and
// the COPY TO renderer (RegOutArgVisible through datumToCopyText). The pre-73rd
// renderers would both have emitted the bare `mytype`, so the sibling agreement
// is the point (Hard-won Rule #2) and the qualified string the pin.
func TestRegCopyAndSelectSiblingArgQualifyAgree(t *testing.T) {
	cat := catalog.NewInMemory()
	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: cat,
	})

	// A search_path that shows neither public nor offpath (e.g. pg_dump's
	// search_path=''): the NAME qualifies (public off the path) AND each arg
	// type whose schema is off the path qualifies.
	emptyPath := func(name string) (string, bool) { return "", true }
	// The production SELECT/COPY predicate: RegObjectSchemaVisible with offpath
	// not on the effective path.
	argVisible := func(s string) bool { return s != "offpath" }

	routine, err := cat.Routines().Create(&catalog.Routine{
		Name:           "argqual_sibling_udf",
		Schema:         "public",
		ArgTypes:       []catalog.Type{{Name: "mytype"}},
		ArgModes:       []string{"i"},
		ArgTypeSchemas: []string{"offpath"},
		ReturnType:     catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create: %v", err)
	}
	procTyp := catalog.Type{Name: "regprocedure"}
	sel := string(srv.appendTypedCellText(nil, executor.NewIntDatum(int64(routine.OID)), procTyp, emptyPath, argVisible))
	copyText := executor.RegOutArgVisible("regprocedure", routine.OID, cat, true, argVisible)
	if sel != copyText {
		t.Errorf("regprocedure(argqual_sibling_udf) arg-qualify: SELECT=%q COPY=%q — renderers diverged", sel, copyText)
	}
	if sel != "public.argqual_sibling_udf(offpath.mytype)" {
		t.Errorf("regprocedure(argqual_sibling_udf) = %q, want %q", sel, "public.argqual_sibling_udf(offpath.mytype)")
	}

	// Array variant: `offpath.mytype[]` — element quoted, suffix re-appended.
	arrRoutine, err := cat.Routines().Create(&catalog.Routine{
		Name:           "argqual_sibling_arr",
		Schema:         "public",
		ArgTypes:       []catalog.Type{{Name: "mytype[]"}},
		ArgModes:       []string{"i"},
		ArgTypeSchemas: []string{"offpath"},
		ReturnType:     catalog.Type{Name: "int4"},
	}, false)
	if err != nil {
		t.Fatalf("Routines().Create(arr): %v", err)
	}
	sel = string(srv.appendTypedCellText(nil, executor.NewIntDatum(int64(arrRoutine.OID)), procTyp, emptyPath, argVisible))
	copyText = executor.RegOutArgVisible("regprocedure", arrRoutine.OID, cat, true, argVisible)
	if sel != copyText {
		t.Errorf("regprocedure(argqual_sibling_arr) arg-qualify: SELECT=%q COPY=%q — renderers diverged", sel, copyText)
	}
	if sel != "public.argqual_sibling_arr(offpath.mytype[])" {
		t.Errorf("regprocedure(argqual_sibling_arr) = %q, want %q", sel, "public.argqual_sibling_arr(offpath.mytype[])")
	}
}
