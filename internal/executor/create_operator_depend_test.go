package executor

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCreateOperatorPopulatesPGDepend verifies CREATE OPERATOR now records
// pg_depend rows for its namespace, oprcode, oprrest, and oprjoin references
// — mirroring PG's makeOperatorDependencies (postgres/src/backend/catalog/
// pg_operator.c) including its isObjectPinned() skip (OID < 12000 is pinned
// EXCEPT the public namespace, which is explicitly carved out as
// never-pinned). Before this fix, catalog.InMemory.PGDependRowsForDBOid had
// NO code path at all for c.userOperators, so every CREATE OPERATOR reported
// zero pg_depend rows — confirmed live against a PG 18.3 oracle via
// alter_operator.sql (M0134-0089), whose very first query
// (`SELECT ... FROM pg_depend WHERE classid='pg_operator'::regclass AND
// objid = '...'::regoperator`) expects exactly 3 rows for a fresh operator
// with a user-defined restrict function and a builtin (pinned, thus
// unlisted) join function.
func TestCreateOperatorPopulatesPGDepend(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE FUNCTION alter_op_test_fn(boolean, boolean) RETURNS boolean AS $$ SELECT NULL::BOOLEAN; $$ LANGUAGE sql IMMUTABLE`); err != nil {
		t.Fatalf("CREATE FUNCTION alter_op_test_fn: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE FUNCTION customcontsel(internal, oid, internal, integer) RETURNS float8 AS 'contsel' LANGUAGE internal STABLE STRICT`); err != nil {
		t.Fatalf("CREATE FUNCTION customcontsel: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE OPERATOR === (
		LEFTARG = boolean, RIGHTARG = boolean, PROCEDURE = alter_op_test_fn,
		COMMUTATOR = ===, NEGATOR = !==, RESTRICT = customcontsel,
		JOIN = contjoinsel, HASHES, MERGES)`); err != nil {
		t.Fatalf("CREATE OPERATOR ===: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)

	op, ok := im.LookupUserOperator("public", "===", "boolean", "boolean")
	if !ok {
		t.Fatal("=== not registered")
	}

	rows := im.PGDependRowsForDBOid(catalog.DefaultDBOid)
	opOIDStr := strconv.FormatUint(uint64(op.OID), 10)
	var opRows [][]string
	for _, r := range rows {
		if r[0] == "2617" && r[1] == opOIDStr {
			opRows = append(opRows, r)
		}
	}
	// namespace (public, unpinned) + oprcode (alter_op_test_fn, user-defined)
	// + oprrest (customcontsel, user-defined) = 3. oprjoin (contjoinsel) has
	// no matching routine at all in this throwaway context (goopg's curated
	// builtin-proc set does not include it either), so it stays unresolved
	// (FuncOID 0) and contributes no row — matching the live PG 18.3
	// oracle's 3-row count for the identical fixture (there contjoinsel IS a
	// resolvable builtin, but it is PINNED, so it is likewise excluded).
	if len(opRows) != 3 {
		t.Fatalf("pg_depend rows for operator === = %d, want 3: %v", len(opRows), opRows)
	}
	publicNS := strconv.FormatUint(uint64(catalog.PublicNamespaceOID), 10)
	funcOID := strconv.FormatUint(uint64(op.FuncOID), 10)
	restOID := strconv.FormatUint(uint64(op.RestrictOID), 10)
	wantRefs := map[[2]string]bool{
		{"2615", publicNS}: true,
		{"1255", funcOID}:  true,
		{"1255", restOID}:  true,
	}
	for _, r := range opRows {
		key := [2]string{r[3], r[4]}
		if !wantRefs[key] {
			t.Errorf("unexpected pg_depend row refclassid/refobjid = %s/%s: %v", r[3], r[4], r)
			continue
		}
		if r[6] != "n" {
			t.Errorf("row %v deptype = %q, want %q", r, r[6], "n")
		}
	}
}

// TestRegoperatorCastResolvesToOID verifies `'name(left,right)'::regoperator`
// resolves to the operator's numeric OID (not a rendered display string) so
// `objid = '...'::regoperator` comparisons against an oid-typed pg_depend
// column work — mirroring the `regclass` CastExpr arm's identical
// string-input/int-output asymmetry. Before this fix the CastExpr's
// regoper/regoperator branch only handled a KindInt (OID) input; a
// KindString (name) input fell through unchanged, so the comparison always
// evaluated false. M0134-0089.
func TestRegoperatorCastResolvesToOID(t *testing.T) {
	ctx := NewContext()
	ctx.Catalog = catalog.NewInMemory()
	if err := runDDL(t, ctx, `CREATE OPERATOR public.~=~ (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4)`); err != nil {
		t.Fatalf("CREATE OPERATOR: %v", err)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	op, ok := im.LookupUserOperator("public", "~=~", "int4", "int4")
	if !ok {
		t.Fatal("~=~ not registered")
	}
	opOIDStr := strconv.FormatUint(uint64(op.OID), 10)

	rows := runQuery(t, ctx, `SELECT '~=~(int4,int4)'::regoperator = `+opOIDStr+`::oid`)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !rows[0][0].BoolValue() {
		t.Errorf("'~=~(int4,int4)'::regoperator = %s::oid = %v, want true", opOIDStr, rows[0][0])
	}

	// Alias spelling (int4's canonical form) must resolve the same way,
	// mirroring LookupBuiltinOperator's TypeNameToOID normalization.
	rows = runQuery(t, ctx, `SELECT '~=~(integer,integer)'::regoperator = `+opOIDStr+`::oid`)
	if len(rows) != 1 || !rows[0][0].BoolValue() {
		t.Errorf("alias-spelled regoperator cast did not resolve to the same OID: %v", rows)
	}

	// A nonexistent operator signature must raise 42883, not silently pass
	// the raw text through.
	_, err := runQueryErr(t, ctx, `SELECT 'nope(int4,int4)'::regoperator`)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42883" {
		t.Fatalf("error = %v, want ExecError{Code: 42883}", err)
	}
}
