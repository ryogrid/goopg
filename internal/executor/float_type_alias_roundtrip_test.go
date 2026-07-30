package executor

import "testing"

// float_type_alias_roundtrip_test.go — root-0034.
//
// End-to-end guard for the defect the parser-side fix addresses: a column
// declared with the SQL-standard `float` spelling silently swallowed its
// rows. `float` had no entry in catalog.TypeNameToOID, so its `default:
// return OIDText` fallback stamped the column as text (atttypid 25), while
// internal/executor's own name tables (codec.go, expr.go) still read the same
// name as float8 — the classic sibling divergence. INSERT reported
// "INSERT 0 1" and the following SELECT returned zero rows.
//
// The visible symptom was regress `index_including` §10 ("Test coverage for
// names stored as cstrings in indexes"), whose fixture is
// `CREATE TABLE nametbl (c1 int, c2 name, c3 float)` — the divergence looked
// like a broken index-only scan over a `name` key but was the `c3 float`
// column losing the row before any index was consulted.
//
// The parser now performs PG's opt_float reduction, so the executor never
// sees the bare name. This test drives the whole path (DDL → INSERT →
// SELECT) rather than the parser alone, because the parser test cannot see
// the encode/decode split that actually destroyed the row.
func TestFloatTypeAliasRowRoundTrip(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// The exact index_including §10 fixture.
	if err := runDDL(t, ctx, "CREATE TABLE nametbl (c1 int, c2 name, c3 float)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO nametbl VALUES (1, 'two', 3.0)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	rows := runQuery(t, ctx, "SELECT c2, c1, c3 FROM nametbl WHERE c2 = 'two' AND c1 = 1")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (the row was swallowed by the float→text fallback)", len(rows))
	}
	if got, want := rows[0][0].Format(), "two"; got != want {
		t.Errorf("c2 = %q, want %q", got, want)
	}
	if got, want := rows[0][1].Format(), "1"; got != want {
		t.Errorf("c1 = %q, want %q", got, want)
	}
	if got, want := rows[0][2].Format(), "3"; got != want {
		t.Errorf("c3 = %q, want %q", got, want)
	}
}

// TestFloatTypeAliasPrecisionStorage pins that float(p) reaches storage as
// the concrete type PG's opt_float selects — float4 for p ≤ 24, float8 above
// — by round-tripping a value that only float8 can hold exactly. A float4
// column must lose precision here exactly as PG's does.
func TestFloatTypeAliasPrecisionStorage(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE ft (a float(24), b float(25), c float)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	// π to 15 significant digits: a 24-bit mantissa cannot hold it, so a
	// float4-backed column renders float4out's shortest round-trip form
	// while the two float8-backed columns keep every digit.
	if err := runDDL(t, ctx, "INSERT INTO ft VALUES (3.14159265358979, 3.14159265358979, 3.14159265358979)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	rows := runQuery(t, ctx, "SELECT a, b, c FROM ft")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if got, want := rows[0][0].Format(), "3.1415927"; got != want {
		t.Errorf("float(24) column a = %q, want %q (must be stored as float4)", got, want)
	}
	if got, want := rows[0][1].Format(), "3.14159265358979"; got != want {
		t.Errorf("float(25) column b = %q, want %q (must be stored as float8)", got, want)
	}
	if got, want := rows[0][2].Format(), "3.14159265358979"; got != want {
		t.Errorf("bare float column c = %q, want %q (must be stored as float8)", got, want)
	}
}
