package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// RelAllVisibleBlocks must key a DBOid-less table under THIS catalog's
// database — the one VACUUM keyed the VM bits by and the one the pg_class
// view reads — not DefaultDBOid. On a server catalog (SetDBOID(5)) the
// package-level RelAllVisible asked database 1, read 0, and priced every
// index-only scan with all its heap fetches (M0145-0029 slice 5).
func TestRelAllVisibleBlocksUsesCatalogDatabase(t *testing.T) {
	c := NewInMemory()
	c.SetDBOID(5)
	tbl, err := c.CreateTable(parser.ObjectName{Name: "vis_t"}, []Column{{Name: "a", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	prev := RelAllVisibleFunc
	t.Cleanup(func() { RelAllVisibleFunc = prev })
	var askedDB uint32
	RelAllVisibleFunc = func(dbOid, relOid uint32) int32 {
		askedDB = dbOid
		if dbOid == 5 {
			return 9
		}
		return 0
	}
	if got := c.RelAllVisibleBlocks(tbl); got != 9 || askedDB != 5 {
		t.Fatalf("RelAllVisibleBlocks = %d (asked db %d), want 9 from db 5", got, askedDB)
	}
	if got := c.relAllVisibleCell(tbl); got != "9" {
		t.Fatalf("pg_class cell = %q, want the same 9 the planner reads", got)
	}
}
