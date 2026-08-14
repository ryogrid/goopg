package executor

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0119-0006 (deferral row 1350): a reg* datum — a plain KindInt holding an
// object OID — cast to a string type must render its OBJECT NAME via the same
// executor.RegOut the SELECT (appendTypedCellText, server/dispatch.go) and
// COPY (datumToCopyText) wire paths use, not the raw numeric OID. The reg*out
// family (regclassout/regprocout/regprocedureout/regtypeout/regroleout/
// regcollationout, postgres/src/backend/utils/adt/regproc.c) all render the
// resolved name; evalCastTyped is the third sibling of the SELECT/COPY
// renderers and must agree (pattern_sibling_paths_must_agree).
//
// A note on shapes: the parser lowers `'pg_type'::regclass` (string input) to
// a KindString in a bare NewInMemory test catalog (system tables are not
// resolvable by name there, so the input arm passes the string through), so
// the STRING-input regclass/regtype shapes do not exercise this guard — the
// KindInt shapes do: a stored reg* COLUMN, `<oid>::regrole/regcollation`
// (no OID-resolving input arm, so the datum stays KindInt), the regproc/
// regprocedure string inputs (which resolve to the OID), and a direct KindInt
// datum. The battery covers every shape; the design doc's five literals are
// pinned as regression checks in the SQL test.

// regCastTextCat is a fixture holding every object the battery resolves: a
// user table qmt in public (qualify-observable), a user routine
// f_varbit(varbit), and a second role. GetSetting is left nil by default so
// the session behaves like the default `"$user", public` search_path
// (qualify=false); tests that need an empty path re-wire it.
func regCastTextCat(t *testing.T) *Context {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	if err := runDDL(t, ctx, `CREATE TABLE qmt (id int)`); err != nil {
		t.Fatalf("CREATE TABLE qmt: %v", err)
	}
	if _, err := ctx.Catalog.Routines().Create(&catalog.Routine{
		Name:           "f_varbit",
		Schema:         "public",
		ArgTypes:       []catalog.Type{{Name: "varbit"}},
		ArgModes:       []string{"i"},
		ArgTypeSchemas: []string{"pg_catalog"},
		ReturnType:     catalog.Type{Name: "int4"},
	}, false); err != nil {
		t.Fatalf("Routines().Create(f_varbit): %v", err)
	}
	ctx.Catalog.(*catalog.InMemory).RegisterRoleWithOID("alice", 7777)
	return ctx
}

// The SQL battery. `char` (CHAROID) is deliberately absent — it keeps
// charin/charout first-byte semantics (design doc acceptance #6).
func TestRegCastToStringRendersName(t *testing.T) {
	ctx := regCastTextCat(t)

	cases := []struct {
		query string
		want  string
	}{
		// The design doc's five literals. regclass's string input stays a
		// KindString in this harness, so these pin the END state rather than
		// the guard (see the file comment); on a server whose catalog resolves
		// pg_type by name they are guard-driven too.
		{`SELECT 'pg_type'::regclass::text`, "pg_type"},
		{`SELECT 'pg_type'::regclass::name`, "pg_type"},
		{`SELECT 'pg_type'::regclass::varchar`, "pg_type"},
		{`SELECT 'pg_type'::regclass::bpchar`, "pg_type"},
		// regproc/regprocedure string inputs DO resolve to a KindInt OID, so
		// these exercise the guard; varbit renders via format_type_be's
		// bit-varying alias (reg_qualify_test.go's 74th-slice alias).
		{`SELECT 'f_varbit'::regproc::text`, "f_varbit"},
		{`SELECT 'f_varbit(varbit)'::regprocedure::text`, "f_varbit(bit varying)"},
		{`SELECT 'f_varbit(varbit)'::regprocedure::varchar`, "f_varbit(bit varying)"},
		{`SELECT 'f_varbit(varbit)'::regprocedure::name`, "f_varbit(bit varying)"},
		{`SELECT 'f_varbit(varbit)'::regprocedure::bpchar`, "f_varbit(bit varying)"},
		// regtype's string input stays a KindString here; pin the end state.
		{`SELECT 'integer'::regtype::text`, "integer"},
		{`SELECT 'integer'::regtype::varchar`, "integer"},
		{`SELECT 'integer'::regtype::name`, "integer"},
		{`SELECT 'integer'::regtype::bpchar`, "integer"},
		// regrole/regcollation have no OID-resolving input arm, so an integer
		// literal keeps its KindInt — the guard drives these. regroleout emits
		// the bare name; regcollationout quote_identifiers every name, so "C"
		// renders with its quotes, byte-identical to PG.
		{`SELECT 10::regrole::text`, "postgres"},
		{`SELECT 10::regrole::varchar`, "postgres"},
		{`SELECT 10::regrole::name`, "postgres"},
		{`SELECT 10::regrole::bpchar`, "postgres"},
		{`SELECT 950::regcollation::text`, `"C"`},
		{`SELECT 950::regcollation::varchar`, `"C"`},
		{`SELECT 950::regcollation::name`, `"C"`},
		{`SELECT 950::regcollation::bpchar`, `"C"`},
	}
	for _, tc := range cases {
		rows := runQuery(t, ctx, tc.query)
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Errorf("%s: got %d×%d rows, want 1×1", tc.query, len(rows), len(rows[0]))
			continue
		}
		got := rows[0][0].StringValue()
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.query, got, tc.want)
		}
	}
}

// The full reg* × string-target matrix driven directly through evalCastTyped
// with a KindInt datum (an already-resolved reg* OID — exactly what a reg*
// COLUMN or the server's `<oid>::reg*` datum holds). This is the precise guard
// under test and fails for all 24 cells if it is removed or weakened.
func TestRegCastToTextDirectKindIntMatrix(t *testing.T) {
	ctx := regCastTextCat(t)
	qmt, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "qmt"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		t.Fatal("qmt not found")
	}
	fvb := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: "f_varbit"})
	if len(fvb) == 0 {
		t.Fatal("f_varbit not found")
	}

	type cell struct {
		source string
		oid    int64
		want   string
	}
	sources := []cell{
		{"regclass", int64(qmt.OID), "qmt"},
		{"regproc", int64(fvb[0].OID), "f_varbit"},
		{"regprocedure", int64(fvb[0].OID), "f_varbit(bit varying)"},
		{"regtype", 23, "integer"},
		{"regrole", 10, "postgres"},
		{"regcollation", 950, `"C"`},
	}
	for _, tc := range sources {
		for _, target := range []string{"text", "varchar", "name", "bpchar"} {
			got, err := evalCastTyped(NewIntDatum(tc.oid), target, tc.source, 0, ctx)
			if err != nil {
				t.Errorf("evalCastTyped(%d, %s, %s): %v", tc.oid, target, tc.source, err)
				continue
			}
			if got.StringValue() != tc.want {
				t.Errorf("%s(oid %d)::%s = %q, want %q", tc.source, tc.oid, target, got.StringValue(), tc.want)
			}
		}
	}
}

// A stored reg* COLUMN cast to text renders its name (the planner stamps
// CastExpr.SourceType from the operand's type, so the guard must also fire for
// `regcol::text` — the shape the ledger did not probe).
func TestRegCastColumnToTextRendersName(t *testing.T) {
	ctx := regCastTextCat(t)
	if err := runDDL(t, ctx, `CREATE TABLE regt (rel regclass, fn regprocedure)`); err != nil {
		t.Fatalf("CREATE TABLE regt: %v", err)
	}
	qmt, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "qmt"},
		catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		t.Fatal("qmt not found")
	}
	fvb := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: "f_varbit"})
	if len(fvb) == 0 {
		t.Fatal("f_varbit not found")
	}
	// Insert the OIDs via explicit ::reg* casts (the regclass input arm cannot
	// resolve a system table by name in this harness; evalCast's reg* arm passes
	// the KindInt OID through, so the stored column datum is a plain KindInt —
	// exactly what a server-side reg* column holds). Raw integer literals are
	// rejected at plan time (no implicit int→regclass assignment cast).
	ins := `INSERT INTO regt VALUES (` + strconv.FormatUint(uint64(qmt.OID), 10) + `::regclass, ` +
		strconv.FormatUint(uint64(fvb[0].OID), 10) + `::regprocedure)`
	if err := runDDL(t, ctx, ins); err != nil {
		t.Fatalf("INSERT INTO regt: %v", err)
	}
	rows := runQuery(t, ctx, `SELECT rel::text, rel::name, rel::bpchar, fn::text FROM regt`)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	wants := []string{"qmt", "qmt", "qmt", "f_varbit(bit varying)"}
	for i, w := range wants {
		if got := rows[0][i].StringValue(); got != w {
			t.Errorf("regt column cast col %d = %q, want %q", i, got, w)
		}
	}
}

// OID 0 renders "-" and a dangling OID renders the numeric fallback — RegOut's
// unchanged three-verdict shape (regproc.c: regclassout/regtypeout's "-",
// regroleout's unquoted %u).
func TestRegCastToTextOidZeroAndDangling(t *testing.T) {
	ctx := regCastTextCat(t)
	for _, src := range []string{"regclass", "regtype", "regrole"} {
		got, err := evalCastTyped(NewIntDatum(0), "text", src, 0, ctx)
		if err != nil || got.StringValue() != "-" {
			t.Errorf("0::%s::text = %q (err %v), want %q", src, got.StringValue(), err, "-")
		}
	}
	if got, err := evalCastTyped(NewIntDatum(999999999), "text", "regtype", 0, ctx); err != nil || got.StringValue() != "999999999" {
		t.Errorf("999999999::regtype::text = %q (err %v), want %q", got.StringValue(), err, "999999999")
	}
}

// Sibling agreement (acceptance #5): the CAST path and the SELECT wire path
// render the same reg* text for the same OID under the same search_path. The
// SELECT path renders via appendTypedCellText → executor.RegOutArgVisible,
// pinned equal to RegOut by internal/server/reg_copy_sibling_test.go's
// TestRegCopyAndSelectSibling*Agree; comparing the FULL cast pipeline (a
// regprocedure string input — a KindInt-carrying shape) against RegOut with
// the canonical qualify expression pins the agreement transitively, on both
// the default path (bare name) and pg_dump's search_path='' (qualified).
func TestRegCastToTextSiblingAgreeQualify(t *testing.T) {
	ctx := regCastTextCat(t)
	fvb := ctx.Catalog.Routines().LookupByName(parser.ObjectName{Name: "f_varbit"})
	if len(fvb) == 0 {
		t.Fatal("f_varbit not found")
	}

	// Default search_path (public visible): qualify=false, bare name.
	rows := runQuery(t, ctx, `SELECT 'f_varbit(varbit)'::regprocedure::text`)
	castDefault := rows[0][0].StringValue()
	selectDefault := RegOut("regprocedure", fvb[0].OID, ctx.Catalog,
		!RegObjectSchemaVisible(ctx, "public"))
	if castDefault != selectDefault {
		t.Errorf("cast(default path) = %q, SELECT path = %q — diverged", castDefault, selectDefault)
	}
	if castDefault != "f_varbit(bit varying)" {
		t.Errorf("cast(default path) = %q, want %q", castDefault, "f_varbit(bit varying)")
	}

	// pg_dump-style search_path='' (public OFF the path): qualify=true, so both
	// paths schema-qualify the routine NAME.
	ctx.GetSetting = func(name string) (string, bool) { return "", true }
	rows = runQuery(t, ctx, `SELECT 'f_varbit(varbit)'::regprocedure::text`)
	castQualified := rows[0][0].StringValue()
	selectQualified := RegOut("regprocedure", fvb[0].OID, ctx.Catalog,
		!RegObjectSchemaVisible(ctx, "public"))
	if castQualified != selectQualified {
		t.Errorf("cast(search_path='') = %q, SELECT path = %q — diverged", castQualified, selectQualified)
	}
	if castQualified != "public.f_varbit(bit varying)" {
		t.Errorf("cast(search_path='') = %q, want %q", castQualified, "public.f_varbit(bit varying)")
	}
}
