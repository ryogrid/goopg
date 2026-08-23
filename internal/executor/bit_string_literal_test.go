package executor

import (
	"strings"
	"testing"
)

// TestBitStringLiteralInsertRoundTrip pins the M0134-0092 bit(n)/varbit(n)
// literal + coercion behavior against the PG 18.3 oracle
// (postgres/src/test/regress/sql/bit.sql BIT_TABLE/VARBIT_TABLE fixture):
// a B'...' literal (internal/parser/expr.go decodeBitStringLit) that doesn't
// match the column's declared length errors with the exact PG message and
// SQLSTATE, and a matching length round-trips unchanged.
func TestBitStringLiteralInsertRoundTrip(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE bit_table(b bit(11))"); err != nil {
		t.Fatalf("CREATE TABLE bit_table: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE varbit_table(v bit varying(11))"); err != nil {
		t.Fatalf("CREATE TABLE varbit_table: %v", err)
	}

	// bit(11): too short errors 22026 "does not match", exact length inserts.
	err := runDDL(t, ctx, "INSERT INTO bit_table VALUES (B'10')")
	assertBitError(t, err, "22026", "bit string length 2 does not match type bit(11)")

	if err := runDDL(t, ctx, "INSERT INTO bit_table VALUES (B'00000000000')"); err != nil {
		t.Fatalf("INSERT exact-length: %v", err)
	}
	// bit(11): too long errors 22026 too.
	err = runDDL(t, ctx, "INSERT INTO bit_table VALUES (B'101011111010')")
	assertBitError(t, err, "22026", "bit string length 12 does not match type bit(11)")

	rows := runDMLRows(t, ctx, "SELECT b FROM bit_table")
	if len(rows) != 1 || rows[0][0].StringValue() != "00000000000" {
		t.Fatalf("SELECT b FROM bit_table = %v, want one row \"00000000000\"", rows)
	}

	// varbit(11): upper bound only — short/empty/max-length all insert, over
	// errors 22001 "too long" (not "does not match" — varying, not fixed).
	for _, v := range []string{"B''", "B'0'", "B'010101'", "B'01010101010'"} {
		if err := runDDL(t, ctx, "INSERT INTO varbit_table VALUES ("+v+")"); err != nil {
			t.Fatalf("INSERT %s: %v", v, err)
		}
	}
	err = runDDL(t, ctx, "INSERT INTO varbit_table VALUES (B'101011111010')")
	assertBitError(t, err, "22001", "bit string too long for type bit varying(11)")

	rows = runDMLRows(t, ctx, "SELECT v FROM varbit_table ORDER BY length(v)")
	want := []string{"", "0", "010101", "01010101010"}
	if len(rows) != len(want) {
		t.Fatalf("SELECT v FROM varbit_table = %d rows, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if got := rows[i][0].StringValue(); got != w {
			t.Errorf("row %d = %q, want %q", i, got, w)
		}
	}
}

// TestBitStringLiteralHexDecode pins the X'...' hex-nibble-to-4-bits
// expansion (MSB first, matching varbit.c bit_in's hex branch) via a
// bit(4)/bit(8) round-trip.
func TestBitStringLiteralHexDecode(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE hex_bit(b bit(8))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO hex_bit VALUES (X'FF')"); err != nil {
		t.Fatalf("INSERT X'FF': %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO hex_bit VALUES (x'a0')"); err != nil {
		t.Fatalf("INSERT x'a0': %v", err)
	}
	rows := runDMLRows(t, ctx, "SELECT b FROM hex_bit ORDER BY b")
	want := []string{"10100000", "11111111"}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if got := rows[i][0].StringValue(); got != w {
			t.Errorf("row %d = %q, want %q", i, got, w)
		}
	}
}

func assertBitError(t *testing.T, err error, wantCode, wantMsg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error %q, got nil", wantMsg)
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err=%T (%v), want *ExecError", err, err)
	}
	if ee.Code != wantCode {
		t.Errorf("Code=%q want %q", ee.Code, wantCode)
	}
	if !strings.Contains(ee.Message, wantMsg) {
		t.Errorf("Message=%q want it to contain %q", ee.Message, wantMsg)
	}
}
