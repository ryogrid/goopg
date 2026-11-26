package catalog

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPublicationSchemaTables covers GetSchemaPublicationRelations with
// PUBLICATION_PART_LEAF and the partition-ancestor schemas pgoutput checks
// (M0149-0053a, subscription/t/025_rep_changes_for_schema.pl): a
// partitioned table in the schema contributes its leaf partitions — even
// ones in another schema — instead of itself, and a table moved out of the
// schema by SET SCHEMA leaves the list (M0149-0053b).
func TestPublicationSchemaTables(t *testing.T) {
	c := NewInMemory()
	c.RegisterSchema("sch1")
	mk := func(schema, name string) *Table {
		t.Helper()
		tbl, err := c.CreateTable(parser.ObjectName{Schema: schema, Name: name}, []Column{{Name: "a", Type: Type{Name: "int4"}}})
		if err != nil {
			t.Fatal(err)
		}
		return tbl
	}
	tab1 := mk("sch1", "tab1")
	parent := mk("sch1", "tab1_parent")
	parent.PartitionKey, parent.PartitionMethod = []string{"a"}, "LIST"
	child1 := mk("public", "tab1_child1")
	child1.PartitionParentOID = parent.OID
	c.RegisterPartitionChild(parent.OID, child1.OID)
	mk("public", "other")
	if tab1.OID < FirstNormalObjectId {
		t.Skipf("CreateTable allocated a system OID (%d)", tab1.OID)
	}

	names := func(ts []*Table) []string {
		var out []string
		for _, x := range ts {
			out = append(out, x.Schema+"."+x.Name)
		}
		return out
	}
	got := names(PublicationSchemaTables(c, []string{"SCH1"}, DefaultDBOid))
	want := []string{"public.tab1_child1", "sch1.tab1"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("PublicationSchemaTables = %v, want %v", got, want)
	}
	if anc := PartitionAncestorSchemas(c, child1); len(anc) != 1 || anc[0] != "sch1" {
		t.Fatalf("PartitionAncestorSchemas(child1) = %v, want [sch1]", anc)
	}

	if err := c.SetTableSchema(tab1, "public", DefaultDBOid); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.LookupTable(parser.ObjectName{Schema: "public", Name: "tab1"}); !ok {
		t.Fatal("public.tab1 does not resolve after SET SCHEMA")
	}
	if _, ok := c.LookupTable(parser.ObjectName{Schema: "sch1", Name: "tab1"}); ok {
		t.Fatal("sch1.tab1 still resolves after SET SCHEMA")
	}
	if got := names(PublicationSchemaTables(c, []string{"sch1"}, DefaultDBOid)); len(got) != 1 || got[0] != "public.tab1_child1" {
		t.Fatalf("after SET SCHEMA: %v, want [public.tab1_child1]", got)
	}
	mk("sch1", "other")
	other, _ := c.LookupTable(parser.ObjectName{Schema: "sch1", Name: "other"})
	if err := c.SetTableSchema(other, "public", DefaultDBOid); !errors.Is(err, ErrRelationExistsInSchema) {
		t.Fatalf("SET SCHEMA onto an existing name: %v, want ErrRelationExistsInSchema", err)
	}
}
