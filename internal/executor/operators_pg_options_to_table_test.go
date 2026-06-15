package executor

// operators_pg_options_to_table_test.go — pg_options_to_table(text[])
// FROM-clause SRF coverage. DU-002 slice 17 (M0110-0001). pg_dump's
// getForeignDataWrappers expands fdwoptions through this SRF.

import "testing"

// TestPgOptionsToTableSplitsNameValue pins the core split-at-first-'='
// semantics: each "name=value" element becomes (option_name, option_value);
// a bare "name" element yields a NULL option_value. Mirrors
// untransformRelOptions in src/backend/foreign/foreign.c.
func TestPgOptionsToTableSplitsNameValue(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT option_name, option_value FROM pg_options_to_table('{a=1,b=2,flag}'::text[])")
	if len(rows) != 3 {
		t.Fatalf("row count = %d, want 3 (rows=%v)", len(rows), rows)
	}
	want := []struct {
		name   string
		value  string
		isNull bool
	}{
		{"a", "1", false},
		{"b", "2", false},
		{"flag", "", true},
	}
	for i, w := range want {
		if got := rows[i][0].StringValue(); got != w.name {
			t.Errorf("row %d option_name = %q, want %q", i, got, w.name)
		}
		if rows[i][1].IsNull() != w.isNull {
			t.Errorf("row %d option_value IsNull = %v, want %v", i, rows[i][1].IsNull(), w.isNull)
		}
		if !w.isNull {
			if got := rows[i][1].StringValue(); got != w.value {
				t.Errorf("row %d option_value = %q, want %q", i, got, w.value)
			}
		}
	}
}

// TestPgOptionsToTableSplitsAtFirstEquals confirms only the first '=' is the
// delimiter; later '=' characters stay in the value (libpq connection
// options such as "options=-c geqo=off" rely on this).
func TestPgOptionsToTableSplitsAtFirstEquals(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT option_name, option_value FROM pg_options_to_table('{k=a=b=c}'::text[])")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "k" {
		t.Errorf("option_name = %q, want %q", got, "k")
	}
	if got := rows[0][1].StringValue(); got != "a=b=c" {
		t.Errorf("option_value = %q, want %q", got, "a=b=c")
	}
}

// TestPgOptionsToTableEmptyArray confirms an empty option array yields zero
// rows (the common case — an FDW with no options).
func TestPgOptionsToTableEmptyArray(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT option_name, option_value FROM pg_options_to_table('{}'::text[])")
	if len(rows) != 0 {
		t.Fatalf("row count = %d, want 0", len(rows))
	}
}

// TestPgOptionsToTableColumnAlias confirms an AS alias(col, col) list renames
// the output columns.
func TestPgOptionsToTableColumnAlias(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx,
		"SELECT k, v FROM pg_options_to_table('{host=db1}'::text[]) AS t(k, v)")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if got := rows[0][0].StringValue(); got != "host" {
		t.Errorf("k = %q, want %q", got, "host")
	}
	if got := rows[0][1].StringValue(); got != "db1" {
		t.Errorf("v = %q, want %q", got, "db1")
	}
}
