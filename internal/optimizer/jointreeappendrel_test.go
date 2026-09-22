package optimizer

// jointreeappendrel_test.go — M0145-0004: UNION ALL subqueries as
// appendrel leaves (jointreeappendrel.go,
// docs/design/0100-0149/m0145-0004-union-all-appendrel-leaf.md).
//
// Layers, in order: the is_simple_union_all port's admissibility matrix,
// the member-scope search forcing (member rels + SETOP rel partials exist
// only on the jointree arm), the hoist unit contract
// (addAppendRelPartialPaths gates and the Rel re-targeting), and the
// shared sizing fix's observable consequence (partial aggregate over a
// UNION ALL leaf gathers in both arms).

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// appendrelTestCat builds the fixture the probes used: two large members
// (parallel-eligible) and one small dimension table.
func appendrelTestCat(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	mk := func(name string, rows int64, pages int) {
		tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, []catalog.Column{
			{Name: "a", Type: catalog.Type{Name: "int4"}},
			{Name: "v", Type: catalog.Type{Name: "int4"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows, Pages: pages, Analyzed: true,
			Columns: []catalog.ColumnStats{{AvgWidth: 4, NDistinct: -1}, {AvgWidth: 4, NDistinct: -1}}}
	}
	mk("zz_m1", 20_000_000, 200_000)
	mk("zz_m2", 20_000_000, 200_000)
	mk("zz_d", 500, 5)
	return cat
}

func TestAddAppendRelPartialPathsTraceVerdicts(t *testing.T) {
	withParallelOn(t, func() {
		enableDPTrace(t)
		cp := defaultCostParams()
		setOpRel := upperRelSetOpWithPartials(t, cp)
		leaf := setOpTestNode(parser.SetOpUnion, true, upperOrderedInput(100), upperOrderedInput(50))
		leaf.setSetOpBranchRel(setOpRel)
		leafRel := &RelOptInfo{Relids: 0b001, ConsiderParallel: true, rangeTblEntry: rangeTblEntry{baseLeaf: leaf}}
		otherRel := &RelOptInfo{Relids: 0b010, ConsiderParallel: true}
		s := &searchCtx{
			parallelModeOK: true,
			cp:             cp,
			joinrels:       [][]*RelOptInfo{nil, {leafRel, otherRel}},
			relInfos:       []baseRelInfo{{appendrel: true}, {}},
			nrels:          2,
			trace:          newSearchTrace([]rangeBinding{{alias: "u"}, {alias: "d"}}),
		}
		s.addAppendRelPartialPaths()
		out := s.trace.render()
		for _, want := range []string{
			"DPTRACE appendrel rel={u} verdict=admitted",
			"DPTRACE appendrel rel={d} verdict=unmarked",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("rendered trace missing %q:\n%s", want, out)
			}
		}
	})
}

// findSetOpLeaf returns the first *SetOp reachable through single-child
// wrappers and join sides — the leaf the hoist targets. Member plans are
// plain scans, so the first SetOp found is the union leaf itself.
func findSetOpLeaf(n Node) *SetOp {
	if n == nil {
		return nil
	}
	if so, ok := n.(*SetOp); ok {
		return so
	}
	for _, k := range boundaryWalkChildren(n) {
		if so := findSetOpLeaf(k); so != nil {
			return so
		}
	}
	return nil
}

// TestSubqueryChainIsSimpleUnionAll pins the is_simple_union_all port:
// every link UNION ALL, no ORDER BY / LIMIT / OFFSET / locking / WITH on
// the chain head, no SetOpOperand grouping nodes, and a parenthesised
// compound member admitted only when its own chain is UNION ALL all the
// way down — on EITHER side (is_simple_union_all_recurse checks larg and
// rarg symmetrically).
func TestSubqueryChainIsSimpleUnionAll(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{`SELECT a, v FROM zz_m1 UNION ALL SELECT a, v FROM zz_m2`, true},
		{`SELECT a, v FROM zz_m1 UNION ALL SELECT a, v FROM zz_m2 UNION ALL SELECT a, v FROM zz_d`, true},
		{`SELECT a, v FROM zz_m1`, false},
		{`SELECT a, v FROM zz_m1 UNION SELECT a, v FROM zz_m2`, false},
		{`SELECT a, v FROM zz_m1 INTERSECT SELECT a, v FROM zz_m2`, false},
		{`SELECT a, v FROM zz_m1 EXCEPT SELECT a, v FROM zz_m2`, false},
		{`SELECT a, v FROM zz_m1 UNION ALL SELECT a, v FROM zz_m2 ORDER BY 1`, false},
		{`SELECT a, v FROM zz_m1 UNION ALL SELECT a, v FROM zz_m2 LIMIT 3`, false},
		{`SELECT a, v FROM zz_m1 UNION ALL SELECT a, v FROM zz_m2 OFFSET 2`, false},
		{`SELECT a, v FROM zz_m1 UNION ALL SELECT a, v FROM zz_m2 FOR UPDATE`, false},
		// A non-ALL link anywhere in the chain refuses, on either lean.
		{`SELECT a, v FROM zz_m1 UNION ALL SELECT a, v FROM zz_m2 UNION SELECT a, v FROM zz_d`, false},
		{`SELECT a, v FROM zz_m1 UNION SELECT a, v FROM zz_m2 UNION ALL SELECT a, v FROM zz_d`, false},
		// Parenthesised members: an all-UNION-ALL compound is atomic but
		// admissible; a non-ALL compound refuses on either side.
		{`SELECT a, v FROM zz_m1 UNION ALL (SELECT a, v FROM zz_m2 UNION ALL SELECT a, v FROM zz_d)`, true},
		{`SELECT a, v FROM zz_m1 UNION ALL (SELECT a, v FROM zz_m2 UNION SELECT a, v FROM zz_d)`, false},
		// A parenthesised member parses as a SetOpOperand grouping node;
		// the port declines those conservatively on either side even
		// when its inner chain is all UNION ALL (PG's recurse would
		// admit them — the corpus has none, so the strictness is
		// deliberate, see the design doc's refusal matrix).
		{`(SELECT a, v FROM zz_m1 UNION ALL SELECT a, v FROM zz_m2) UNION ALL SELECT a, v FROM zz_d`, false},
		{`(SELECT a, v FROM zz_m1 UNION SELECT a, v FROM zz_m2) UNION ALL SELECT a, v FROM zz_d`, false},
	}
	for _, c := range cases {
		sel, ok := parseOne(t, c.sql).(*parser.SelectStmt)
		if !ok {
			t.Fatalf("%s: not a SelectStmt", c.sql)
		}
		if got := subqueryChainIsSimpleUnionAll(sel); got != c.want {
			t.Errorf("%s\n  got %v, want %v", c.sql, got, c.want)
		}
	}
}

// TestAppendrelMarkSurvivesEarlierFromSibling pins the Q71 regression: the
// planner supplies a non-nil resolution context to every later FROM item, not
// only SQL LATERAL items. The appendrel mark must use RangeVar.Lateral rather
// than treating that ordinary scope plumbing as a lateral dependency.
func TestAppendrelMarkSurvivesEarlierFromSibling(t *testing.T) {
	cat := appendrelTestCat(t)
	for _, tc := range []struct {
		name string
		sql  string
		want bool
	}{
		{
			name: "plain later FROM subquery is marked",
			sql:  `SELECT * FROM zz_d d, (SELECT a, v FROM zz_m1 UNION ALL SELECT a, v FROM zz_m2) u`,
			want: true,
		},
		{
			name: "LATERAL subquery remains refused",
			sql:  `SELECT * FROM zz_d d, LATERAL (SELECT a, v FROM zz_m1 UNION ALL SELECT a, v FROM zz_m2) u`,
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(v bool) { jointreePipeline = v }(jointreePipeline)
			jointreePipeline = true
			sel, ok := parseOne(t, tc.sql).(*parser.SelectStmt)
			if !ok {
				t.Fatal("parsed statement is not a SELECT")
			}
			_, ctx, err := planFromClause(sel, cat, DefaultPlannerSettings(), newRtableScope())
			if err != nil {
				t.Fatal(err)
			}
			if len(ctx.bindings) != 2 {
				t.Fatalf("bindings = %d, want 2", len(ctx.bindings))
			}
			if got := ctx.bindings[1].appendrel; got != tc.want {
				t.Errorf("later union binding appendrel = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAppendrelMemberScopeEngagesSearch pins the member-scope forcing: on
// the jointree arm a marked leaf's members ran through the join search —
// each branch answers a searched rel with base-rel parallel partials, so
// the nested SETOP rel carries the Parallel Append candidate the hoist
// lifts. On the legacy arm the mark is never set, the members keep the
// isSimpleSingle bypass, and no partial is filed — the A/B boundary.
func TestAppendrelMemberScopeEngagesSearch(t *testing.T) {
	withParallelOn(t, func() {
		cat := appendrelTestCat(t)
		sql := `SELECT * FROM (SELECT a, v FROM zz_m1 UNION ALL SELECT a, v FROM zz_m2) u, zz_d d WHERE u.a = d.a`

		node := planOnPipeline(t, sql, cat, true)
		leaf := findSetOpLeaf(node)
		if leaf == nil {
			t.Fatal("jointree arm: no *SetOp leaf in the plan")
		}
		carrier, ok := Node(leaf).(setOpBranchRelNode)
		if !ok {
			t.Fatalf("jointree arm: leaf %T is not a setOpBranchRelNode carrier", leaf)
		}
		sr := carrier.setOpBranchRel()
		if sr == nil {
			t.Fatal("jointree arm: leaf carrier answers a nil SETOP rel")
		}
		if len(sr.PartialPathlist) == 0 {
			t.Fatal("jointree arm: SETOP rel has no partial path — the Parallel Append candidate was never filed")
		}
		for name, branch := range map[string]Node{"left": leaf.Left, "right": leaf.Right} {
			br := setOpBranchRelOf(branch)
			if br == nil {
				t.Fatalf("jointree arm: %s member has no searched rel — member-scope forcing did not engage", name)
			}
			if !br.ConsiderParallel {
				t.Errorf("jointree arm: %s member rel ConsiderParallel = false", name)
			}
			if len(br.PartialPathlist) == 0 {
				t.Errorf("jointree arm: %s member rel has no partial path", name)
			}
		}

		legacy := planOnPipeline(t, sql, cat, false)
		lleaf := findSetOpLeaf(legacy)
		if lleaf == nil {
			t.Fatal("legacy arm: no *SetOp leaf in the plan")
		}
		lcarrier, ok := Node(lleaf).(setOpBranchRelNode)
		if !ok {
			t.Fatalf("legacy arm: leaf %T is not a carrier", lleaf)
		}
		lsr := lcarrier.setOpBranchRel()
		if lsr != nil && len(lsr.PartialPathlist) != 0 {
			t.Errorf("legacy arm: SETOP rel has %d partial paths — the appendrel machinery leaked off-knob", len(lsr.PartialPathlist))
		}
	})
}

// TestAddAppendRelPartialPathsHoists pins the hoist's own contract: the
// nested SETOP rel's partial paths are copied onto the leaf rel with
// Rel re-targeted, and every gate fails closed.
func TestAddAppendRelPartialPathsHoists(t *testing.T) {
	withParallelOn(t, func() {
		cp := defaultCostParams()
		build := func(t *testing.T) (*searchCtx, *RelOptInfo, *RelOptInfo, *RelOptInfo) {
			setOpRel := upperRelSetOpWithPartials(t, cp)
			leaf := setOpTestNode(parser.SetOpUnion, true,
				upperOrderedInput(100), upperOrderedInput(50))
			leaf.setSetOpBranchRel(setOpRel)
			leafRel := &RelOptInfo{ConsiderParallel: true, rangeTblEntry: rangeTblEntry{baseLeaf: leaf}}
			otherRel := &RelOptInfo{ConsiderParallel: true}
			s := &searchCtx{
				parallelModeOK: true,
				cp:             cp,
				joinrels:       [][]*RelOptInfo{nil, {leafRel, otherRel}},
				relInfos:       []baseRelInfo{{appendrel: true}, {}},
				nrels:          2,
			}
			return s, leafRel, otherRel, setOpRel
		}

		t.Run("files the retargeted path on the marked leaf only", func(t *testing.T) {
			s, leafRel, otherRel, setOpRel := build(t)
			s.addAppendRelPartialPaths()
			if len(leafRel.PartialPathlist) != 1 {
				t.Fatalf("leaf PartialPathlist = %d, want the hoisted 1", len(leafRel.PartialPathlist))
			}
			got := leafRel.PartialPathlist[0]
			if got.Rel != leafRel {
				t.Error("hoisted path was not re-targeted to the leaf rel")
			}
			if got == setOpRel.PartialPathlist[0] {
				t.Error("hoisted path aliases the SETOP rel's entry — it must be a copy so the owner change cannot leak back")
			}
			if got.Kind != PathSetOp {
				t.Errorf("hoisted path kind = %v, want PathSetOp", got.Kind)
			}
			if len(otherRel.PartialPathlist) != 0 {
				t.Errorf("unmarked sibling got %d partial paths", len(otherRel.PartialPathlist))
			}
		})

		t.Run("refuses without the mark", func(t *testing.T) {
			s, leafRel, _, _ := build(t)
			s.relInfos[0].appendrel = false
			s.addAppendRelPartialPaths()
			if len(leafRel.PartialPathlist) != 0 {
				t.Error("unmarked leaf got a hoisted path")
			}
		})

		t.Run("refuses when the leaf is not parallel-considered", func(t *testing.T) {
			s, leafRel, _, _ := build(t)
			leafRel.ConsiderParallel = false
			s.addAppendRelPartialPaths()
			if len(leafRel.PartialPathlist) != 0 {
				t.Error("non-parallel leaf got a hoisted path")
			}
		})

		t.Run("refuses when the carrier is wrapped", func(t *testing.T) {
			s, leafRel, _, _ := build(t)
			leafRel.baseLeaf = &Project{Child: leafRel.baseLeaf}
			s.addAppendRelPartialPaths()
			if len(leafRel.PartialPathlist) != 0 {
				t.Error("wrapped leaf got a hoisted path — wrappers change rows the hoisted emission cannot reproduce")
			}
		})

		t.Run("refuses with no nested partials", func(t *testing.T) {
			s, leafRel, _, setOpRel := build(t)
			setOpRel.PartialPathlist = nil
			s.addAppendRelPartialPaths()
			if len(leafRel.PartialPathlist) != 0 {
				t.Error("leaf got a hoisted path from an empty SETOP partial list")
			}
		})

		t.Run("refuses when parallel mode is off", func(t *testing.T) {
			s, leafRel, _, _ := build(t)
			s.parallelModeOK = false
			s.addAppendRelPartialPaths()
			if len(leafRel.PartialPathlist) != 0 {
				t.Error("leaf got a hoisted path with parallelModeOK=false")
			}
		})
	})
}

// TestCreateSetOpPlanLeafLayout pins the M0145-0004 emission follow-on:
// a PathSetOp re-targeted onto a leaf rel emits with the leaf's
// contiguous baseRelLayout — the hoist makes a PathSetOp a legal join
// input, and joinInputsFor panics on a nil child layout (the Q5 knob-arm
// crash: "PathHashJoin over a child whose column coordinates are
// unknown"). Upper-rel SETOP paths keep nil (baseLeaf == nil).
func TestCreateSetOpPlanLeafLayout(t *testing.T) {
	l := upperOrderedInput(100)
	r := upperOrderedInput(50)
	leaf := setOpTestNode(parser.SetOpUnion, true, l, r)
	p := &Path{
		Kind:  PathSetOp,
		SetOp: leaf,
		Children: []*Path{
			{Kind: PathPrebuilt, node: l},
			{Kind: PathPrebuilt, node: r},
		},
	}

	p.Rel = &RelOptInfo{rangeTblEntry: rangeTblEntry{baseLeaf: leaf, baseOffset: 3}}
	n, lay := createSetOpPlan(p)
	if n == nil {
		t.Fatal("leaf-owned PathSetOp built no node")
	}
	if len(lay) != len(leaf.Output()) {
		t.Fatalf("leaf-owned PathSetOp layout = %v (len %d), want the contiguous leaf layout (len %d)",
			lay, len(lay), len(leaf.Output()))
	}
	for i, off := range lay {
		if off != 3+i {
			t.Fatalf("layout[%d] = %d, want baseOffset+i = %d", i, off, 3+i)
		}
	}

	p.Rel = &RelOptInfo{}
	if _, lay := createSetOpPlan(p); lay != nil {
		t.Errorf("upper-rel PathSetOp layout = %v, want nil (no baseLeaf)", lay)
	}
}

// TestAppendrelConsiderParallelInheritsSetOp pins the leaf-safety rule:
// the marked leaf's ConsiderParallel is the nested SETOP rel's — the AND
// of the member rels' — not the opaque rtekind verdict, which reads a
// *SetOp leaf as `other` and fails closed.
func TestAppendrelConsiderParallelInheritsSetOp(t *testing.T) {
	withParallelOn(t, func() {
		cp := defaultCostParams()
		setOpRel := upperRelSetOpWithPartials(t, cp)
		leaf := setOpTestNode(parser.SetOpUnion, true,
			upperOrderedInput(100), upperOrderedInput(50))
		leaf.setSetOpBranchRel(setOpRel)

		leafRel := &RelOptInfo{rangeTblEntry: rangeTblEntry{baseLeaf: leaf}}
		s := &searchCtx{
			cp:       cp,
			joinrels: [][]*RelOptInfo{nil, {leafRel}},
			relInfos: []baseRelInfo{{appendrel: true}},
		}
		// relConsiderParallel would refuse the opaque *SetOp leaf; the
		// appendrel arm must stamp the SETOP rel's verdict instead.
		setOpRel.ConsiderParallel = true
		s.setBaseRelConsiderParallel(nil)
		if !leafRel.ConsiderParallel {
			t.Error("marked leaf did not inherit the SETOP rel's parallel safety")
		}

		setOpRel.ConsiderParallel = false
		leafRel.ConsiderParallel = true
		s.setBaseRelConsiderParallel(nil)
		if leafRel.ConsiderParallel {
			t.Error("marked leaf stayed parallel-considered when a member was unsafe")
		}
	})
}

// TestPartialAggOverUnionLeafGathers pins the observable consequence of
// the shared drivingScans sizing fix: an aggregate over a UNION ALL leaf
// can split into partial-agg → Gather → partial-agg → SetOp because
// computeParallelWorkers/upperSplitWorkers now see the member scans
// through the SetOp driving node. Shared machinery — both arms produce
// the shape.
func TestPartialAggOverUnionLeafGathers(t *testing.T) {
	withParallelOn(t, func() {
		cat := appendrelTestCat(t)
		sql := `SELECT count(*) FROM (SELECT a, v FROM zz_m1 UNION ALL SELECT a, v FROM zz_m2) u`
		for _, on := range []bool{false, true} {
			func() {
				defer func(v bool) { jointreePipeline = v }(jointreePipeline)
				jointreePipeline = on
				ps := DefaultPlannerSettings()
				ps.ParallelStatementOK = true
				node, err := PlanWithSettings(parseOne(t, sql), cat, ps)
				if err != nil {
					t.Fatal(err)
				}
				// Walk for Aggregate → … → Gather → … → SetOp.
				agg, _ := node.(*Aggregate)
				if agg == nil {
					if p, ok := node.(*Project); ok {
						agg, _ = p.Child.(*Aggregate)
					}
				}
				if agg == nil {
					t.Fatalf("arm=%v: no top Aggregate (got %T)", on, node)
				}
				var gather *Gather
				var setop *SetOp
				var walk func(n Node)
				walk = func(n Node) {
					switch x := n.(type) {
					case *Gather:
						gather = x
						walk(x.Child)
					case *SetOp:
						setop = x
					case *Aggregate:
						walk(x.Child)
					case *Project:
						walk(x.Child)
					case *Filter:
						walk(x.Child)
					}
				}
				walk(agg.Child)
				if gather == nil || setop == nil {
					t.Fatalf("arm=%v: want Aggregate→Gather→…→SetOp, gather=%v setop=%v", on, gather != nil, setop != nil)
				}
			}()
		}
	})
}
