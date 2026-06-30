package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestExpandColumnPrivs verifies the column-GRANT privilege keyword expansion:
// ALL / ALL PRIVILEGES fan out to the column-grantable subset (in canonical
// attacl bit order), a single recognised keyword passes through, and an
// unrecognised keyword yields nil (a no-op grant). M0119-0004-ACLHEAP.
func TestExpandColumnPrivs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"SELECT", []string{"SELECT"}},
		{"insert", []string{"INSERT"}}, // case-folded
		{"UPDATE", []string{"UPDATE"}},
		{"REFERENCES", []string{"REFERENCES"}},
		{"ALL", attrACLAllPrivs},
		{"ALL PRIVILEGES", attrACLAllPrivs},
		{"DELETE", nil}, // not column-grantable
		{"", nil},
	}
	for _, tc := range cases {
		got := expandColumnPrivs(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("expandColumnPrivs(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("expandColumnPrivs(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// TestColumnAttNum verifies the column-name → 1-based attnum resolution
// (Ordinal+1, the same mapping buildUserPGAttributeRow writes), the
// case-insensitive match, the missing-column 0 sentinel, and the inverse
// columnByAttNum lookup. M0119-0004-ACLHEAP.
func TestColumnAttNum(t *testing.T) {
	tbl := &catalog.Table{
		Name: "t",
		Columns: []catalog.Column{
			{Name: "cola", Ordinal: 0},
			{Name: "colb", Ordinal: 1},
			{Name: "ColC", Ordinal: 2},
		},
	}
	if got := columnAttNum(tbl, "cola"); got != 1 {
		t.Errorf("columnAttNum(cola) = %d, want 1", got)
	}
	if got := columnAttNum(tbl, "colb"); got != 2 {
		t.Errorf("columnAttNum(colb) = %d, want 2", got)
	}
	// case-insensitive match
	if got := columnAttNum(tbl, "colc"); got != 3 {
		t.Errorf("columnAttNum(colc) = %d, want 3", got)
	}
	// missing column
	if got := columnAttNum(tbl, "nope"); got != 0 {
		t.Errorf("columnAttNum(nope) = %d, want 0", got)
	}
	// inverse lookup
	col, ok := columnByAttNum(tbl, 2)
	if !ok || col.Name != "colb" {
		t.Errorf("columnByAttNum(2) = (%+v, %v), want colb,true", col, ok)
	}
	if _, ok := columnByAttNum(tbl, 9); ok {
		t.Errorf("columnByAttNum(9) = found, want not found")
	}
}
