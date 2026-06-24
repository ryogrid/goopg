package executor

import "testing"

// TestRegclassInvalidOidRendersDash pins PG's regclassout behaviour for
// InvalidOid: `0::regclass` renders as "-", not the name of the first virtual
// relation whose OID is left unset (which is also 0). This surfaced while
// probing the reindex-concurrently-toast isolation spec (M0118-0008): a table
// with no TOAST relation has pg_class.reltoastrelid = 0, and
// `reltoastrelid::regclass::text` previously rendered "routines"
// (information_schema.routines, OID 0) instead of "-".
//
// Mirrors src/backend/utils/adt/regproc.c regclassout:
//
//	if (classid == InvalidOid) { result = pstrdup("-"); ... }
func TestRegclassInvalidOidRendersDash(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if got := runQuery(t, ctx, `SELECT 0::regclass::text`)[0][0].StringValue(); got != "-" {
		t.Errorf("0::regclass::text = %q, want %q", got, "-")
	}

	// reltoastrelid of a table with no TOAST relation is 0; it must render "-".
	if err := runDDL(t, ctx, `CREATE TABLE no_toast (id int primary key)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	rows := runQuery(t, ctx,
		`SELECT reltoastrelid::regclass::text FROM pg_class WHERE relname = 'no_toast'`)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "-" {
		t.Errorf("reltoastrelid::regclass::text = %q, want %q", got, "-")
	}
}
