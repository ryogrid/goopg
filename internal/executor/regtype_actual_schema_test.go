package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestRegtypeOffPathActualSchema guards deferral-ledger row 1355 (slice B):
// the `::regtype` cast resolves a user type's ACTUAL schema from its
// NamespaceOID (slice A) and renders it schema-qualified via regOutQualified —
// myschema.mood, NOT the old hardcoded "public.mood" — and stays bare when
// the type is on-path (in a visible schema). Mirrors PG's regtypeout →
// format_type_be → format_type_extended (format_type.c:303-326:
// quote_qualified_identifier when !TypeIsVisible), where the qualification
// decision is the type's REAL typnamespace being off the session's effective
// search_path.
func TestRegtypeOffPathActualSchema(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	im.RegisterSchema("myschema")

	if err := runDDL(t, ctx, `CREATE TYPE myschema.mood AS ENUM ('sad', 'ok', 'happy')`); err != nil {
		t.Fatalf("CREATE TYPE myschema.mood: %v", err)
	}
	et, found := im.LookupEnum("mood")
	if !found {
		t.Fatal("enum not registered")
	}
	if err := runDDL(t, ctx, `CREATE DOMAIN myschema.posint AS int CHECK (VALUE > 0)`); err != nil {
		t.Fatalf("CREATE DOMAIN myschema.posint: %v", err)
	}
	dom, found := im.LookupDomain("posint")
	if !found {
		t.Fatal("domain not registered")
	}

	// Default search_path ($user, public): myschema is off-path, so the enum
	// and domain render with their ACTUAL schema (the old code rendered bare
	// "mood" here because its fixed predicate only watched "public").
	for _, tc := range []struct {
		name string
		oid  uint32
		want string
	}{
		{"enum", et.OID, "myschema.mood"},
		{"domain", dom.OID, "myschema.posint"},
	} {
		rows := runQuery(t, ctx, fmt.Sprintf("SELECT %d::regtype", tc.oid))
		if len(rows) != 1 || rows[0][0].StringValue() != tc.want {
			t.Errorf("%d::regtype (off-path, default path) = %v, want %q", tc.oid, rows, tc.want)
		}
	}

	// pg_dump-style search_path='' (every schema off-path): the off-path enum
	// still renders with its ACTUAL schema — NOT the old "public.mood" that the
	// hardcoded prefix produced.
	ctx.GetSetting = func(name string) (string, bool) { return "", true }
	rows := runQuery(t, ctx, fmt.Sprintf("SELECT %d::regtype", et.OID))
	if len(rows) != 1 || rows[0][0].StringValue() != "myschema.mood" {
		t.Errorf("%d::regtype (off-path, search_path='') = %v, want %q", et.OID, rows, "myschema.mood")
	}

	// On-path: an unqualified CREATE → public, which IS on the default
	// search_path, so the name renders bare.
	ctx.GetSetting = nil
	if err := runDDL(t, ctx, `CREATE TYPE pubmood AS ENUM ('a', 'b')`); err != nil {
		t.Fatalf("CREATE TYPE pubmood: %v", err)
	}
	pet, found := im.LookupEnum("pubmood")
	if !found {
		t.Fatal("public enum not registered")
	}
	rows = runQuery(t, ctx, fmt.Sprintf("SELECT %d::regtype", pet.OID))
	if len(rows) != 1 || rows[0][0].StringValue() != "pubmood" {
		t.Errorf("%d::regtype (on-path, default path) = %v, want %q", pet.OID, rows, "pubmood")
	}
}

// TestRegtypeMixedCaseQuoted guards the quoting half of slice B: a mixed-case
// user type name renders quote_qualified_identifier'd — myschema."MyType" —
// exactly as format_type_extended does. The name is ALWAYS quote_identifier'd
// even when not schema-qualified, a behavior the old "public."-prefix string
// concat could never produce.
//
// NOTE: goopg's four type registries lower-case type names at Register*
// (pre-existing gap; PG preserves the quoted typname "MyType" — this slice
// does not change that). To exercise the RENDERER's quoting independently of
// that DDL limitation, the test registers a lowercase name via DDL (so
// NamespaceOID is populated) and then rewrites the registry struct's Name to
// the mixed-case value a case-preserving registry would carry. The ::regtype
// renderer must quote it regardless.
func TestRegtypeMixedCaseQuoted(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	im.RegisterSchema("myschema")

	if err := runDDL(t, ctx, `CREATE TYPE myschema.mytype AS ENUM ('a', 'b')`); err != nil {
		t.Fatalf("CREATE TYPE myschema.mytype: %v", err)
	}
	et, found := im.LookupEnum("mytype")
	if !found {
		t.Fatal("enum not registered")
	}
	et.Name = "MyType" // simulate a case-preserving registry (see NOTE)

	// Off-path under the default search_path → qualified AND quoted.
	rows := runQuery(t, ctx, fmt.Sprintf("SELECT %d::regtype", et.OID))
	if len(rows) != 1 || rows[0][0].StringValue() != `myschema."MyType"` {
		t.Errorf("%d::regtype = %v, want %q", et.OID, rows, `myschema."MyType"`)
	}

	// On-path (public, default search_path) → bare but still quote_identifier'd.
	if err := runDDL(t, ctx, `CREATE TYPE pubmood AS ENUM ('a', 'b')`); err != nil {
		t.Fatalf("CREATE TYPE pubmood: %v", err)
	}
	pet, found := im.LookupEnum("pubmood")
	if !found {
		t.Fatal("public enum not registered")
	}
	pet.Name = "PubMood" // simulate a case-preserving registry (see NOTE)
	rows = runQuery(t, ctx, fmt.Sprintf("SELECT %d::regtype", pet.OID))
	if len(rows) != 1 || rows[0][0].StringValue() != `"PubMood"` {
		t.Errorf("%d::regtype (on-path) = %v, want %q", pet.OID, rows, `"PubMood"`)
	}
}

// TestRegtypeSiblingAgreementOffPath extends the reg* sibling-agreement family
// (TestRegArraySelectCopySiblingAgree / TestRegCastToTextSiblingAgreeQualify)
// to regtype's off-path case: under a pg_dump-style search_path='' (every
// schema off-path) the SELECT wire (appendTypedCellText → RegOutArgVisible),
// COPY TO (EncodeCopyTextRow), the `::regtype` cast, and bare RegOut all
// render the same off-path enum with its ACTUAL schema — myschema.mood.
// Before slice B the wire/COPY/cast paths each used their own fixed
// "public"-based flag that could never express an off-path non-public schema,
// so an off-path enum rendered inconsistently across siblings.
func TestRegtypeSiblingAgreementOffPath(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}
	im.RegisterSchema("myschema")
	if err := runDDL(t, ctx, `CREATE TYPE myschema.mood AS ENUM ('sad', 'ok', 'happy')`); err != nil {
		t.Fatalf("CREATE TYPE myschema.mood: %v", err)
	}
	et, found := im.LookupEnum("mood")
	if !found {
		t.Fatal("enum not registered")
	}

	// pg_dump-style search_path='': public NOT visible → every path qualifies.
	ctx.GetSetting = func(name string) (string, bool) { return "", true }

	// `::regtype` cast (per-schema predicate from RegObjectSchemaVisible).
	cast := runQuery(t, ctx, fmt.Sprintf("SELECT %d::regtype", et.OID))
	if len(cast) != 1 {
		t.Fatalf("%d::regtype = %v", et.OID, cast)
	}
	castText := cast[0][0].StringValue()

	// SELECT wire: appendTypedCellText → RegOutArgVisible with
	// qualify = !publicSchemaVisible(search_path='') = true.
	wire := RegOutArgVisible("regtype", et.OID, ctx.Catalog, true, nil)

	// COPY TO: datumToCopyText → RegOutArgVisible with the same qualify flag.
	copyRow, err := EncodeCopyTextRow(nil, Row{NewIntDatum(int64(et.OID))},
		[]catalog.Column{{Name: "r", Type: catalog.Type{Name: "regtype"}}},
		"ISO", "MDY", "", ctx.Catalog, true)
	if err != nil {
		t.Fatalf("COPY TO: %v", err)
	}
	copyText := strings.TrimSuffix(string(copyRow), "\n")

	// Bare RegOut with the same qualify flag.
	regOut := RegOut("regtype", et.OID, ctx.Catalog, true)

	want := "myschema.mood"
	for _, got := range []struct{ name, v string }{
		{"::regtype cast", castText},
		{"SELECT wire (RegOutArgVisible)", wire},
		{"COPY TO (EncodeCopyTextRow)", copyText},
		{"RegOut", regOut},
	} {
		if got.v != want {
			t.Errorf("%s = %q, want %q (off-path, search_path='')", got.name, got.v, want)
		}
	}
}
