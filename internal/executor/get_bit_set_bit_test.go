package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestGetBitSetBitGetByteSetByte pins get_bit/set_bit/get_byte/set_byte
// against the PG oracle (postgres/src/backend/utils/adt/varlena.c:
// byteaGetByte:3310, byteaGetBit:3330, byteaSetByte:3369, byteaSetBit:3400).
// Bit numbering is LSB-first within each byte (bitNo = n%8, tested via
// `byte & (1 << bitNo)`, varlena.c:3361). M0134-0070.
func TestGetBitSetBitGetByteSetByte(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql      string
		wantKind DatumKind
		wantInt  int64
		wantStr  string // for bytea results, hex text via encode(...,'hex')
	}{
		{sql: `select get_bit('\x1234567890abcdef00'::bytea, 43)`, wantKind: KindInt, wantInt: 1},
		{sql: `select get_byte('\x1234567890abcdef00'::bytea, 3)`, wantKind: KindInt, wantInt: 120},
		{sql: `select encode(set_bit('\x1234567890abcdef00'::bytea, 43, 0), 'hex')`, wantKind: KindString, wantStr: "1234567890a3cdef00"},
		{sql: `select encode(set_byte('\x1234567890abcdef00'::bytea, 7, 11), 'hex')`, wantKind: KindString, wantStr: "1234567890abcd0b00"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			advanceStmtCounter(ctx)
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
			if err != nil {
				t.Fatalf("Plan(%q): %v", tc.sql, err)
			}
			op, err := Build(plan)
			if err != nil {
				t.Fatalf("Build(%q): %v", tc.sql, err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("Open(%q): %v", tc.sql, err)
			}
			rows, err := drainScan(op)
			_ = op.Close()
			if err != nil {
				t.Fatalf("exec(%q): %v", tc.sql, err)
			}
			if len(rows) != 1 || len(rows[0]) != 1 {
				t.Fatalf("%q: want 1x1 result, got %d rows", tc.sql, len(rows))
			}
			d := rows[0][0]
			if d.Kind != tc.wantKind {
				t.Fatalf("%q: Kind = %d, want %d", tc.sql, d.Kind, tc.wantKind)
			}
			switch tc.wantKind {
			case KindInt:
				if d.Int != tc.wantInt {
					t.Errorf("%q: got %d, want %d", tc.sql, d.Int, tc.wantInt)
				}
			case KindString:
				if got := d.StringValue(); got != tc.wantStr {
					t.Errorf("%q: got %q, want %q", tc.sql, got, tc.wantStr)
				}
			}
		})
	}
}

// TestGetBitSetBitGetByteSetByteErrors pins the out-of-range (2202E) and
// invalid-new-bit-value (22023) error paths.
func TestGetBitSetBitGetByteSetByteErrors(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql      string
		wantCode string
		wantMsg  string
	}{
		{sql: `select get_bit('\x1234567890abcdef00'::bytea, 99)`, wantCode: "2202E", wantMsg: "index 99 out of valid range, 0..71"},
		{sql: `select set_bit('\x1234567890abcdef00'::bytea, 99, 0)`, wantCode: "2202E", wantMsg: "index 99 out of valid range, 0..71"},
		{sql: `select get_byte('\x1234567890abcdef00'::bytea, 99)`, wantCode: "2202E", wantMsg: "index 99 out of valid range, 0..8"},
		{sql: `select set_byte('\x1234567890abcdef00'::bytea, 99, 11)`, wantCode: "2202E", wantMsg: "index 99 out of valid range, 0..8"},
		{sql: `select set_bit('\x1234567890abcdef00'::bytea, 0, 2)`, wantCode: "22023", wantMsg: "new bit must be 0 or 1"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			advanceStmtCounter(ctx)
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
			if err != nil {
				t.Fatalf("Plan(%q): %v", tc.sql, err)
			}
			op, err := Build(plan)
			if err != nil {
				t.Fatalf("Build(%q): %v", tc.sql, err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("Open(%q): %v", tc.sql, err)
			}
			_, err = drainScan(op)
			_ = op.Close()
			if err == nil {
				t.Fatalf("%q: want error, got none", tc.sql)
			}
			execErr, ok := err.(*ExecError)
			if !ok {
				t.Fatalf("%q: want *ExecError, got %T: %v", tc.sql, err, err)
			}
			if execErr.Code != tc.wantCode {
				t.Errorf("%q: Code = %q, want %q", tc.sql, execErr.Code, tc.wantCode)
			}
			if execErr.Message != tc.wantMsg {
				t.Errorf("%q: Message = %q, want %q", tc.sql, execErr.Message, tc.wantMsg)
			}
		})
	}
}
