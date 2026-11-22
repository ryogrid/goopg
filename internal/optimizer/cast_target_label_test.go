package optimizer

import "testing"

// TestCastTargetLabelArrayDims pins M0149-0001h1a85b against PG 18.3:
// FigureColnameInternal names a cast by its type name's last component,
// which never carries array bounds, so `'{{1}}'::int[][]` is int4 like
// `'{1}'::int[]`.
func TestCastTargetLabelArrayDims(t *testing.T) {
	for in, want := range map[string]string{
		"int[]":     "int4",
		"int[][]":   "int4",
		"int[][][]": "int4",
		"text[][]":  "text",
		"rainbow[]": "rainbow",
		"integer":   "int4",
	} {
		if got := castTargetLabel(in); got != want {
			t.Errorf("castTargetLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
