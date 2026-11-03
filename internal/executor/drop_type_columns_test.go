package executor

import (
	"errors"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestDropTypeColumnDependents pins M0148-0001j2's DROP TYPE / DROP DOMAIN
// edges (PG 18.3 output): table columns of the type and of its array type,
// composite-type attributes, and domains over it (recursing to the domain's
// own columns) are reported by RESTRICT in findDependentObjects order, and
// CASCADE drops the COLUMNS (and the domain), never the tables.
func TestDropTypeColumnDependents(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TYPE ct AS (a int, b text)",
		"CREATE TABLE tc (x int, y ct, z ct[])",
		"CREATE TYPE ct2 AS (p ct, q int)",
		"CREATE DOMAIN dct AS ct",
		"CREATE TABLE tc2 (d dct, k int)",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	err := runDDL(t, ctx, "DROP TYPE ct")
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != "2BP01" {
		t.Fatalf("DROP TYPE ct (RESTRICT): err = %v, want 2BP01", err)
	}
	wantDetail := strings.Join([]string{
		"column z of table tc depends on type ct[]",
		"column y of table tc depends on type ct",
		"column p of composite type ct2 depends on type ct",
		"type dct depends on type ct",
		"column d of table tc2 depends on type dct",
	}, "\n")
	if ee.Detail != wantDetail {
		t.Errorf("DETAIL:\n%s\nwant (PG 18.3):\n%s", ee.Detail, wantDetail)
	}
	ctx.TakeNoticesWithDetail()
	if err := runDDL(t, ctx, "DROP TYPE ct CASCADE"); err != nil {
		t.Fatalf("DROP TYPE ct CASCADE: %v", err)
	}
	nd := ctx.TakeNoticesWithDetail()
	wantCascade := strings.Join([]string{
		"drop cascades to column z of table tc",
		"drop cascades to column y of table tc",
		"drop cascades to column p of composite type ct2",
		"drop cascades to type dct",
		"drop cascades to column d of table tc2",
	}, "\n")
	if len(nd) != 1 || nd[0].Message != "drop cascades to 5 other objects" || nd[0].Detail != wantCascade {
		t.Fatalf("notices %+v, want (PG 18.3) the 5-object cascade:\n%s", nd, wantCascade)
	}
	im := ctx.Catalog.(*catalog.InMemory)
	cols := func(name string) string {
		tbl, ok := im.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			return "<dropped>"
		}
		var out []string
		for _, c := range tbl.Columns {
			if !c.Dropped {
				out = append(out, c.Name)
			}
		}
		return strings.Join(out, ",")
	}
	if got := cols("tc"); got != "x" {
		t.Errorf("tc columns after cascade = %q, want x", got)
	}
	if got := cols("tc2"); got != "k" {
		t.Errorf("tc2 columns after cascade = %q, want k", got)
	}
	if f := im.LookupCompositeTypeFields("ct2"); len(f) != 1 || f[0].Name != "q" {
		t.Errorf("ct2 fields after cascade = %+v, want [q]", f)
	}
	if _, ok := im.LookupDomain("dct"); ok {
		t.Errorf("domain dct survived the cascade")
	}

	// DROP DOMAIN: RESTRICT names the column; CASCADE drops the column and
	// keeps the table (goopg used to drop the whole table).
	for _, q := range []string{
		"CREATE DOMAIN posint AS int CHECK (VALUE > 0)",
		"CREATE TABLE td (id int, v posint)",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	err = runDDL(t, ctx, "DROP DOMAIN posint")
	if !errors.As(err, &ee) || ee.Code != "2BP01" || ee.Detail != "column v of table td depends on type posint" {
		t.Fatalf("DROP DOMAIN posint (RESTRICT): err = %v detail %q", err, ee.Detail)
	}
	ctx.TakeNotices()
	if err := runDDL(t, ctx, "DROP DOMAIN posint CASCADE"); err != nil {
		t.Fatalf("DROP DOMAIN posint CASCADE: %v", err)
	}
	if n := ctx.TakeNotices(); len(n) != 1 || n[0] != "drop cascades to column v of table td" {
		t.Errorf("notices %q, want (PG 18.3) drop cascades to column v of table td", n)
	}
	if got := cols("td"); got != "id" {
		t.Errorf("td columns after DROP DOMAIN CASCADE = %q, want id", got)
	}
}
