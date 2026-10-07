package optimizer

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// M0146-0049b/c — a parameterised Append over a flattened UNION ALL leaf.
//
// PG plans each UNION ALL member as a child rel of the appendrel, gives every
// child its own parameterised index paths, and add_paths_to_append_rel
// (allpaths.c:1321) builds one Append per child parameterisation from
// get_cheapest_parameterized_child_path (allpaths.c:2048). A nested loop then
// binds the parameter into the whole Append (create_nestloop_plan's
// NestLoopParams, createplan.c:4341), and ExecReScanAppend
// (nodeAppend.c:421) hands it to every child. TPC-DS Q54 drives `item` into
// Append(Bitmap Heap catalog_sales, Index Scan web_sales_pkey) that way.
//
// goopg's appendrel is a hoist over finished member nodes (M0145-0004): the
// members are not rels of the search. Each member that is a plain scan of one
// table — `[Project →] [Filter →] SeqScan` — gets a synthetic member rel
// whose baseLeaf is that scan, so the base-rel producer
// (addOneParameterizedIndexPath) prices its probe exactly as it prices any
// base rel's; the leaf's join clauses are translated onto the member by
// output position through the member's projection. When every member has a
// path for a parameterisation, the leaf gains a PathParamAppend over them,
// costed as cost_append's unordered arm (costsize.c:2255): the first child's
// startup, the sum of the children's totals and rows, plus
// cpu_tuple_cost * APPEND_CPU_COST_MULTIPLIER per row.
//
// The plan is R25's NLI contract with an Append on the inside: a lateral
// Join whose right child is the leaf's own UNION ALL with every member scan
// replaced by its probe, keys bound as level-1 OuterColumnRefs.

// paramAppendInfo is a PathParamAppend's carrier: the leaf's UNION ALL node
// and, per member in Children order, the member's projection (nil for a bare
// scan).
type paramAppendInfo struct {
	carrier  *SetOp
	members  []Node // the original member roots, in carrier order
	projects []*Project
}

// paramAppendMember resolves one member root into the scan the synthetic rel
// wraps and the base column each leaf output position reads. ok is false when
// the member is not `[Project →] [Filter →] scan` with plain-column targets.
func paramAppendMember(m Node, width int) (base Node, proj *Project, cols []string, ok bool) {
	base = m
	if p, isProj := m.(*Project); isProj {
		proj, base = p, p.Child
	}
	id, _, leafOK := scanLeafFor(base)
	if !leafOK || id == nil || id.table == nil {
		return nil, nil, nil, false
	}
	if _, _, absorbable := absorbableLeafCond(base); !absorbable {
		return nil, nil, nil, false
	}
	baseOut := base.Output()
	cols = make([]string, width)
	for j := 0; j < width; j++ {
		var cr *ColumnRef
		if proj != nil {
			if j >= len(proj.Targets) {
				return nil, nil, nil, false
			}
			c, isCol := proj.Targets[j].(*ColumnRef)
			if !isCol {
				continue // an expression column: not probeable, but harmless
			}
			cr = c
		} else {
			if j >= len(baseOut) {
				return nil, nil, nil, false
			}
			cols[j] = baseOut[j].Name
			continue
		}
		if cr.Index < 0 || cr.Index >= len(baseOut) {
			return nil, nil, nil, false
		}
		cols[j] = baseOut[cr.Index].Name
	}
	return base, proj, cols, true
}

// addParameterizedAppendPaths files a PathParamAppend on every flattened
// UNION ALL leaf whose members can all be probed under a parameterisation.
// Runs after addParameterizedIndexPaths (the same clause list, the same
// considered parameterisations).
func (s *searchCtx) addParameterizedAppendPaths(cat catalog.Catalog) {
	if s == nil || cat == nil || s.clauses == nil || len(s.clauses.all) == 0 {
		return
	}
	totalPages := s.totalTablePages()
	for i, rel := range s.levelRels(1) {
		if i >= len(s.relInfos) {
			break
		}
		if !s.relInfos[i].appendrel {
			continue
		}
		so, isSetOp := rel.baseLeaf.(*SetOp)
		if !isSetOp {
			continue
		}
		// The serial appendrel shape only (orderedappend.go's predicate:
		// plain UNION ALL links, no merge, no parallel-aware link, no
		// type-coerced branch).
		members := unionAllMembers(so)
		if len(members) < 2 {
			continue
		}
		cands := indexableJoinClausesFor(rel.Relids, s.clauses.all)
		if len(cands) == 0 {
			continue
		}
		leafOut := so.Output()
		added := false
		for _, req := range consideredParameterizations(cands) {
			if p := s.paramAppendPathFor(rel, so, members, leafOut, cands, req, cat, totalPages); p != nil {
				addPath(rel, p, "param-append")
				added = true
			}
		}
		if added {
			setCheapest(rel)
		}
	}
}

// paramAppendPathFor builds the Append for one parameterisation, or nil when
// some member has no parameterised path under it (PG's
// get_cheapest_parameterized_child_path returning NULL abandons that
// parameterisation for the whole appendrel).
func (s *searchCtx) paramAppendPathFor(rel *RelOptInfo, so *SetOp, members []Node, leafOut Schema, cands []paramIndexClause, req RelSet, cat catalog.Catalog, totalPages float64) *Path {
	info := &paramAppendInfo{carrier: so, members: members}
	children := make([]*Path, 0, len(members))
	for _, m := range members {
		base, proj, cols, ok := paramAppendMember(m, len(leafOut))
		if !ok {
			return nil
		}
		// The leaf's clauses, re-read onto this member's column of the same
		// output position. The restrictInfo is kept, so the probe enforces
		// THAT clause (probeEnforcedClauses) and the join drops it.
		var mcands []paramIndexClause
		for _, c := range cands {
			lc, isCol := c.innerKey.(*ColumnRef)
			if !isCol {
				continue
			}
			pos := -1
			for j, sc := range leafOut {
				if strings.EqualFold(sc.Name, lc.Name) {
					if pos >= 0 {
						pos = -2 // ambiguous output name
						break
					}
					pos = j
				}
			}
			if pos < 0 || cols[pos] == "" {
				continue
			}
			mc := c
			mc.innerCol = cols[pos]
			mc.innerKey = &ColumnRef{Name: cols[pos], Type: lc.Type}
			mcands = append(mcands, mc)
		}
		if len(mcands) == 0 {
			return nil
		}
		id, _, _ := scanLeafFor(base)
		tbl := id.table
		scanNode := base
		for {
			f, isF := scanNode.(*Filter)
			if !isF {
				break
			}
			scanNode = f.Child
		}
		relTuples := float64(EstimateRows(scanNode))
		if relTuples < 1 {
			relTuples = 1
		}
		member := newRelOptInfo(rel.Relids, float64(EstimateRows(base)), rel.Width)
		member.baseLeaf = base
		// An appendrel child gets its own set_rel_consider_parallel
		// (allpaths.c:589), bounded by the parent's: the member probes are
		// then parallel-safe exactly when PG's would be (M0146-0049e).
		member.ConsiderParallel = rel.ConsiderParallel && relConsiderParallel(base, tbl, cat)
		// A member's needed columns are its projection's, not the
		// statement's: the statement-wide set cannot attribute them, so the
		// index-only arm is kept out of member probes (ledgered).
		saved := s.neededColsKnown
		s.neededColsKnown = false
		relPages := baseRelPages(tbl, relTuples)
		s.addOneParameterizedIndexPath(member, tbl, cat, mcands, req, relPages, relTuples, totalPages)
		s.neededColsKnown = saved
		// And the parameterised bitmap heap path, as create_index_paths
		// builds both for a child rel (PG's Q54 probes catalog_sales by
		// bitmap): the base-rel bitmap producer's per-index builder.
		T := float64(relPages)
		if T < 1 {
			T = 1
		}
		maxEntries := bitmapMaxEntries(s.cp.workMem)
		for _, idx := range cat.IndexesOnTable(tbl) {
			// A catalog-only index (gist/spgist/gin/brin) has nothing to scan (M0146-0069).
			if !idx.HasStorage() {
				continue
			}
			if pth := s.buildOneParameterizedBitmapPath(member, tbl, idx, mcands, req,
				relPages, relTuples, T, totalPages, maxEntries); pth != nil {
				addPath(member, pth, "param-append.bitmap")
			}
		}
		var best *Path
		for _, p := range member.Pathlist {
			if p == nil || p.RequiredOuter != req || (p.Kind != PathIndexScan && p.Kind != PathBitmapHeapScan) {
				continue
			}
			if best == nil || p.Cost.Total < best.Cost.Total {
				best = p
			}
		}
		if best == nil {
			return nil
		}
		children = append(children, best)
		info.projects = append(info.projects, proj)
	}
	p := &Path{
		Kind:          PathParamAppend,
		Rel:           rel,
		Children:      children,
		RequiredOuter: req,
		paramAppend:   info,
		// create_append_path (pathnode.c:1339, :1380): parallel-safe when the rel
		// considers parallelism and every subpath is parallel-safe. Left
		// false, the partial nested loop refused this inner (V7), so PG's
		// Q54 Gather → NL(Parallel Seq Scan item, Append(probes)) could not
		// form (M0146-0049e).
		ParallelSafe: parallelSafeWith(rel, children...),
	}
	p.Cost.Startup = children[0].Cost.Startup
	for _, c := range children {
		p.Rows += c.Rows
		p.Cost.Total += c.Cost.Total
	}
	p.Cost.Total += s.cp.cpuTupleCost * appendCPUCostMultiplier * p.Rows
	return p
}

// createParamAppendNode is the plan for a PathParamAppend: the leaf's UNION
// ALL with every member scan replaced by its probe, under the member's own
// projection. The probe keys are left in binding coordinates; the nested-loop
// arm that binds them (createNestLoopParamAppendPlan) re-roots them.
func createParamAppendNode(p *Path) Node {
	info := p.paramAppend
	if info == nil || len(info.members) != len(p.Children) {
		panic("createPlan: PathParamAppend without its member carrier")
	}
	repl := make(map[Node]Node, len(info.members))
	for i, child := range p.Children {
		var probe Node
		switch child.Kind {
		case PathBitmapHeapScan:
			n, err := createBitmapHeapScanPlan(child)
			if err != nil {
				panic("createPlan: PathParamAppend bitmap member: " + err.Error())
			}
			probe = n
		default:
			probe = createIndexScanPlan(child)
		}
		stampPlanCost(probe, child)
		var memberRoot Node = probe
		if pr := info.projects[i]; pr != nil {
			cp := *pr
			cp.Child = probe
			memberRoot = &cp
		}
		repl[info.members[i]] = memberRoot
	}
	return cloneUnionAllWith(info.carrier, repl)
}

// cloneUnionAllWith copies the UNION ALL chain, replacing its member roots.
func cloneUnionAllWith(so *SetOp, repl map[Node]Node) *SetOp {
	cp := *so
	cp.PlanCost = PlanCost{}
	cp.InitPlanCharge = InitPlanCharge{}
	sub := func(n Node) Node {
		if r, ok := repl[n]; ok {
			return r
		}
		if inner, ok := n.(*SetOp); ok {
			return cloneUnionAllWith(inner, repl)
		}
		panic(fmt.Sprintf("createPlan: PathParamAppend member %T was not replaced", n))
	}
	cp.Left, cp.Right = sub(so.Left), sub(so.Right)
	return &cp
}

// paramAppendProbes returns the member probes of a createParamAppendNode
// tree, in member order.
func paramAppendProbes(n Node) []Node {
	var out []Node
	var walk func(n Node)
	walk = func(n Node) {
		switch x := n.(type) {
		case *SetOp:
			walk(x.Left)
			walk(x.Right)
		case *Project:
			walk(x.Child)
		case *Filter:
			walk(x.Child)
		default:
			out = append(out, n)
		}
	}
	walk(n)
	return out
}

// createNestLoopParamAppendPlan binds a PathParamAppend's parameter: R25's
// lateral Join over the Append, every member probe keyed by level-1
// OuterColumnRefs re-rooted from the outer layout (outerParamKey).
func createNestLoopParamAppendPlan(p *Path, innerPath *Path) (Node, outputLayout) {
	const kind = "PathNestLoop(param-append)"
	outerPath := p.Children[0]
	if outerPath == nil || outerPath.Rel == nil {
		panic("createPlan: param-append nested loop over an outer with no RelOptInfo")
	}
	if unsupplied := innerPath.RequiredOuter &^ outerPath.Rel.Relids; unsupplied != 0 {
		panic(fmt.Sprintf("createPlan: param-append inner is parameterised by %#08x, which the outer relset %#08x does not supply",
			uint32(unsupplied), uint32(outerPath.Rel.Relids)))
	}
	in := joinInputsFor(p, kind, outerPath, innerPath)
	jt := planJoinTypeFor(p, kind)
	if jt == JoinTypeRight {
		panic("createPlan: param-append nested loop with jointype RIGHT; the preserved side is the probe (R64)")
	}
	outerLay := in.lay[:len(in.outer.Output())]
	outerIndex := outerLay.bindingIndex()
	probes := paramAppendProbes(in.inner)
	if len(probes) != len(innerPath.Children) {
		panic(fmt.Sprintf("createPlan: param-append built %d probes for %d member paths", len(probes), len(innerPath.Children)))
	}
	for i, child := range innerPath.Children {
		idxPath := child
		if child.Kind == PathBitmapHeapScan {
			if len(child.Children) != 1 || child.Children[0] == nil {
				panic("createPlan: param-append bitmap member without its index path")
			}
			idxPath = child.Children[0]
		}
		bindParamProbe("param-append", probes[i], idxPath, outerLay, outerIndex)
	}
	j := &Join{
		pos:       in.outer.Pos(),
		Type:      jt,
		Algo:      JoinAlgoNestedLoop,
		Lateral:   true,
		Left:      in.outer,
		Right:     in.inner,
		Predicate: in.joinPredicate(kind, nil, p.Residual, p.ecClausesLast()),
		schema:    in.publishedSchema(jt),
	}
	return j, in.publishedLayout(jt)
}
