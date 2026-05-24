package planner

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// articlesFDCatalog builds a catalog with an `articles` table whose
// `id` column carries a PRIMARY KEY index and whose `body` column
// carries a UNIQUE (non-primary) index. This mirrors the upstream
// functional_deps regress fixture used to verify that only PRIMARY KEY
// constraints establish a GROUP BY functional dependency.
func articlesFDCatalog(t *testing.T) (catalog.Catalog, *catalog.Table) {
	t.Helper()
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "articles"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "keywords", Type: catalog.Type{Name: "text"}},
		{Name: "body", Type: catalog.Type{Name: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// PRIMARY KEY on id.
	if _, err := c.CreateIndex(parser.ObjectName{Name: "articles_pkey"}, tbl, []string{"id"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	// UNIQUE (non-primary) on body.
	if _, err := c.CreateIndex(parser.ObjectName{Name: "articles_body_key"}, tbl, []string{"body"}, true, "btree", false); err != nil {
		t.Fatal(err)
	}
	return c, tbl
}

// TestGroupByPrimaryKeyEstablishesFunctionalDependency confirms that a
// GROUP BY over the primary-key column lets the other columns pass
// through ungrouped — PostgreSQL's check_functional_grouping accepts
// this. M0097-0003 (functional_deps).
func TestGroupByPrimaryKeyEstablishesFunctionalDependency(t *testing.T) {
	cat, _ := articlesFDCatalog(t)
	stmt := parseOne(t, "SELECT id, keywords, body FROM articles GROUP BY id")
	if _, err := Plan(stmt, cat); err != nil {
		t.Fatalf("GROUP BY primary key should be accepted, got error: %v", err)
	}
}

// TestGroupByUniqueColumnRejected confirms that a GROUP BY over a
// merely UNIQUE column does NOT establish a functional dependency, so
// the ungrouped columns are rejected. PostgreSQL's
// check_functional_grouping recognises only PRIMARY KEY constraints —
// not unique constraints — because a unique constraint can be
// deferrable and (when nullable) admits multiple NULL rows. Before the
// fix, isColumnFunctionallyDetermined also accepted unique indexes,
// silently allowing `GROUP BY body` (and `CREATE VIEW ... GROUP BY
// body`) to succeed. M0097-0003 (functional_deps).
func TestGroupByUniqueColumnRejected(t *testing.T) {
	cat, _ := articlesFDCatalog(t)
	stmt := parseOne(t, "SELECT id, keywords, body FROM articles GROUP BY body")
	_, err := Plan(stmt, cat)
	if err == nil {
		t.Fatal("GROUP BY a unique (non-primary-key) column must be rejected, got no error")
	}
	pe, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("error type = %T, want *PlanError", err)
	}
	if pe.Code != "42803" {
		t.Errorf("error code = %q, want 42803", pe.Code)
	}
	if !strings.Contains(pe.Message, "must appear in the GROUP BY clause") {
		t.Errorf("error message = %q, want grouping-error wording", pe.Message)
	}
}
