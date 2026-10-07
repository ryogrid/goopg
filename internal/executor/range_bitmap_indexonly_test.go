package executor

// M0146-0061: a range restriction gets PG's bitmap and index-only paths.
//
// PG 18.3 plans `SELECT a, c FROM dsc WHERE a > 97` (3000 rows, 60
// matching, dsc_a on a DESC) as Bitmap Heap Scan / Bitmap Index Scan with
// `Index Cond: (a > 97)`, and `SELECT a FROM dsc WHERE a > 97` as an Index
// Only Scan with the same Index Cond. goopg's search had neither path for a
// range: its bitmap producer bound equalities only, and its index-only
// producer declined a range. With no bitmap path, the search elected a Seq
// Scan, which the rule-based single-relation producer then overrode with a
// heuristic Index Scan.
//
// The same change refuses a numeric literal as an integer index key. PG
// compares `(a)::numeric > 98.5` there, and integer_ops cannot index that.
// goopg encoded the literal into the int key, which rounded it: `a > 98.5`
// probed `a > 99`, and `a = 98.5` matched a = 99.

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// rangeScanKinds collects the plan's range-bounded scan nodes by kind:
// "bitmap" for a BitmapIndexScan with LowKey/HighKey, "indexonly" for an
// IndexOnlyScan with them, "index" for an IndexScan with them.
func rangeScanKinds(n optimizer.Node) map[string]bool {
	out := map[string]bool{}
	seen := map[uintptr]bool{}
	var walk func(v reflect.Value, depth int)
	walk = func(v reflect.Value, depth int) {
		if depth > 200 || !v.IsValid() {
			return
		}
		switch v.Kind() {
		case reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), depth+1)
			}
		case reflect.Ptr:
			if v.IsNil() || seen[v.Pointer()] {
				return
			}
			seen[v.Pointer()] = true
			if v.CanInterface() {
				switch x := v.Interface().(type) {
				case *optimizer.BitmapIndexScan:
					if x.LowKey != nil || x.HighKey != nil {
						out["bitmap"] = true
					}
				case *optimizer.IndexOnlyScan:
					if x.LowKey != nil || x.HighKey != nil {
						out["indexonly"] = true
					}
				case *optimizer.IndexScan:
					if x.LowKey != nil || x.HighKey != nil {
						out["index"] = true
					}
				}
			}
			walk(v.Elem(), depth+1)
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if f := v.Field(i); f.Kind() == reflect.Interface || f.Kind() == reflect.Ptr || f.Kind() == reflect.Slice {
					walk(f, depth+1)
				}
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), depth+1)
			}
		}
	}
	walk(reflect.ValueOf(n), 0)
	return out
}

func TestRangeRestrictionBitmapAndIndexOnly(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE dsc (a int, b int, c int)",
		"INSERT INTO dsc SELECT g % 100, g % 13, g FROM generate_series(1, 3000) g",
		"CREATE INDEX dsc_a ON dsc (a DESC)",
	} {
		runSQL(t, ctx, q)
	}
	// The in-process ANALYZE is a no-op; seed what it would record.
	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "dsc"})
	if !ok {
		t.Fatal("table dsc missing")
	}
	// a's histogram spans 0..99, so `a > 97` keeps 2% of the rows as in PG.
	hist := make([]string, 100)
	for i := range hist {
		hist[i] = strconv.Itoa(i)
	}
	tbl.Stats = &catalog.TableStats{RowCount: 3000, Columns: []catalog.ColumnStats{
		{NDistinct: 100, Histogram: hist}, {NDistinct: 13}, {NDistinct: 3000},
	}}

	planWith := func(sql string, seqscan, bitmap bool) optimizer.Node {
		t.Helper()
		ps := optimizer.DefaultPlannerSettings()
		ps.EnableSeqScan = seqscan
		ps.EnableBitmapScan = bitmap
		ps.MaxParallelWorkersPerGather = 0
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("parse %q: %v", sql, err)
		}
		p, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
		if err != nil {
			t.Fatalf("plan %q: %v", sql, err)
		}
		return p
	}
	plan := func(sql string, seqscan bool) optimizer.Node {
		t.Helper()
		return planWith(sql, seqscan, true)
	}
	run := func(p optimizer.Node, sql string) string {
		t.Helper()
		advanceStmtCounter(ctx)
		op, err := Build(p)
		if err != nil {
			t.Fatalf("build %q: %v", sql, err)
		}
		rows, err := Run(op, ctx)
		if err != nil {
			t.Fatalf("run %q: %v", sql, err)
		}
		return strings.Join(renderRows(rows), ";")
	}

	// PG's shapes: a bitmap probe where the heap is read, an index-only
	// probe where the index covers the query.
	if k := rangeScanKinds(plan("SELECT a, c FROM dsc WHERE a > 97", true)); !k["bitmap"] {
		t.Errorf("a, c … a > 97: no range Bitmap Index Scan (PG's plan); got %v", k)
	}
	if k := rangeScanKinds(plan("SELECT a, c FROM dsc WHERE a BETWEEN 3 AND 4", true)); !k["bitmap"] {
		t.Errorf("a, c … BETWEEN 3 AND 4: no range Bitmap Index Scan (PG's plan); got %v", k)
	}
	// PG's index-only scan wins on a vacuumed table, whose visibility map
	// the in-process fixture cannot set. With seq and bitmap scans off the
	// only range probe left is build_index_paths' single path for the
	// index, which is index-only because dsc_a covers the query.
	if k := rangeScanKinds(planWith("SELECT a FROM dsc WHERE a > 97", false, false)); !k["indexonly"] || k["index"] {
		t.Errorf("a … a > 97: no range Index Only Scan (PG's path for a covering index); got %v", k)
	}

	// Values, PG 18.3's, through whichever scan each setting elects. The
	// DESC index swaps value bounds into index order, strict bounds
	// exclude their endpoint, and a numeric literal on the int column
	// keeps its fraction.
	for _, c := range []struct{ sql, want string }{
		{"SELECT count(*), sum(c) FROM dsc WHERE a > 97", "60|92910"},
		{"SELECT count(*), sum(c) FROM dsc WHERE a BETWEEN 3 AND 4", "60|87210"},
		{"SELECT count(*), sum(c) FROM dsc WHERE a >= 98 AND c > 10", "60|92910"},
		{"SELECT count(*), sum(c) FROM dsc WHERE a > 97 AND a < 99", "30|46440"},
		{"SELECT count(*), sum(a) FROM dsc WHERE a <= 1", "60|30"},
		{"SELECT count(*) FROM dsc WHERE a > 98.5", "30"},
		{"SELECT count(*) FROM dsc WHERE a = 98.5", "0"},
		{"SELECT count(*) FROM dsc WHERE a = 98.0", "30"},
		{"SELECT a FROM dsc WHERE a < 2 ORDER BY a DESC LIMIT 3", "1;1;1"},
	} {
		for _, seqscan := range []bool{true, false} {
			if got := run(plan(c.sql, seqscan), c.sql); got != c.want {
				t.Errorf("%s (enable_seqscan=%v)\ngot  %s\nwant PG's %s", c.sql, seqscan, got, c.want)
			}
		}
	}
}
