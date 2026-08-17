package analyzer_test

import (
	"testing"
	
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser/analyzer"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

func TestPubQueryPrattrs(t *testing.T) {
	cat := catalog.NewInMemory()
	pgPub := &catalog.Table{
		Name: "pg_publication", Schema: "pg_catalog",
		Columns: []catalog.Column{
			{Name: "oid", Type: catalog.Type{Name: "oid"}},
			{Name: "pubname", Type: catalog.Type{Name: "text"}},
			{Name: "puballtables", Type: catalog.Type{Name: "bool"}},
		},
		Virtual: true, VirtualRows: func() [][]string { return nil },
	}
	pgPubRel := &catalog.Table{
		Name: "pg_publication_rel", Schema: "pg_catalog",
		Columns: []catalog.Column{
			{Name: "oid", Type: catalog.Type{Name: "oid"}},
			{Name: "prpubid", Type: catalog.Type{Name: "oid"}},
			{Name: "prrelid", Type: catalog.Type{Name: "oid"}},
			{Name: "prqual", Type: catalog.Type{Name: "text"}},
			{Name: "prattrs", Type: catalog.Type{Name: "int2vector"}},
		},
		Virtual: true, VirtualRows: func() [][]string { return nil },
	}
	pgAttr := &catalog.Table{
		Name: "pg_attribute", Schema: "pg_catalog",
		Columns: []catalog.Column{
			{Name: "attrelid", Type: catalog.Type{Name: "oid"}},
			{Name: "attnum", Type: catalog.Type{Name: "int2"}},
			{Name: "attname", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true, VirtualRows: func() [][]string { return nil },
	}
	for _, tbl := range []*catalog.Table{pgPub, pgPubRel, pgAttr} {
		if err := cat.RegisterVirtualTable(tbl); err != nil {
			t.Fatal(err)
		}
	}
	
	query := `SELECT pubname, pg_get_expr(pr.prqual, c.oid),
(CASE WHEN pr.prattrs IS NOT NULL THEN
    (SELECT string_agg(attname, ', ')
     FROM generate_series(0, array_upper(pr.prattrs::int2[], 1)) s,
          pg_catalog.pg_attribute
     WHERE attrelid = pr.prrelid AND attnum = prattrs[s])
ELSE NULL END)
FROM pg_catalog.pg_publication p
JOIN pg_catalog.pg_publication_rel pr ON p.oid = pr.prpubid
JOIN pg_catalog.pg_class c ON c.oid = pr.prrelid
WHERE pr.prrelid = '12345'`
	
	stmts, err := parser.Parse(query)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(stmts) == 0 {
		t.Fatal("no statements")
	}
	
	if err := analyzer.Analyze(stmts[0], cat); err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	
	_, err = optimizer.Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}
	t.Log("Plan succeeded!")
}
