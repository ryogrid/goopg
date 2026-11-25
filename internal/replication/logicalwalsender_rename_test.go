package replication

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
)

// TestLivePublicationFilterFollowsRename pins pgoutput's reload of its
// publication_names on publication invalidation (M0149-0043b,
// subscription/t/007_ddl.pl): when the publication a stream subscribes to
// is renamed away and an empty one is renamed into its name, the next change
// to the table is no longer streamed; swapping back restores it.
func TestLivePublicationFilterFollowsRename(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreatePublication("pub_for_tab", []string{"public.test1"}, catalog.DefaultPublicationOptions()); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.CreatePublication("pub_empty", nil, catalog.DefaultPublicationOptions()); err != nil {
		t.Fatal(err)
	}
	f := newLivePublicationFilter(ps, []string{"pub_for_tab"})
	test1 := &xlog.RelationDef{Schema: "public", Name: "test1"}
	if !f.Allows(test1, xlog.ChangeInsert) {
		t.Fatal("test1 not streamed before the rename")
	}
	swap := func() {
		t.Helper()
		for _, r := range [][2]string{{"pub_for_tab", "tap_pub_tmp"}, {"pub_empty", "pub_for_tab"}, {"tap_pub_tmp", "pub_empty"}} {
			if _, err := ps.RenamePublication(r[0], r[1]); err != nil {
				t.Fatalf("rename %s -> %s: %v", r[0], r[1], err)
			}
		}
	}
	swap()
	if f.Allows(test1, xlog.ChangeInsert) {
		t.Fatal("test1 still streamed after its publication was renamed away")
	}
	swap()
	if !f.Allows(test1, xlog.ChangeInsert) {
		t.Fatal("test1 not streamed after the swap back")
	}
	if _, err := ps.RenamePublication("pub_empty", "pub_for_tab"); err != catalog.ErrPublicationExists {
		t.Fatalf("rename onto an existing name: %v, want ErrPublicationExists", err)
	}
	if _, err := ps.RenamePublication("missing", "x"); err != catalog.ErrPublicationNotFound {
		t.Fatalf("rename of a missing publication: %v, want ErrPublicationNotFound", err)
	}
}
