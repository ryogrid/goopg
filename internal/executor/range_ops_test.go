package executor

import "testing"

// TestRangeOperators pins rangetypes.c results measured on PG 18.3.
func TestRangeOperators(t *testing.T) {
	r := func(s string) rangeValue {
		v, err := rangeDeserialize("int4range", s, 0, nil)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		return v
	}
	n := func(s string) rangeValue {
		v, err := rangeDeserialize("numrange", s, 0, nil)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		return v
	}
	if ok, _ := rangeContainsElem(r("[1,5)"), NewIntDatum(5), 0); ok {
		t.Error("[1,5) contains 5")
	}
	if ok, _ := rangeContains(r("[1,5)"), r("empty"), 0); !ok {
		t.Error("every range contains empty")
	}
	if ok, _ := rangeOverlaps(r("[1,5)"), r("[4,8)"), 0); !ok {
		t.Error("[1,5) && [4,8)")
	}
	if ok, _ := boundsAdjacent(n("[1,5)"), n("[1,5)").upper, n("[5,8)").lower, 0, nil); !ok {
		t.Error("numrange [1,5) -|- [5,8)")
	}
	if ok, _ := boundsAdjacent(n("[1,5)"), n("[1,5)").upper, n("[6,8)").lower, 0, nil); ok {
		t.Error("numrange [1,5) -|- [6,8)")
	}
	if c, _ := rangeCmp(r("empty"), r("[1,2)"), 0); c >= 0 {
		t.Error("empty sorts first")
	}
	if c, _ := rangeCmp(r("(,2)"), r("[1,2)"), 0); c >= 0 {
		t.Error("an unbounded lower bound sorts first")
	}
	cases := []struct {
		got  func() (Datum, error)
		want string
	}{
		{func() (Datum, error) { return rangeUnion(r("[1,5)"), r("[4,8)"), 0, nil) }, "[1,8)"},
		{func() (Datum, error) { return rangeIntersect(r("[1,5)"), r("[4,8)"), 0, nil) }, "[4,5)"},
		{func() (Datum, error) { return rangeIntersect(r("[1,5)"), r("[20,30)"), 0, nil) }, "empty"},
		{func() (Datum, error) { return rangeMinus(r("[1,10)"), r("[1,5)"), 0, nil) }, "[5,10)"},
		{func() (Datum, error) { return rangeMinus(r("[1,10)"), r("[5,10)"), 0, nil) }, "[1,5)"},
	}
	for i, c := range cases {
		d, err := c.got()
		if err != nil || d.StringValue() != c.want {
			t.Errorf("case %d = %v %v, want %s", i, d, err, c.want)
		}
	}
	if _, err := rangeMinus(r("[1,9)"), r("[4,6)"), 0, nil); err == nil {
		t.Error("a split difference must raise")
	}
	if _, err := rangeUnion(r("[1,5)"), r("[7,8)"), 0, nil); err == nil {
		t.Error("a gap in a union must raise")
	}
}
