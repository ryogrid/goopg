package replication

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
)

// TestPublicationFilterTablesInSchema covers pgoutput's schema publication
// membership (M0149-0053a, 025_rep_changes_for_schema.pl): a relation in
// the schema is published whole (no column list), a partition whose
// ancestor is in the schema is published, a table elsewhere is not, and
// dropping the schema from the publication stops the stream.
func TestPublicationFilterTablesInSchema(t *testing.T) {
	ps := catalog.NewPubSub()
	if _, err := ps.CreatePublication("tap_pub_schema", []string{"sch1.tab1"}, catalog.DefaultPublicationOptions()); err != nil {
		t.Fatal(err)
	}
	if err := ps.SetPublicationColumnLists("tap_pub_schema", map[string][]string{"sch1.tab1": {"a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.SetPublicationSchemas("tap_pub_schema", []string{"sch1"}); err != nil {
		t.Fatal(err)
	}
	f := newLivePublicationFilter(ps, []string{"tap_pub_schema"})
	tab1 := &xlog.RelationDef{Schema: "sch1", Name: "tab1"}
	tab2 := &xlog.RelationDef{Schema: "sch1", Name: "tab2"}
	child := &xlog.RelationDef{Schema: "public", Name: "tab1_child1", AncestorSchemas: []string{"sch1"}}
	other := &xlog.RelationDef{Schema: "public", Name: "tab3"}
	for _, c := range []struct {
		rel  *xlog.RelationDef
		want bool
	}{{tab1, true}, {tab2, true}, {child, true}, {other, false}} {
		if got := f.Allows(c.rel, xlog.ChangeInsert); got != c.want {
			t.Errorf("Allows(%s.%s) = %v, want %v", c.rel.Schema, c.rel.Name, got, c.want)
		}
	}
	if cols, ok := f.ColumnList(tab1); ok {
		t.Errorf("ColumnList(tab1) = %v: a schema publication publishes the table whole", cols)
	}
	if _, err := ps.SetPublicationSchemas("tap_pub_schema", nil); err != nil {
		t.Fatal(err)
	}
	if f.Allows(tab2, xlog.ChangeInsert) || f.Allows(child, xlog.ChangeInsert) {
		t.Error("still published after DROP TABLES IN SCHEMA")
	}
	if !f.Allows(tab1, xlog.ChangeInsert) {
		t.Error("FOR TABLE member lost with the schema")
	}
}
