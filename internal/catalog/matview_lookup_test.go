package catalog

import (
    "fmt"
    "testing"
    "github.com/goopg/goopg/internal/parser"
)

func TestMatviewPgClassLookup(t *testing.T) {
    c := NewInMemory()
    
    // Create a regular table
    tblCols := []Column{
        {Name: "id", Type: Type{Name: "int4"}, Ordinal: 0},
        {Name: "type", Type: Type{Name: "text"}, Ordinal: 1},
    }
    tbl, err := c.CreateTable(parser.ObjectName{Name: "mvtest_t"}, tblCols)
    if err != nil {
        t.Fatal(err)
    }
    t.Logf("Created table mvtest_t with OID %d", tbl.OID)
    
    // Create a matview
    mvCols := []Column{
        {Name: "type", Type: Type{Name: "text"}, Ordinal: 0},
        {Name: "totamt", Type: Type{Name: "numeric"}, Ordinal: 1},
    }
    mv, err := c.CreateTable(parser.ObjectName{Name: "mvtest_tm"}, mvCols)
    if err != nil {
        t.Fatal(err)
    }
    mv.IsMatView = true
    mv.IsPopulated = false
    t.Logf("Created matview mvtest_tm with OID %d", mv.OID)
    
    // Try to look up the matview
    found, ok := c.LookupTable(parser.ObjectName{Name: "mvtest_tm"})
    if !ok || found == nil {
        t.Fatal("LookupTable could not find mvtest_tm!")
    }
    t.Logf("LookupTable found mvtest_tm with OID %d, IsMatView=%v", found.OID, found.IsMatView)
    
    // Check pg_class rows
    pgClassTbl, ok2 := c.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
    if !ok2 || pgClassTbl == nil {
        t.Fatal("pg_class not found!")
    }
    rows := pgClassTbl.VirtualRows()
    t.Logf("pg_class has %d rows", len(rows))
    found_in_pgclass := false
    for _, r := range rows {
        if len(r) > 1 && r[1] == "mvtest_tm" {
            t.Logf("Found mvtest_tm in pg_class: oid=%s relkind=%s relispopulated=%s", r[0], r[2], r[7])
            found_in_pgclass = true
        }
    }
    if !found_in_pgclass {
        t.Error("mvtest_tm NOT found in pg_class VirtualRows!")
    }
    
    // Check that OID lookup would work
    oid := found.OID
    t.Logf("Checking if mvtest_tm OID %d appears in pg_class", oid)
    for _, r := range rows {
        if len(r) > 0 && r[0] == fmt.Sprintf("%d", int(oid)) {
            t.Logf("Found row with matching OID: relname=%s", r[1])
        }
    }
}
