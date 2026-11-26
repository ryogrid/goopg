package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPublicationPublishedAttnums pins pg_get_publication_tables' attrs
// without a column list (pg_publication.c): dropped and virtual generated
// columns are never listed; a stored generated column is listed only under
// publish_generated_columns = stored.
func TestPublicationPublishedAttnums(t *testing.T) {
	tbl := &catalog.Table{Columns: []catalog.Column{
		{Name: "a", Ordinal: 0},
		{Name: "gone", Ordinal: 1, Dropped: true},
		{Name: "b", Ordinal: 2, GeneratedExpr: "a * 2", GeneratedAlways: true},
		{Name: "c", Ordinal: 3, GeneratedExpr: "a * 3", GeneratedAlways: true, GeneratedVirtual: true},
		{Name: "d", Ordinal: 4},
	}}
	for _, tc := range []struct {
		gencols byte
		want    string
	}{{'n', "[1 5]"}, {'s', "[1 3 5]"}} {
		got := publicationPublishedAttnums(&catalog.Publication{PublishGeneratedColumns: tc.gencols}, tbl)
		if fmt.Sprint(got) != tc.want {
			t.Errorf("pubgencols %c: attrs %v, want %s", tc.gencols, got, tc.want)
		}
	}
}
