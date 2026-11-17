package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestSchemaAndNextObjectGetDistinctOIDs pins that a schema's OID is not
// reused by the next object minted from the shared counter: RegisterSchema
// and CreateTablespace pre-incremented nextOID, leaving it naming the OID
// they had just used, so the first table created afterwards (and the
// default-ACL entry, ...) got the same OID and shared its ACL entry.
func TestSchemaAndNextObjectGetDistinctOIDs(t *testing.T) {
	c := NewInMemory()
	c.RegisterSchema("sx")
	nsp := c.SchemaOID("sx")
	tbl, err := c.CreateTable(parser.ObjectName{Schema: "sx", Name: "t1"}, []Column{{Name: "a", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	if nsp == 0 || tbl.OID == nsp {
		t.Fatalf("schema oid %d, table oid %d: must differ", nsp, tbl.OID)
	}
	ts, err := c.CreateTablespace("tsx", "postgres", "")
	if err != nil {
		t.Fatal(err)
	}
	if next := c.DefaultACLOID(10, 0, DefaultACLObjTypeRelation); next == ts {
		t.Fatalf("tablespace oid %d reused by the next allocation", ts)
	}
}
