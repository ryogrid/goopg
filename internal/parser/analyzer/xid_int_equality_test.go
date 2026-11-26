package analyzer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestXidIntEquality: PG defines xid = int4 and xid <> int4 (xideqint4,
// xidneqint4) but no ordering operators for the pair. M0149-0051.
func TestXidIntEquality(t *testing.T) {
	xid := catalog.Type{Name: "xid"}
	i8 := catalog.Type{Name: "int8"}
	cases := []struct {
		op          parser.OpCode
		left, right catalog.Type
		want        bool
	}{
		{parser.OpEq, xid, i8, true},
		{parser.OpNe, i8, xid, true},
		{parser.OpEq, xid, catalog.Type{Name: "int4"}, true},
		{parser.OpLt, xid, i8, false},
		{parser.OpEq, xid, catalog.Type{Name: "text"}, false},
		{parser.OpEq, catalog.Type{Name: "xid", IsArray: true}, i8, false},
	}
	for _, c := range cases {
		if got := xidIntEquality(c.op, c.left, c.right); got != c.want {
			t.Errorf("xidIntEquality(%v, %s, %s) = %v, want %v", c.op, c.left.Name, c.right.Name, got, c.want)
		}
	}
}
