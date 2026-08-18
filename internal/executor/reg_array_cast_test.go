package executor

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestRegArrayCastResolvesElements covers the M0134-0005 S07 fix: before it,
// evalCast (internal/executor/expr.go) had no case for "regclass[]" or any
// other reg*-array type, so a `'{name,...}'::regclass[]` literal fell through
// to the unknown-type pass-through and stayed raw text — every downstream
// `conrelid = ANY('{tbl}'::regclass[])` silently evaluated false instead of
// matching (see tmp/ralph-handoffs/m0134-0005-s06-nnconstraint-info-zero-rows
// /report.md for the ruling diagnosis). This test FAILS pre-fix (pg_typeof
// stays "text"/element resolution never happens) and PASSES post-fix.
func TestRegArrayCastResolvesElements(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runComposite(t, ctx,
		"CREATE TABLE regarr_t1 (id int)",
		"CREATE TABLE regarr_t2 (id int)",
	)
	connDBOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
	t1, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "regarr_t1"}, connDBOid)
	if !ok {
		t.Fatal("regarr_t1 not found in catalog")
	}
	t2, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "regarr_t2"}, connDBOid)
	if !ok {
		t.Fatal("regarr_t2 not found in catalog")
	}

	t.Run("single element resolves to catalog OID", func(t *testing.T) {
		rows := runQuery(t, ctx, "SELECT '{regarr_t1}'::regclass[]")
		if len(rows) != 1 {
			t.Fatalf("rows=%d, want 1", len(rows))
		}
		want := "{" + strconv.FormatUint(uint64(t1.OID), 10) + "}"
		if got := rows[0][0].StringValue(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("two-element array resolves both elements", func(t *testing.T) {
		rows := runQuery(t, ctx, "SELECT '{regarr_t1,regarr_t2}'::regclass[]")
		if len(rows) != 1 {
			t.Fatalf("rows=%d, want 1", len(rows))
		}
		want := "{" + strconv.FormatUint(uint64(t1.OID), 10) + "," + strconv.FormatUint(uint64(t2.OID), 10) + "}"
		if got := rows[0][0].StringValue(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("unknown relation raises an error, not silent pass-through", func(t *testing.T) {
		_, err := runQueryErr(t, ctx, "SELECT '{no_such_regarr_table}'::regclass[]")
		if err == nil {
			t.Fatal("expected an error for an unknown relation, got nil")
		}
		if ee, ok := err.(*ExecError); !ok || ee.Code != "42P01" {
			t.Fatalf("err = %v, want *ExecError{Code: 42P01}", err)
		}
	})

	t.Run("numeric-OID element passes through as that OID", func(t *testing.T) {
		rows := runQuery(t, ctx, "SELECT '{1259}'::regclass[]")
		if len(rows) != 1 {
			t.Fatalf("rows=%d, want 1", len(rows))
		}
		if got := rows[0][0].StringValue(); got != "{1259}" {
			t.Fatalf("got %q, want %q", got, "{1259}")
		}
	})

	t.Run("dash element yields OID 0 (parseRegDashOrOid contract)", func(t *testing.T) {
		rows := runQuery(t, ctx, "SELECT '{-}'::regclass[]")
		if len(rows) != 1 {
			t.Fatalf("rows=%d, want 1", len(rows))
		}
		if got := rows[0][0].StringValue(); got != "{0}" {
			t.Fatalf("got %q, want %q", got, "{0}")
		}
	})

	t.Run("regtype[] (non-regclass family member) resolves to a builtin type OID", func(t *testing.T) {
		rows := runQuery(t, ctx, "SELECT '{int4}'::regtype[]")
		if len(rows) != 1 {
			t.Fatalf("rows=%d, want 1", len(rows))
		}
		want := "{" + strconv.FormatUint(uint64(catalog.OIDInt4), 10) + "}"
		if got := rows[0][0].StringValue(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("regtype[] rejects an unknown type name", func(t *testing.T) {
		_, err := runQueryErr(t, ctx, "SELECT '{no_such_regarr_type}'::regtype[]")
		if err == nil {
			t.Fatal("expected an error for an unknown type, got nil")
		}
		if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
			t.Fatalf("err = %v, want *ExecError{Code: 42704}", err)
		}
	})

	t.Run("regtype[] oidvector direction (space-separated, no braces) is untouched", func(t *testing.T) {
		// Guards the sibling fix in evalExprSlot's "regtype[]" CastExpr arm:
		// the oidvector→name-array direction (space-separated OID text, as
		// e.g. pg_proc.proargtypes::regtype[] produces) must still work after
		// adding the brace-literal guard that routes '{...}' input to the new
		// name→OID arm instead. int4's builtin OID is 23, date's is 1082.
		rows := runQuery(t, ctx, "SELECT ('23 1082')::regtype[]")
		if len(rows) != 1 {
			t.Fatalf("rows=%d, want 1", len(rows))
		}
		want := "[0:1]={integer,date}"
		if got := rows[0][0].StringValue(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("regclass[] end-to-end matches pg_constraint via = ANY (the real symptom)", func(t *testing.T) {
		runComposite(t, ctx,
			"CREATE TABLE regarr_nn (a int, b int)",
			"ALTER TABLE regarr_nn ADD CONSTRAINT regarr_nn_a NOT NULL a NOT VALID",
		)
		rows := runQuery(t, ctx,
			"SELECT conname FROM pg_constraint WHERE conrelid = ANY('{regarr_nn}'::regclass[])")
		if len(rows) != 1 {
			t.Fatalf("rows=%d, want 1 (the real-symptom masking bug)", len(rows))
		}
		if got := rows[0][0].StringValue(); got != "regarr_nn_a" {
			t.Fatalf("conname = %q, want %q", got, "regarr_nn_a")
		}
	})
}
