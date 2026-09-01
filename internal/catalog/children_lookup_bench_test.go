package catalog

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// BenchmarkPartitionChildren measures review/260831 CA-1/CA-2: resolving a
// parent's children used to scan the whole namespace once per child
// (O(children x tables)); it is one pass now. The namespace is padded with
// unrelated tables, which is what a real database looks like — goopg's own
// bootstrap registers several hundred catalog and information_schema tables
// before any user table exists.
func BenchmarkPartitionChildren(b *testing.B) {
	for _, nchildren := range []int{4, 64} {
		b.Run(fmt.Sprintf("children=%d", nchildren), func(b *testing.B) {
			c := NewInMemory()
			parent, err := c.CreateTable(parser.ObjectName{Name: "parent"}, []Column{{Name: "id"}})
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < nchildren; i++ {
				child, err := c.CreateTable(parser.ObjectName{Name: fmt.Sprintf("part%03d", i)}, []Column{{Name: "id"}})
				if err != nil {
					b.Fatal(err)
				}
				c.RegisterPartitionChild(parent.OID, child.OID)
			}
			for i := 0; i < 300; i++ {
				if _, err := c.CreateTable(parser.ObjectName{Name: fmt.Sprintf("other%03d", i)}, []Column{{Name: "id"}}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				if got := c.PartitionChildren(parent.OID); len(got) != nchildren {
					b.Fatalf("PartitionChildren returned %d children, want %d", len(got), nchildren)
				}
			}
		})
	}
}

// TestPartitionChildrenOrder pins that the one-pass resolution still returns
// the children in registration order, which callers (partition routing, FK
// cascade) rely on for deterministic behaviour.
func TestPartitionChildrenOrder(t *testing.T) {
	c := NewInMemory()
	parent, err := c.CreateTable(parser.ObjectName{Name: "p"}, []Column{{Name: "id"}})
	if err != nil {
		t.Fatal(err)
	}
	var want []uint32
	for i := 0; i < 8; i++ {
		child, err := c.CreateTable(parser.ObjectName{Name: fmt.Sprintf("p%d", i)}, []Column{{Name: "id"}})
		if err != nil {
			t.Fatal(err)
		}
		c.RegisterPartitionChild(parent.OID, child.OID)
		want = append(want, child.OID)
	}
	// An OID registered as a child but with no table must simply be skipped.
	c.RegisterPartitionChild(parent.OID, 999999)
	got := c.PartitionChildren(parent.OID)
	if len(got) != len(want) {
		t.Fatalf("got %d children, want %d", len(got), len(want))
	}
	for i, tbl := range got {
		if tbl.OID != want[i] {
			t.Errorf("child %d: OID %d, want %d (registration order)", i, tbl.OID, want[i])
		}
	}
}
