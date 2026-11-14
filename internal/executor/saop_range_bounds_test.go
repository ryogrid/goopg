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

// TestInequalitySAOP pins M0148-0001c3a4 (a) against PG 18.3: an OR of one
// inequality on a column is matched as `col < ANY (…)`
// (match_orclause_to_indexcol / group_similar_or_args), as is an explicit
// `col >= ANY (array[…])`, and nbtree runs it as the one range of the extreme
// element (_bt_find_extreme_element) — on every probe (bitmap, plain, index
// only) through saopWindows. Counts match a `+ 0` twin on ASC and DESC
// indexes, with NULL elements ignored.
func TestInequalitySAOP(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE tq (unique1 int NOT NULL, hundred int NOT NULL, thousand int NOT NULL, tenthous int NOT NULL, pad text)")
	runSQL(t, ctx, "INSERT INTO tq SELECT g, g%100, g%1000, g, repeat('x', 100) FROM generate_series(0, 9999) g")
	runSQL(t, ctx, "CREATE INDEX tq_tt ON tq (thousand, tenthous)")
	runSQL(t, ctx, "CREATE INDEX tq_h ON tq (hundred)")
	runSQL(t, ctx, "CREATE INDEX tq_u ON tq (unique1)")
	runSQL(t, ctx, "VACUUM tq")
	runSQL(t, ctx, "ANALYZE tq")
	ps := optimizer.DefaultPlannerSettings()
	plan := func(q string) string {
		return strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)), "\n")
	}
	for _, c := range []struct{ q, want string }{
		{"SELECT count(*) FROM tq WHERE hundred = 42 AND (thousand < 42 OR thousand < 99 OR 43 > thousand OR 42 > thousand)",
			"Aggregate\n  ->  Bitmap Heap Scan on tq\n        Recheck Cond: ((hundred = 42) AND ((thousand < 42) OR (thousand < 99) OR (43 > thousand) OR (42 > thousand)))\n" +
				"        ->  BitmapAnd\n              ->  Bitmap Index Scan on tq_h\n                    Index Cond: (hundred = 42)\n" +
				"              ->  Bitmap Index Scan on tq_tt\n                    Index Cond: (thousand < ANY ('{42,99,43,42}'::integer[]))"},
		{"SELECT * FROM tq WHERE unique1 < 1 OR unique1 < 3 OR hundred < 2",
			"Bitmap Heap Scan on tq\n  Recheck Cond: (((unique1 < 1) OR (unique1 < 3)) OR (hundred < 2))\n  ->  BitmapOr\n" +
				"        ->  Bitmap Index Scan on tq_u\n              Index Cond: (unique1 < ANY ('{1,3}'::integer[]))\n" +
				"        ->  Bitmap Index Scan on tq_h\n              Index Cond: (hundred < 2)"},
		{"SELECT * FROM tq WHERE unique1 >= ANY (array[9995, 9998])",
			"Index Scan using tq_u on tq\n  Index Cond: (unique1 >= ANY ('{9995,9998}'::integer[]))"},
	} {
		if got := plan(c.q); got != c.want {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.want)
		}
	}
	counts := func(w string) {
		t.Helper()
		got := strings.Join(renderRows(runSQLWith(t, ctx, "SELECT count(*) FROM tq WHERE "+w, ps)), ";")
		tw := strings.NewReplacer("thousand <", "thousand + 0 <", "thousand >", "thousand + 0 >", "> thousand", "> thousand + 0",
			"unique1 <", "unique1 + 0 <", "unique1 >", "unique1 + 0 >", "hundred <", "hundred + 0 <").Replace(w)
		want := strings.Join(renderRows(runSQLWith(t, ctx, "SELECT count(*) FROM tq WHERE "+tw, ps)), ";")
		if got != want {
			t.Errorf("%s: count %s, want %s (twin %s)", w, got, want, tw)
		}
	}
	for _, idx := range []string{"", "CREATE INDEX tq_ttd ON tq (thousand DESC, tenthous)"} {
		if idx != "" {
			runSQL(t, ctx, "DROP INDEX tq_tt")
			runSQL(t, ctx, idx)
			runSQL(t, ctx, "ANALYZE tq")
		}
		counts("hundred = 42 AND (thousand < 42 OR thousand < 99 OR 43 > thousand OR 42 > thousand)")
		counts("hundred = 7 AND (thousand > 990 OR thousand > 995)")
		counts("hundred = 7 AND (thousand >= 990 OR thousand >= 995)")
		counts("unique1 < 1 OR unique1 < 3 OR hundred < 2")
		counts("unique1 >= ANY (array[9995, 9998])")
		counts("unique1 > ANY (array[9997, NULL])")
		counts("unique1 <= ANY ('{NULL,NULL}'::int[])")
	}
}

// TestSAOPArrayLiteralAndColumnQuals pins M0148-0001c3a4 (i) against PG
// 18.3. A lone array literal (`= ANY ('{7, 8, 9}')`) is probed element by
// element — it used to be encoded as one integer key and fail with 22P02 —
// and NULL elements print inside the literal. A further `= ANY` or a bound
// on the array's own column is merged into the array (_bt_preprocess_keys)
// and printed in the same Index Cond.
func TestSAOPArrayLiteralAndColumnQuals(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE ti (unique1 int NOT NULL, hundred int NOT NULL, pad text)")
	runSQL(t, ctx, "INSERT INTO ti SELECT g, g%100, repeat('x', 100) FROM generate_series(0, 9999) g")
	runSQL(t, ctx, "CREATE INDEX ti_u ON ti (unique1)")
	runSQL(t, ctx, "VACUUM ti")
	runSQL(t, ctx, "ANALYZE ti")
	ps := optimizer.DefaultPlannerSettings()
	for _, c := range []struct{ w, cond, rows string }{
		{"unique1 IN (1, 42, 7) and unique1 = ANY('{7, 8, 9}')",
			"((unique1 = ANY ('{1,42,7}'::integer[])) AND (unique1 = ANY ('{7,8,9}'::integer[])))", "7"},
		{"unique1 = ANY('{NULL,NULL,NULL}')", "(unique1 = ANY ('{NULL,NULL,NULL}'::integer[]))", ""},
		{"unique1 = ANY('{9, 3,NULL}')", "(unique1 = ANY ('{9,3,NULL}'::integer[]))", "3;9"},
		{"unique1 = ANY('{9,3}'::int[])", "(unique1 = ANY ('{9,3}'::integer[]))", "3;9"},
		{"unique1 IN (1, 42, 7) and unique1 >= 42", "((unique1 = ANY ('{1,42,7}'::integer[])) AND (unique1 >= 42))", "42"},
		{"unique1 IN (1, 42, 7) and unique1 > 42", "((unique1 = ANY ('{1,42,7}'::integer[])) AND (unique1 > 42))", ""},
		{"unique1 < 8 and unique1 IN (1, 42, 7)", "((unique1 < 8) AND (unique1 = ANY ('{1,42,7}'::integer[])))", "1;7"},
		// An empty array merged into the probe admits nothing.
		{"unique1 IN (1, 42, 7) and unique1 = ANY('{}')",
			"((unique1 = ANY ('{1,42,7}'::integer[])) AND (unique1 = ANY ('{}'::integer[])))", ""},
		// Two arrays and a bound keep the order they were written in.
		{"unique1 IN (42, 43, 7) and unique1 IN (43, 7, 8) and unique1 < 40",
			"((unique1 = ANY ('{42,43,7}'::integer[])) AND (unique1 = ANY ('{43,7,8}'::integer[])) AND (unique1 < 40))", "7"},
	} {
		q := "SELECT unique1 FROM ti WHERE " + c.w + " ORDER BY 1"
		want := "Index Only Scan using ti_u on ti\n  Index Cond: " + c.cond
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)), "\n"); got != want {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.w, got, want)
		}
		if got := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.w, got, c.rows)
		}
	}
}

// TestIndexOnlySAOPBehindPrefix pins M0148-0001c3a4 (i) against PG 18.3: an
// array behind an equality prefix on an Index Only Scan (IndexOnlyScan.Keys
// ahead of SAOPKeys), with the array's own column quals, bounds on the column
// after it, and an inequality array.
func TestIndexOnlySAOPBehindPrefix(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE tj (unique1 int NOT NULL, hundred int NOT NULL, ten int NOT NULL, pad text)")
	runSQL(t, ctx, "INSERT INTO tj SELECT g, g%100, g%10, repeat('x', 100) FROM generate_series(0, 9999) g")
	runSQL(t, ctx, "CREATE INDEX tj_hu ON tj (hundred, unique1)")
	runSQL(t, ctx, "CREATE INDEX tj_thu ON tj (ten, hundred, unique1)")
	runSQL(t, ctx, "VACUUM tj")
	runSQL(t, ctx, "ANALYZE tj")
	ps := optimizer.DefaultPlannerSettings()
	for _, c := range []struct {
		q, plan, rows string
		noBitmap      bool
	}{
		// ORDER BY would add the constant-prefix Sort M0148-0001c3a5 (d)
		// tracks; the backward IOS waits on it too.
		{"SELECT unique1 FROM tj WHERE hundred = 42 and unique1 IN (42, 142, 7)",
			"Index Only Scan using tj_hu on tj\n  Index Cond: ((hundred = 42) AND (unique1 = ANY ('{42,142,7}'::integer[])))", "42;142", false},
		{"SELECT unique1 FROM tj WHERE hundred = 42 and unique1 IN (42, 142, 7) and unique1 < 100",
			"Index Only Scan using tj_hu on tj\n  Index Cond: ((hundred = 42) AND (unique1 = ANY ('{42,142,7}'::integer[])) AND (unique1 < 100))", "42", false},
		{"SELECT hundred, unique1 FROM tj WHERE ten = 2 and hundred IN (42, 12, 7) and unique1 > 9800",
			"Index Only Scan using tj_thu on tj\n  Index Cond: ((ten = 2) AND (hundred = ANY ('{42,12,7}'::integer[])) AND (unique1 > 9800))",
			"12|9812;12|9912;42|9842;42|9942", false},
		{"SELECT unique1 FROM tj WHERE hundred = 42 and unique1 < ANY ('{100,300}'::int[])",
			// The fixture keeps relallvisible at 0, so its index-only
			// heap-fetch charge loses to the bitmap PG does not pick; a
			// live server prices both as PG does (8.65 vs 22.89).
			"Index Only Scan using tj_hu on tj\n  Index Cond: ((hundred = 42) AND (unique1 < ANY ('{100,300}'::integer[])))", "42;142;242", true},
	} {
		ps := ps
		ps.EnableBitmapScan = !c.noBitmap
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestIndexOnlySAOPBehindLeadingRange pins M0148-0001c3a4 (i) against PG
// 18.3: an `= ANY` array on the second column behind a leading-column range
// is an index qual of the Index Only Scan (IndexOnlyScan.TrailingSAOP),
// forward and backward, with a column qual merged into it. The rows pin the
// per-entry check: in this fixture no page is all-visible, so every entry
// takes the decode-then-check path ahead of its heap fetch.
func TestIndexOnlySAOPBehindLeadingRange(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE tk (thousand int NOT NULL, tenthous int NOT NULL, pad text)")
	runSQL(t, ctx, "INSERT INTO tk SELECT g%1000, g, repeat('x', 100) FROM generate_series(0, 9999) g")
	runSQL(t, ctx, "CREATE INDEX tk_tt ON tk (thousand, tenthous)")
	runSQL(t, ctx, "VACUUM tk")
	runSQL(t, ctx, "ANALYZE tk")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableBitmapScan = false
	for _, c := range []struct{ q, plan, rows string }{
		{"SELECT thousand, tenthous FROM tk WHERE thousand < 2 AND tenthous IN (1001,3000) ORDER BY thousand",
			"Index Only Scan using tk_tt on tk\n  Index Cond: ((thousand < 2) AND (tenthous = ANY ('{1001,3000}'::integer[])))", "0|3000;1|1001"},
		{"SELECT thousand, tenthous FROM tk WHERE thousand < 2 AND tenthous IN (1001,3000) ORDER BY thousand DESC, tenthous DESC",
			"Index Only Scan Backward using tk_tt on tk\n  Index Cond: ((thousand < 2) AND (tenthous = ANY ('{1001,3000}'::integer[])))", "1|1001;0|3000"},
		{"SELECT thousand, tenthous FROM tk WHERE thousand > 995 and tenthous in (998, 999, 9996, 1997) ORDER BY thousand desc",
			"Index Only Scan Backward using tk_tt on tk\n  Index Cond: ((thousand > 995) AND (tenthous = ANY ('{998,999,9996,1997}'::integer[])))", "999|999;998|998;997|1997;996|9996"},
		{"SELECT thousand, tenthous FROM tk WHERE thousand < 2 AND tenthous IN (1001,3000,4001) AND tenthous > 2000",
			"Index Only Scan using tk_tt on tk\n  Index Cond: ((thousand < 2) AND (tenthous = ANY ('{1001,3000,4001}'::integer[])) AND (tenthous > 2000))", "0|3000;1|4001"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestIndexScanSAOPBehindLeadingRange is the plain Index Scan twin of
// TestIndexOnlySAOPBehindLeadingRange (M0148-0001c3a4 (i)): a query the index
// does not cover reads the heap, and IndexScan.TrailingSAOP is checked on each
// entry's key before its heap fetch (trailingKeyCheck). Plans and rows are PG
// 18.3's, forward and backward, with a column qual merged into the array.
func TestIndexScanSAOPBehindLeadingRange(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE tk (thousand int NOT NULL, tenthous int NOT NULL, pad text)")
	runSQL(t, ctx, "INSERT INTO tk SELECT g%1000, g, repeat('x', 100) FROM generate_series(0, 9999) g")
	runSQL(t, ctx, "CREATE INDEX tk_tt ON tk (thousand, tenthous)")
	runSQL(t, ctx, "VACUUM tk")
	runSQL(t, ctx, "ANALYZE tk")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableBitmapScan = false
	for _, c := range []struct{ q, plan, rows string }{
		{"SELECT thousand, tenthous FROM tk WHERE thousand < 2 AND tenthous IN (1001,3000) AND pad IS NOT NULL",
			"Index Scan using tk_tt on tk\n  Index Cond: ((thousand < 2) AND (tenthous = ANY ('{1001,3000}'::integer[])))\n  Filter: (pad IS NOT NULL)",
			"0|3000;1|1001"},
		{"SELECT thousand, tenthous FROM tk WHERE thousand < 2 AND tenthous IN (1001,3000) AND pad IS NOT NULL ORDER BY thousand DESC, tenthous DESC",
			"Index Scan Backward using tk_tt on tk\n  Index Cond: ((thousand < 2) AND (tenthous = ANY ('{1001,3000}'::integer[])))\n  Filter: (pad IS NOT NULL)",
			"1|1001;0|3000"},
		{"SELECT thousand, tenthous FROM tk WHERE thousand > 995 AND tenthous IN (998,999,9996,1997) AND tenthous > 998 AND pad IS NOT NULL",
			"Index Scan using tk_tt on tk\n  Index Cond: ((thousand > 995) AND (tenthous = ANY ('{998,999,9996,1997}'::integer[])) AND (tenthous > 998))\n  Filter: (pad IS NOT NULL)",
			"996|9996;997|1997;999|999"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestBitmapOrSubORInAndArm pins M0148-0001c3a4 (d) against PG 18.3: a
// sub-OR inside an AND arm of a BitmapOr no longer drops the whole BitmapOr.
// build_paths_for_OR keeps it among the arm's clauses, where
// match_orclause_to_indexcol takes it as a ScalarArrayOp behind the arm's
// equality prefix.
func TestBitmapOrSubORInAndArm(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE tk (thousand int NOT NULL, tenthous int NOT NULL, pad text)")
	runSQL(t, ctx, "INSERT INTO tk SELECT g%1000, g, repeat('x', 100) FROM generate_series(0, 9999) g")
	runSQL(t, ctx, "CREATE INDEX tk_tt ON tk (thousand, tenthous)")
	runSQL(t, ctx, "VACUUM tk")
	runSQL(t, ctx, "ANALYZE tk")
	ps := optimizer.DefaultPlannerSettings()
	q := "SELECT thousand, tenthous FROM tk WHERE thousand = 42 AND (tenthous = 1042 OR tenthous = 3) OR thousand = 41"
	want := "Bitmap Heap Scan on tk\n" +
		"  Recheck Cond: (((thousand = 42) AND ((tenthous = 1042) OR (tenthous = 3))) OR (thousand = 41))\n" +
		"  ->  BitmapOr\n" +
		"        ->  Bitmap Index Scan on tk_tt\n" +
		"              Index Cond: ((thousand = 42) AND (tenthous = ANY ('{1042,3}'::integer[])))\n" +
		"        ->  Bitmap Index Scan on tk_tt\n" +
		"              Index Cond: (thousand = 41)"
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)), "\n"); got != want {
		t.Errorf("got:\n%s\nwant (PG 18.3):\n%s", got, want)
	}
	if got := strings.Join(renderRows(runSQLWith(t, ctx, q+" ORDER BY 1, 2", ps)), ";"); got != "41|41;41|1041;41|2041;41|3041;41|4041;41|5041;41|6041;41|7041;41|8041;41|9041;42|1042" {
		t.Errorf("rows %q", got)
	}
}

// TestSkipScanRangeOnLaterColumn pins M0148-0001c3a4 (j) against PG 18.3: a
// range on a non-leading column with nothing bound before it is an index
// qual of PG 18's skip scan (`Index Cond: (tenthous < 2)`), on the plain,
// index-only and bitmap paths and backward. The scan reads the whole index
// and checks each entry's key column (trailingKeyCheck), so the rows pin
// that check too.
func TestSkipScanRangeOnLaterColumn(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE tk (thousand int NOT NULL, tenthous int NOT NULL, pad text)")
	runSQL(t, ctx, "INSERT INTO tk SELECT g%1000, g, repeat('x', 100) FROM generate_series(0, 9999) g")
	runSQL(t, ctx, "CREATE INDEX tk_tt ON tk (thousand, tenthous)")
	runSQL(t, ctx, "VACUUM tk")
	runSQL(t, ctx, "ANALYZE tk")
	base := optimizer.DefaultPlannerSettings()
	noSeq := base
	noSeq.EnableSeqScan = false
	noSeqBitmap := noSeq
	noSeqBitmap.EnableBitmapScan = false
	onlyBitmap := noSeq
	onlyBitmap.EnableIndexScan = false
	for _, c := range []struct {
		q, plan, rows string
		ps            optimizer.PlannerSettings
	}{
		{"SELECT thousand, tenthous FROM tk WHERE tenthous < 2",
			"Index Only Scan using tk_tt on tk\n  Index Cond: (tenthous < 2)", "0|0;1|1", noSeqBitmap},
		{"SELECT thousand, tenthous FROM tk WHERE tenthous < 2 AND pad IS NOT NULL ORDER BY thousand DESC, tenthous DESC",
			"Index Scan Backward using tk_tt on tk\n  Index Cond: (tenthous < 2)\n  Filter: (pad IS NOT NULL)", "1|1;0|0", noSeqBitmap},
		{"SELECT thousand, tenthous FROM tk WHERE tenthous > 9995 AND tenthous <= 9998 AND pad IS NOT NULL",
			"Index Scan using tk_tt on tk\n  Index Cond: ((tenthous > 9995) AND (tenthous <= 9998))\n  Filter: (pad IS NOT NULL)", "996|9996;997|9997;998|9998", noSeqBitmap},
		{"SELECT thousand, tenthous FROM tk WHERE tenthous < 2 AND pad IS NOT NULL",
			"Bitmap Heap Scan on tk\n  Recheck Cond: (tenthous < 2)\n  Filter: (pad IS NOT NULL)\n  ->  Bitmap Index Scan on tk_tt\n        Index Cond: (tenthous < 2)", "0|0;1|1", onlyBitmap},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, c.ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, c.ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestPartialIndexPredicateProof pins M0148-0001c3a4 (e) against PG 18.3: a
// partial index is usable when predicate_implied_by proves its predicate from
// the restriction (or a BitmapOr arm) as a whole — an OR predicate by
// operator inference (`unique1 = 0` ⇒ `unique1 < 20 OR unique1 > 980`), a
// `name` predicate bytewise (its collation is always C), and a qual the
// predicate implies leaves the Filter.
func TestPartialIndexPredicateProof(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE ok2 (unique1 int, unique2 int, s1 name)")
	runSQL(t, ctx, "INSERT INTO ok2 SELECT g, (g*7)%1000, chr(65+(g%26))||'AAA' FROM generate_series(0, 999) g")
	runSQL(t, ctx, "CREATE INDEX ok2_u1_prtl ON ok2 (unique1) WHERE unique1 < 20 OR unique1 > 980")
	runSQL(t, ctx, "CREATE INDEX ok2_u2_prtl ON ok2 (unique2) WHERE s1 < 'B'")
	runSQL(t, ctx, "VACUUM ok2")
	runSQL(t, ctx, "ANALYZE ok2")
	ps := optimizer.DefaultPlannerSettings()
	for _, c := range []struct{ q, plan, rows string }{
		{"SELECT unique1 FROM ok2 WHERE unique2 = 11 AND s1 < 'B'",
			"Index Scan using ok2_u2_prtl on ok2\n  Index Cond: (unique2 = 11)", ""},
		{"SELECT unique1 FROM ok2 WHERE unique2 = 11 AND s1 = 'AAAA'",
			"Index Scan using ok2_u2_prtl on ok2\n  Index Cond: (unique2 = 11)\n  Filter: (s1 = 'AAAA'::name)", ""},
		{"SELECT unique1, unique2 FROM ok2 WHERE (unique2 = 11 AND s1 < 'B') OR unique1 = 0",
			"Bitmap Heap Scan on ok2\n  Recheck Cond: (((unique2 = 11) AND (s1 < 'B'::name)) OR (unique1 = 0))\n  ->  BitmapOr\n" +
				"        ->  Bitmap Index Scan on ok2_u2_prtl\n              Index Cond: (unique2 = 11)\n" +
				"        ->  Bitmap Index Scan on ok2_u1_prtl\n              Index Cond: (unique1 = 0)", "0|0"},
		{"SELECT unique1, unique2 FROM ok2 WHERE (unique2 = 11 OR unique1 = 0) AND s1 < 'B'",
			"Bitmap Heap Scan on ok2\n  Recheck Cond: (((unique2 = 11) AND (s1 < 'B'::name)) OR (unique1 = 0))\n  Filter: (s1 < 'B'::name)\n  ->  BitmapOr\n" +
				"        ->  Bitmap Index Scan on ok2_u2_prtl\n              Index Cond: (unique2 = 11)\n" +
				"        ->  Bitmap Index Scan on ok2_u1_prtl\n              Index Cond: (unique1 = 0)", "0|0"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestBooleanIndexQualAndConstantPrefixPathkeys pins M0148-0001c3a5 (d)
// against PG 18.3 (create_index's boolindex queries, on 2000 analyzed rows): a boolean column alone,
// negated, or under IS TRUE / IS FALSE is the index qual `b = true` / `b =
// false` (match_boolean_index_clause), and with b pinned the index on (b, i)
// delivers `ORDER BY i` in either direction (build_index_pathkeys skips the
// constant column) — no Sort.
func TestBooleanIndexQualAndConstantPrefixPathkeys(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	// i NOT NULL: the unit fixture's cluster keeps no NULL-keyed index
	// entries, so a probe leaving a nullable i unbound is refused there.
	runSQL(t, ctx, "create table boolindex (b bool, i int NOT NULL, junk float)")
	runSQL(t, ctx, "create unique index boolindex_b_i_key on boolindex (b, i)")
	runSQL(t, ctx, "insert into boolindex select g%2=0, g, 0 from generate_series(1,2000) g")
	runSQL(t, ctx, "VACUUM boolindex")
	runSQL(t, ctx, "ANALYZE boolindex")
	ps := optimizer.DefaultPlannerSettings()
	for _, c := range []struct{ q, plan string }{
		{"select * from boolindex where b order by i limit 10",
			"Limit\n  ->  Index Scan using boolindex_b_i_key on boolindex\n        Index Cond: (b = true)"},
		{"select * from boolindex where not b order by i limit 10",
			"Limit\n  ->  Index Scan using boolindex_b_i_key on boolindex\n        Index Cond: (b = false)"},
		{"select * from boolindex where b is true order by i desc limit 10",
			"Limit\n  ->  Index Scan Backward using boolindex_b_i_key on boolindex\n        Index Cond: (b = true)"},
		{"select * from boolindex where b is false order by i desc limit 10",
			"Limit\n  ->  Index Scan Backward using boolindex_b_i_key on boolindex\n        Index Cond: (b = false)"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
	for _, c := range []struct{ q, rows string }{
		{"select i from boolindex where b order by i limit 5", "2;4;6;8;10"},
		{"select i from boolindex where b is false order by i desc limit 5", "1999;1997;1995;1993;1991"},
		{"select i from boolindex where not b order by i limit 3", "1;3;5"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// M0148-0001c3a5 (a): a range-partitioned table delivers its partition
// key's order through a plain Append of per-partition ordered scans
// (generate_orderedappend_paths' partitions_are_ordered arm) — the regress
// inherit range_parted witness. The partitions are attached out of bound
// order so the Append must re-sequence them; DESC reverses them and scans
// each index backward.
func TestOrderedPartitionAppend(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "create table range_parted (a int NOT NULL, b int NOT NULL, c int NOT NULL) partition by range(a, b)")
	runSQL(t, ctx, "create table range_parted2 partition of range_parted for values from (10,10) to (20,20)")
	runSQL(t, ctx, "create table range_parted1 partition of range_parted for values from (0,0) to (10,10)")
	runSQL(t, ctx, "create index on range_parted (a,b,c)")
	runSQL(t, ctx, "insert into range_parted select g%20, g%10, g from generate_series(1,400) g where (g%20, g%10) >= (0,0) and (g%20 < 10 or (g%20, g%10) >= (10,10))")
	runSQL(t, ctx, "VACUUM range_parted1")
	runSQL(t, ctx, "VACUUM range_parted2")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableSort = false // the witness's table is empty; pin the arm on a populated one
	for _, c := range []struct{ q, plan string }{
		{"select * from range_parted order by a,b,c",
			"Append\n  ->  Index Only Scan using range_parted1_a_b_c_idx on range_parted1 range_parted_2\n  ->  Index Only Scan using range_parted2_a_b_c_idx on range_parted2 range_parted_1"},
		{"select * from range_parted order by a desc,b desc,c desc",
			"Append\n  ->  Index Only Scan Backward using range_parted2_a_b_c_idx on range_parted2 range_parted_1\n  ->  Index Only Scan Backward using range_parted1_a_b_c_idx on range_parted1 range_parted_2"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant:\n%s", c.q, got, c.plan)
		}
	}
	for _, c := range []struct{ q, rows string }{
		{"select a,b,c from range_parted order by a,b,c limit 3", "0|0|20;0|0|40;0|0|60"},
		{"select a,b,c from range_parted order by a desc,b desc,c desc limit 3", "19|9|399;19|9|379;19|9|359"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// M0148-0001c3a5 (a): build_index_paths files ONE path per index carrying
// every restriction clause it matches, so a clause the index can bind is
// never left as the Filter of a full ordered scan of that index. The
// ordered full-scan producer (index.ordered) declines such an index; the
// index-only path with the clause in Index Cond wins, as on PG 18.3.
func TestOrderedFullScanYieldsToIndexClausePath(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "create table ofs (a int NOT NULL, b int NOT NULL, pad text)")
	runSQL(t, ctx, "create index ofs_a_b on ofs (a, b)")
	runSQL(t, ctx, "insert into ofs select g, g % 7, 'x' from generate_series(1,5000) g")
	runSQL(t, ctx, "VACUUM ofs")
	runSQL(t, ctx, "ANALYZE ofs")
	ps := optimizer.DefaultPlannerSettings()
	q := "select a from ofs where a >= -100 order by a limit 3"
	want := "Limit\n  ->  Index Only Scan using ofs_a_b on ofs\n        Index Cond: (a >= '-100'::integer)"
	got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)), "\n")
	if got != want {
		t.Errorf("%s:\ngot:\n%s\nwant:\n%s", q, got, want)
	}
	if rows := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), ";"); rows != "1;2;3" {
		t.Errorf("%s: rows %q", q, rows)
	}
}

// M0148-0001c3a5 (a): plain comparisons on the second index column behind a
// leading range or NullTest are index quals, checked per entry (TrailingSAOP
// with no Elems) — PG 18.3's `Index Cond: ((a IS NOT NULL) AND (b = 5))`.
// Plans and rows were taken from PG on the same data.
func TestTrailingColumnQualsBehindLeadingBound(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "create table tq (a int, b int, c int)")
	runSQL(t, ctx, "create index tqi on tq (a, b)")
	runSQL(t, ctx, "insert into tq select case when g % 50 = 0 then null else g % 97 end, case when g % 41 = 0 then null else g % 13 end, g from generate_series(1, 5000) g")
	runSQL(t, ctx, "VACUUM tq")
	runSQL(t, ctx, "ANALYZE tq")
	ps := optimizer.DefaultPlannerSettings()
	for _, c := range []struct{ q, plan string }{
		{"select a from tq where a > 90 and b = 5 order by a limit 3",
			"Limit\n  ->  Index Only Scan using tqi on tq\n        Index Cond: ((a > 90) AND (b = 5))"},
		{"select * from tq where a is not null and b = 5 order by a limit 3",
			"Limit\n  ->  Index Scan using tqi on tq\n        Index Cond: ((a IS NOT NULL) AND (b = 5))"},
		{"select a, b from tq where a is not null and b > 11 order by a desc limit 3",
			"Limit\n  ->  Index Only Scan Backward using tqi on tq\n        Index Cond: ((a IS NOT NULL) AND (b > 11))"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
	for _, c := range []struct{ q, rows string }{
		{"select count(*), sum(c) from tq where a > 90 and b = 5", "23|56470"},
		{"select a, c from tq where a is not null and b = 5 order by a, c limit 3", "0|291;0|1552;0|2813"},
		{"select a, b from tq where a is not null and b > 11 order by a desc, b desc limit 3", "96|12;96|12;96|12"},
		{"select count(*), sum(c) from tq where a < 3 and b >= 4 and b < 6", "24|59388"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// M0148-0001c3a5 (a): build_minmax_path plans the min/max subquery through
// the path search, so a WHERE over another index column still gets PG's
// index-only InitPlans; an Index Only Scan prints a varchar key column
// bare (its index attribute is text_ops' text), where an Index Scan
// prints the relabel. Plans and rows from PG 18.3 on the same data.
func TestMinMaxSubqueryPlannedBySearch(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "create table tq (a int, b int, c int)")
	runSQL(t, ctx, "create index tqi on tq (a, b)")
	runSQL(t, ctx, "insert into tq select case when g % 50 = 0 then null else g % 97 end, case when g % 41 = 0 then null else g % 13 end, g from generate_series(1, 5000) g")
	runSQL(t, ctx, "create table vr (a int, v varchar(16), w text)")
	runSQL(t, ctx, "create index vr_v on vr (v)")
	runSQL(t, ctx, "insert into vr select g, 'v'||g, 'x' from generate_series(1,3000) g")
	runSQL(t, ctx, "VACUUM tq")
	runSQL(t, ctx, "ANALYZE tq")
	runSQL(t, ctx, "VACUUM vr")
	runSQL(t, ctx, "ANALYZE vr")
	ps := optimizer.DefaultPlannerSettings()
	for _, c := range []struct{ q, plan string }{
		{"select min(a), max(a) from tq where b = 5",
			"Result\n  InitPlan 1\n    ->  Limit\n          ->  Index Only Scan using tqi on tq\n                Index Cond: ((a IS NOT NULL) AND (b = 5))\n" +
				"  InitPlan 2\n    ->  Limit\n          ->  Index Only Scan Backward using tqi on tq tq_1\n                Index Cond: ((a IS NOT NULL) AND (b = 5))"},
		{"select v from vr where v = 'v42'",
			"Index Only Scan using vr_v on vr\n  Index Cond: (v = 'v42'::text)"},
		{"select * from vr where v = 'v42'",
			"Index Scan using vr_v on vr\n  Index Cond: ((v)::text = 'v42'::text)"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "select min(a), max(a) from tq where b = 5", ps)), ";"); got != "0|96" {
		t.Errorf("min/max rows %q, want 0|96", got)
	}
}

// M0148-0001c3a5 (a): a partitioned table with one leaf plans that leaf as
// its appendrel's only member — under the parent's refname, with the
// leaf's own index paths — and min/max over it is build_minmax_path's
// subquery planned by the search (regress inherit's parted_minmax, PG 18.3).
func TestSinglePartitionAppendRelMember(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "create table parted_minmax (a int, b varchar(16)) partition by range (a)")
	runSQL(t, ctx, "create table parted_minmax1 partition of parted_minmax for values from (1) to (3000)")
	runSQL(t, ctx, "create index parted_minmax1i on parted_minmax1 (a, b)")
	runSQL(t, ctx, "insert into parted_minmax select g, 'k' || (g % 7) from generate_series(1, 2000) g")
	runSQL(t, ctx, "VACUUM parted_minmax1")
	runSQL(t, ctx, "ANALYZE parted_minmax1")
	ps := optimizer.DefaultPlannerSettings()
	want := "Result\n  InitPlan 1\n    ->  Limit\n          ->  Index Only Scan using parted_minmax1i on parted_minmax1 parted_minmax\n" +
		"                Index Cond: ((a IS NOT NULL) AND (b = 'k3'::text))\n  InitPlan 2\n    ->  Limit\n" +
		"          ->  Index Only Scan Backward using parted_minmax1i on parted_minmax1 parted_minmax_1\n" +
		"                Index Cond: ((a IS NOT NULL) AND (b = 'k3'::text))"
	q := "select min(a), max(a) from parted_minmax where b = 'k3'"
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)), "\n"); got != want {
		t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", q, got, want)
	}
	for _, c := range []struct{ q, rows string }{
		{q, "3|1998"},
		{"select tableoid::regclass, a from parted_minmax where a = 3", "parted_minmax1|3"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}
