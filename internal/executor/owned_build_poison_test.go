package executor

// EX1-04 Cut 1 (test-only): poison-tripwire pins for the Project-narrowed
// hash-build shape.
//
// P4-01 slices 1-3 landed: the planner narrows hash build inputs with a
// Project (Q9 10->7 cols, Batches 2->1). EX1-04's original `[0,bound)`
// truncation mechanism is SUPERSEDED — this file implements NO narrowing.
// It ports only the `poisonDeformTail`/`checkDeformPoison` DEBUG idea
// (docs/design/executor-ex1-04-owned/DESIGN.md §2) to the build shape that
// actually landed: retained hash-build rows must match the narrowed schema
// at the enumerated readers (join keys, residual, everything above).
//
// Reuse (checked before writing — no duplicates defined here):
//   - poisonDeformTail / checkDeformPoison / isDeformPoison /
//     seqScanDeformPoison from scan_deform.go (EX1-01 tail-poison);
//   - ownedBuildRow from operators_join_agg.go (retention copy);
//   - renderDeformRows / testPlanDeform from scan_deform_bound_test.go;
//   - runDDL / newDDLFixture / runQueryWithErr from sibling test files.
// Guards pinned (internal/optimizer/narrowoutput.go): hash-only kind
// refusal, coordinate-identity precondition, NeededColsKnown-false decline
// (unknown-target fallback), corrAbove decline, prebuilt-boundary leaves —
// all observed through the public optimizer.Plan API, since the helpers
// themselves are unexported from this package.

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// ---------------------------------------------------------------------------
// Plan walkers (explicit type switch; unknown wrappers are reported so a
// "zero narrow builds" assertion can never pass vacuously).
// ---------------------------------------------------------------------------

// obpUnknown records wrapper types the walker does not understand.
type obpUnknown []string

// obpWalk visits every node in the plan tree. Unknown (possibly
// child-bearing) node types are appended to unknown instead of being
// silently skipped.
func obpWalk(n optimizer.Node, visit func(optimizer.Node), unknown *obpUnknown) {
	seen := make(map[optimizer.Node]struct{})
	var rec func(x optimizer.Node)
	rec = func(x optimizer.Node) {
		if x == nil {
			return
		}
		if _, dup := seen[x]; dup {
			return
		}
		seen[x] = struct{}{}
		visit(x)
		switch t := x.(type) {
		case *optimizer.Join:
			rec(t.Left)
			rec(t.Right)
		case *optimizer.Project:
			rec(t.Child)
		case *optimizer.Filter:
			rec(t.Child)
		case *optimizer.Sort:
			rec(t.Child)
		case *optimizer.Limit:
			rec(t.Child)
		case *optimizer.Aggregate:
			rec(t.Child)
		case *optimizer.Result:
			rec(t.Child)
		case *optimizer.Distinct:
			rec(t.Child)
		case *optimizer.DistinctOn:
			rec(t.Child)
		case *optimizer.ProjectSet:
			rec(t.Child)
		case *optimizer.WindowAgg:
			rec(t.Child)
		case *optimizer.Gather:
			rec(t.Child)
		case *optimizer.SetOp:
			rec(t.Left)
			rec(t.Right)
		case *optimizer.CTEScan:
			rec(t.Child)
		case *optimizer.Memoize:
			rec(t.Child)
		case *optimizer.NestedLoopIndexJoin:
			rec(t.Outer)
			rec(t.Inner)
		case *optimizer.BitmapHeapScan:
			rec(t.Outer)
		case *optimizer.SeqScan, *optimizer.IndexScan, *optimizer.IndexOnlyScan,
			*optimizer.Values, *optimizer.WorkTableScan, *optimizer.BitmapIndexScan:
			// Leaves: nothing to descend into.
		default:
			*unknown = append(*unknown, fmt.Sprintf("%T", x))
		}
	}
	rec(n)
}

// obpNarrowBuild records a build-side narrowing Project with its parent join.
type obpNarrowBuild struct {
	proj *optimizer.Project
	algo optimizer.JoinAlgo
}

// obpIsNarrowBuild reports whether p is a build-side narrowing Project:
// strictly narrower than its child and renaming nothing (a narrowing
// Project keeps a name-subset of its child's schema). Mirrors the
// Slice-3 collector in the optimizer's pathtarget tests.
func obpIsNarrowBuild(p *optimizer.Project) bool {
	if p == nil || p.Child == nil {
		return false
	}
	if len(p.Output()) >= len(p.Child.Output()) {
		return false
	}
	childNames := make(map[string]bool, len(p.Child.Output()))
	for _, c := range p.Child.Output() {
		childNames[c.Name] = true
	}
	for _, c := range p.Output() {
		if !childNames[c.Name] {
			return false
		}
	}
	return true
}

// obpNarrowBuilds collects every narrowing Project that is a direct input
// of a *Join, optionally skipping the subtree rooted at skip. Every hit
// must sit under a hash join (the hash-only kind refusal, observed).
func obpNarrowBuilds(n, skip optimizer.Node, unknown *obpUnknown) []obpNarrowBuild {
	var out []obpNarrowBuild
	obpWalk(n, func(x optimizer.Node) {
		j, ok := x.(*optimizer.Join)
		if !ok {
			return
		}
		for _, side := range []optimizer.Node{j.Left, j.Right} {
			if skip != nil && side == skip {
				continue
			}
			if p, isProj := side.(*optimizer.Project); isProj && obpIsNarrowBuild(p) {
				out = append(out, obpNarrowBuild{proj: p, algo: j.Algo})
			}
		}
	}, unknown)
	return out
}

// obpFindJoins returns every *Join in the tree (nil-safe).
func obpFindJoins(n optimizer.Node, unknown *obpUnknown) []*optimizer.Join {
	var out []*optimizer.Join
	obpWalk(n, func(x optimizer.Node) {
		if j, ok := x.(*optimizer.Join); ok {
			out = append(out, j)
		}
	}, unknown)
	return out
}

// obpFindLateral returns the first lateral *Join in the tree, if any.
func obpFindLateral(n optimizer.Node, unknown *obpUnknown) *optimizer.Join {
	for _, j := range obpFindJoins(n, unknown) {
		if j.Lateral {
			return j
		}
	}
	return nil
}

// obpProjectNames returns a Project's output column names in order.
func obpProjectNames(p *optimizer.Project) []string {
	out := p.Output()
	got := make([]string, len(out))
	for i, c := range out {
		got[i] = c.Name
	}
	return got
}

// obpNameSet returns the output names as a set.
func obpNameSet(p *optimizer.Project) map[string]bool {
	dst := make(map[string]bool, len(p.Output()))
	for _, c := range p.Output() {
		dst[c.Name] = true
	}
	return dst
}

// obpSetEqual reports whether got holds exactly the want names.
func obpSetEqual(got map[string]bool, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !got[w] {
			return false
		}
	}
	return true
}

// obpPlan parses and plans one SQL statement against cat.
func obpPlan(t *testing.T, cat catalog.Catalog, sql string) optimizer.Node {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("parse: %d stmts, want 1", len(stmts))
	}
	plan, err := optimizer.Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

// obpMustPanic runs fn and fails unless it panics (the tripwire contract:
// an out-of-schema read must fail loudly, never silently).
func obpMustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic reading a dropped build column, got none", name)
		}
	}()
	fn()
}

// obpAssertHashOnly fails when any narrowing build sits under a non-hash
// join (the hash-only kind refusal, observed from the executor side:
// narrowing a streaming merge input would pay the projection for no
// memory saving, and NL/probe internals are uninventoried).
func obpAssertHashOnly(t *testing.T, builds []obpNarrowBuild) {
	t.Helper()
	for _, b := range builds {
		if b.algo != optimizer.JoinAlgoHash {
			t.Errorf("narrow build %v under join algo %v, want hash-only",
				obpProjectNames(b.proj), b.algo)
		}
	}
}

// obpNoUnknown fails when the walker met a wrapper it cannot see through.
func obpNoUnknown(t *testing.T, unknown obpUnknown) {
	t.Helper()
	if len(unknown) != 0 {
		t.Fatalf("walker met unknown node types %v; narrow-build assertions would be vacuous", []string(unknown))
	}
}

// ---------------------------------------------------------------------------
// Catalog fixtures (mirror the optimizer Slice-3 fixtures so the same
// derivation runs; stats steer the search to hash joins).
// ---------------------------------------------------------------------------

func obpMkTable(t *testing.T, c catalog.Catalog, name string, rows int64, cols ...string) {
	t.Helper()
	cc := make([]catalog.Column, len(cols))
	for i, cn := range cols {
		ty := "int4"
		switch cn {
		case "p_name", "o_orderpriority", "n_name":
			ty = "text"
		case "o_orderdate":
			ty = "date"
		case "pad":
			ty = "text"
		}
		cc[i] = catalog.Column{Name: cn, Type: catalog.Type{Name: ty}}
	}
	tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cc)
	if err != nil {
		t.Fatal(err)
	}
	c.SetTableStats(tbl, &catalog.TableStats{RowCount: rows, Pages: int(rows / 100), Analyzed: true})
}

// obpWidePair is a two-table wide-schema fixture: SELECT * needs every
// column (identity-decline), while a selective projection narrows 10->~2.
func obpWidePair(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	obpMkTable(t, c, "t1", 300_000, "k", "x", "f1", "f2", "g1", "g2", "h1", "h2", "h3", "h4")
	obpMkTable(t, c, "t2", 500_000, "k", "y", "p1", "p2", "q1", "q2", "r1", "r2", "r3", "r4")
	return c
}

// obpLateralCatalog mirrors the Slice-3 lateral fixture: the outer
// statement's needed set is unknown (lateral rangevar declines the
// collector) while the correlated body is corrAbove-marked.
func obpLateralCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	obpMkTable(t, c, "supplier", 10_000, "s_suppkey", "s_name", "s_address", "s_nationkey", "s_phone", "s_acctbal", "s_comment")
	obpMkTable(t, c, "nation", 500_000, "n_nationkey", "n_name", "n_regionkey", "n_comment")
	obpMkTable(t, c, "lineitem", 6_000_000, "l_orderkey", "l_partkey", "l_suppkey", "l_linenumber", "l_quantity", "l_extendedprice", "l_discount", "l_tax", "l_returnflag", "l_linestatus", "l_shipdate", "l_commitdate", "l_receiptdate", "l_shipinstruct", "l_shipmode", "l_comment")
	obpMkTable(t, c, "orders", 1_500_000, "o_orderkey", "o_custkey", "o_orderstatus", "o_totalprice", "o_orderdate", "o_orderpriority", "o_clerk", "o_shippriority", "o_comment")
	return c
}

const obpLateralSQL = `select s_name, n_name, dt.o, dt.od from supplier s, nation n, lateral (select l_orderkey as o, o_orderdate as od from lineitem l, orders o where l_orderkey = o_orderkey and l_suppkey = s.s_suppkey and o_orderpriority = '1-URGENT') dt where s_nationkey = n_nationkey`

// obpQ9Catalog mirrors the Slice-3 Q9 fixture: leaf-local LIKE filters
// force prebuilt leaves, over which the derivation still applies.
func obpQ9Catalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	rows := map[string]int64{
		"part": 200_000, "supplier": 10_000, "lineitem": 6_000_000,
		"partsupp": 800_000, "orders": 1_500_000, "nation": 25,
	}
	mk := func(name string, cols ...string) {
		t.Helper()
		cc := make([]catalog.Column, len(cols))
		for i, cn := range cols {
			ty := "int4"
			switch cn {
			case "p_name":
				ty = "text"
			case "o_orderdate":
				ty = "date"
			}
			cc[i] = catalog.Column{Name: cn, Type: catalog.Type{Name: ty}}
		}
		tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cc)
		if err != nil {
			t.Fatal(err)
		}
		c.SetTableStats(tbl, &catalog.TableStats{RowCount: rows[name], Pages: int(rows[name] / 100), Analyzed: true})
	}
	mk("part", "p_partkey", "p_name", "p_mfgr", "p_brand", "p_type", "p_size", "p_container", "p_retailprice", "p_comment")
	mk("supplier", "s_suppkey", "s_name", "s_address", "s_nationkey", "s_phone", "s_acctbal", "s_comment")
	mk("lineitem", "l_orderkey", "l_partkey", "l_suppkey", "l_linenumber", "l_quantity", "l_extendedprice", "l_discount", "l_tax", "l_returnflag", "l_linestatus", "l_shipdate", "l_commitdate", "l_receiptdate", "l_shipinstruct", "l_shipmode", "l_comment")
	mk("partsupp", "ps_partkey", "ps_suppkey", "ps_availqty", "ps_supplycost", "ps_comment")
	mk("orders", "o_orderkey", "o_custkey", "o_orderstatus", "o_totalprice", "o_orderdate", "o_orderpriority", "o_clerk", "o_shippriority", "o_comment")
	mk("nation", "n_nationkey", "n_name", "n_regionkey", "n_comment")
	return c
}

const obpQ9InnerSQL = `select n_name as nation, extract(year from o_orderdate) as o_year, l_extendedprice * (1 - l_discount) - ps_supplycost * l_quantity as amount from part, supplier, lineitem, partsupp, orders, nation where s_suppkey = l_suppkey and ps_suppkey = l_suppkey and ps_partkey = l_partkey and p_partkey = l_partkey and o_orderkey = l_orderkey and s_nationkey = n_nationkey and p_name like '%green%'`

// obpReuseOp is a buffer-reusing build child: every Next returns a slot
// over ONE shared buffer, so a retained row that aliases the child buffer
// is clobbered by the following Next (the M0097-0058 aliasing class).
type obpReuseOp struct {
	schema optimizer.Schema
	rows   []Row
	i      int
	buf    Row
}

func (o *obpReuseOp) Open(*Context) error      { o.i = 0; return nil }
func (o *obpReuseOp) Schema() optimizer.Schema { return o.schema }
func (o *obpReuseOp) Close() error             { return nil }

func (o *obpReuseOp) Next() (TupleSlot, error) { //nolint:ireturn
	if o.i >= len(o.rows) {
		return nil, EOF
	}
	if o.buf == nil {
		o.buf = make(Row, len(o.rows[o.i]))
	}
	copy(o.buf, o.rows[o.i])
	o.i++
	return SlotFromRow(o.schema, o.buf), nil
}

// ---------------------------------------------------------------------------
// (a) Retained build rows match the narrowed schema at enumerated readers.
// ---------------------------------------------------------------------------

// TestOwnedBuildPoisonNarrowedWidths ports the poisonDeformTail idea to the
// Project-narrowed build shape: the dropped columns are stamped poison on
// the wide row, the retained (narrowed + owned) rows must carry none of it,
// and every enumerated reader over a retained row stays quiet while any
// dropped-column read panics. The build loop half drives buildLoopRight
// over a buffer-reusing child and asserts every retained hash row has the
// narrowed width; the live half runs a real narrowed join poison-armed with
// identical values.
func TestOwnedBuildPoisonNarrowedWidths(t *testing.T) {
	old := seqScanDeformPoison
	seqScanDeformPoison = true
	defer func() { seqScanDeformPoison = old }()

	// 10-wide source row; the narrowing keeps 7 (drops 2, 3, 7 — mid-row
	// drops, proving this is not a tail cut).
	wide := make(Row, 10)
	for i := range wide {
		wide[i] = NewIntDatum(int64(100 + i))
	}
	keep := []int{0, 1, 4, 5, 6, 8, 9}
	dropped := []int{2, 3, 7}
	poisonDeformTail(wide[2:4], 0)
	poisonDeformTail(wide[7:8], 0)
	for _, d := range dropped {
		if !isDeformPoison(wide[d]) {
			t.Fatalf("wide slot %d not poisoned", d)
		}
	}

	// The Project-narrowed row: only kept positions survive.
	narrowed := make(Row, len(keep))
	for i, c := range keep {
		narrowed[i] = wide[c]
	}
	if len(narrowed) != 7 {
		t.Fatalf("narrowed width = %d, want 7", len(narrowed))
	}
	for i, d := range narrowed {
		if isDeformPoison(d) {
			t.Fatalf("narrowed slot %d carries poison; a dropped column leaked into the retained row", i)
		}
	}
	// Enumerated readers (every kept position) stay quiet under the flag.
	for i, d := range narrowed {
		func() {
			defer func() {
				if recover() != nil {
					t.Errorf("enumerated reader %d panicked on a clean retained slot", i)
				}
			}()
			checkDeformPoison(d)
		}()
	}
	// Dropped-column reads trip the wire, both raw and through evaluation.
	for _, d := range dropped {
		obpMustPanic(t, fmt.Sprintf("raw dropped slot %d", d), func() {
			checkDeformPoison(wide[d])
		})
		obpMustPanic(t, fmt.Sprintf("evaluated dropped slot %d", d), func() {
			_, _ = evalExprSlot(&optimizer.ColumnRef{Index: d}, rowSlotView(wide), NewContext())
		})
	}

	// ownedBuildRow preserves the narrowed width with owned storage.
	owned := ownedBuildRow(narrowed)
	if len(owned) != len(narrowed) {
		t.Fatalf("owned width = %d, want narrowed %d", len(owned), len(narrowed))
	}
	for i := range owned {
		if owned[i].Int != narrowed[i].Int {
			t.Fatalf("owned slot %d = %v, want %v", i, owned[i], narrowed[i])
		}
	}
	narrowed[0] = NewIntDatum(-1)
	if owned[0].Int == -1 {
		t.Fatal("owned build row aliases the child buffer; the next Next would clobber it")
	}

	// Build-loop half: narrowed (width-3) rows through a buffer-reusing
	// child; every retained hash row must have the narrowed width.
	const leftWidth = 2
	child := &obpReuseOp{rows: []Row{
		{NewIntDatum(1), NewStringDatum("one"), NewIntDatum(11)},
		{NewIntDatum(2), NewStringDatum("two"), NewIntDatum(22)},
		{NewIntDatum(3), NewStringDatum("three"), NewIntDatum(33)},
	}}
	jo := &joinOp{
		plan: &optimizer.Join{
			Type:     optimizer.JoinTypeInner,
			Algo:     optimizer.JoinAlgoHash,
			RightKey: &optimizer.ColumnRef{Index: leftWidth},
		},
		right:  child,
		lazyRW: 3,
	}
	if err := child.Open(nil); err != nil {
		t.Fatalf("open child: %v", err)
	}
	if err := jo.buildLoopRight(nil, leftWidth); err != nil {
		t.Fatalf("buildLoopRight: %v", err)
	}
	if len(jo.lazyHash) != 3 {
		t.Fatalf("hash table has %d keys, want 3", len(jo.lazyHash))
	}
	for k, rows := range jo.lazyHash {
		for _, r := range rows {
			if len(r) != 3 {
				t.Fatalf("key %q: retained width = %d, want narrowed 3", k, len(r))
			}
			for i, d := range r {
				if isDeformPoison(d) {
					t.Fatalf("key %q: retained slot %d carries poison", k, i)
				}
			}
			// Enumerated readers over the retained row stay quiet.
			for i := range r {
				i := i
				func() {
					defer func() {
						if recover() != nil {
							t.Errorf("key %q: enumerated reader %d panicked", k, i)
						}
					}()
					_, _ = evalExprSlot(&optimizer.ColumnRef{Index: i}, rowSlotView(r), NewContext())
				}()
			}
		}
	}

	// Live half: a real 10-wide join narrowed to key+payload, run
	// poison-armed; values must equal the unpoisoned run.
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, `CREATE TABLE obp_big(k int, a int, b int, c int, d int, e int, f int, g int, h int, pad text)`); err != nil {
		t.Fatalf("create obp_big: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE obp_small(k int, x int, y1 int, y2 int, y3 int, z1 int, z2 int, z3 int, w int, pad text)`); err != nil {
		t.Fatalf("create obp_small: %v", err)
	}
	for i := 1; i <= 20; i++ {
		if err := runDDL(t, ctx, fmt.Sprintf(`INSERT INTO obp_big VALUES (%d, %d, %d, 0,0,0,0,0,0,'p%d')`, i, 100+i, 200+i, i)); err != nil {
			t.Fatalf("insert big: %v", err)
		}
		if err := runDDL(t, ctx, fmt.Sprintf(`INSERT INTO obp_small VALUES (%d, %d, 0,0,0,0,0,0,0,'q%d')`, i, 1000+i, i)); err != nil {
			t.Fatalf("insert small: %v", err)
		}
	}
	for _, name := range []string{"obp_big", "obp_small"} {
		tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: name})
		if !ok {
			t.Fatalf("table %s not in catalog", name)
		}
		ctx.Catalog.SetTableStats(tbl, &catalog.TableStats{RowCount: 400_000, Pages: 4000, Analyzed: true})
	}
	const liveSQL = `SELECT b.a, s.x FROM obp_big b, obp_small s WHERE b.k = s.k ORDER BY b.a`
	plan, err := testPlanDeform(t, ctx, liveSQL)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var unknown obpUnknown
	joins := obpFindJoins(plan, &unknown)
	obpNoUnknown(t, unknown)
	hashes := 0
	for _, j := range joins {
		if j.Algo == optimizer.JoinAlgoHash {
			hashes++
		}
	}
	if hashes == 0 {
		t.Fatalf("no hash join in the live plan; the narrowed-build shape under test is gone")
	}
	builds := obpNarrowBuilds(plan, nil, &unknown)
	obpNoUnknown(t, unknown)
	if len(builds) == 0 {
		t.Fatal("no narrowing build Project in the live plan; want the 10->~2 build narrow")
	}
	obpAssertHashOnly(t, builds)
	for _, b := range builds {
		if len(b.proj.Output()) >= len(b.proj.Child.Output()) {
			t.Errorf("build %v not narrower than its child", obpProjectNames(b.proj))
		}
		t.Logf("live narrow build %v (child width %d)", obpProjectNames(b.proj), len(b.proj.Child.Output()))
	}
	poisonRows, err := runQueryWithErr(ctx, liveSQL)
	if err != nil {
		t.Fatalf("poison-armed run: %v", err)
	}
	seqScanDeformPoison = false
	plainRows, err := runQueryWithErr(ctx, liveSQL)
	if err != nil {
		t.Fatalf("plain run: %v", err)
	}
	seqScanDeformPoison = true
	if len(poisonRows) == 0 {
		t.Fatal("poison-armed run returned 0 rows; the live half proves nothing")
	}
	if got, want := renderDeformRows(poisonRows), renderDeformRows(plainRows); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("poison-armed rows %v != plain rows %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// (b) Identity-decline path: SELECT * needs every column, so the build
// keeps the full width and narrowPlanOutput must decline (no Project).
// ---------------------------------------------------------------------------

// TestOwnedBuildPoisonIdentityDecline pins the identity-decline path: a
// SELECT * hash join keeps the full build width, so no narrowing Project
// may appear under any join, and a full-width retained row copies full
// width (passthrough, not truncation).
func TestOwnedBuildPoisonIdentityDecline(t *testing.T) {
	plan := obpPlan(t, obpWidePair(t), `select * from t1 a, t2 b where a.k = b.k`)
	var unknown obpUnknown
	joins := obpFindJoins(plan, &unknown)
	obpNoUnknown(t, unknown)
	hashes := 0
	for _, j := range joins {
		if j.Algo == optimizer.JoinAlgoHash {
			hashes++
		}
	}
	if hashes == 0 {
		t.Fatal("no hash join in the SELECT * plan; the identity case under test is gone")
	}
	if builds := obpNarrowBuilds(plan, nil, &unknown); len(builds) != 0 {
		obpNoUnknown(t, unknown)
		for _, b := range builds {
			t.Errorf("unexpected narrow build %v under full-width SELECT *; identity must decline",
				obpProjectNames(b.proj))
		}
	}
	obpNoUnknown(t, unknown)

	// Full-width retained rows copy full width.
	full := make(Row, 10)
	for i := range full {
		full[i] = NewIntDatum(int64(i))
	}
	if got := ownedBuildRow(full); len(got) != 10 {
		t.Fatalf("identity owned width = %d, want 10 (passthrough)", len(got))
	}
}

// ---------------------------------------------------------------------------
// (c) Unknown-target fallback: the lateral outer statement's needed set is
// unknown, so no outer build may narrow.
// ---------------------------------------------------------------------------

// TestOwnedBuildPoisonUnknownTargetFallback pins the unknown-target
// fallback: a lateral rangevar declines the needed collector
// (NeededColsKnown false), so the outer statement falls back
// bit-identically — no narrowing Project outside the lateral body.
func TestOwnedBuildPoisonUnknownTargetFallback(t *testing.T) {
	plan := obpPlan(t, obpLateralCatalog(t), obpLateralSQL)
	var unknown obpUnknown
	lat := obpFindLateral(plan, &unknown)
	obpNoUnknown(t, unknown)
	if lat == nil {
		t.Fatal("no Lateral join in the plan; the shape under test is gone")
	}
	outer := obpNarrowBuildsExcept(plan, lat.Right, &unknown)
	obpNoUnknown(t, unknown)
	if len(outer) != 0 {
		for _, b := range outer {
			t.Errorf("outer narrow build %v outside the lateral body; unknown outer targets must fall back",
				obpProjectNames(b.proj))
		}
	}
}

// obpNarrowBuildsExcept collects narrowing join-input Projects everywhere
// except inside the subtree rooted at skip.
func obpNarrowBuildsExcept(n, skip optimizer.Node, unknown *obpUnknown) []obpNarrowBuild {
	var out []obpNarrowBuild
	obpWalkExcept(n, skip, func(x optimizer.Node) {
		j, ok := x.(*optimizer.Join)
		if !ok {
			return
		}
		for _, side := range []optimizer.Node{j.Left, j.Right} {
			if p, isProj := side.(*optimizer.Project); isProj && obpIsNarrowBuild(p) {
				out = append(out, obpNarrowBuild{proj: p, algo: j.Algo})
			}
		}
	}, unknown)
	return out
}

// obpWalkExcept is obpWalk rooted at n but never descending into skip.
func obpWalkExcept(n, skip optimizer.Node, visit func(optimizer.Node), unknown *obpUnknown) {
	if n == nil || n == skip {
		return
	}
	seen := map[optimizer.Node]struct{}{skip: {}}
	var rec func(x optimizer.Node)
	rec = func(x optimizer.Node) {
		if x == nil {
			return
		}
		if _, dup := seen[x]; dup {
			return
		}
		seen[x] = struct{}{}
		visit(x)
		switch t := x.(type) {
		case *optimizer.Join:
			rec(t.Left)
			rec(t.Right)
		case *optimizer.Project:
			rec(t.Child)
		case *optimizer.Filter:
			rec(t.Child)
		case *optimizer.Sort:
			rec(t.Child)
		case *optimizer.Limit:
			rec(t.Child)
		case *optimizer.Aggregate:
			rec(t.Child)
		case *optimizer.Result:
			rec(t.Child)
		case *optimizer.Distinct:
			rec(t.Child)
		case *optimizer.DistinctOn:
			rec(t.Child)
		case *optimizer.ProjectSet:
			rec(t.Child)
		case *optimizer.WindowAgg:
			rec(t.Child)
		case *optimizer.Gather:
			rec(t.Child)
		case *optimizer.SetOp:
			rec(t.Left)
			rec(t.Right)
		case *optimizer.CTEScan:
			rec(t.Child)
		case *optimizer.Memoize:
			rec(t.Child)
		case *optimizer.NestedLoopIndexJoin:
			rec(t.Outer)
			rec(t.Inner)
		case *optimizer.BitmapHeapScan:
			rec(t.Outer)
		case *optimizer.SeqScan, *optimizer.IndexScan, *optimizer.IndexOnlyScan,
			*optimizer.Values, *optimizer.WorkTableScan, *optimizer.BitmapIndexScan:
		default:
			*unknown = append(*unknown, fmt.Sprintf("%T", x))
		}
	}
	rec(n)
}

// ---------------------------------------------------------------------------
// (d) corrAbove decline: the correlated body narrows by the Slice-2
// (statement-wide) arms only, keeping its leaf-local filter column.
// ---------------------------------------------------------------------------

// TestOwnedBuildPoisonCorrAboveDecline pins the corrAbove gate: the lateral
// body's WHERE reads the outer level, so parent-aware narrowing is declined
// there — the orders build keeps the filter column o_orderpriority (3
// cols), which a parent-aware keep would drop (2).
func TestOwnedBuildPoisonCorrAboveDecline(t *testing.T) {
	plan := obpPlan(t, obpLateralCatalog(t), obpLateralSQL)
	var unknown obpUnknown
	lat := obpFindLateral(plan, &unknown)
	obpNoUnknown(t, unknown)
	if lat == nil {
		t.Fatal("no Lateral join in the plan; the shape under test is gone")
	}
	if lat.Right == nil {
		t.Fatal("lateral body is nil")
	}
	body := obpNarrowBuilds(lat.Right, nil, &unknown)
	obpNoUnknown(t, unknown)
	if len(body) != 1 {
		for _, b := range body {
			t.Logf("body narrow build %v", obpProjectNames(b.proj))
		}
		t.Fatalf("body narrow builds = %d, want 1 (the orders build side)", len(body))
	}
	obpAssertHashOnly(t, body)
	if got := obpNameSet(body[0].proj); !obpSetEqual(got, "o_orderkey", "o_orderdate", "o_orderpriority") {
		t.Errorf("body orders build = %v, want [o_orderkey o_orderdate o_orderpriority] (statement-wide, filter kept)",
			obpProjectNames(body[0].proj))
	}
}

// ---------------------------------------------------------------------------
// (e) Prebuilt-boundary leaves: keeps apply ABOVE the built subtree by
// name; leaf-local filters run below the narrow point.
// ---------------------------------------------------------------------------

// TestOwnedBuildPoisonPrebuiltBoundary pins the prebuilt wake-up: a
// PathPrebuilt leaf (here forced by the p_name LIKE leaf-local filter) is
// a narrowable boundary — the part build still narrows 2->1 above the
// filter, the LIKE runs below on un-narrowed rows, and no narrow build
// above keeps the filtered-away p_name.
func TestOwnedBuildPoisonPrebuiltBoundary(t *testing.T) {
	plan := obpPlan(t, obpQ9Catalog(t), obpQ9InnerSQL)
	var unknown obpUnknown
	builds := obpNarrowBuilds(plan, nil, &unknown)
	obpNoUnknown(t, unknown)
	if len(builds) == 0 {
		t.Fatal("no narrow builds over prebuilt leaves; the derivation went dormant again")
	}
	obpAssertHashOnly(t, builds)
	for _, b := range builds {
		t.Logf("narrow build %v (child width %d)", obpProjectNames(b.proj), len(b.proj.Child.Output()))
	}

	// The LIKE filter runs below every narrow point on un-narrowed rows:
	// exactly one Filter predicate mentions p_name (leaf-local), its child
	// subtree still carries a p_name scan, and no narrow build above keeps
	// it (asserted below).
	likes := 0
	obpWalk(plan, func(x optimizer.Node) {
		f, ok := x.(*optimizer.Filter)
		if !ok || f.Predicate == nil {
			return
		}
		if obpExprMentions(f.Predicate, "p_name") {
			likes++
			if !obpScanBelowCarries(f.Child, "p_name") {
				t.Error("p_name LIKE filter has no p_name-carrying scan below it; the filter must run before narrowing")
			}
		}
	}, &unknown)
	obpNoUnknown(t, unknown)
	if likes != 1 {
		t.Fatalf("p_name filters = %d, want exactly 1 (the leaf-local LIKE)", likes)
	}

	// The filtered-away column drops after filtering, never before.
	partKept := false
	for _, b := range builds {
		got := obpNameSet(b.proj)
		if got["p_name"] {
			t.Errorf("narrow build %v keeps p_name; filter columns drop after filtering",
				obpProjectNames(b.proj))
		}
		if obpSetEqual(got, "p_partkey") {
			partKept = true
		}
	}
	if !partKept {
		t.Error("no [p_partkey]-only part build; want the 2->1 filter-column drop over the prebuilt leaf")
	}
}

// obpExprType / obpNodeType are the reflection handles for the same-scope
// expression walk below.
var (
	obpExprType = reflect.TypeOf((*optimizer.Expr)(nil)).Elem()
	obpNodeType = reflect.TypeOf((*optimizer.Node)(nil)).Elem()
)

// obpWalkSameScopeExpr visits e and every same-scope subexpression, stepping
// over inner plans (Node-typed fields such as SubqueryExpr.Plan) the way the
// optimizer's scopeIgnore policy does: an outer ref sealed inside a subplan
// belongs to that body's own scope. Only exported fields are traversed, and
// every planner Expr under test stores its operands in exported fields.
func obpWalkSameScopeExpr(e optimizer.Expr, visit func(optimizer.Expr)) {
	if e == nil {
		return
	}
	visit(e)
	rv := reflect.ValueOf(e)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return
	}
	obpWalkExprStructFields(rv, visit)
}

// obpWalkExprStructFields recurses into the exported Expr-bearing fields of
// one struct value: Expr fields, []Expr fields, and nested operand structs
// (e.g. []CaseWhen). Node-typed fields (inner plans) are stepped over.
func obpWalkExprStructFields(rv reflect.Value, visit func(optimizer.Expr)) {
	rt := rv.Type()
	for i := 0; i < rv.NumField(); i++ {
		sf := rt.Field(i)
		if sf.PkgPath != "" {
			continue // unexported
		}
		f := rv.Field(i)
		ft := sf.Type
		if ft.Implements(obpNodeType) {
			continue // inner plan: step over
		}
		if ft.Implements(obpExprType) {
			if f.Kind() == reflect.Interface || f.Kind() == reflect.Ptr {
				if f.IsNil() {
					continue
				}
			}
			if ne, ok := f.Interface().(optimizer.Expr); ok && ne != nil {
				obpWalkSameScopeExpr(ne, visit)
			}
			continue
		}
		switch f.Kind() {
		case reflect.Slice, reflect.Array:
			et := ft.Elem()
			switch {
			case et.Implements(obpExprType):
				for j := 0; j < f.Len(); j++ {
					el := f.Index(j)
					if el.Kind() == reflect.Interface || el.Kind() == reflect.Ptr {
						if el.IsNil() {
							continue
						}
					}
					if ne, ok := el.Interface().(optimizer.Expr); ok && ne != nil {
						obpWalkSameScopeExpr(ne, visit)
					}
				}
			case et.Kind() == reflect.Struct:
				for j := 0; j < f.Len(); j++ {
					obpWalkExprStructFields(f.Index(j), visit)
				}
			case et.Kind() == reflect.Ptr && et.Elem().Kind() == reflect.Struct &&
				!et.Implements(obpExprType) && !et.Implements(obpNodeType):
				for j := 0; j < f.Len(); j++ {
					el := f.Index(j)
					if el.IsNil() {
						continue
					}
					obpWalkExprStructFields(el.Elem(), visit)
				}
			}
		case reflect.Struct:
			obpWalkExprStructFields(f, visit)
		case reflect.Ptr:
			if !f.IsNil() && ft.Elem().Kind() == reflect.Struct &&
				!ft.Implements(obpExprType) && !ft.Implements(obpNodeType) {
				obpWalkExprStructFields(f.Elem(), visit)
			}
		}
	}
}

// obpExprMentions reports whether e references the named column at the
// current scope (inner plans stepped over, as above).
func obpExprMentions(e optimizer.Expr, name string) bool {
	if e == nil {
		return false
	}
	seen := false
	obpWalkSameScopeExpr(e, func(n optimizer.Expr) {
		if cr, ok := n.(*optimizer.ColumnRef); ok && cr.Name == name {
			seen = true
		}
	})
	return seen
}

// obpScanBelowCarries reports whether any SeqScan under n carries the named
// column in its output schema.
func obpScanBelowCarries(n optimizer.Node, name string) bool {
	found := false
	var unknown obpUnknown
	obpWalk(n, func(x optimizer.Node) {
		if s, ok := x.(*optimizer.SeqScan); ok {
			for _, c := range s.Output() {
				if c.Name == name {
					found = true
					return
				}
			}
		}
	}, &unknown)
	return found && len(unknown) == 0
}
