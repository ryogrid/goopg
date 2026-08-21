package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestReverseByteaByteReversal pins reverse(bytea) to a plain byte-for-byte
// reversal (postgres/src/backend/utils/adt/varlena.c:3458-3474, bytea_reverse)
// — no rune/codepoint awareness, unlike the text sibling (text_reverse,
// varlena.c:5841+, which is left unchanged). M0134-0070.
func TestReverseByteaByteReversal(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql      string
		wantKind DatumKind
		wantStr  string
		wantByte []byte
	}{
		{sql: `select reverse('\xaabb'::bytea)`, wantKind: KindBytes, wantByte: []byte{0xbb, 0xaa}},
		{sql: `select reverse('\xcdab'::bytea)`, wantKind: KindBytes, wantByte: []byte{0xab, 0xcd}},
		// text sibling is unchanged (rune-reverse).
		{sql: `select reverse('hello')`, wantKind: KindString, wantStr: "olleh"},
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
			case KindBytes:
				if got := d.BytesValue(); string(got) != string(tc.wantByte) {
					t.Errorf("%q: got % x, want % x", tc.sql, got, tc.wantByte)
				}
			case KindString:
				if got := d.StringValue(); got != tc.wantStr {
					t.Errorf("%q: got %q, want %q", tc.sql, got, tc.wantStr)
				}
			}
		})
	}
}

// TestSplitPartPGSemantics pins split_part()'s PG-faithful edge-case
// behavior: fldnum==0 error, negative field indexing, and the
// empty-delimiter special case (postgres/src/backend/utils/adt/varlena.c:
// 4621-4750, split_part). M0134-0070.
func TestSplitPartPGSemantics(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct {
		sql      string
		wantCode string
		wantMsg  string
		want     string
	}{
		{sql: `select split_part('joeuser@mydatabase','@',0)`,
			wantCode: "22023", wantMsg: "field position must not be zero"},
		{sql: `select split_part('joeuser@mydatabase','@',-1)`, want: "mydatabase"},
		{sql: `select split_part('joeuser@mydatabase','@',1)`, want: "joeuser"},
		{sql: `select split_part('@joeuser@mydatabase@','@',-2)`, want: "mydatabase"},
		{sql: `select split_part('@joeuser@mydatabase@','@',-1)`, want: ""},
		{sql: `select split_part('@joeuser@mydatabase@','@',-4)`, want: ""},
		// empty delimiter: whole string is field 1 / field -1.
		{sql: `select split_part('abc','',1)`, want: "abc"},
		{sql: `select split_part('abc','',-1)`, want: "abc"},
		{sql: `select split_part('abc','',2)`, want: ""},
		{sql: `select split_part('abc','',-2)`, want: ""},
		// empty input string with empty delimiter.
		{sql: `select split_part('',',',1)`, want: ""},
		{sql: `select split_part('',',',-1)`, want: ""},
		// out-of-range positive index.
		{sql: `select split_part('a,b,c',',',5)`, want: ""},
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
