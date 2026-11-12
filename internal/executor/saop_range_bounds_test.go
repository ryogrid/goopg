package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestSAOPRangeBoundsExactAndElementOrder pins M0148-0001c3a4 (c) against
// PG 18.3: a bound behind a leading ScalarArrayOp is an exact index qual —
// each element's descent is bounded by indexRangeBounds (the DESC swap and
// the NULL-group stop), so no Filter recheck remains — and the elements
// descend in index order, each once (_bt_sort_array_elements), so the
// index ordering the path claims holds for an unsorted, duplicated list.
// Rows are compared with a twin whose `+ 0` keeps the index out.
func TestSAOPRangeBoundsExactAndElementOrder(t *testing.T) {
	for _, capable := range []bool{false, true} {
		catalog.SetNullKeyedIndexEntries(capable)
		ctx, cleanup := newVMFixture(t)
		runSQL(t, ctx, "CREATE TABLE sr (thousand int NOT NULL, tenthous int, pad text)")
		runSQL(t, ctx, "INSERT INTO sr SELECT g%1000, CASE WHEN g%7=0 THEN NULL ELSE g END, repeat('x',30) FROM generate_series(0, 9999) g")
		ps := optimizer.DefaultPlannerSettings()
		ps.EnableBitmapScan = false
		for _, ddl := range []string{"CREATE INDEX sr_i ON sr (thousand, tenthous)",
			"CREATE INDEX sr_i ON sr (thousand, tenthous DESC)",
			"CREATE INDEX sr_i ON sr (thousand DESC, tenthous)"} {
			runSQL(t, ctx, "DROP INDEX IF EXISTS sr_i")
			runSQL(t, ctx, ddl)
			runSQL(t, ctx, "ANALYZE sr")
			if ddl == "CREATE INDEX sr_i ON sr (thousand, tenthous)" {
				q := "EXPLAIN (COSTS OFF) SELECT thousand, tenthous, pad FROM sr WHERE thousand IN (182, 183) AND tenthous > 7550 ORDER BY 1,2"
				want := "Index Scan using sr_i on sr\n  Index Cond: ((thousand = ANY ('{182,183}'::integer[])) AND (tenthous > 7550))"
				if got := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n"); got != want {
					t.Errorf("capable=%v %s: plan\n%s\nwant (PG 18.3):\n%s", capable, ddl, got, want)
				}
			}
			for _, w := range []string{
				"thousand IN (182, 183) AND tenthous > 7550",
				"thousand IN (182, 183, 5) AND tenthous >= 8005",
				"thousand IN (183, 182) AND tenthous < 3183",
				"thousand IN (182, 183) AND tenthous <= 3182",
				"thousand IN (182, 183, 182) AND tenthous BETWEEN 2183 AND 7182",
				"thousand IN (182, NULL, 183) AND tenthous > 5000",
				"thousand IN (182, 183) AND tenthous > NULL",
				"thousand IN (999, 5, 182, 5, 183)",
			} {
				q := "SELECT thousand, tenthous FROM sr WHERE " + w + " ORDER BY 1, 2 LIMIT 7"
				twin := "SELECT thousand, tenthous FROM sr WHERE " + strings.ReplaceAll(strings.ReplaceAll(w, "thousand IN", "thousand + 0 IN"), "tenthous ", "tenthous + 0 ") + " ORDER BY 1, 2 LIMIT 7"
				got := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), ";")
				want := strings.Join(renderRows(runSQLWith(t, ctx, twin, ps)), ";")
				if got != want {
					t.Errorf("capable=%v %s: %s\n got %s\nwant %s", capable, ddl, w, got, want)
				}
				if plan := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)), "\n"); strings.Contains(plan, "Filter: (tenthous") {
					t.Errorf("capable=%v %s: %s keeps the bound as a Filter recheck:\n%s", capable, ddl, w, plan)
				}
			}
		}
		cleanup()
	}
	catalog.SetNullKeyedIndexEntries(false)
}

// TestSAOPBehindEqualityPrefix pins M0148-0001c3a4 (b) against PG 18.3: an
// array behind an equality prefix (`thousand = 42 AND (tenthous = 1 OR …)`,
// or an IN) is a plain Index Scan with
// `Index Cond: ((thousand = 42) AND (tenthous = ANY (…)))` — the prefix in
// IndexScan.RangePrefix, the array on the next column — optionally bounded
// on the column after it. Rows match a `+ 0` twin under ORDER BY … LIMIT,
// and DML through indexScanPredicate's rebuilt qual touches only the probed
// rows.
func TestSAOPBehindEqualityPrefix(t *testing.T) {
	for _, capable := range []bool{false, true} {
		catalog.SetNullKeyedIndexEntries(capable)
		ctx, cleanup := newVMFixture(t)
		runSQL(t, ctx, "CREATE TABLE tk (unique1 int NOT NULL, thousand int NOT NULL, tenthous int NOT NULL, c int, pad text)")
		runSQL(t, ctx, "INSERT INTO tk SELECT g, g%1000, g, g%7, repeat('x', 100) FROM generate_series(0, 9999) g")
		runSQL(t, ctx, "CREATE INDEX tk_tt ON tk (thousand, tenthous)")
		runSQL(t, ctx, "VACUUM tk")
		runSQL(t, ctx, "ANALYZE tk")
		ps := optimizer.DefaultPlannerSettings()
		plan := func(q string) string {
			return strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)), "\n")
		}
		q := "SELECT * FROM tk WHERE thousand = 42 AND (tenthous = 1 OR tenthous = 3 OR tenthous = 42 OR tenthous = 0)"
		if got, want := plan(q), "Index Scan using tk_tt on tk\n  Index Cond: ((thousand = 42) AND (tenthous = ANY ('{1,3,42,0}'::integer[])))"; got != want {
			t.Errorf("capable=%v: plan\n%s\nwant (PG 18.3):\n%s", capable, got, want)
		}
		rowsEq := func(w string) {
			t.Helper()
			got := strings.Join(renderRows(runSQLWith(t, ctx, "SELECT unique1, tenthous FROM tk WHERE "+w+" ORDER BY tenthous LIMIT 3", ps)), ";")
			tw := strings.NewReplacer("thousand =", "thousand + 0 =", "tenthous IN", "tenthous + 0 IN", "c >", "c + 0 >").Replace(w)
			want := strings.Join(renderRows(runSQLWith(t, ctx, "SELECT unique1, tenthous FROM tk WHERE "+tw+" ORDER BY tenthous LIMIT 3", ps)), ";")
			if got != want || want == "" {
				t.Errorf("capable=%v %s: rows %s, want %s", capable, w, got, want)
			}
		}
		rowsEq("thousand = 42 AND tenthous IN (9042, 42, 1042, 42)")
		rowsEq("thousand = 42 AND tenthous IN (9042, NULL, 1042)")
		// indexScanPredicate (extractScan's DML fallback) must rebuild the
		// prefix equality, or it would match `tenthous IN (…)` rows of every
		// thousand. DML plans a Seq Scan today, so check the rebuilt qual
		// directly on the planned IndexScan.
		stmts, perr := parser.Parse("SELECT * FROM tk WHERE thousand = 43 AND tenthous IN (42, 1043)")
		if perr != nil {
			t.Fatal(perr)
		}
		node, plerr := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
		if plerr != nil {
			t.Fatal(plerr)
		}
		var ix *optimizer.IndexScan
		for n := node; n != nil && ix == nil; {
			switch x := n.(type) {
			case *optimizer.IndexScan:
				ix = x
			case *optimizer.Project:
				n = x.Child
			case *optimizer.Filter:
				n = x.Child
			default:
				n = nil
			}
		}
		if ix == nil || len(ix.RangePrefix) != 1 || len(ix.SAOPKeys) != 2 {
			t.Fatalf("capable=%v: want an IndexScan with RangePrefix + SAOPKeys, got %T:\n%s", capable, node, plan("SELECT * FROM tk WHERE thousand = 43 AND tenthous IN (42, 1043)"))
		}
		var prefixEq, in bool
		optimizer.WalkExprTree(indexScanPredicate(ix), func(e optimizer.Expr) {
			if b, ok := e.(*optimizer.BinaryOp); ok && b.Op == parser.OpEq {
				if cr, ok := b.Left.(*optimizer.ColumnRef); ok && cr.Name == "thousand" {
					prefixEq = true
				}
			}
			if ie, ok := e.(*optimizer.InExpr); ok {
				if cr, ok := ie.Operand.(*optimizer.ColumnRef); ok && cr.Name == "tenthous" {
					in = true
				}
			}
		})
		if !prefixEq || !in {
			t.Errorf("capable=%v: indexScanPredicate lost the prefix equality (%v) or the IN on tenthous (%v)", capable, prefixEq, in)
		}
		runSQL(t, ctx, "DROP INDEX tk_tt")
		runSQL(t, ctx, "CREATE INDEX tk_ttc ON tk (thousand, tenthous, c)")
		runSQL(t, ctx, "ANALYZE tk")
		q = "SELECT * FROM tk WHERE thousand = 42 AND tenthous IN (42, 1042, 2042, 3042) AND c > 2"
		if got, want := plan(q), "Index Scan using tk_ttc on tk\n  Index Cond: ((thousand = 42) AND (tenthous = ANY ('{42,1042,2042,3042}'::integer[])) AND (c > 2))"; got != want {
			t.Errorf("capable=%v: plan\n%s\nwant (PG 18.3):\n%s", capable, got, want)
		}
		rowsEq("thousand = 42 AND tenthous IN (42, 1042, 2042, 3042) AND c > 2")
		cleanup()
	}
	catalog.SetNullKeyedIndexEntries(false)
}

// TestIndexOnlySAOP pins M0148-0001c3a4 (h) against PG 18.3: a leading
// ScalarArrayOp (optionally bounded on the second column) drives an Index
// Only Scan — IndexOnlyScan.SAOPKeys over the same sorted, de-duplicated
// element windows as the plain scan (saopWindows) — forward and backward
// (the IOS reverses its materialised rows). Rows match a `+ 0` twin.
func TestIndexOnlySAOP(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE th (unique1 int NOT NULL, hundred int NOT NULL, thousand int NOT NULL, tenthous int NOT NULL, pad text)")
	runSQL(t, ctx, "INSERT INTO th SELECT g, g%100, g%1000, g, repeat('x', 100) FROM generate_series(0, 9999) g")
	runSQL(t, ctx, "CREATE INDEX th_tt ON th (thousand, tenthous)")
	runSQL(t, ctx, "CREATE INDEX th_u ON th (unique1)")
	runSQL(t, ctx, "VACUUM th")
	runSQL(t, ctx, "ANALYZE th")
	ps := optimizer.DefaultPlannerSettings()
	for _, c := range []struct {
		q, twin string
		want    []string
	}{
		{"SELECT thousand, tenthous FROM th WHERE thousand IN (182, 183) AND tenthous > 7550 ORDER BY 1, 2",
			"SELECT thousand, tenthous FROM th WHERE thousand + 0 IN (182, 183) AND tenthous + 0 > 7550 ORDER BY 1, 2",
			[]string{"Index Only Scan using th_tt on th", "  Index Cond: ((thousand = ANY ('{182,183}'::integer[])) AND (tenthous > 7550))"}},
		{"SELECT unique1 FROM th WHERE unique1 IN (7, 1, 42, 7) ORDER BY unique1",
			"SELECT unique1 FROM th WHERE unique1 + 0 IN (7, 1, 42) ORDER BY unique1",
			[]string{"Index Only Scan using th_u on th", "  Index Cond: (unique1 = ANY ('{7,1,42,7}'::integer[]))"}},
		{"SELECT unique1 FROM th WHERE unique1 IN (7, 1, 42) ORDER BY unique1 DESC",
			"SELECT unique1 FROM th WHERE unique1 + 0 IN (7, 1, 42) ORDER BY unique1 DESC",
			[]string{"Index Only Scan Backward using th_u on th", "  Index Cond: (unique1 = ANY ('{7,1,42}'::integer[]))"}},
		{"SELECT thousand, tenthous FROM th WHERE thousand IN (5, 3) ORDER BY thousand DESC, tenthous DESC LIMIT 3",
			"SELECT thousand, tenthous FROM th WHERE thousand + 0 IN (5, 3) ORDER BY thousand DESC, tenthous DESC LIMIT 3",
			[]string{"Limit", "  ->  Index Only Scan Backward using th_tt on th", "        Index Cond: (thousand = ANY ('{5,3}'::integer[]))"}},
	} {
		got := renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps))
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
		rows := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";")
		want := strings.Join(renderRows(runSQLWith(t, ctx, c.twin, ps)), ";")
		if rows != want || want == "" {
			t.Errorf("%s: rows %s, want %s", c.q, rows, want)
		}
	}
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "SELECT unique1 FROM th WHERE unique1 = ANY ('{NULL,NULL}'::int[])", ps)), ";"); got != "" {
		t.Errorf("an all-NULL array matches nothing; got %s", got)
	}
}
