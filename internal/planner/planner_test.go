package planner

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// pgbenchCatalog seeds a catalog with the four tables pgbench -i
// creates, so plan-side tests don't have to redefine them per case.
func pgbenchCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "pgbench_accounts"}, []catalog.Column{
		{Name: "aid", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "bid", Type: catalog.Type{Name: "int4"}},
		{Name: "abalance", Type: catalog.Type{Name: "int4"}},
		{Name: "filler", Type: catalog.Type{Name: "char", Args: []int64{84}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "pgbench_history"}, []catalog.Column{
		{Name: "tid", Type: catalog.Type{Name: "int4"}},
		{Name: "bid", Type: catalog.Type{Name: "int4"}},
		{Name: "aid", Type: catalog.Type{Name: "int4"}},
		{Name: "delta", Type: catalog.Type{Name: "int4"}},
		{Name: "mtime", Type: catalog.Type{Name: "timestamp"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func parseOne(t *testing.T, sql string) parser.Stmt {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Parse(%q): %d stmts", sql, len(stmts))
	}
	return stmts[0]
}

// TestPlanPgbenchSelect: pgbench's --select-only canonical query
// plans into Project(Filter(SeqScan)) and resolves `abalance` to the
// right column ordinal.
func TestPlanPgbenchSelect(t *testing.T) {
	cat := pgbenchCatalog(t)
	stmt := parseOne(t, "SELECT abalance FROM pgbench_accounts WHERE aid = $1")
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	if len(proj.Targets) != 1 {
		t.Fatalf("targets=%d", len(proj.Targets))
	}
	if cr, ok := proj.Targets[0].(*ColumnRef); !ok || cr.Index != 2 || cr.Name != "abalance" {
		t.Errorf("target=%+v", proj.Targets[0])
	}
	filt, ok := proj.Child.(*Filter)
	if !ok {
		t.Fatalf("Project.Child=%T want *Filter", proj.Child)
	}
	if _, ok := filt.Child.(*SeqScan); !ok {
		t.Fatalf("Filter.Child=%T want *SeqScan", filt.Child)
	}
	pred, ok := filt.Predicate.(*BinaryOp)
	if !ok || pred.Op != parser.OpEq {
		t.Fatalf("predicate=%+v", filt.Predicate)
	}
	if cr, ok := pred.Left.(*ColumnRef); !ok || cr.Index != 0 || cr.Name != "aid" {
		t.Errorf("predicate.Left=%+v", pred.Left)
	}
	if pr, ok := pred.Right.(*ParamRef); !ok || pr.Number != 1 {
		t.Errorf("predicate.Right=%+v", pred.Right)
	}
}

func TestPlanSelectResolvesTableAlias(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t, "SELECT a.abalance FROM pgbench_accounts a WHERE a.aid = $1"), cat)
	if err != nil {
		t.Fatal(err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	filt, ok := proj.Child.(*Filter)
	if !ok {
		t.Fatalf("child=%T want *Filter", proj.Child)
	}
	pred, ok := filt.Predicate.(*BinaryOp)
	if !ok || pred.Op != parser.OpEq {
		t.Fatalf("predicate=%+v", filt.Predicate)
	}
	if _, ok := pred.Left.(*ColumnRef); !ok {
		t.Fatalf("predicate.Left=%T want *ColumnRef", pred.Left)
	}
}

func TestPlanSelectJoin(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t, "SELECT a.aid, h.delta FROM pgbench_accounts a JOIN pgbench_history h ON a.aid = h.aid"), cat)
	if err != nil {
		t.Fatal(err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	j, ok := proj.Child.(*Join)
	if !ok {
		t.Fatalf("child=%T want *Join", proj.Child)
	}
	if j.Type != JoinTypeInner {
		t.Fatalf("join type=%v want inner", j.Type)
	}
	if j.Predicate == nil {
		t.Fatal("join predicate should not be nil")
	}
	if len(j.Output()) != 9 {
		t.Fatalf("join output=%d want 9", len(j.Output()))
	}
}

// TestPlanJoinPicksHashAlgo pins the planner's hash-join
// promotion: a single equality predicate with disjoint-side
// ColumnRefs flips the Join algorithm to JoinAlgoHash and
// populates LeftKey/RightKey. Predicates that don't decompose
// stay on JoinAlgoNestedLoop.
func TestPlanJoinPicksHashAlgo(t *testing.T) {
	cat := pgbenchCatalog(t)
	cases := []struct {
		sql      string
		wantHash bool
	}{
		// Equality on disjoint sides → hash.
		{"SELECT a.aid FROM pgbench_accounts a JOIN pgbench_history h ON a.aid = h.aid", true},
		// Reversed equality flipped at plan time → still hash.
		{"SELECT a.aid FROM pgbench_accounts a JOIN pgbench_history h ON h.aid = a.aid", true},
		// LEFT join also takes the hash path.
		{"SELECT a.aid FROM pgbench_accounts a LEFT JOIN pgbench_history h ON a.aid = h.aid", true},
		// RIGHT/FULL use merge join, not hash.
		{"SELECT a.aid FROM pgbench_accounts a RIGHT JOIN pgbench_history h ON a.aid = h.aid", false},
		{"SELECT a.aid FROM pgbench_accounts a FULL JOIN pgbench_history h ON a.aid = h.aid", false},
		// Inequality predicate → nested-loop fallback.
		{"SELECT a.aid FROM pgbench_accounts a JOIN pgbench_history h ON a.aid < h.aid", false},
		// CROSS join (no predicate) → nested-loop.
		{"SELECT a.aid FROM pgbench_accounts a CROSS JOIN pgbench_history h", false},
	}
	for _, tc := range cases {
		node, err := Plan(parseOne(t, tc.sql), cat)
		if err != nil {
			t.Errorf("Plan(%q): %v", tc.sql, err)
			continue
		}
		proj, ok := node.(*Project)
		if !ok {
			t.Errorf("%q: root=%T want *Project", tc.sql, node)
			continue
		}
		j, ok := proj.Child.(*Join)
		if !ok {
			t.Errorf("%q: child=%T want *Join", tc.sql, proj.Child)
			continue
		}
		gotHash := j.Algo == JoinAlgoHash
		if gotHash != tc.wantHash {
			t.Errorf("%q: Algo=%v want JoinAlgoHash=%v", tc.sql, j.Algo, tc.wantHash)
		}
		if tc.wantHash && (j.LeftKey == nil || j.RightKey == nil) {
			t.Errorf("%q: hash algo but LeftKey/RightKey nil", tc.sql)
		}
	}
}

// TestPlanJoinHashBuildSidePicksSmaller pins the planner's
// build-side selection: when EstimateRows says the LEFT input is
// smaller, the planner sets BuildLeft=true so the executor builds
// the hash on the smaller relation. Build-side selection is
// INNER-only — LEFT joins keep the right-as-build default because
// the executor's outer-row emission walks the left side as the
// probe stream.
func TestPlanJoinHashBuildSidePicksSmaller(t *testing.T) {
	smallStats := &catalog.TableStats{
		RowCount: 100,
		Columns:  []catalog.ColumnStats{{NDistinct: 100}, {}, {}, {}},
	}
	bigStats := &catalog.TableStats{
		RowCount: 10000,
		Columns:  []catalog.ColumnStats{{}, {}, {NDistinct: 100}, {}, {}},
	}

	cases := []struct {
		name          string
		accountsStats *catalog.TableStats
		historyStats  *catalog.TableStats
		sql           string
		wantBuildLeft bool
	}{
		{
			name:          "small left, big right -> build=left",
			accountsStats: smallStats,
			historyStats:  bigStats,
			sql:           "SELECT a.aid FROM pgbench_accounts a JOIN pgbench_history h ON a.aid = h.aid",
			wantBuildLeft: true,
		},
		{
			name:          "big left, small right -> build=right (default)",
			accountsStats: bigStats,
			historyStats:  smallStats,
			sql:           "SELECT a.aid FROM pgbench_accounts a JOIN pgbench_history h ON a.aid = h.aid",
			wantBuildLeft: false,
		},
		{
			name:          "no stats -> build=right (default)",
			sql:           "SELECT a.aid FROM pgbench_accounts a JOIN pgbench_history h ON a.aid = h.aid",
			wantBuildLeft: false,
		},
		{
			name:          "LEFT JOIN never flips build side, even when left is smaller",
			accountsStats: smallStats,
			historyStats:  bigStats,
			sql:           "SELECT a.aid FROM pgbench_accounts a LEFT JOIN pgbench_history h ON a.aid = h.aid",
			wantBuildLeft: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := pgbenchCatalog(t)
			if tc.accountsStats != nil {
				if tbl, ok := cat.LookupTable(parser.ObjectName{Name: "pgbench_accounts"}); ok {
					cat.SetTableStats(tbl, tc.accountsStats)
				}
			}
			if tc.historyStats != nil {
				if tbl, ok := cat.LookupTable(parser.ObjectName{Name: "pgbench_history"}); ok {
					cat.SetTableStats(tbl, tc.historyStats)
				}
			}
			node, err := Plan(parseOne(t, tc.sql), cat)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			proj, ok := node.(*Project)
			if !ok {
				t.Fatalf("root=%T want *Project", node)
			}
			j, ok := proj.Child.(*Join)
			if !ok {
				t.Fatalf("child=%T want *Join", proj.Child)
			}
			if j.Algo != JoinAlgoHash {
				t.Fatalf("Algo=%v want JoinAlgoHash", j.Algo)
			}
			if j.BuildLeft != tc.wantBuildLeft {
				t.Errorf("BuildLeft=%v want %v", j.BuildLeft, tc.wantBuildLeft)
			}
		})
	}
}

// TestPlanCommaFromPushesEqualityIntoJoin pins the predicate-
// pushdown pass: comma-FROM with WHERE-side equalities should
// produce real INNER hash joins instead of CROSS+Filter.
func TestPlanCommaFromPushesEqualityIntoJoin(t *testing.T) {
	cat := pgbenchCatalog(t)

	// Two-table case: SELECT FROM a, h WHERE a.aid = h.aid.
	// Expected: Project -> Join(INNER, hash) over a and h with
	// no surrounding Filter (the only conjunct moved into the
	// Join). The build-side stays at the default (right) because
	// neither table has stats.
	t.Run("two-table eq pushed into join", func(t *testing.T) {
		sql := "SELECT a.aid FROM pgbench_accounts a, pgbench_history h WHERE a.aid = h.aid"
		node, err := Plan(parseOne(t, sql), cat)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		proj, ok := node.(*Project)
		if !ok {
			t.Fatalf("root=%T want *Project", node)
		}
		j, ok := proj.Child.(*Join)
		if !ok {
			t.Fatalf("Project.Child=%T want *Join (no Filter wrapper)", proj.Child)
		}
		if j.Type != JoinTypeInner {
			t.Errorf("Join.Type=%v want JoinTypeInner", j.Type)
		}
		if j.Algo != JoinAlgoHash {
			t.Errorf("Join.Algo=%v want JoinAlgoHash", j.Algo)
		}
		if j.LeftKey == nil || j.RightKey == nil {
			t.Errorf("LeftKey/RightKey unset after pushdown")
		}
		if j.Predicate == nil {
			t.Errorf("Join.Predicate nil after pushdown")
		}
	})

	// Mixed case: an eq-conjunct should land on the Join while a
	// single-table filter stays on top.
	t.Run("non-pushable conjunct stays on Filter", func(t *testing.T) {
		sql := "SELECT a.aid FROM pgbench_accounts a, pgbench_history h WHERE a.aid = h.aid AND a.bid = 5"
		node, err := Plan(parseOne(t, sql), cat)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		proj, ok := node.(*Project)
		if !ok {
			t.Fatalf("root=%T want *Project", node)
		}
		f, ok := proj.Child.(*Filter)
		if !ok {
			t.Fatalf("Project.Child=%T want *Filter (a.bid=5 stays here)", proj.Child)
		}
		j, ok := f.Child.(*Join)
		if !ok {
			t.Fatalf("Filter.Child=%T want *Join", f.Child)
		}
		if j.Type != JoinTypeInner || j.Algo != JoinAlgoHash {
			t.Errorf("Join Type=%v Algo=%v want INNER+Hash", j.Type, j.Algo)
		}
	})
}

// TestPlanOrderByAliasAndPositional pins the ORDER BY
// substitution: bare aliases and positional indices rewrite to
// the underlying target-list expression so TPC-H Q3/Q5/Q9/Q10/Q21
// (ORDER BY <agg-alias> DESC) plan and execute end-to-end.
// Qualified column refs (`t.col`) are NOT substituted even if
// the bare name happens to collide with a target alias.
func TestPlanOrderByAliasAndPositional(t *testing.T) {
	cat := pgbenchCatalog(t)

	t.Run("alias resolves to target expression", func(t *testing.T) {
		sql := "SELECT a.aid + 10 AS xx FROM pgbench_accounts a ORDER BY xx DESC"
		node, err := Plan(parseOne(t, sql), cat)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		// Project -> Sort -> SeqScan
		proj, ok := node.(*Project)
		if !ok {
			t.Fatalf("root=%T want *Project", node)
		}
		sort, ok := proj.Child.(*Sort)
		if !ok {
			t.Fatalf("Project.Child=%T want *Sort", proj.Child)
		}
		if len(sort.Keys) != 1 {
			t.Fatalf("Sort.Keys=%d want 1", len(sort.Keys))
		}
		// The substituted expression is `aid + 10` — not the
		// undefined alias name.
		if _, ok := sort.Keys[0].Expr.(*BinaryOp); !ok {
			t.Errorf("Sort.Keys[0].Expr=%T want *BinaryOp (a.aid + 10)", sort.Keys[0].Expr)
		}
	})

	t.Run("positional index resolves to target", func(t *testing.T) {
		sql := "SELECT a.aid, a.bid FROM pgbench_accounts a ORDER BY 2"
		node, err := Plan(parseOne(t, sql), cat)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		proj := node.(*Project)
		sort := proj.Child.(*Sort)
		// The substituted expression is the second target's
		// resolved column ref (a.bid, index 1).
		cr, ok := sort.Keys[0].Expr.(*ColumnRef)
		if !ok {
			t.Fatalf("Sort.Keys[0].Expr=%T want *ColumnRef", sort.Keys[0].Expr)
		}
		if cr.Index != 1 {
			t.Errorf("Index=%d want 1 (a.bid)", cr.Index)
		}
	})

	t.Run("non-alias bare ident still resolves via FROM", func(t *testing.T) {
		// `aid` isn't a target alias here — should resolve to
		// a.aid via the regular column-ref path.
		sql := "SELECT a.aid + 10 FROM pgbench_accounts a ORDER BY aid"
		_, err := Plan(parseOne(t, sql), cat)
		if err != nil {
			t.Fatalf("Plan: %v (should resolve aid via FROM)", err)
		}
	})
}

// TestPlanDerivedTable pins the FROM-clause-subquery
// (`(SELECT …) AS alias`) shape that TPC-H Q13 uses. The
// outer SELECT must see the derived table's columns under its
// alias and the inner SELECT's plan must be substituted in
// place of a SeqScan.
func TestPlanDerivedTable(t *testing.T) {
	cat := pgbenchCatalog(t)

	t.Run("derived table with explicit aliases", func(t *testing.T) {
		sql := "SELECT t.k FROM (SELECT a.aid AS k FROM pgbench_accounts a) AS t ORDER BY t.k"
		_, err := Plan(parseOne(t, sql), cat)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
	})

	t.Run("derived table with bare ColumnRef target", func(t *testing.T) {
		sql := "SELECT s.aid FROM (SELECT a.aid FROM pgbench_accounts a) AS s ORDER BY s.aid"
		_, err := Plan(parseOne(t, sql), cat)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
	})

	t.Run("derived table with aggregate", func(t *testing.T) {
		// Q13 shape: GROUP BY in inner, GROUP BY in outer.
		sql := "SELECT c_count, count(*) AS custdist FROM (SELECT a.aid, count(*) AS c_count FROM pgbench_accounts a GROUP BY a.aid) AS c_orders GROUP BY c_count"
		_, err := Plan(parseOne(t, sql), cat)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
	})

	t.Run("derived table without alias auto-generates synthetic alias", func(t *testing.T) {
		// PostgreSQL 16+ allows omitting the alias on a derived table; goopg
		// mirrors this by injecting a synthetic "__sq_<pos>" alias at parse time.
		_, err := parser.Parse("SELECT * FROM (SELECT 1) ORDER BY 1")
		if err != nil {
			t.Fatalf("unexpected parser error for derived table without alias: %v", err)
		}
	})
}

// TestPlanGroupByAliasAndPositional pins the GROUP BY
// substitution: bare aliases and positional indices rewrite to
// the underlying target-list expression. PG accepts this as an
// extension; TPC-H Q7 leans on it
// (`extract(year FROM ...) AS l_year ... GROUP BY l_year`).
func TestPlanGroupByAliasAndPositional(t *testing.T) {
	cat := pgbenchCatalog(t)

	t.Run("alias resolves to target expression", func(t *testing.T) {
		sql := "SELECT a.aid + 10 AS xx, sum(a.bid) FROM pgbench_accounts a GROUP BY xx"
		_, err := Plan(parseOne(t, sql), cat)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
	})

	t.Run("positional index resolves to target", func(t *testing.T) {
		sql := "SELECT a.aid, sum(a.bid) FROM pgbench_accounts a GROUP BY 1"
		_, err := Plan(parseOne(t, sql), cat)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
	})
}

func TestPlanJoinPicksMergeAlgoForRightFullEquality(t *testing.T) {
	cat := pgbenchCatalog(t)
	cases := []struct {
		sql string
	}{
		{"SELECT a.aid FROM pgbench_accounts a RIGHT JOIN pgbench_history h ON a.aid = h.aid"},
		{"SELECT a.aid FROM pgbench_accounts a FULL JOIN pgbench_history h ON h.aid = a.aid"},
	}
	for _, tc := range cases {
		node, err := Plan(parseOne(t, tc.sql), cat)
		if err != nil {
			t.Errorf("Plan(%q): %v", tc.sql, err)
			continue
		}
		proj, ok := node.(*Project)
		if !ok {
			t.Errorf("%q: root=%T want *Project", tc.sql, node)
			continue
		}
		j, ok := proj.Child.(*Join)
		if !ok {
			t.Errorf("%q: child=%T want *Join", tc.sql, proj.Child)
			continue
		}
		if j.Algo != JoinAlgoMerge {
			t.Errorf("%q: Algo=%v want JoinAlgoMerge", tc.sql, j.Algo)
		}
		if j.LeftKey == nil || j.RightKey == nil {
			t.Errorf("%q: merge algo but LeftKey/RightKey nil", tc.sql)
		}
	}
}

func TestPlanSelectGroupByHaving(t *testing.T) {
	cat := pgbenchCatalog(t)
	sql := "SELECT a.aid, sum(h.delta) FROM pgbench_accounts a JOIN pgbench_history h ON a.aid = h.aid GROUP BY a.aid HAVING sum(h.delta) > 0"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	having, ok := proj.Child.(*Filter)
	if !ok {
		t.Fatalf("project child=%T want *Filter", proj.Child)
	}
	agg, ok := having.Child.(*Aggregate)
	if !ok {
		t.Fatalf("having child=%T want *Aggregate", having.Child)
	}
	if len(agg.GroupExprs) != 1 {
		t.Fatalf("group exprs=%d want 1", len(agg.GroupExprs))
	}
	if len(agg.Aggs) != 1 || agg.Aggs[0].Name != "sum" {
		t.Fatalf("aggs=%+v", agg.Aggs)
	}
}

func TestPlanSelectCommaFromUsesCrossJoin(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t, "SELECT * FROM pgbench_accounts a, pgbench_history h"), cat)
	if err != nil {
		t.Fatal(err)
	}
	proj := node.(*Project)
	j, ok := proj.Child.(*Join)
	if !ok {
		t.Fatalf("child=%T want *Join", proj.Child)
	}
	if j.Type != JoinTypeCross {
		t.Fatalf("join type=%v want cross", j.Type)
	}
	if len(proj.Targets) != 9 {
		t.Fatalf("targets=%d want 9", len(proj.Targets))
	}
}

func TestPlanPgbenchSelectUsesIndexScanRule(t *testing.T) {
	cat := pgbenchCatalog(t)
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "pgbench_accounts_aid_idx"}, tbl, []string{"aid"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		sql      string
		wantExpr string
	}{
		{sql: "SELECT abalance FROM pgbench_accounts WHERE aid = $1", wantExpr: "param"},
		{sql: "SELECT abalance FROM pgbench_accounts WHERE 1 = aid", wantExpr: "int"},
	}
	for _, tc := range tests {
		node, err := Plan(parseOne(t, tc.sql), cat)
		if err != nil {
			t.Fatalf("Plan(%q): %v", tc.sql, err)
		}
		proj, ok := node.(*Project)
		if !ok {
			t.Fatalf("Plan(%q) root=%T want *Project", tc.sql, node)
		}
		idx, ok := proj.Child.(*IndexScan)
		if !ok {
			t.Fatalf("Plan(%q) child=%T want *IndexScan", tc.sql, proj.Child)
		}
		if idx.Index.Name != "pgbench_accounts_aid_idx" {
			t.Fatalf("index=%q want pgbench_accounts_aid_idx", idx.Index.Name)
		}
		switch tc.wantExpr {
		case "param":
			if _, ok := idx.Key.(*ParamRef); !ok {
				t.Fatalf("key expr=%T want *ParamRef", idx.Key)
			}
		case "int":
			if _, ok := idx.Key.(*IntegerConst); !ok {
				t.Fatalf("key expr=%T want *IntegerConst", idx.Key)
			}
		}
	}
}

// TestPlanSelectStarExpansion verifies SELECT * expands to the full
// column list and the Project's output schema reflects the table.
func TestPlanSelectStarExpansion(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t, "SELECT * FROM pgbench_accounts"), cat)
	if err != nil {
		t.Fatal(err)
	}
	proj := node.(*Project)
	if len(proj.Targets) != 4 {
		t.Errorf("targets=%d want 4", len(proj.Targets))
	}
	if len(proj.Output()) != 4 || proj.Output()[0].Name != "aid" {
		t.Errorf("output=%+v", proj.Output())
	}
}

// TestPlanSelectStarJoinUsingMergesColumn: an unqualified `SELECT *` over a
// JOIN USING (or NATURAL join) emits each merged column once — the right-side
// copy of the join column is hidden. Without this, `SELECT * FROM t1 JOIN t2
// USING (id)` wrongly produced a duplicate `id` column (`id,t,id,t` vs PG's
// `id,t,t`). A table-qualified star (`t2.*`) still expands to all of that
// relation's columns, join column included. M0097-0036.
func TestPlanSelectStarJoinUsingMergesColumn(t *testing.T) {
	c := catalog.NewInMemory()
	for _, name := range []string{"t1", "t2"} {
		if _, err := c.CreateTable(parser.ObjectName{Name: name}, []catalog.Column{
			{Name: "id", Type: catalog.Type{Name: "int4"}},
			{Name: "t", Type: catalog.Type{Name: "text"}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Unqualified star: join column merged → id, t (left), t (right).
	node, err := Plan(parseOne(t, "SELECT * FROM t1 JOIN t2 USING (id)"), c)
	if err != nil {
		t.Fatalf("Plan unqualified: %v", err)
	}
	proj := node.(*Project)
	gotNames := make([]string, 0, len(proj.Output()))
	for _, col := range proj.Output() {
		gotNames = append(gotNames, col.Name)
	}
	want := []string{"id", "t", "t"}
	if len(gotNames) != len(want) {
		t.Fatalf("unqualified star columns=%v want %v", gotNames, want)
	}
	for i := range want {
		if gotNames[i] != want[i] {
			t.Fatalf("unqualified star columns=%v want %v", gotNames, want)
		}
	}

	// NATURAL join over both shared columns: id and t merged → id, t.
	nnode, err := Plan(parseOne(t, "SELECT * FROM t1 NATURAL JOIN t2"), c)
	if err != nil {
		t.Fatalf("Plan natural: %v", err)
	}
	nproj := nnode.(*Project)
	if got := len(nproj.Output()); got != 2 {
		names := make([]string, 0, got)
		for _, col := range nproj.Output() {
			names = append(names, col.Name)
		}
		t.Fatalf("natural star columns=%v want [id t]", names)
	}

	// Table-qualified star is NOT merged: t2.* keeps the join column.
	qnode, err := Plan(parseOne(t, "SELECT t2.* FROM t1 JOIN t2 USING (id)"), c)
	if err != nil {
		t.Fatalf("Plan qualified: %v", err)
	}
	qproj := qnode.(*Project)
	if got := len(qproj.Output()); got != 2 {
		t.Fatalf("qualified t2.* columns=%d want 2 (id,t)", got)
	}
}

// TestPlanInsertResolvesColumns: pgbench's INSERT INTO pgbench_history
// (tid, bid, aid, delta, mtime) ... resolves all five names and
// builds a Values feeding an Insert.
func TestPlanInsertResolvesColumns(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t, "INSERT INTO pgbench_history (tid, bid, aid, delta, mtime) VALUES ($1, $2, $3, $4, $5)"), cat)
	if err != nil {
		t.Fatal(err)
	}
	ins, ok := node.(*Insert)
	if !ok {
		t.Fatalf("got %T", node)
	}
	if len(ins.ColumnIndex) != 5 {
		t.Fatalf("colIndex=%+v", ins.ColumnIndex)
	}
	for i, want := range []int{0, 1, 2, 3, 4} {
		if ins.ColumnIndex[i] != want {
			t.Errorf("colIndex[%d]=%d want %d", i, ins.ColumnIndex[i], want)
		}
	}
	values, ok := ins.Source.(*Values)
	if !ok {
		t.Fatalf("ins.Source=%T", ins.Source)
	}
	if len(values.Rows) != 1 || len(values.Rows[0]) != 5 {
		t.Fatalf("values shape=%v", values.Rows)
	}
}

// TestPlanInsertValuesDefaultSubstitutesColumnDefault: rung 15 — a bare
// DEFAULT cell in a VALUES row is substituted at plan time by the
// target column's catalog DefaultExpr. The executor never observes a
// DefaultMarker, so a Values row that parsed as `(1, DEFAULT)` against
// `(id int, note text DEFAULT 'auto')` plans into a row whose second
// cell evaluates to the literal `'auto'`.
func TestPlanInsertValuesDefaultSubstitutesColumnDefault(t *testing.T) {
	c := catalog.NewInMemory()
	// DefaultExpr on the catalog column models what execCreateTable
	// would have populated from `CREATE TABLE t (id int, note text
	// DEFAULT 'auto')`.
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
		{Name: "note", Type: catalog.Type{Name: "text"}, DefaultExpr: &parser.StringConst{Value: "auto"}},
	}); err != nil {
		t.Fatal(err)
	}
	node, err := Plan(parseOne(t, "INSERT INTO t (id, note) VALUES (1, DEFAULT)"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ins, ok := node.(*Insert)
	if !ok {
		t.Fatalf("got %T", node)
	}
	values, ok := ins.Source.(*Values)
	if !ok {
		t.Fatalf("ins.Source=%T", ins.Source)
	}
	if len(values.Rows) != 1 || len(values.Rows[0]) != 2 {
		t.Fatalf("values shape=%v", values.Rows)
	}
	// Cell 0: explicit integer.
	if _, ok := values.Rows[0][0].(*IntegerConst); !ok {
		t.Errorf("row[0][0]=%T want *IntegerConst", values.Rows[0][0])
	}
	// Cell 1: substituted from column's DefaultExpr — a planner StringConst
	// resolved from the catalog's parser.StringConst.
	sc, ok := values.Rows[0][1].(*StringConst)
	if !ok {
		t.Fatalf("row[0][1]=%T want *StringConst (the substituted DEFAULT)", values.Rows[0][1])
	}
	if sc.Value != "auto" {
		t.Errorf("row[0][1].Value=%q want %q", sc.Value, "auto")
	}
}

// TestPlanInsertValuesDefaultColumnWithoutDefaultGivesNull: rung 15 —
// DEFAULT against a column without a DefaultExpr plans to NULL (matches
// upstream PG: DEFAULT for a column with no default is NULL).
func TestPlanInsertValuesDefaultColumnWithoutDefaultGivesNull(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
		{Name: "bare", Type: catalog.Type{Name: "text"}}, // no DefaultExpr
	}); err != nil {
		t.Fatal(err)
	}
	node, err := Plan(parseOne(t, "INSERT INTO t (id, bare) VALUES (1, DEFAULT)"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	values := node.(*Insert).Source.(*Values)
	if _, ok := values.Rows[0][1].(*NullConst); !ok {
		t.Errorf("row[0][1]=%T want *NullConst", values.Rows[0][1])
	}
}

// TestPlanUpdateSetDefaultSubstitutesColumnDefault: rung 16 — a bare
// DEFAULT on the RHS of an UPDATE SET assignment is substituted at plan
// time by the target column's catalog DefaultExpr. The executor never
// observes a DefaultMarker; the resolved Set slot at the column's
// ordinal holds the substituted constant. Symmetric with rung 15.
func TestPlanUpdateSetDefaultSubstitutesColumnDefault(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
		{Name: "note", Type: catalog.Type{Name: "text"}, DefaultExpr: &parser.StringConst{Value: "auto"}},
	}); err != nil {
		t.Fatal(err)
	}
	node, err := Plan(parseOne(t, "UPDATE t SET note = DEFAULT WHERE id = 1"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	upd, ok := node.(*Update)
	if !ok {
		t.Fatalf("got %T", node)
	}
	if len(upd.Set) != 2 {
		t.Fatalf("Set len=%d want 2", len(upd.Set))
	}
	// Set[0] (id) should be untouched (nil — UPDATE preserves the row's
	// existing value for columns not named in SET).
	if upd.Set[0] != nil {
		t.Errorf("Set[0]=%T want nil", upd.Set[0])
	}
	// Set[1] (note) is the substituted DEFAULT — a planner StringConst
	// resolved from the catalog's parser.StringConst.
	sc, ok := upd.Set[1].(*StringConst)
	if !ok {
		t.Fatalf("Set[1]=%T want *StringConst (the substituted DEFAULT)", upd.Set[1])
	}
	if sc.Value != "auto" {
		t.Errorf("Set[1].Value=%q want %q", sc.Value, "auto")
	}
}

// TestPlanUpdateSetDefaultColumnWithoutDefaultGivesNull: rung 16 —
// DEFAULT against a column without a DefaultExpr plans to NULL. Mirrors
// upstream PG semantics ("DEFAULT for a column with no default is
// NULL") and rung 15's INSERT VALUES path.
func TestPlanUpdateSetDefaultColumnWithoutDefaultGivesNull(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
		{Name: "bare", Type: catalog.Type{Name: "text"}}, // no DefaultExpr
	}); err != nil {
		t.Fatal(err)
	}
	node, err := Plan(parseOne(t, "UPDATE t SET bare = DEFAULT WHERE id = 1"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	upd := node.(*Update)
	if _, ok := upd.Set[1].(*NullConst); !ok {
		t.Errorf("Set[1]=%T want *NullConst", upd.Set[1])
	}
}

// TestPlanInsertDefaultValuesExpandsToColumnDefaults: rung 17 — the
// all-defaults `INSERT INTO t DEFAULT VALUES` form is expanded by
// rewriteInsertDefaultMarkers into a single VALUES row whose cells
// are each the corresponding column's DefaultExpr (or NULL for
// columns without a DEFAULT). Generated columns are skipped (same
// rule planInsert uses for the implicit-column-list case).
func TestPlanInsertDefaultValuesExpandsToColumnDefaults(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, DefaultExpr: &parser.IntegerConst{Value: 7}},
		{Name: "note", Type: catalog.Type{Name: "text"}, DefaultExpr: &parser.StringConst{Value: "auto"}},
		{Name: "bare", Type: catalog.Type{Name: "text"}}, // no DEFAULT
	}); err != nil {
		t.Fatal(err)
	}
	node, err := Plan(parseOne(t, "INSERT INTO t DEFAULT VALUES"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ins, ok := node.(*Insert)
	if !ok {
		t.Fatalf("got %T", node)
	}
	values, ok := ins.Source.(*Values)
	if !ok {
		t.Fatalf("ins.Source=%T", ins.Source)
	}
	if len(values.Rows) != 1 || len(values.Rows[0]) != 3 {
		t.Fatalf("values shape=%v", values.Rows)
	}
	// Cell 0: id's IntegerConst DEFAULT.
	if ic, ok := values.Rows[0][0].(*IntegerConst); !ok || ic.Value != 7 {
		t.Errorf("row[0][0]=%v want IntegerConst{Value: 7}", values.Rows[0][0])
	}
	// Cell 1: note's StringConst DEFAULT.
	if sc, ok := values.Rows[0][1].(*StringConst); !ok || sc.Value != "auto" {
		t.Errorf("row[0][1]=%v want StringConst{Value: \"auto\"}", values.Rows[0][1])
	}
	// Cell 2: bare has no DefaultExpr → substituted as NullConst.
	if _, ok := values.Rows[0][2].(*NullConst); !ok {
		t.Errorf("row[0][2]=%T want *NullConst", values.Rows[0][2])
	}
	// ColumnIndex covers all three columns in declared order (no
	// generated columns in this fixture).
	if got, want := ins.ColumnIndex, []int{0, 1, 2}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("ColumnIndex=%v want %v", got, want)
	}
}

// TestPlanInsertDefaultValuesSkipsGeneratedColumns: rung 17 — generated
// columns are excluded from the expansion, matching planInsert's
// implicit-column-list rule. The Insert's source then has arity one
// less than len(tbl.Columns); the executor populates the generated
// column via computeGeneratedColumns.
func TestPlanInsertDefaultValuesSkipsGeneratedColumns(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, DefaultExpr: &parser.IntegerConst{Value: 1}},
		{Name: "g", Type: catalog.Type{Name: "int4"}, GeneratedAlways: true},
	}); err != nil {
		t.Fatal(err)
	}
	node, err := Plan(parseOne(t, "INSERT INTO t DEFAULT VALUES"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	ins := node.(*Insert)
	values := ins.Source.(*Values)
	if len(values.Rows[0]) != 1 {
		t.Fatalf("expansion size=%d want 1 (generated col excluded)", len(values.Rows[0]))
	}
	if len(ins.ColumnIndex) != 1 || ins.ColumnIndex[0] != 0 {
		t.Errorf("ColumnIndex=%v want [0]", ins.ColumnIndex)
	}
}

// TestPlanUpdate: pgbench's abalance UPDATE plans into
// Update(Filter(SeqScan)) with Set[2] populated.
func TestPlanUpdate(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t, "UPDATE pgbench_accounts SET abalance = abalance + $1 WHERE aid = $2"), cat)
	if err != nil {
		t.Fatal(err)
	}
	upd, ok := node.(*Update)
	if !ok {
		t.Fatalf("got %T", node)
	}
	if len(upd.Set) != 4 {
		t.Fatalf("Set len=%d want 4", len(upd.Set))
	}
	if upd.Set[0] != nil || upd.Set[1] != nil || upd.Set[3] != nil {
		t.Errorf("non-target columns should be nil: %+v", upd.Set)
	}
	if upd.Set[2] == nil {
		t.Fatal("Set[abalance] should be populated")
	}
	if _, ok := upd.Child.(*Filter); !ok {
		t.Fatalf("upd.Child=%T", upd.Child)
	}
}

// TestPlanDelete: simple DELETE plans into Delete(Filter(SeqScan)).
func TestPlanDelete(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t, "DELETE FROM pgbench_history WHERE aid = $1"), cat)
	if err != nil {
		t.Fatal(err)
	}
	del := node.(*Delete)
	if _, ok := del.Child.(*Filter); !ok {
		t.Errorf("del.Child=%T", del.Child)
	}
}

// TestPlanDeleteUsing: DELETE … USING binds USING-table columns into
// the WHERE/RETURNING resolve context (M0097-0076). Mirrors the
// UPDATE … FROM planner path; UsingTables/UsingPred get populated and
// RETURNING column references resolve against the joined schema.
func TestPlanDeleteUsing(t *testing.T) {
	cat := pgbenchCatalog(t)
	node, err := Plan(parseOne(t,
		"DELETE FROM pgbench_history USING pgbench_accounts a "+
			"WHERE pgbench_history.aid = a.aid RETURNING pgbench_history.aid, a.bid"), cat)
	if err != nil {
		t.Fatal(err)
	}
	del := node.(*Delete)
	if len(del.UsingTables) != 1 || del.UsingTables[0].Name != "pgbench_accounts" {
		t.Fatalf("UsingTables=%+v", del.UsingTables)
	}
	if del.UsingPred == nil {
		t.Fatal("UsingPred should be populated when USING + WHERE are present")
	}
	if len(del.Returning) != 2 {
		t.Fatalf("Returning len=%d want 2", len(del.Returning))
	}
}

// TestPlanDDLAndUtilityPassThrough: DDL/utility statements wrap
// without decomposing.
func TestPlanDDLAndUtilityPassThrough(t *testing.T) {
	cat := pgbenchCatalog(t)
	if n, err := Plan(parseOne(t, "BEGIN"), cat); err != nil || n.(*Transaction).Verb != TxBegin {
		t.Errorf("BEGIN: %v / %+v", err, n)
	}
	if n, err := Plan(parseOne(t, "DROP TABLE pgbench_history"), cat); err != nil {
		t.Errorf("DROP: %v / %T", err, n)
	} else if _, ok := n.(*DDL); !ok {
		t.Errorf("DROP got %T", n)
	}
	if n, err := Plan(parseOne(t, "VACUUM ANALYZE"), cat); err != nil {
		t.Errorf("VACUUM: %v", err)
	} else if _, ok := n.(*Utility); !ok {
		t.Errorf("VACUUM got %T", n)
	}
}

// TestPlanResolutionErrors pins SQLSTATE-aligned codes for the
// canonical resolution failures.
func TestPlanResolutionErrors(t *testing.T) {
	cat := pgbenchCatalog(t)
	cases := []struct {
		sql  string
		code string
	}{
		{"SELECT * FROM nope", "42P01"},                                        // undefined_table
		{"SELECT bogus FROM pgbench_accounts", "42703"},                        // undefined_column
		{"INSERT INTO pgbench_history (nope) VALUES (1)", "42703"},             // undefined_column
		{"UPDATE pgbench_accounts SET nope = 1 WHERE aid = $1", "42703"},       // undefined_column
		{"INSERT INTO pgbench_history (tid, bid, aid) VALUES (1, 2)", "42601"}, // explicit col-list arity mismatch
		{"SELECT 1 UNION SELECT 2, 3", "42601"},                                // set-op column-count mismatch
		{"SELECT aid FROM pgbench_accounts a JOIN pgbench_history h ON a.aid = h.aid", "42702"},
		{"SELECT aid FROM pgbench_accounts HAVING aid > 0", "42803"},
	}
	for _, c := range cases {
		_, err := Plan(parseOne(t, c.sql), cat)
		if err == nil {
			t.Errorf("Plan(%q) expected error", c.sql)
			continue
		}
		pe, ok := err.(*PlanError)
		if !ok {
			t.Errorf("Plan(%q) err type=%T", c.sql, err)
			continue
		}
		if pe.Code != c.code {
			t.Errorf("Plan(%q) code=%s want %s", c.sql, pe.Code, c.code)
		}
	}
}

// TestPlanLateralSrfArgResolvesAgainstLeftFromItem pins the LATERAL
// FROM-clause SRF resolution path (M0103-0008). The libpqrcv
// column-list probe ships
//
//	SELECT ... FROM pg_publication p,
//	  LATERAL pg_get_publication_tables(p.pubname) gpt,
//	  pg_class c WHERE gpt.relid = ... AND c.oid = gpt.relid
//
// against the goopg publisher during CREATE SUBSCRIPTION. The SRF arg
// `p.pubname` is an outer column reference from the FROM list's left
// sibling — the planner threads the partial FROM context as a LATERAL
// scope so the arg resolves and `gpt.attrs` (the SRF's static column)
// is reachable at the top-level target list.
func TestPlanLateralSrfArgResolvesAgainstLeftFromItem(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "pg_publication"}, []catalog.Column{
		{Name: "pubname", Type: catalog.Type{Name: "text"}, Ordinal: 0},
	}); err != nil {
		t.Fatal(err)
	}
	sql := `SELECT gpt.attrs FROM pg_publication p, LATERAL pg_get_publication_tables(p.pubname) gpt`
	plan, err := Plan(parseOne(t, sql), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out := plan.Output()
	if len(out) != 1 {
		t.Fatalf("output cols = %d, want 1", len(out))
	}
	if got := out[0].Name; got != "attrs" {
		t.Errorf("output col name = %q, want %q", got, "attrs")
	}
}

// TestPlanVerifyHeapamLateralArgResolvesAgainstLeftFromItem pins the
// M0110-0003 gap #6 fix: pg_amcheck builds each per-relation heap check as
// an implicit-LATERAL comma-join
//
//	... FROM pg_catalog.pg_class c, verify_heapam(relation := c.oid, …) v
//
// where the SRF's first argument is the correlated reference `c.oid` into the
// left sibling. Before the fix planVerifyHeapam resolved its args against an
// empty context, so `c.oid` raised "column oid does not exist" at plan time.
// Now the args resolve against the lateral outer context and the wrapping Join
// is marked Lateral so the executor drives the SRF per outer row (mirrors
// TestPlanLateralSrfArgResolvesAgainstLeftFromItem for pg_get_publication_tables).
func TestPlanVerifyHeapamLateralArgResolvesAgainstLeftFromItem(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "rels"}, []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
	}); err != nil {
		t.Fatal(err)
	}
	sql := `SELECT v.blkno FROM rels c, verify_heapam(relation := c.oid) v`
	plan, err := Plan(parseOne(t, sql), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out := plan.Output()
	if len(out) != 1 || !strings.EqualFold(out[0].Name, "blkno") {
		t.Fatalf("expected single output column 'blkno', got %+v", out)
	}
	// The wrapping Join must be flagged Lateral so the executor drives the SRF
	// per outer row (BindLateralOuter), not as a materialise-both-sides cross
	// join (which would evaluate the correlated arg against a nil outer slot).
	if !planTreeHasLateralJoin(plan) {
		t.Fatalf("expected a Lateral Join in the plan tree, got none:\n%s", plan)
	}
}

// planTreeHasLateralJoin reports whether any Join in the plan tree has
// Lateral == true. Helper for TestPlanVerifyHeapamLateralArgResolvesAgainstLeftFromItem.
func planTreeHasLateralJoin(n Node) bool {
	switch x := n.(type) {
	case *Join:
		if x.Lateral {
			return true
		}
		return planTreeHasLateralJoin(x.Left) || planTreeHasLateralJoin(x.Right)
	case *Project:
		return planTreeHasLateralJoin(x.Child)
	}
	return false
}

// findLateralJoin returns the first Lateral Join in the plan tree, or nil.
func findLateralJoin(n Node) *Join {
	switch x := n.(type) {
	case *Join:
		if x.Lateral {
			return x
		}
		if j := findLateralJoin(x.Left); j != nil {
			return j
		}
		return findLateralJoin(x.Right)
	case *Project:
		return findLateralJoin(x.Child)
	case *Filter:
		return findLateralJoin(x.Child)
	}
	return nil
}

// exprMentionsColumnName reports whether e contains a named ColumnRef for name.
func exprMentionsColumnName(e Expr, name string) bool {
	found := false
	visitColumnRefsByName(e, func(n string) {
		if strings.EqualFold(n, name) {
			found = true
		}
	})
	return found
}

// TestPlanOuterQualPushedBelowLateralJoin pins the lateral outer-qual
// pushdown that unblocks pg_amcheck's relation-scoped heap probes. The
// pg_amcheck heap command is
//
//	SELECT v.* FROM pg_catalog.pg_class c, verify_heapam(relation := c.oid) v
//	  WHERE c.oid = N
//
// where the WHERE restricts only the outer (left) relation. Without the
// pushdown, the residual Filter sits ABOVE the lateral nested-loop, so
// verify_heapam is opened for EVERY pg_class row and raises "could not open
// relation" on the first non-heap relation (an index/sequence OID) before the
// filter can drop it. pushOuterQualsIntoLaterals moves the outer-only qual onto
// the join's outer child so the SRF is only opened for the matching relation —
// matching PostgreSQL's nested-loop qual placement. M0110-0003.
func TestPlanOuterQualPushedBelowLateralJoin(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "rels"}, []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
	}); err != nil {
		t.Fatal(err)
	}
	sql := `SELECT v.blkno FROM rels c, verify_heapam(relation := c.oid) v WHERE c.oid = 16404`
	plan, err := Plan(parseOne(t, sql), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	j := findLateralJoin(plan)
	if j == nil {
		t.Fatalf("expected a Lateral Join in the plan, got none:\n%s", plan)
	}
	// The outer-only qual must now live as a Filter on the join's LEFT child,
	// so the lateral RHS is opened only for outer rows that pass it.
	lf, ok := j.Left.(*Filter)
	if !ok {
		t.Fatalf("lateral Join.Left is %T, want *Filter carrying the pushed qual:\n%s", j.Left, plan)
	}
	if !exprMentionsColumnName(lf.Predicate, "oid") {
		t.Errorf("pushed Filter predicate does not mention oid: %v", lf.Predicate)
	}
	// And it must no longer remain in a Filter ABOVE the lateral join (which is
	// where it would force per-outer-row evaluation of the SRF).
	if f, ok := plan.(*Filter); ok && exprMentionsColumnName(f.Predicate, "oid") {
		t.Errorf("oid qual still sits above the lateral join: %v", f.Predicate)
	}
	if p, ok := plan.(*Project); ok {
		if f, ok := p.Child.(*Filter); ok && exprMentionsColumnName(f.Predicate, "oid") {
			t.Errorf("oid qual still sits above the lateral join (under Project): %v", f.Predicate)
		}
	}
}

// TestPlanFetchTableListAggDerivedSubquery pins the M0103-0008 rung-7
// gap surfaced by dropping the t.Skip on
// `internal/testport/pgoutput_interop_test.go::TestPort_PgoutputInteropGoopgToPG`.
//
// libpqrcv's `fetch_table_list` ships the same SRF call wrapped in a
// derived subquery whose argument list is an aggregate:
//
//	SELECT … gpt.attrs FROM …
//	  JOIN ( SELECT (pg_get_publication_tables(VARIADIC
//	         array_agg(pubname::text))).*
//	         FROM pg_publication WHERE pubname IN (…)) AS gpt …
//
// The non-aggregate IndirectionStar variant is rewritten at parse time
// into a FROM-clause TableFuncRef + `__irs_0.*` target — the analyzer's
// `tableFuncColumns` (loop 4) hands the outer scope the SRF's static
// three-column shape and outer references resolve. The aggregate-arg
// variant skips the parse-time rewrite (parser passes nil
// `onAggregate`) and the planner lowers it via `ProjectSet` (loop 5).
// `synthesizeSubqueryTable` does not yet expand `*parser.IndirectionStar`
// targets — it falls back to `?column?1` so outer references like
// `gpt.attrs` raise `42703: column "attrs" does not exist`.
//
// The Skip stays until the analyzer expansion lands; flip it to a
// positive plan-and-output assertion in the next M0103-0008 loop.
func TestPlanFetchTableListAggDerivedSubquery(t *testing.T) {
	// M0103-0008 rung 7: synthesizeSubqueryTable expands
	// `(srf(<agg>)).*` derived-subquery targets so outer references like
	// `gpt.attrs` resolve.
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "pg_publication"}, []catalog.Column{
		{Name: "pubname", Type: catalog.Type{Name: "text"}, Ordinal: 0},
	}); err != nil {
		t.Fatal(err)
	}
	sql := `SELECT gpt.attrs FROM ( SELECT (pg_get_publication_tables(VARIADIC array_agg(pubname::text))).* FROM pg_publication WHERE pubname IN ('p')) AS gpt`
	plan, err := Plan(parseOne(t, sql), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out := plan.Output()
	if len(out) != 1 || !strings.EqualFold(out[0].Name, "attrs") {
		t.Fatalf("expected single output column 'attrs', got %+v", out)
	}
}

// TestAggregateFilterDistinguishedInDedupKey pins the M0097-0032 fix:
// aggregateCallKey must fold the FILTER (WHERE ...) predicate into the
// dedup key. Without it, `count(*)` and `count(*) FILTER (WHERE p)` (and two
// filters differing only by IS NULL vs IS NOT NULL) collapsed onto a single
// aggregate slot, so the filtered counts silently reported the unfiltered
// total — the sysviews pg_hba_file_rules `no_err` query was the symptom.
func TestAggregateFilterDistinguishedInDedupKey(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "e", Type: catalog.Type{Name: "text"}, Ordinal: 1},
	}); err != nil {
		t.Fatal(err)
	}
	sql := `SELECT count(*), ` +
		`count(*) FILTER (WHERE id > 1), ` +
		`count(*) FILTER (WHERE e IS NULL), ` +
		`count(*) FILTER (WHERE e IS NOT NULL) FROM t`
	plan, err := Plan(parseOne(t, sql), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var agg *Aggregate
	var find func(n Node)
	find = func(n Node) {
		switch x := n.(type) {
		case *Aggregate:
			agg = x
		case *Project:
			find(x.Child)
		case *Filter:
			find(x.Child)
		case *Sort:
			find(x.Child)
		}
	}
	find(plan)
	if agg == nil {
		t.Fatalf("no Aggregate node in plan %T", plan)
	}
	// Four distinct count(*) aggregates: one bare + three differently
	// filtered. A collapsed key would yield fewer slots.
	if len(agg.Aggs) != 4 {
		t.Fatalf("got %d aggregate slots, want 4 (bare + 3 distinct FILTERs): %+v", len(agg.Aggs), agg.Aggs)
	}
	nFiltered := 0
	for _, a := range agg.Aggs {
		if a.Filter != nil {
			nFiltered++
		}
	}
	if nFiltered != 3 {
		t.Errorf("got %d filtered aggregates, want 3", nFiltered)
	}
}

// TestPromoteIntTypeSerialFamily verifies that the SERIAL pseudo-types promote
// as their integer base in arithmetic result-type inference: serial→int4,
// bigserial→int8, smallserial→int2. Regression for `serial_col + 1` producing
// a wrong (or "unknown") result type because isIntegerLikeType / promoteIntType
// did not recognise the SERIAL aliases.
func TestPromoteIntTypeSerialFamily(t *testing.T) {
	for _, name := range []string{"serial", "bigserial", "smallserial"} {
		if !isIntegerLikeType(name) {
			t.Errorf("isIntegerLikeType(%q) = false, want true", name)
		}
	}
	cases := []struct {
		a, b string
		want string
	}{
		{"serial", "serial", "int4"},
		{"serial", "int8", "int8"},
		{"smallserial", "smallserial", "int2"},
		{"smallserial", "serial", "int4"},
		{"bigserial", "int4", "int8"},
		{"bigserial", "serial", "int8"},
		{"int2", "smallserial", "int2"},
	}
	for _, tc := range cases {
		if got := promoteIntType(tc.a, tc.b); got.Name != tc.want {
			t.Errorf("promoteIntType(%q, %q) = %q, want %q", tc.a, tc.b, got.Name, tc.want)
		}
	}
}
