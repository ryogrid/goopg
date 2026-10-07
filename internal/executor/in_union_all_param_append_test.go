package executor

// M0146-0049g: `x IN (SELECT … UNION ALL SELECT …)` probes each member.
//
// PG 18.3 pulls the ANY sublink up into a semi join whose inner is the
// UNION ALL subquery, flattened into an appendrel. Its Nested Loop Semi
// Join then probes every member by index under the outer row:
//
//	Nested Loop Semi Join
//	  -> Seq Scan on li  (cat = 3)
//	  -> Append
//	       -> Bitmap Heap Scan on cs1  (item = li.id)
//	       -> Bitmap Heap Scan on ws1  (item = li.id)
//
// goopg's pulled ANY body is one derived leaf whose binding is marked
// appendrel, but seamLeafRelInfo dropped the mark for table-less leaves.
// addParameterizedAppendPaths then skipped the leaf, and goopg hash-joined
// a HashAggregate over seq scans of both members. Values were identical
// either way; the plan was not PG's.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// planHasSemiParamAppend reports whether n contains a lateral nested-loop
// SEMI join whose right side is a UNION ALL SetOp — the parameterised
// Append under the pulled-up ANY sublink.
func planHasSemiParamAppend(n optimizer.Node) bool {
	switch x := n.(type) {
	case *optimizer.Join:
		if x.Algo == optimizer.JoinAlgoNestedLoop && x.Lateral && x.Type == optimizer.JoinTypeSemi {
			if _, ok := x.Right.(*optimizer.SetOp); ok {
				return true
			}
		}
		return planHasSemiParamAppend(x.Left) || planHasSemiParamAppend(x.Right)
	case *optimizer.Project:
		return planHasSemiParamAppend(x.Child)
	case *optimizer.Filter:
		return planHasSemiParamAppend(x.Child)
	case *optimizer.Sort:
		return planHasSemiParamAppend(x.Child)
	case *optimizer.Aggregate:
		return planHasSemiParamAppend(x.Child)
	case *optimizer.Gather:
		return planHasSemiParamAppend(x.Child)
	}
	return false
}

func TestInUnionAllProbesEachMember(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	// The M0146-0049f recon fixture: li 2000 rows (cat = 3 keeps 40),
	// cs1 100k and ws1 50k rows keyed (item, ord).
	for _, ddl := range []string{
		"CREATE TABLE li (id int PRIMARY KEY, cat int)",
		"CREATE TABLE cs1 (item int, ord int, amt numeric, PRIMARY KEY (item, ord))",
		"CREATE TABLE ws1 (item int, ord int, amt numeric, PRIMARY KEY (item, ord))",
		"INSERT INTO li SELECT g, g % 50 FROM generate_series(1, 2000) g",
		"INSERT INTO cs1 SELECT g % 4000, g, g * 0.5 FROM generate_series(1, 100000) g",
		"INSERT INTO ws1 SELECT g % 4000, g, g * 0.25 FROM generate_series(1, 50000) g",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	// The in-process ANALYZE is a no-op; seed what it would record.
	seed := func(name string, rows int64, nd ...int64) {
		tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("table %s missing", name)
		}
		cols := make([]catalog.ColumnStats, len(nd))
		for i, d := range nd {
			cols[i] = catalog.ColumnStats{NDistinct: d}
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows, Columns: cols}
	}
	seed("li", 2000, 2000, 50)
	seed("cs1", 100000, 4000, 100000, 100000)
	seed("ws1", 50000, 4000, 50000, 50000)

	const sql = `SELECT count(*), sum(li.id) FROM li WHERE li.cat = 3 AND li.id IN
		(SELECT item FROM cs1 UNION ALL SELECT item FROM ws1)`
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !planHasSemiParamAppend(plan) {
		t.Errorf("no Nested Loop Semi Join over the parameterised Append (PG's plan); got %#v", plan)
	}
	// PG 18.3: 40 rows, sum 39120.
	if got := renderRows(runSQL(t, ctx, sql)); len(got) != 1 || got[0] != "40|39120" {
		t.Errorf("result = %v, want PG's 40|39120", got)
	}
}
