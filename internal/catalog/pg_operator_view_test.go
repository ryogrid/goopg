package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPgOperatorViewServesSeed pins the virtual pg_operator view to every
// pg_operator.dat row (M0149-0001h1a19): it used to hold only CREATE
// OPERATOR rows, so opr_sanity saw an empty catalog. oprcode / oprrest /
// oprjoin are regproc-typed, as in PG.
func TestPgOperatorViewServesSeed(t *testing.T) {
	c := NewInMemory()
	tbl, ok := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_operator"})
	if !ok {
		t.Fatal("pg_operator not registered")
	}
	rows := tbl.VirtualRows()
	if want := len(PGOperatorAllEntries()); len(rows) != want {
		t.Fatalf("rows = %d, want %d", len(rows), want)
	}
	// 551 is int4 + int4 (int4pl, 177).
	var found bool
	for _, r := range rows {
		if r[0] == "551" {
			found = true
			if r[1] != "+" || r[7] != "23" || r[8] != "23" || r[12] != "177" {
				t.Errorf("551 row = %v", r)
			}
		}
	}
	if !found {
		t.Error("operator 551 missing")
	}
	for _, col := range tbl.Columns {
		switch col.Name {
		case "oprcode", "oprrest", "oprjoin":
			if col.Type.Name != "regproc" {
				t.Errorf("%s type = %q, want regproc", col.Name, col.Type.Name)
			}
		}
	}
}
