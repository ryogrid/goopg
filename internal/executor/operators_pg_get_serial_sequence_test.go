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

// TestPgGetSerialSequenceExplicitSchemaQualifiedOwnedBy pins
// bareOwnedByTableColumn (operators_ddl.go): an explicit CREATE SEQUENCE
// ... OWNED BY schema.table.column stores a schema-qualified string, but
// FindSequenceOwnedBy's callers (autoGenerateSerialValues, this function)
// always probe with the bare "table.column" form — without stripping the
// schema qualifier first, the lookup silently misses and this returns NULL
// instead of the owned sequence's name.
func TestPgGetSerialSequenceExplicitSchemaQualifiedOwnedBy(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	// Table/sequence names deliberately distinct from every other fixture in
	// this file: seqRegistry is a process-global sync.Map with no per-test
	// reset, and FindSequenceOwnedBy matches on the bare "table.column" string
	// with no schema/database scoping — a leftover "orders"/"id"-owned
	// sequence from an earlier test in this same binary can otherwise be
	// picked up by Range()'s unordered iteration instead of the one this test
	// just created (observed while developing this test: collided with
	// TestPgGetSerialSequenceFollowsRename's renamed sequence).
	if err := runDDL(t, ctx, "CREATE TABLE sq_ownedby_create_tbl (id integer, note text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE SEQUENCE sq_ownedby_create_seq OWNED BY public.sq_ownedby_create_tbl.id"); err != nil {
		t.Fatalf("CREATE SEQUENCE ... OWNED BY: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT pg_get_serial_sequence('sq_ownedby_create_tbl', 'id')")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "public.sq_ownedby_create_seq" {
		t.Errorf("pg_get_serial_sequence = %q, want \"public.sq_ownedby_create_seq\"", got)
	}
}

// TestPgGetSerialSequenceAlterSequenceSchemaQualifiedOwnedBy is the ALTER
// SEQUENCE ... OWNED BY sibling of the CREATE SEQUENCE case above (same
// bareOwnedByTableColumn normalization, different call site).
func TestPgGetSerialSequenceAlterSequenceSchemaQualifiedOwnedBy(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE sq_ownedby_alter_tbl (id integer, note text)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE SEQUENCE sq_ownedby_alter_seq"); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER SEQUENCE sq_ownedby_alter_seq OWNED BY public.sq_ownedby_alter_tbl.id"); err != nil {
		t.Fatalf("ALTER SEQUENCE ... OWNED BY: %v", err)
	}

	rows := runQueryRows(t, ctx, "SELECT pg_get_serial_sequence('sq_ownedby_alter_tbl', 'id')")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "public.sq_ownedby_alter_seq" {
		t.Errorf("pg_get_serial_sequence = %q, want \"public.sq_ownedby_alter_seq\"", got)
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
