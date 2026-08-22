package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestEvalOverlay pins PostgreSQL-compatible overlay() semantics
// (postgres/src/backend/utils/adt/varlena.c text_overlay/bytea_overlay),
// matching the strings.sql T312 fixture lines (~399-406, 900-902). M0134-0070.
func TestEvalOverlay(t *testing.T) {
	cases := []struct {
		name string
		args []optimizer.Expr
		want Datum
	}{
		{
			name: "3-arg text: abc45f",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "abcdef"},
				&optimizer.StringConst{Value: "45"},
				&optimizer.IntegerConst{Value: 4},
			},
			want: NewStringDatum("abc45f"),
		},
		{
			name: "3-arg text: yabadaba",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "yabadoo"},
				&optimizer.StringConst{Value: "daba"},
				&optimizer.IntegerConst{Value: 5},
			},
			want: NewStringDatum("yabadaba"),
		},
		{
			name: "4-arg text FOR 0: pure insertion, yabadabadoo",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "yabadoo"},
				&optimizer.StringConst{Value: "daba"},
				&optimizer.IntegerConst{Value: 5},
				&optimizer.IntegerConst{Value: 0},
			},
			want: NewStringDatum("yabadabadoo"),
		},
		{
			name: "4-arg text: bubba",
			args: []optimizer.Expr{
				&optimizer.StringConst{Value: "babosa"},
				&optimizer.StringConst{Value: "ubb"},
				&optimizer.IntegerConst{Value: 2},
				&optimizer.IntegerConst{Value: 4},
			},
			want: NewStringDatum("bubba"),
		},
		{
			name: "NULL src propagates",
			args: []optimizer.Expr{
				&optimizer.NullConst{},
				&optimizer.StringConst{Value: "45"},
				&optimizer.IntegerConst{Value: 4},
			},
			want: NullDatum,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &optimizer.FuncCall{Name: "overlay", Args: c.args}
			got, err := evalFuncCall(fc, nil, &Context{})
			if err != nil {
				t.Fatalf("evalFuncCall: %v", err)
			}
			if got.Kind != c.want.Kind || got.IsNull() != c.want.IsNull() {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			if !c.want.IsNull() && got.StringValue() != c.want.StringValue() {
				t.Fatalf("got %q, want %q", got.StringValue(), c.want.StringValue())
			}
		})
	}
}

// TestEvalOverlayBytea covers the bytea sibling
// (postgres/src/backend/utils/adt/varlena.c bytea_overlay), which must
// return a bytea-kind Datum, not text. Driven end-to-end through the parser's
// OVERLAY PLACING/FROM/FOR grammar (not evalFuncCall directly) since a bytea
// literal is produced by a `::bytea` cast, not a dedicated optimizer.Expr
// constant node. PG oracle wanted values captured from strings.sql:900,902.
func TestEvalOverlayBytea(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// Expected bytes computed directly from the byte-indexed algorithm (see
	// evalOverlay): part1 = s[:sp-1], part2 = s[sp+sl-1:] (clamped).
	tests := []struct {
		name string
		sql  string
		want []byte
	}{
		{
			name: "3-arg bytea",
			sql:  `select overlay(E'Th\\000omas'::bytea placing E'Th\\001omas'::bytea from 2)`,
			want: []byte("TTh\x01omas"),
		},
		{
			name: "4-arg bytea",
			sql:  `select overlay(E'Th\\000omas'::bytea placing E'\\002\\003'::bytea from 5 for 3)`,
			want: []byte("Th\x00o\x02\x03"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, colType := byteaExprResult(t, ctx, tc.sql)
			if d.Kind != KindBytes {
				t.Fatalf("Kind=%v, want KindBytes", d.Kind)
			}
			if colType != "bytea" {
				t.Errorf("colType=%q, want bytea", colType)
			}
			if string(d.BytesValue()) != string(tc.want) {
				t.Errorf("got %q, want %q", d.BytesValue(), tc.want)
			}
		})
	}
}

// TestEvalOverlayNegativeStartErrors pins the sp<=0 → 22011 error path
// (varlena.c text_overlay: "negative substring length not allowed" — PG's
// real wording for the sp<=0 check, reusing evalSubstr's error constant).
func TestEvalOverlayNegativeStartErrors(t *testing.T) {
	fc := &optimizer.FuncCall{
		Name: "overlay",
		Args: []optimizer.Expr{
			&optimizer.StringConst{Value: "abc"},
			&optimizer.StringConst{Value: "x"},
			&optimizer.IntegerConst{Value: 0},
		},
	}
	_, err := evalFuncCall(fc, nil, &Context{})
	if err == nil {
		t.Fatal("expected sp<=0 error, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err=%T, want *ExecError", err)
	}
	if ee.Code != "22011" {
		t.Errorf("Code=%q, want 22011", ee.Code)
	}
	if ee.Message != "negative substring length not allowed" {
		t.Errorf("Message=%q, want exact PG wording", ee.Message)
	}
}

// TestParseOverlaySQLNegativeStartErrorEndToEnd pins the sp<=0 error through
// the full parser→executor pipeline for the SQL-standard OVERLAY syntax
// (not just the desugared FuncCall).
func TestParseOverlaySQLNegativeStartErrorEndToEnd(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	ee := byteaExprErr(t, ctx, `select overlay('abc' placing 'x' from 0)`)
	if ee.Code != "22011" {
		t.Errorf("Code=%q, want 22011", ee.Code)
	}
}
