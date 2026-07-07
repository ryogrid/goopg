package executor

// operators_pg_get_serial_sequence_test.go — pg_get_serial_sequence() must
// resolve the column's actual OWNED-BY sequence (real PG dependency lookup,
// ruleutils.c's pg_get_serial_sequence scans pg_depend for an auto/internal
// dependency), not fabricate a "table_column_seq" name by convention. That
// convention breaks for a renamed sequence, an explicit non-conventional
// OWNED BY target, and any plain (non-owned) column — which must return
// NULL. M0122 follow-up.

import "testing"

func TestPgGetSerialSequenceSerialColumn(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE orders (id serial PRIMARY KEY, note text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT pg_get_serial_sequence('orders', 'id')")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "public.orders_id_seq" {
		t.Errorf("pg_get_serial_sequence = %q, want \"public.orders_id_seq\"", got)
	}
}

func TestPgGetSerialSequenceNonSerialColumnReturnsNull(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE orders (id serial PRIMARY KEY, note text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT pg_get_serial_sequence('orders', 'note')")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if !rows[0][0].IsNull() {
		t.Errorf("pg_get_serial_sequence('orders','note') = %q, want NULL (plain column owns no sequence)",
			rows[0][0].StringValue())
	}
}

// TestPgGetSerialSequenceFollowsRename pins the real dependency-lookup
// behavior: once the backing sequence is renamed, pg_get_serial_sequence must
// report the NEW name, not the stale "table_column_seq" convention.
func TestPgGetSerialSequenceFollowsRename(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE orders (id serial PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER SEQUENCE orders_id_seq RENAME TO orders_id_custom_seq"); err != nil {
		t.Fatalf("ALTER SEQUENCE RENAME: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT pg_get_serial_sequence('orders', 'id')")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "public.orders_id_custom_seq" {
		t.Errorf("pg_get_serial_sequence = %q, want \"public.orders_id_custom_seq\" (post-rename)", got)
	}
}

func TestPgGetSerialSequenceIdentityColumn(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx,
		"CREATE TABLE widgets (id integer GENERATED ALWAYS AS IDENTITY, name text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT pg_get_serial_sequence('widgets', 'id')")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "public.widgets_id_seq" {
		t.Errorf("pg_get_serial_sequence = %q, want \"public.widgets_id_seq\"", got)
	}
}
