package executor

import (
	"testing"
)

// TestSplitRowElements verifies the row-literal parser used for row comparison.
func TestSplitRowElements(t *testing.T) {
	tests := []struct {
		s    string
		want []string
	}{
		{"(100,99.097)", []string{"100", "99.097"}},
		{"(324.78,42)", []string{"324.78", "42"}},
		{"(0,0.09561)", []string{"0", "0.09561"}},
		{"(a,b,c)", []string{"a", "b", "c"}},
		{"()", []string{}},
		{"", nil},
		{"notarow", nil},
		{"(a,(b,c))", []string{"a", "(b,c)"}},
	}
	for _, tc := range tests {
		got := splitRowElements(tc.s)
		if len(got) != len(tc.want) {
			t.Errorf("splitRowElements(%q): got %v, want %v", tc.s, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitRowElements(%q)[%d]: got %q, want %q", tc.s, i, got[i], tc.want[i])
			}
		}
	}
}

// TestCompareRowStrings verifies that row composite-type comparison works
// numerically (not lexicographically) so max(row(a,b)) returns the correct row.
func TestCompareRowStrings(t *testing.T) {
	// max(row(a,b)) from aggtest data: should be (100,99.097) > (56,7.8)
	cmp := compareRowStrings("(100,99.097)", "(56,7.8)")
	if cmp <= 0 {
		t.Errorf("(100,99.097) should be > (56,7.8), got cmp=%d", cmp)
	}

	// min(row(a,b)) should be (0,0.09561)
	cmp = compareRowStrings("(0,0.09561)", "(56,7.8)")
	if cmp >= 0 {
		t.Errorf("(0,0.09561) should be < (56,7.8), got cmp=%d", cmp)
	}

	// max(row(b,a)): (324.78,42) > (99.097,100) because 324.78 > 99.097
	cmp = compareRowStrings("(324.78,42)", "(99.097,100)")
	if cmp <= 0 {
		t.Errorf("(324.78,42) should be > (99.097,100), got cmp=%d", cmp)
	}

	// Equal rows
	cmp = compareRowStrings("(1,2)", "(1,2)")
	if cmp != 0 {
		t.Errorf("(1,2) == (1,2) should give 0, got %d", cmp)
	}

	// String comparison fallback for non-numeric elements
	cmp = compareRowStrings("(abc,1)", "(abd,1)")
	if cmp >= 0 {
		t.Errorf("(abc,...) should be < (abd,...), got cmp=%d", cmp)
	}
}

// TestIsFloat4TypeName verifies the type name helper.
func TestIsFloat4TypeName(t *testing.T) {
	yes := []string{"float4", "real", "FLOAT4", "Real"}
	no := []string{"float8", "double precision", "numeric", "int4", "text", "float"}
	for _, name := range yes {
		if !isFloat4TypeName(name) {
			t.Errorf("isFloat4TypeName(%q) = false, want true", name)
		}
	}
	for _, name := range no {
		if isFloat4TypeName(name) {
			t.Errorf("isFloat4TypeName(%q) = true, want false", name)
		}
	}
}
