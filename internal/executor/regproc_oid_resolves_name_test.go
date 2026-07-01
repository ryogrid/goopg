package executor

import (
	"fmt"
	"testing"
)

// TestRegprocOIDCastResolvesName pins the M0119-0004 regproc-cast gap: a
// non-zero OID cast via `::regproc`/`::regprocedure` previously returned the
// raw input datum unchanged (a no-op cast), so downstream output rendered
// the bare numeric OID instead of PG's regprocout function name. InvalidOid
// (0) already rendered "-" (DU-002 slice 375); this pins the general case
// alongside it, for both a built-in pg_proc OID and a CREATE FUNCTION OID.
//
// Mirrors src/backend/utils/adt/regproc.c regprocout.
func TestRegprocOIDCastResolvesName(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if got := runQuery(t, ctx, `SELECT 0::regproc::text`)[0][0].StringValue(); got != "-" {
		t.Errorf("0::regproc::text = %q, want %q", got, "-")
	}
	// 43 = int4out (postgres/src/include/catalog/pg_proc.dat).
	if got := runQuery(t, ctx, `SELECT 43::regproc::text`)[0][0].StringValue(); got != "int4out" {
		t.Errorf("43::regproc::text = %q, want %q", got, "int4out")
	}
	if got := runQuery(t, ctx, `SELECT 43::regprocedure::text`)[0][0].StringValue(); got != "int4out" {
		t.Errorf("43::regprocedure::text = %q, want %q", got, "int4out")
	}

	if err := runDDL(t, ctx, `CREATE FUNCTION my_regproc_udf() RETURNS int4 LANGUAGE sql AS $$ SELECT 1 $$`); err != nil {
		t.Fatalf("create function: %v", err)
	}
	rows := runQuery(t, ctx, `SELECT 'my_regproc_udf'::regproc`)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	udfOID := rows[0][0].Int

	q := fmt.Sprintf(`SELECT %d::regproc::text`, udfOID)
	if got := runQuery(t, ctx, q)[0][0].StringValue(); got != "my_regproc_udf" {
		t.Errorf("<user-defined-oid>::regproc::text = %q, want %q", got, "my_regproc_udf")
	}
}
