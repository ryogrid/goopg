package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestRelationRowtypeOIDReservation pins M0149-0001h1a34 slice 2: a table or
// view takes three OIDs as heap_create_with_catalog does (relation N, array
// type N+1, rowtype N+2); a sequence takes one and has no rowtype.
func TestRelationRowtypeOIDReservation(t *testing.T) {
	c := NewInMemory()
	t1, err := c.CreateTable(parser.ObjectName{Name: "t1"}, []Column{{Name: "a", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	if t1.RowTypeOID != t1.OID+2 {
		t.Fatalf("t1 rowtype = %d, want %d", t1.RowTypeOID, t1.OID+2)
	}
	seq, err := c.CreateSequenceRelation(parser.ObjectName{Name: "s1"}, []Column{{Name: "last_value", Type: Type{Name: "int8"}}})
	if err != nil {
		t.Fatal(err)
	}
	if seq.OID != t1.OID+3 || seq.RowTypeOID != 0 {
		t.Fatalf("sequence oid/rowtype = %d/%d, want %d/0", seq.OID, seq.RowTypeOID, t1.OID+3)
	}
	v, err := c.CreateView(parser.ObjectName{Name: "v1"}, []Column{{Name: "x", Type: Type{Name: "int4"}}}, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if v.OID != seq.OID+1 || v.RowTypeOID != v.OID+2 {
		t.Fatalf("view oid/rowtype = %d/%d, want %d/%d", v.OID, v.RowTypeOID, seq.OID+1, seq.OID+3)
	}
	if got, arr, ok := c.LookupTableByRowTypeOID(t1.RowTypeOID - 1); !ok || !arr || got != t1 {
		t.Fatalf("LookupTableByRowTypeOID(array) = %v %v %v", got, arr, ok)
	}
	if got := relTypeOf(t1); got != t1.RowTypeOID {
		t.Fatalf("pg_class.reltype(t1) = %d, want %d", got, t1.RowTypeOID)
	}
}
