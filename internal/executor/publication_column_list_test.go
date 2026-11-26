package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPublicationColumnList pins pub_collist_validate: unknown, system,
// virtual generated and repeated columns are refused with PG's codes; the
// accepted list comes back in table order (prattrs is sorted).
func TestPublicationColumnList(t *testing.T) {
	tbl := &catalog.Table{Name: "t", Columns: []catalog.Column{
		{Name: "a", Ordinal: 0},
		{Name: "b", Ordinal: 1, GeneratedExpr: "a", GeneratedAlways: true},
		{Name: "v", Ordinal: 2, GeneratedExpr: "a", GeneratedAlways: true, GeneratedVirtual: true},
	}}
	got, err := publicationColumnList(tbl, []string{"b", "a"})
	if err != nil || fmt.Sprint(got) != "[a b]" {
		t.Fatalf("list = %v, %v; want [a b]", got, err)
	}
	if n := publicationColumnAttnums(tbl, got); fmt.Sprint(n) != "[1 2]" {
		t.Fatalf("attnums = %v", n)
	}
	for _, tc := range []struct {
		cols []string
		code string
	}{{[]string{"nope"}, "42703"}, {[]string{"xmin"}, "42P10"}, {[]string{"v"}, "42P10"}, {[]string{"a", "a"}, "42710"}} {
		_, err := publicationColumnList(tbl, tc.cols)
		if ee, ok := err.(*ExecError); !ok || ee.Code != tc.code {
			t.Errorf("%v: err %v, want %s", tc.cols, err, tc.code)
		}
	}
	pub := &catalog.Publication{Columns: map[string][]string{"t": {"b"}}}
	if n := publicationPublishedAttnums(pub, tbl); fmt.Sprint(n) != "[2]" {
		t.Fatalf("attrs with a column list = %v, want [2]", n)
	}
}
