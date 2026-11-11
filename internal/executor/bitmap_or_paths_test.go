package executor

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestBitmapOrOverOrClauses pins M0148-0001c3a2's BitmapOr producer (PG's
// generate_bitmap_or_paths / build_paths_for_OR, indxpath.c): an OR
// restriction whose every arm an index serves plans as a Bitmap Heap Scan over
// a BitmapOr, with the ORed arm quals as the Recheck Cond and the conjuncts
// they imply dropped from the Filter (create_bitmap_scan_plan). The expected
// plans are PG 18.3's for the same table, data and indexes. Every answer must
// equal a non-indexable twin's (`(col + 0)`), since a wrong arm qual or a
// wrongly dropped Filter conjunct shows as missing or extra rows.
func TestBitmapOrOverOrClauses(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	// The table PG 18.3 was probed on, column for column (the width sets the
	// page count the bitmap costs read).
	runSQL(t, ctx, "CREATE TABLE t1 (unique1 int NOT NULL, unique2 int NOT NULL, hundred int NOT NULL, thousand int NOT NULL, tenthous int NOT NULL, stringu1 name)")
	runSQL(t, ctx, "INSERT INTO t1 SELECT g, (g*7919)%10000, g%100, g%1000, g, 'S'||(g%500) FROM generate_series(0, 9999) g")
	runSQL(t, ctx, "CREATE INDEX t1_u1 ON t1 (unique1)")
	runSQL(t, ctx, "CREATE INDEX t1_u2 ON t1 (unique2)")
	runSQL(t, ctx, "CREATE INDEX t1_h ON t1 (hundred)")
	runSQL(t, ctx, "CREATE INDEX t1_tt ON t1 (thousand, tenthous)")
	runSQL(t, ctx, "ANALYZE t1")

	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"SELECT * FROM t1 WHERE unique1 = 1 OR unique2 = 3", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: ((unique1 = 1) OR (unique2 = 3))",
			"  ->  BitmapOr",
			"        ->  Bitmap Index Scan on t1_u1",
			"              Index Cond: (unique1 = 1)",
			"        ->  Bitmap Index Scan on t1_u2",
			"              Index Cond: (unique2 = 3)",
		}},
		// A restriction clause outside the OR stays a Filter.
		{"SELECT * FROM t1 WHERE (unique1 = 1 OR unique2 = 3) AND hundred = 5", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: ((unique1 = 1) OR (unique2 = 3))",
			"  Filter: (hundred = 5)",
			"  ->  BitmapOr",
			"        ->  Bitmap Index Scan on t1_u1",
			"              Index Cond: (unique1 = 1)",
			"        ->  Bitmap Index Scan on t1_u2",
			"              Index Cond: (unique2 = 3)",
		}},
		// The other restriction clause completes an arm's index prefix
		// (build_paths_for_OR's other_clauses); it stays a Filter because
		// the second arm's qual does not imply it.
		{"SELECT * FROM t1 WHERE thousand = 42 AND (tenthous = 1 OR unique1 = 3)", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: (((thousand = 42) AND (tenthous = 1)) OR (unique1 = 3))",
			"  Filter: (thousand = 42)",
			"  ->  BitmapOr",
			"        ->  Bitmap Index Scan on t1_tt",
			"              Index Cond: ((thousand = 42) AND (tenthous = 1))",
			"        ->  Bitmap Index Scan on t1_u1",
			"              Index Cond: (unique1 = 3)",
		}},
		// An AND arm the index serves only in part: the arm's qual does not
		// imply the OR, so the OR stays in the Filter.
		{"SELECT * FROM t1 WHERE (unique2 = 11 AND hundred = 3) OR unique1 = 0", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: ((unique2 = 11) OR (unique1 = 0))",
			"  Filter: (((unique2 = 11) AND (hundred = 3)) OR (unique1 = 0))",
			"  ->  BitmapOr",
			"        ->  Bitmap Index Scan on t1_u2",
			"              Index Cond: (unique2 = 11)",
			"        ->  Bitmap Index Scan on t1_u1",
			"              Index Cond: (unique1 = 0)",
		}},
		// PG 18's group_similar_or_args: the two unique2 arms fold into one
		// sub-OR, matched as one ScalarArrayOp arm; the recheck keeps the
		// sub-OR nested and, its second arm not implying a single Filter
		// arm (predicate_implied_by's OR => OR rule), the OR stays a Filter.
		{"SELECT * FROM t1 WHERE unique1 = 1 OR unique2 = 3 OR unique2 = 7", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: ((unique1 = 1) OR ((unique2 = 3) OR (unique2 = 7)))",
			"  Filter: ((unique1 = 1) OR (unique2 = 3) OR (unique2 = 7))",
			"  ->  BitmapOr",
			"        ->  Bitmap Index Scan on t1_u1",
			"              Index Cond: (unique1 = 1)",
			"        ->  Bitmap Index Scan on t1_u2",
			"              Index Cond: (unique2 = ANY ('{3,7}'::integer[]))",
		}},
		// The group goes where its first arm stood.
		{"SELECT * FROM t1 WHERE unique2 = 3 OR unique1 = 1 OR unique2 = 7", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: (((unique2 = 3) OR (unique2 = 7)) OR (unique1 = 1))",
			"  Filter: ((unique2 = 3) OR (unique1 = 1) OR (unique2 = 7))",
			"  ->  BitmapOr",
			"        ->  Bitmap Index Scan on t1_u2",
			"              Index Cond: (unique2 = ANY ('{3,7}'::integer[]))",
			"        ->  Bitmap Index Scan on t1_u1",
			"              Index Cond: (unique1 = 1)",
		}},
		// An OR wholly on one column is one ScalarArrayOp index qual of a
		// plain bitmap path (match_orclause_to_indexcol).
		{"SELECT * FROM t1 WHERE unique2 = 3 OR unique2 = 7", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: ((unique2 = 3) OR (unique2 = 7))",
			"  ->  Bitmap Index Scan on t1_u2",
			"        Index Cond: (unique2 = ANY ('{3,7}'::integer[]))",
		}},
		{"SELECT * FROM t1 WHERE unique2 IN (3, 7)", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: (unique2 = ANY ('{3,7}'::integer[]))",
			"  ->  Bitmap Index Scan on t1_u2",
			"        Index Cond: (unique2 = ANY ('{3,7}'::integer[]))",
		}},
		// A grouped arm behind an equality prefix from the other clauses.
		{"SELECT * FROM t1 WHERE thousand = 42 AND (tenthous = 1 OR tenthous = 3 OR unique1 = 5)", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: (((thousand = 42) AND ((tenthous = 1) OR (tenthous = 3))) OR (unique1 = 5))",
			"  Filter: ((thousand = 42) AND ((tenthous = 1) OR (tenthous = 3) OR (unique1 = 5)))",
			"  ->  BitmapOr",
			"        ->  Bitmap Index Scan on t1_tt",
			"              Index Cond: ((thousand = 42) AND (tenthous = ANY ('{1,3}'::integer[])))",
			"        ->  Bitmap Index Scan on t1_u1",
			"              Index Cond: (unique1 = 5)",
		}},
		{"SELECT * FROM t1 WHERE unique1 IN (1, 5, 9) OR unique2 = 4", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: ((unique1 = ANY ('{1,5,9}'::integer[])) OR (unique2 = 4))",
			"  ->  BitmapOr",
			"        ->  Bitmap Index Scan on t1_u1",
			"              Index Cond: (unique1 = ANY ('{1,5,9}'::integer[]))",
			"        ->  Bitmap Index Scan on t1_u2",
			"              Index Cond: (unique2 = 4)",
		}},
		// choose_bitmap_and (indxpath.c) over every restriction bitmap path,
		// OR paths included: candidates are redundant only when they share
		// a CLAUSE (classify_index_clause_usage), and priced on the
		// relation's tuples. PG 18.3 plans each of these as a BitmapAnd.
		{"SELECT * FROM t1 WHERE hundred = 42 AND unique2 < 3000", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: ((hundred = 42) AND (unique2 < 3000))",
			"  ->  BitmapAnd",
			"        ->  Bitmap Index Scan on t1_h",
			"              Index Cond: (hundred = 42)",
			"        ->  Bitmap Index Scan on t1_u2",
			"              Index Cond: (unique2 < 3000)",
		}},
		{"SELECT * FROM t1 WHERE hundred = 42 AND (thousand = 42 OR thousand = 99 OR unique1 < 2)", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: ((hundred = 42) AND (((thousand = 42) OR (thousand = 99)) OR (unique1 < 2)))",
			"  Filter: ((thousand = 42) OR (thousand = 99) OR (unique1 < 2))",
			"  ->  BitmapAnd",
			"        ->  Bitmap Index Scan on t1_h",
			"              Index Cond: (hundred = 42)",
			"        ->  BitmapOr",
			"              ->  Bitmap Index Scan on t1_tt",
			"                    Index Cond: (thousand = ANY ('{42,99}'::integer[]))",
			"              ->  Bitmap Index Scan on t1_u1",
			"                    Index Cond: (unique1 < 2)",
		}},
		{"SELECT * FROM t1 WHERE hundred = 42 AND (unique2 = 5 OR unique1 < 2000)", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: ((hundred = 42) AND ((unique2 = 5) OR (unique1 < 2000)))",
			"  ->  BitmapAnd",
			"        ->  Bitmap Index Scan on t1_h",
			"              Index Cond: (hundred = 42)",
			"        ->  BitmapOr",
			"              ->  Bitmap Index Scan on t1_u2",
			"                    Index Cond: (unique2 = 5)",
			"              ->  Bitmap Index Scan on t1_u1",
			"                    Index Cond: (unique1 < 2000)",
		}},
		{"SELECT * FROM t1 WHERE hundred = 42 AND thousand < 300 AND unique2 < 4000", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: ((hundred = 42) AND (thousand < 300))",
			"  Filter: (unique2 < 4000)",
			"  ->  BitmapAnd",
			"        ->  Bitmap Index Scan on t1_h",
			"              Index Cond: (hundred = 42)",
			"        ->  Bitmap Index Scan on t1_tt",
			"              Index Cond: (thousand < 300)",
		}},
		{"SELECT * FROM t1 WHERE unique1 < 3 OR unique2 = 7", []string{
			"Bitmap Heap Scan on t1",
			"  Recheck Cond: ((unique1 < 3) OR (unique2 = 7))",
			"  ->  BitmapOr",
			"        ->  Bitmap Index Scan on t1_u1",
			"              Index Cond: (unique1 < 3)",
			"        ->  Bitmap Index Scan on t1_u2",
			"              Index Cond: (unique2 = 7)",
		}},
	} {
		got := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+tc.q)
		if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", tc.q, strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
		}
	}

	cases := []struct{ indexed, twin string }{
		{"SELECT * FROM t1 WHERE unique1 = 1 OR unique2 = 3", "SELECT * FROM t1 WHERE unique1 + 0 = 1 OR unique2 + 0 = 3"},
		{"SELECT * FROM t1 WHERE (unique1 = 1 OR unique2 = 3) AND hundred = 1", "SELECT * FROM t1 WHERE (unique1 + 0 = 1 OR unique2 + 0 = 3) AND hundred + 0 = 1"},
		{"SELECT * FROM t1 WHERE thousand = 42 AND (tenthous = 1042 OR unique1 = 3)", "SELECT * FROM t1 WHERE thousand + 0 = 42 AND (tenthous + 0 = 1042 OR unique1 + 0 = 3)"},
		{"SELECT * FROM t1 WHERE (unique2 = 11 AND hundred = 3) OR unique1 = 0", "SELECT * FROM t1 WHERE (unique2 + 0 = 11 AND hundred + 0 = 3) OR unique1 + 0 = 0"},
		{"SELECT * FROM t1 WHERE (unique2 = 4602 AND hundred = 38) OR unique1 = 0", "SELECT * FROM t1 WHERE (unique2 + 0 = 4602 AND hundred + 0 = 38) OR unique1 + 0 = 0"},
		{"SELECT * FROM t1 WHERE unique1 < 3 OR unique2 = 7", "SELECT * FROM t1 WHERE unique1 + 0 < 3 OR unique2 + 0 = 7"},
		{"SELECT * FROM t1 WHERE hundred = 4 OR thousand < 300", "SELECT * FROM t1 WHERE hundred + 0 = 4 OR thousand + 0 < 300"},
		{"SELECT * FROM t1 WHERE unique1 = 1 OR unique2 = 3 OR unique2 = 7", "SELECT * FROM t1 WHERE unique1 + 0 = 1 OR unique2 + 0 = 3 OR unique2 + 0 = 7"},
		{"SELECT * FROM t1 WHERE thousand = 42 AND (tenthous = 42 OR tenthous = 1042 OR unique1 = 5)", "SELECT * FROM t1 WHERE thousand + 0 = 42 AND (tenthous + 0 = 42 OR tenthous + 0 = 1042 OR unique1 + 0 = 5)"},
		{"SELECT * FROM t1 WHERE unique1 IN (1, 5, 9, 9, NULL) OR unique2 = 4", "SELECT * FROM t1 WHERE unique1 + 0 IN (1, 5, 9) OR unique2 + 0 = 4"},
		{"SELECT * FROM t1 WHERE unique2 IN (3, 7, 3)", "SELECT * FROM t1 WHERE unique2 + 0 IN (3, 7)"},
		{"SELECT * FROM t1 WHERE hundred IN (1, 2, 3) OR thousand = 7 OR thousand = 8", "SELECT * FROM t1 WHERE hundred + 0 IN (1, 2, 3) OR thousand + 0 = 7 OR thousand + 0 = 8"},
		{"SELECT * FROM t1 WHERE hundred = 42 AND unique2 < 3000", "SELECT * FROM t1 WHERE hundred + 0 = 42 AND unique2 + 0 < 3000"},
		{"SELECT * FROM t1 WHERE hundred = 42 AND (thousand = 42 OR thousand = 99 OR unique1 < 2)", "SELECT * FROM t1 WHERE hundred + 0 = 42 AND (thousand + 0 = 42 OR thousand + 0 = 99 OR unique1 + 0 < 2)"},
		{"SELECT * FROM t1 WHERE hundred = 42 AND (unique2 = 5 OR unique1 < 2000)", "SELECT * FROM t1 WHERE hundred + 0 = 42 AND (unique2 + 0 = 5 OR unique1 + 0 < 2000)"},
		{"SELECT * FROM t1 WHERE hundred = 42 AND thousand < 300 AND unique2 < 4000", "SELECT * FROM t1 WHERE hundred + 0 = 42 AND thousand + 0 < 300 AND unique2 + 0 < 4000"},
	}
	checkCases := func(label string) {
		t.Helper()
		for _, c := range cases {
			got, want := sortedRowStrings(t, ctx, c.indexed), sortedRowStrings(t, ctx, c.twin)
			if len(want) == 0 {
				t.Errorf("%s: twin returned no rows; the case tests nothing", c.twin)
			}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("%s %s: indexed %d rows, twin %d rows\nplan:\n%s", label, c.indexed, len(got), len(want),
					strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+c.indexed), "\n"))
			}
		}
	}
	checkCases("exact")
	// A work_mem far below the bitmaps' size makes their pages lossy, so the
	// heap scan answers from the Recheck Cond (the ORed arm quals) alone.
	ctx.WorkMem = 4 << 10
	checkCases("lossy")

	// Parallel and lossy, on regress bitmapops' own table (big enough for a
	// parallel scan): a Parallel Bitmap Heap Scan walks each lossy page it
	// claims across Next() calls. It used to return only the page's first
	// match — `a = 1` gave 1273 (the lossy page count) of 1321 rows.
	runSQL(t, ctx, "CREATE TABLE bmscantest (a int, b int, t text)")
	runSQL(t, ctx, "INSERT INTO bmscantest SELECT (r%53), (r%59), repeat('o', 107) FROM generate_series(1, 70000) r")
	runSQL(t, ctx, "CREATE INDEX i_bmtest_a ON bmscantest (a)")
	runSQL(t, ctx, "CREATE INDEX i_bmtest_b ON bmscantest (b)")
	runSQL(t, ctx, "ANALYZE bmscantest")
	ctx.WorkMem = 64 << 10
	for _, pc := range []struct {
		q    string
		want int
	}{
		{"SELECT a FROM bmscantest WHERE a = 1", 1321},
		{"SELECT a FROM bmscantest WHERE a = 1 OR b = 1", 2485},
	} {
		stmts, err := parser.Parse(pc.q)
		if err != nil {
			t.Fatal(err)
		}
		ps := optimizer.DefaultPlannerSettings()
		ps.EnableSeqScan, ps.EnableIndexScan = false, false
		ps.MaxParallelWorkersPerGather = 0
		ps.WorkMem = ctx.WorkMem
		advanceStmtCounter(ctx)
		serial, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
		if err != nil {
			t.Fatal(err)
		}
		// The serial plan is a Project over the Bitmap Heap Scan; a Gather
		// wrapped over it runs one shared TBM whose pages the participants
		// claim, as the parallel-NLI tests wrap theirs.
		proj, ok := serial.(*optimizer.Project)
		if !ok {
			t.Fatalf("%s: plan root %T, want *optimizer.Project", pc.q, serial)
		}
		if _, ok := proj.Child.(*optimizer.BitmapHeapScan); !ok {
			t.Fatalf("%s: plan is not a Bitmap Heap Scan (%T); the case tests nothing", pc.q, proj.Child)
		}
		plan := optimizer.NewGather(0, serial, 2)
		ctx.MaxParallelWorkers = 8
		ctx.ParallelLeaderParticipation = true
		op, err := Build(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := op.Open(ctx); err != nil {
			t.Fatal(err)
		}
		rows := 0
		for {
			_, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			rows++
		}
		_ = op.Close()
		if rows != pc.want {
			t.Errorf("%s (parallel, lossy): %d rows, want %d", pc.q, rows, pc.want)
		}
	}
}

// TestOrEqualitiesAsIndexSAOP pins M0148-0001c3a2's plain-path
// match_orclause_to_indexcol: an OR of equalities on an index's leading
// column is the Index Scan's ScalarArrayOp qual (PG 18.3 with bitmap scans
// off: `Index Cond: (unique2 = ANY ('{3,7}'::integer[]))`). It also pins the
// qual-context NULL arm rules of find_duplicate_ors, which a NULL arm used to
// break: `x OR NULL` folded to NULL, so `WHERE hundred = 3 OR NULL` planned as
// `One-Time Filter: false` and returned nothing.
func TestOrEqualitiesAsIndexSAOP(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE t2 (unique1 int NOT NULL, unique2 int NOT NULL, hundred int NOT NULL)")
	runSQL(t, ctx, "INSERT INTO t2 SELECT g, (g*7919)%10000, g%100 FROM generate_series(0, 9999) g")
	runSQL(t, ctx, "CREATE INDEX t2_u2 ON t2 (unique2)")
	runSQL(t, ctx, "CREATE INDEX t2_h ON t2 (hundred)")
	runSQL(t, ctx, "ANALYZE t2")

	stmts, err := parser.Parse("SELECT * FROM t2 WHERE unique2 = 3 OR 7 = unique2 OR unique2 = NULL")
	if err != nil {
		t.Fatal(err)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableBitmapScan = false
	advanceStmtCounter(ctx)
	plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
	if err != nil {
		t.Fatal(err)
	}
	var is *optimizer.IndexScan
	for n := plan; n != nil && is == nil; {
		switch v := n.(type) {
		case *optimizer.IndexScan:
			is = v
		case *optimizer.Project:
			n = v.Child
		case *optimizer.Filter:
			t.Errorf("the OR stayed a Filter (%s); it is the index qual's exact recheck", formatExprQual(v.Predicate, nil, false))
			n = v.Child
		default:
			n = nil
		}
	}
	if is == nil || len(is.SAOPKeys) != 2 {
		t.Fatalf("want an Index Scan with a 2-element SAOP (the NULL arm dropped), got %T %+v", plan, is)
	}

	for _, c := range []struct {
		q    string
		want int
	}{
		{"SELECT * FROM t2 WHERE unique2 = 3 OR 7 = unique2 OR unique2 = NULL", 2},
		{"SELECT * FROM t2 WHERE hundred = 3 OR NULL", 100},
		{"SELECT * FROM t2 WHERE NULL OR hundred = 3", 100},
		{"SELECT * FROM t2 WHERE hundred = 3 AND NULL", 0},
		{"SELECT * FROM t2 WHERE NOT (hundred = 3 AND NULL)", 9900},
		{"SELECT * FROM t2 WHERE NOT (hundred = 3 OR NULL)", 0},
	} {
		if got := len(sortedRowStrings(t, ctx, c.q)); got != c.want {
			t.Errorf("%s: %d rows, want %d (PG 18.3)", c.q, got, c.want)
		}
	}
	// Outside a qual NULL is not FALSE: `x OR NULL` is TRUE or NULL,
	// `x AND NULL` FALSE or NULL.
	got := sortedRowStrings(t, ctx, "SELECT unique1, (unique1 = 1) OR NULL, (unique1 = 1) AND NULL FROM t2 WHERE unique1 IN (1, 2)")
	if want := []string{"1,1,NULL", "2,NULL,0"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("select-list AND/OR with NULL: got %v, want %v", got, want)
	}
}

// TestRangeBehindEqualityPrefix pins M0148-0001c3a2 item (2): bounds on the
// column after an equality prefix are exact index quals, for the plain Index
// Scan (RangePrefix) and now the bitmap producer too — PG's `Index Cond:
// ((a = 3) AND (b > 900))` with no Filter. They probe through
// indexRangeBounds, which stops an open end at the column's NULL group, so
// rows with b NULL never leak in; every answer must equal a non-indexable
// twin's on ASC, DESC and NULLS FIRST indexes, with and without NULL-keyed
// index entries.
func TestRangeBehindEqualityPrefix(t *testing.T) {
	for _, capable := range []bool{false, true} {
		catalog.SetNullKeyedIndexEntries(capable)
		ctx, cleanup := newVMFixture(t)
		runSQL(t, ctx, "CREATE TABLE pr (a int, b int, c int)")
		runSQL(t, ctx, "INSERT INTO pr SELECT g % 50, CASE WHEN g % 7 = 0 THEN NULL ELSE g % 1000 END, g FROM generate_series(1, 20000) g")
		for _, ddl := range []string{"CREATE INDEX pr_i ON pr (a, b)", "CREATE INDEX pr_i ON pr (a, b DESC)", "CREATE INDEX pr_i ON pr (a, b NULLS FIRST)"} {
			runSQL(t, ctx, "DROP INDEX IF EXISTS pr_i")
			runSQL(t, ctx, ddl)
			runSQL(t, ctx, "ANALYZE pr")
			for _, c := range []struct{ indexed, twin string }{
				{"SELECT c FROM pr WHERE a = 3 AND b > 900", "SELECT c FROM pr WHERE a + 0 = 3 AND b + 0 > 900"},
				{"SELECT c FROM pr WHERE a = 3 AND b < 200", "SELECT c FROM pr WHERE a + 0 = 3 AND b + 0 < 200"},
				{"SELECT c FROM pr WHERE a = 3 AND b >= 500 AND b < 520", "SELECT c FROM pr WHERE a + 0 = 3 AND b + 0 >= 500 AND b + 0 < 520"},
				{"SELECT c FROM pr WHERE a = 3 AND b > 900 AND b IS NOT NULL", "SELECT c FROM pr WHERE a + 0 = 3 AND b + 0 > 900"},
			} {
				want := sortedRowStrings(t, ctx, c.twin)
				if len(want) == 0 {
					t.Fatalf("%s: twin returned no rows", c.twin)
				}
				for _, mode := range []string{"index", "bitmap"} {
					stmts, err := parser.Parse(c.indexed)
					if err != nil {
						t.Fatal(err)
					}
					ps := optimizer.DefaultPlannerSettings()
					ps.EnableSeqScan = false
					if mode == "index" {
						ps.EnableBitmapScan = false
					} else {
						ps.EnableIndexScan = false
					}
					advanceStmtCounter(ctx)
					plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
					if err != nil {
						t.Fatal(err)
					}
					var got []string
					op, err := Build(plan)
					if err != nil {
						t.Fatal(err)
					}
					if err := op.Open(ctx); err != nil {
						t.Fatal(err)
					}
					for {
						slot, err := op.Next()
						if err == EOF {
							break
						}
						if err != nil {
							t.Fatal(err)
						}
						got = append(got, fmt.Sprint(slot.Row()[0].Int))
						slot.Release()
					}
					_ = op.Close()
					sort.Strings(got)
					wantInts := make([]string, len(want))
					copy(wantInts, want)
					sort.Strings(wantInts)
					if strings.Join(got, ",") != strings.Join(wantInts, ",") {
						t.Errorf("capable=%v %s %s [%s]: %d rows, want %d", capable, ddl, c.indexed, mode, len(got), len(wantInts))
					}
				}
			}
		}
		// PG's plan text: both bounds in the Index Cond, no Filter.
		runSQL(t, ctx, "DROP INDEX IF EXISTS pr_i")
		runSQL(t, ctx, "CREATE INDEX pr_i ON pr (a, b)")
		runSQL(t, ctx, "ANALYZE pr")
		want := []string{
			"Bitmap Heap Scan on pr",
			"  Recheck Cond: ((a = 3) AND (b < 200))",
			"  ->  Bitmap Index Scan on pr_i",
			"        Index Cond: ((a = 3) AND (b < 200))",
		}
		if got := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM pr WHERE a = 3 AND b < 200"); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("capable=%v plan:\n%s\nwant (PG 18.3):\n%s", capable, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		cleanup()
	}
	catalog.SetNullKeyedIndexEntries(false)
}

// TestDuplicateAndContradictoryNullTests pins M0148-0001c3a2 item (3): every
// `col IS [NOT] NULL` on an index column is an index qual, as PG's
// match_clause_to_indexcol takes them (`Index Cond: ((a IS NULL) AND (a IS
// NOT NULL))`); duplicates are redundant and `IS NULL AND IS NOT NULL` makes
// the scan empty (_bt_preprocess_keys). Every answer, on the plain, bitmap
// and index-only paths, must equal a non-indexable twin's.
func TestDuplicateAndContradictoryNullTests(t *testing.T) {
	catalog.SetNullKeyedIndexEntries(true)
	defer catalog.SetNullKeyedIndexEntries(false)
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	runSQL(t, ctx, "CREATE TABLE ntd (a int, b int, c int)")
	runSQL(t, ctx, "INSERT INTO ntd SELECT CASE WHEN g % 10 = 0 THEN NULL ELSE g END, g % 7, g FROM generate_series(1, 5000) g")
	runSQL(t, ctx, "CREATE INDEX ntd_a ON ntd (a)")
	runSQL(t, ctx, "CREATE INDEX ntd_ba ON ntd (b, a)")
	runSQL(t, ctx, "ANALYZE ntd")

	if got := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM ntd WHERE a IS NULL AND a IS NOT NULL"); len(got) < 2 ||
		got[1] != "  Index Cond: ((a IS NULL) AND (a IS NOT NULL))" {
		t.Errorf("contradictory NullTests: plan\n%s\nwant both as the Index Cond (PG 18.3)", strings.Join(got, "\n"))
	}
	for _, c := range []struct{ indexed, twin string }{
		{"SELECT c FROM ntd WHERE a IS NULL AND a IS NULL", "SELECT c FROM ntd WHERE (a + 0) IS NULL"},
		{"SELECT c FROM ntd WHERE a IS NULL AND a IS NOT NULL", "SELECT c FROM ntd WHERE c < 0"},
		{"SELECT c FROM ntd WHERE a IS NOT NULL AND a IS NULL", "SELECT c FROM ntd WHERE c < 0"},
		{"SELECT c FROM ntd WHERE a IS NOT NULL AND a IS NOT NULL AND a < 50", "SELECT c FROM ntd WHERE a + 0 < 50"},
		{"SELECT c FROM ntd WHERE b = 3 AND a IS NULL AND a IS NOT NULL", "SELECT c FROM ntd WHERE c < 0"},
		{"SELECT c FROM ntd WHERE b = 3 AND a IS NOT NULL AND a < 100 AND a IS NOT NULL", "SELECT c FROM ntd WHERE b + 0 = 3 AND a + 0 < 100"},
		{"SELECT a FROM ntd WHERE a IS NULL AND a IS NOT NULL", "SELECT a FROM ntd WHERE c < 0"},
	} {
		want := sortedRowStrings(t, ctx, c.twin)
		for _, mode := range []string{"index", "bitmap"} {
			stmts, err := parser.Parse(c.indexed)
			if err != nil {
				t.Fatal(err)
			}
			ps := optimizer.DefaultPlannerSettings()
			ps.EnableSeqScan = false
			if mode == "index" {
				ps.EnableBitmapScan = false
			} else {
				ps.EnableIndexScan = false
			}
			advanceStmtCounter(ctx)
			plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
			if err != nil {
				t.Fatal(err)
			}
			op, err := Build(plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatal(err)
			}
			var got []string
			for {
				slot, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if d := slot.Row()[0]; d.IsNull() {
					got = append(got, "NULL")
				} else {
					got = append(got, fmt.Sprint(d.Int))
				}
				slot.Release()
			}
			_ = op.Close()
			sort.Strings(got)
			w := append([]string(nil), want...)
			sort.Strings(w)
			if strings.Join(got, ",") != strings.Join(w, ",") {
				t.Errorf("%s [%s]: %d rows, want %d", c.indexed, mode, len(got), len(w))
			}
		}
	}
}
