package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestChrRejectsNonPositive pins chr()'s two PG error checks
// (postgres/src/backend/utils/adt/oracle_compat.c:1030-1047) — M0134-0070.
// A negative codepoint is 22023 "character number must be positive"; a zero
// codepoint is 54000 "null character not permitted". The happy-path chr(65)
// = 'A' is unchanged.
func TestChrRejectsNonPositive(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql      string
		wantCode string
		wantMsg  string
		want     string
	}{
		{sql: `select chr(0)`, wantCode: "54000", wantMsg: "null character not permitted"},
		{sql: `select chr(-1)`, wantCode: "22023", wantMsg: "character number must be positive"},
		{sql: `select chr(65)`, want: "A"},
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
			if tc.wantCode != "" {
				if err == nil {
					t.Fatalf("%q: expected error, got none (rows=%v)", tc.sql, rows)
				}
				ee, ok := err.(*ExecError)
				if !ok {
					t.Fatalf("%q: err type=%T, want *ExecError (%v)", tc.sql, err, err)
				}
				if ee.Code != tc.wantCode {
					t.Errorf("%q: SQLSTATE=%s want %s", tc.sql, ee.Code, tc.wantCode)
				}
				if ee.Message != tc.wantMsg {
					t.Errorf("%q: Message=%q want %q", tc.sql, ee.Message, tc.wantMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("exec(%q): %v", tc.sql, err)
			}
			if len(rows) != 1 || len(rows[0]) != 1 {
				t.Fatalf("%q: want 1x1 result, got %d rows", tc.sql, len(rows))
			}
			d := rows[0][0]
			if d.Kind != KindString {
				t.Fatalf("%q: Kind = %d, want KindString", tc.sql, d.Kind)
			}
			if got := d.StringValue(); got != tc.want {
				t.Errorf("%q: got %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}

// TestByteaTrimByteSet pins the btrim/ltrim/rtrim bytea dispatch fix — bytea
// input must trim by byte-set membership (not rune semantics) and return a
// KindBytes Datum, not KindString with an embedded raw 0x00.
// PG: postgres/src/backend/utils/adt/oracle_compat.c:638-703 (dobyteatrim,
// bytealtrim/byteartrim/byteatrim) — M0134-0070.
func TestByteaTrimByteSet(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want []byte
	}{
		// leading-only trim: only the leading NUL is stripped.
		{sql: `select trim(leading E'\\000'::bytea from E'\\000Tom\\000'::bytea)`,
			want: []byte("Tom\x00")},
		// trailing-only trim: only the trailing NUL is stripped.
		{sql: `select trim(trailing E'\\000'::bytea from E'\\000Tom\\000'::bytea)`,
			want: []byte("\x00Tom")},
		// both-ends btrim.
		{sql: `select btrim(E'\\000Tom\\000'::bytea, E'\\000'::bytea)`,
			want: []byte("Tom")},
		// empty cutset => no-op.
		{sql: `select btrim(E'\\000trim\\000'::bytea, ''::bytea)`,
			want: []byte("\x00trim\x00")},
		// ltrim/rtrim direct calls.
		{sql: `select ltrim(E'\\000Tom\\000'::bytea, E'\\000'::bytea)`,
			want: []byte("Tom\x00")},
		{sql: `select rtrim(E'\\000Tom\\000'::bytea, E'\\000'::bytea)`,
			want: []byte("\x00Tom")},
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
			if d.Kind != KindBytes {
				t.Fatalf("%q: Kind = %d, want KindBytes", tc.sql, d.Kind)
			}
			if got := d.BytesValue(); string(got) != string(tc.want) {
				t.Errorf("%q: got %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}

// TestByteaTrimTextModeUnaffected guards that the pre-existing text-mode
// btrim/ltrim/rtrim path (Kind != KindBytes) is byte-for-byte unchanged by
// the new bytea branch. M0134-0070.
func TestByteaTrimTextModeUnaffected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql  string
		want string
	}{
		{sql: `select btrim('  trim me  ')`, want: "trim me"},
		{sql: `select btrim('xxTomxx', 'x')`, want: "Tom"},
		{sql: `select ltrim('  trim me  ')`, want: "trim me  "},
		{sql: `select ltrim('xxTomxx', 'x')`, want: "Tomxx"},
		{sql: `select rtrim('  trim me  ')`, want: "  trim me"},
		{sql: `select rtrim('xxTomxx', 'x')`, want: "xxTom"},
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
			if d.Kind != KindString {
				t.Fatalf("%q: Kind = %d, want KindString", tc.sql, d.Kind)
			}
			if got := d.StringValue(); got != tc.want {
				t.Errorf("%q: got %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}
