package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/utils/mmgr"
)

// TestStringCompareInPlaceMatchesInterpreted pins M0122-0012 slice 89: a
// character-string comparison of two operands that live in place (a row's
// column, an outer row's column, a parameter, a constant) compares them
// there when both are plain strings — evalFastBinaryApply's plain-string
// branch without the operand copies. Every result must be the interpreted
// twin's: bpchar against a literal (trailing blanks dropped on both sides),
// text against a literal (kept), bpchar against text, an outer bpchar column,
// and the strings the plain test turns away (a '/' — pg_lsn shape — a leading
// '(' or '{', a uuid's length), NULLs and empty strings, for every
// comparison operator.
func TestStringCompareInPlaceMatchesInterpreted(t *testing.T) {
	old := seqScanDeformPoison
	seqScanDeformPoison = false
	t.Cleanup(func() { seqScanDeformPoison = old })

	bp := catalog.Type{Name: "bpchar", Args: []int64{9}}
	text := catalog.Type{Name: "text"}
	b := &optimizer.ColumnRef{Index: 0, Name: "b", Type: bp}
	x := &optimizer.ColumnRef{Index: 1, Name: "x", Type: text}
	ob := &optimizer.OuterColumnRef{Level: 1, Index: 0, Name: "ob", Type: bp}
	lit := func(s string) optimizer.Expr { return &optimizer.StringConst{Value: s} }
	var exprs []optimizer.Expr
	for _, op := range []parser.OpCode{parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe} {
		for _, pair := range [][2]optimizer.Expr{
			{b, lit("Sunday")}, {lit("Sunday  "), b}, {x, lit("Sunday ")}, {x, lit("Sunday")},
			{b, x}, {x, b}, {b, ob}, {ob, x}, {b, lit("")}, {x, lit("1/2")}, {b, lit("(a")},
		} {
			exprs = append(exprs, &optimizer.BinaryOp{Op: op, Left: pair[0], Right: pair[1], ResultType: "bool"})
		}
	}
	rows := []Row{
		{NewStringDatum("Sunday   "), NewStringDatum("Sunday")},
		{NewStringDatum("Sunday"), NewStringDatum("Sunday ")},
		{NewStringDatum("Monday   "), NewStringDatum("monday")},
		{NewStringDatum("         "), NewStringDatum("")},
		{NewStringDatum("1/2"), NewStringDatum("1/2")},
		{NewStringDatum("(a"), NewStringDatum("{b}")},
		{NewStringDatum("0e5c3b8e-6b4b-4c8a-9f43-1f5b2a7c9d10"), NewStringDatum("0e5c3b8e6b4b4c8a9f431f5b2a7c9d10")},
		{NullDatum, NewStringDatum("x")},
		{NewStringDatum("Tue"), NullDatum},
	}
	outers := []Row{{NewStringDatum("Sunday   ")}, {NewStringDatum("Tue")}, {NullDatum}}
	fired := false
	for oi, outer := range outers {
		ctx := &Context{OuterRows: []Row{outer}}
		for ei, e := range exprs {
			var slab exprTreeSlab
			idx := slab.buildExprCtx(e, ctx)
			if slab[idx].payload[16]&4 != 0 {
				fired = true
			}
			for r := range rows {
				slot := &MaterializedSlot{row: rows[r]}
				want, wantErr := evalExprSlot(e, slot, ctx)
				got, gotErr := evalFastExpr(slab, idx, slot, ctx)
				if !sameResult(got, gotErr, want, wantErr) {
					t.Fatalf("outer %d expr %d (%v) row %d: compiled %q (%v), interpreted %q (%v)", oi, ei, e, r, got.Format(), gotErr, want.Format(), wantErr)
				}
			}
		}
	}
	if !fired {
		t.Fatal("no comparison compiled as a character-string comparison; the in-place lane is never reached")
	}
}

// TestPlainCompareStringMatchesIndexByteForm pins slice 90's byte-loop
// plainCompareString to the strings.IndexByte form it replaced, on every
// edge the test has: a '/' at each position, a leading '(' or '{' (and one
// that is not leading), lengths around the 32-byte uuid cut, and the empty
// string.
func TestPlainCompareStringMatchesIndexByteForm(t *testing.T) {
	ref := func(s string) bool {
		if len(s) >= 32 || strings.IndexByte(s, '/') >= 0 {
			return false
		}
		return len(s) == 0 || (s[0] != '(' && s[0] != '{')
	}
	cases := []string{"", "GA", "Sunday   ", "(a", "{b}", "a(", "a{", "/", " /", "( /", strings.Repeat("x", 31),
		strings.Repeat("x", 32), strings.Repeat("x", 33), strings.Repeat("x", 30) + "/"}
	base := "abcdefghij"
	for i := 0; i <= len(base); i++ {
		cases = append(cases, base[:i]+"/"+base[i:])
	}
	for _, s := range cases {
		if got, want := plainCompareString(s), ref(s); got != want {
			t.Fatalf("plainCompareString(%q) = %v, the IndexByte form says %v", s, got, want)
		}
	}
}

// TestStringValueInlineMatchesStringValue pins slice 90's inlinable string
// read: an owned string, an empty and a nil buffer, and an arena-backed
// string all read as StringValue reads them.
func TestStringValueInlineMatchesStringValue(t *testing.T) {
	arena := mmgr.Acquire(nil, mmgr.KindStmt)
	defer arena.Release()
	for name, d := range map[string]Datum{
		"owned": NewStringDatum("Sunday   "),
		"empty": NewStringDatum(""),
		"nil":   {Kind: KindString},
		"arena": cut1ArenaString(t, arena, "GA"),
	} {
		if got, want := stringValueInline(&d), d.StringValue(); got != want {
			t.Fatalf("%s: stringValueInline = %q, StringValue = %q", name, got, want)
		}
	}
}
