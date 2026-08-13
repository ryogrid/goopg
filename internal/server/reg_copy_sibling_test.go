package server

import (
	"io"
	"log/slog"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

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
		{"regcollation", 950, "C"},
		{"regcollation", 100, "default"},
		{"regcollation", 0, "-"},
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
// goopg has no pg_class entry for a schema-qualified system catalog OID that a
// bare InMemory catalog can serve, so this uses the user table pattern that
// survives a search_path without public.
func TestRegCopyAndSelectSiblingQualifyAgree(t *testing.T) {
	cat := catalog.NewInMemory()

	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: cat,
	})

	// A search_path that names no public schema (e.g. the --search_path of a
	// pg_dump restore) must make both renderers qualify. 1259 is pg_class,
	// which goopg renders schema-qualified when public is not visible; assert
	// only that the two agree, since the exact qualification of a system
	// catalog is a separate concern.
	noPublic := func(name string) (string, bool) { return `"$user"`, true }
	typ := catalog.Type{Name: "regclass"}
	sel := string(srv.appendTypedCellText(nil, executor.NewIntDatum(1259), typ, noPublic))
	copyText := executor.RegOut("regclass", 1259, cat, true)
	if sel != copyText {
		t.Errorf("regclass(1259) qualify=true: SELECT=%q COPY=%q — renderers diverged", sel, copyText)
	}
}
