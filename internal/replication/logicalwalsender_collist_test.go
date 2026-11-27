package replication

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
)

// TestPublicationFilterColumnListConflict pins pgoutput_column_list_init:
// publications that give a relation different column lists end the stream
// with "cannot use different column lists for table ... in different
// publications"; a publication without a list counts as all the published
// columns, so it agrees with a list naming every column. M0149-0060.
func TestPublicationFilterColumnListConflict(t *testing.T) {
	ps := catalog.NewPubSub()
	for _, name := range []string{"p_ab", "p_ac", "p_abc", "p_all"} {
		if _, err := ps.CreatePublication(name, []string{"public.t"}, catalog.DefaultPublicationOptions()); err != nil {
			t.Fatal(err)
		}
	}
	for name, cols := range map[string][]string{"p_ab": {"a", "b"}, "p_ac": {"a", "c"}, "p_abc": {"a", "b", "c"}} {
		if err := ps.SetPublicationColumnLists(name, map[string][]string{"public.t": cols}); err != nil {
			t.Fatal(err)
		}
	}
	rel := &xlog.RelationDef{Schema: "public", Name: "t", Columns: []xlog.ColumnDef{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
	for _, tc := range []struct {
		pubs     []string
		conflict bool
	}{
		{[]string{"p_ab"}, false},
		{[]string{"p_ab", "p_ac"}, true},
		{[]string{"p_ab", "p_all"}, true},
		{[]string{"p_abc", "p_all"}, false},
	} {
		err := buildPublicationFilter(ps, tc.pubs).CheckColumnLists(rel)
		var cle *ColumnListConflictError
		if got := errors.As(err, &cle); got != tc.conflict {
			t.Errorf("%v: err = %v, want conflict %v", tc.pubs, err, tc.conflict)
		}
		if tc.conflict && err != nil && err.Error() != `cannot use different column lists for table "public.t" in different publications` {
			t.Errorf("%v: message %q", tc.pubs, err.Error())
		}
	}
}
