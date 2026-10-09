package optimizer

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// M0146-0049d3 — a parameterised hash join, and the nested loop that binds it.
//
// PG builds a joinrel's parameterised paths from its inputs' parameterised
// ones: `hash_inner_and_outer` (joinpath.c) pairs every member of the two
// rels' `cheapest_parameterized_paths`, and `try_hashjoin_path` keeps a pair
// whose `calc_non_nestloop_required_outer` overlaps the joinrel's
// `param_source_rels` — the relations some join-order restriction keeps from
// joining the inputs directly. The path's rows are `ppi_rows`,
// `get_parameterized_joinrel_size` (costsize.c) over
// `get_joinrel_parampathinfo`'s clause list. A nested loop above then binds the
// parameter into the whole hash join (`create_nestloop_plan`'s NestLoopParams,
// createplan.c), and the executor rescans it per outer row
// (`ExecReScanHashJoin`).
//
// TPC-DS Q95 is the shape: the semi join's RHS `ws_wh ⋈ web_returns` is a hash
// join whose hashed side is an index probe of `web_returns` keyed by the outer
// `ws1.ws_order_number`, and `NL Semi Join(ws1, that hash join)` binds it. The
// executor half landed first (0049d1: an empty hash table never reads the
// probe side; 0049d2: a statement-level CTE under a LATERAL is materialised
// once).
//
// Scope: INNER joins whose parameterised inputs are plain parameterised index
// probes (`PathIndexScan`). The lowering is R25's NLI contract one level
// deeper: `createNestLoopParamJoinPlan` emits `Join{Lateral}` over the hash
// join, and the probe's keys become level-1 OuterColumnRefs. The probe sits
// below the hash join, so its node is found through a sink the nested loop
// hangs on the parameterised paths before building them — PG's
// `root->curOuterRels` / `curOuterParams`, the create_plan-time state that
// tells a parameterised scan which nested loop supplies its parameter.

// paramProbeNode records a parameterised index probe built under a binding
// nested loop: the node createPlanNode returned for it and the path it came
// from. A parameterised hash join with ParamFilter clauses records itself
// with bind set instead (M0146-0135): the loop calls it with its outer layout
// to add the clauses to the join's predicate.
type paramProbeNode struct {
	node Node
	path *Path
	bind func(outerLay outputLayout, outerIndex map[int]int)
}

// paramFilterClause is one ppi clause of a parameterised hash join:
// `reqKey = local`, reqKey a member of the class on the required-outer rel
// and local its member on the join's probe side, both in binding coordinates.
type paramFilterClause struct {
	reqKey, local Expr
	ri            *restrictInfo
}

// paramJoinFilterClauses is get_joinrel_parampathinfo's equivalence-class
// clauses for a hash join of outer (probe side, path o) and inner (hashed,
// path i) parameterised by req (relnode.c): for each class that links req
// to the joinrel, generate_join_implied_equalities produces one req = member
// clause. A clause movable into the parameterised inner is dropped and
// regenerated against the outer input ("Z.Z = X.X"), and an unparameterised
// outer accepts no parameterised clause, so the join keeps `req-member =
// outer-member` — TPC-DS Q95's `ws1.ws_order_number = ws_wh_1.ws_order_number`
// Join Filter over the semijoin RHS, whose web_returns probe enforces the
// class against ws1 (M0146-0135).
//
// Only that case is derived: the inner's probe enforces the class against
// req and the outer path is unparameterised. With a parameterised outer, PG's
// pick depends on the class's member order (ledgered).
func (s *searchCtx) paramJoinFilterClauses(outer *RelOptInfo, o, i *Path, req RelSet) []paramFilterClause {
	if s == nil || s.clauses == nil || o == nil || i == nil || o.RequiredOuter != 0 || i.RequiredOuter == 0 {
		return nil
	}
	var out []paramFilterClause
	done := map[int]bool{}
	for ri := range probeEnforcedClauses(i) {
		if ri == nil || !ri.isEquijoin || ri.ecID == noEquivClass || done[ri.ecID] {
			continue
		}
		var reqKey Expr
		switch {
		case ri.leftRelids != 0 && relsSubset(ri.leftRelids, req):
			reqKey = ri.leftKey
		case ri.rightRelids != 0 && relsSubset(ri.rightRelids, req):
			reqKey = ri.rightKey
		default:
			continue
		}
		var local Expr
		for _, c := range s.clauses.all {
			if c == nil || !c.isEquijoin || c.ecID != ri.ecID {
				continue
			}
			if c.leftRelids != 0 && relsSubset(c.leftRelids, outer.Relids) {
				local = c.leftKey
			} else if c.rightRelids != 0 && relsSubset(c.rightRelids, outer.Relids) {
				local = c.rightKey
			}
			if local != nil {
				break
			}
		}
		if local == nil {
			continue
		}
		done[ri.ecID] = true
		clause := &BinaryOp{Op: parser.OpEq, Left: reqKey, Right: local}
		out = append(out, paramFilterClause{reqKey: reqKey, local: local,
			ri: &restrictInfo{clause: clause, ecID: ri.ecID, isEquijoin: true}})
	}
	return out
}

// paramJoinLeafOK reports whether p can be a parameterised input of a
// parameterised hash join: an unparameterised path, or a plain parameterised
// index or bitmap probe whose keys the binding nested loop knows how to
// rebind.
func paramJoinLeafOK(p *Path) bool {
	if p == nil {
		return false
	}
	if p.RequiredOuter == 0 {
		return true
	}
	ip := paramProbeIndexPath(p)
	return ip != nil && len(ip.IndexClauses) > 0 &&
		!hasRangeClause(ip.IndexClauses) && len(ip.IndexClauses[0].saop) == 0
}

// paramProbeIndexPath is the index path whose clauses a parameterised probe
// binds: the probe itself for an index scan, the single bitmap index scan
// under a bitmap heap scan; nil for any other shape.
func paramProbeIndexPath(p *Path) *Path {
	switch p.Kind {
	case PathIndexScan:
		return p
	case PathBitmapHeapScan:
		if len(p.Children) == 1 && p.Children[0] != nil && p.Children[0].Kind == PathBitmapIndexScan {
			return p.Children[0]
		}
	}
	return nil
}

// addParameterizedHashJoinPaths is `hash_inner_and_outer`'s parameterised
// pairing for one join direction (`outer` probes, `inner` is hashed).
func addParameterizedHashJoinPaths(s *searchCtx, joinrel, outer, inner *RelOptInfo, cp costParams, jt parser.JoinType, clauses []*restrictInfo, paramSrc RelSet, uniq uniqueSide, sjinfo *SpecialJoinInfo) {
	if s == nil || jt != parser.JoinInner || uniq != uniqueSideNone || paramSrc == 0 {
		return
	}
	if len(outer.CheapestParameterized) == 0 && len(inner.CheapestParameterized) == 0 {
		return
	}
	keys, residual := splitJoinClauses(outer.Relids, inner.Relids, clauses)
	if len(keys) == 0 {
		return
	}
	outers := append([]*Path{outer.CheapestTotal}, outer.CheapestParameterized...)
	inners := append([]*Path{inner.CheapestTotal}, inner.CheapestParameterized...)
	var final *hashJoinFinalCostInput
	bucket := -1.0
	for _, o := range outers {
		if o == nil || pathParamByRel(o, inner) || !paramJoinLeafOK(o) {
			continue
		}
		for _, i := range inners {
			if i == nil || pathParamByRel(i, outer) || !paramJoinLeafOK(i) {
				continue
			}
			if o.RequiredOuter == 0 && i.RequiredOuter == 0 {
				continue // the unparameterised pair: hash_inner_and_outer's first try
			}
			// try_hashjoin_path's admission test.
			req := calcNonNestloopRequiredOuter(o, i)
			if req == 0 || !relsOverlap(req, paramSrc) {
				continue
			}
			if final == nil {
				f := s.hashJoinFinalCostInputFor(joinrel, outer, inner, jt, keys, clauses)
				final = &f
				bucket = s.estimateHashBucketSize(keys, inner.Relids)
			}
			rows := s.parameterizedJoinrelSize(joinrel, outer, inner, o, i, req, clauses, sjinfo)
			// M0146-0135: the ppi clauses join the restrict list the
			// hash join evaluates per match (final_cost_hashjoin's
			// qp_qual over restrict_clauses).
			pf := s.paramJoinFilterClauses(outer, o, i, req)
			quals := residual
			for _, c := range pf {
				quals = append(quals[:len(quals):len(quals)], c.ri)
			}
			cost := hashJoinCost(cp, hashJoinInputs{
				qualPerTuple: joinQualPerTuple(cp, quals),
				outer:        o.Cost, inner: i.Cost,
				outerRows: o.Rows, innerRows: i.Rows,
				outputRows:       rows,
				numHashClauses:   len(keys),
				hashQualCost:     hashClausesPerTuple(cp, keys),
				innerBucketSize:  bucket,
				final:            *final,
				outerWidth:       pathWidth(o),
				innerWidth:       pathWidth(i),
				outerCols:        pathNCols(o),
				innerCols:        pathNCols(i),
				outerAvgVarBytes: pathAvgVarBytes(o),
				innerAvgVarBytes: pathAvgVarBytes(i),
			})
			addPath(joinrel, &Path{
				Kind:          PathHashJoin,
				Jointype:      jt,
				SJInfo:        sjinfo,
				DisabledNodes: disabledNodesFor(!cp.enableHashJoin, o, i),
				Rel:           joinrel,
				// ppi_rows: one execution with the parameter bound.
				Rows:          rows,
				Cost:          cost,
				Children:      []*Path{o, i},
				OuterRelids:   outer.Relids,
				InnerRelids:   inner.Relids,
				HashKeys:      keys,
				Residual:      residual,
				RequiredOuter: req,
				ParamFilter:   pf,
				ParallelSafe:  parallelSafeWith(joinrel, o, i),
			}, "join.hash.param")
		}
	}
}

// parameterizedJoinrelSize is `get_parameterized_joinrel_size`
// (costsize.c): the join's size estimate over the input PATHS' rows (each a
// parameterised input's ppi_rows) and the join's clauses plus the ones the
// parameterisation moves onto it, capped by the unparameterised estimate
// ("for safety").
//
// The moved clauses are `get_joinrel_parampathinfo`'s: those joining a
// required-outer relation to this joinrel that neither input's probe already
// applies. goopg does not re-derive PG's dropped-EC clause (the `Z.Z = X.X`
// regeneration); a clause the search did not list is not sized here.
func (s *searchCtx) parameterizedJoinrelSize(joinrel, outer, inner *RelOptInfo, o, i *Path, req RelSet, clauses []*restrictInfo, sjinfo *SpecialJoinInfo) float64 {
	sized := clauses
	if s.clauses != nil {
		enforced := map[*restrictInfo]bool{}
		for ri := range probeEnforcedClauses(o) {
			enforced[ri] = true
		}
		for ri := range probeEnforcedClauses(i) {
			enforced[ri] = true
		}
		all := joinrel.Relids | req
		for _, ri := range s.clauses.all {
			if ri == nil || enforced[ri] || ri.relids&req == 0 || ri.relids&joinrel.Relids == 0 || ri.relids&^all != 0 {
				continue
			}
			sized = append(sized[:len(sized):len(sized)], ri)
		}
	}
	oRel, iRel := *outer, *inner
	oRel.Rows, iRel.Rows = o.Rows, i.Rows
	rows, _ := s.calcJoinrelSize(s.cat, &oRel, &iRel, sized, sjinfo)
	if rows > joinrel.Rows {
		rows = joinrel.Rows
	}
	return clampRowEst(rows)
}

// paramJoinPaths walks a parameterised join inner and returns every path in it
// that carries a parameterisation, the probes included.
func paramJoinPaths(p *Path, out []*Path) []*Path {
	if p == nil || p.RequiredOuter == 0 {
		return out
	}
	out = append(out, p)
	if p.Kind == PathHashJoin {
		for _, c := range p.Children {
			out = paramJoinPaths(c, out)
		}
	}
	return out
}

// paramProbeScan finds the index probe under the wrappers createPlanNode puts
// on a leaf (its local quals' Filter, a narrowing Project).
func paramProbeScan(n Node) Node {
	for {
		switch x := n.(type) {
		case *IndexScan, *IndexOnlyScan, *BitmapHeapScan:
			return x
		case *Filter:
			n = x.Child
		case *Project:
			n = x.Child
		default:
			panic(fmt.Sprintf("createPlan: a parameterised index probe was built as %T; its keys cannot be bound", n))
		}
	}
}

// createNestLoopParamJoinPlan lowers a nested loop whose inner is a
// parameterised hash join: `Join{Algo: NestedLoop, Lateral: true}` over the
// hash join, each parameterised probe's keys rebound onto the outer row as
// level-1 OuterColumnRefs — the NLI arm's binding, applied to the probes found
// below the hash join.
func createNestLoopParamJoinPlan(p *Path, innerPath *Path) (Node, outputLayout) {
	outerPath := p.Children[0]
	if outerPath == nil || outerPath.Rel == nil {
		panic("createPlan: parameterised-join nested loop over an outer child with no RelOptInfo")
	}
	if unsupplied := innerPath.RequiredOuter &^ outerPath.Rel.Relids; unsupplied != 0 {
		panic(fmt.Sprintf("createPlan: nested-loop inner join is parameterised by %#08x, which the outer relset %#08x does not supply",
			uint32(unsupplied), uint32(outerPath.Rel.Relids)))
	}
	jt := planJoinTypeFor(p, "PathNestLoop(param-join)")
	if jt == JoinTypeRight {
		panic("createPlan: PathNestLoop(param-join) with jointype RIGHT; the preserved side is the parameterised inner (R64)")
	}
	var probes []paramProbeNode
	marked := paramJoinPaths(innerPath, nil)
	for _, mp := range marked {
		mp.paramSink = &probes
	}
	in := joinInputsFor(p, "PathNestLoop(param-join)", outerPath, innerPath)
	for _, mp := range marked {
		mp.paramSink = nil
	}
	nLeaves := 0
	for _, mp := range marked {
		if paramProbeIndexPath(mp) != nil {
			nLeaves++
		}
	}
	nProbes := 0
	for _, pr := range probes {
		if pr.bind == nil {
			nProbes++
		}
	}
	if nProbes != nLeaves || nLeaves == 0 {
		panic(fmt.Sprintf("createPlan: parameterised join inner built %d probes for %d parameterised leaves", len(probes), nLeaves))
	}
	outerLay := in.lay[:len(in.outer.Output())]
	outerIndex := outerLay.bindingIndex()
	for _, pr := range probes {
		if pr.bind != nil {
			pr.bind(outerLay, outerIndex)
			continue
		}
		bindParamProbe("param-join", paramProbeScan(pr.node), paramProbeIndexPath(pr.path), outerLay, outerIndex)
	}
	j := &Join{
		pos:       in.outer.Pos(),
		Type:      jt,
		Algo:      JoinAlgoNestedLoop,
		Lateral:   true,
		Left:      in.outer,
		Right:     in.inner,
		Predicate: in.joinPredicate("PathNestLoop(param-join)", nil, p.Residual, p.ecClausesLast()),
		schema:    in.publishedSchema(jt),
		SJInfo:    p.SJInfo,
	}
	return j, in.publishedLayout(jt)
}

// bindParamProbe rebinds one parameterised probe's keys onto a nested loop's
// outer row, as level-1 OuterColumnRefs (outerParamKey): an index scan's keys
// directly; a bitmap heap scan's on its bitmap index scan, with the recheck
// (PG's bitmapqualorig) kept on the heap scan's own Cond — under a lateral
// join there is no merged outer++inner row for BitmapQual's coordinates, so
// the probe column is compared against the same nestloop param the index key
// reads. idxPath is the path whose IndexClauses the probe binds.
func bindParamProbe(what string, probe Node, idxPath *Path, outerLay outputLayout, outerIndex map[int]int) {
	if idxPath == nil {
		panic(fmt.Sprintf("createPlan: %s probe %T has no index path to bind", what, probe))
	}
	keys := make([]Expr, 0, len(idxPath.IndexClauses))
	for _, c := range idxPath.IndexClauses {
		if c.key == nil {
			panic(fmt.Sprintf("createPlan: %s probe clause with no key", what))
		}
		keys = append(keys, outerParamKey(what+" probe key", c.key, outerLay, outerIndex))
	}
	if len(keys) == 0 {
		panic(fmt.Sprintf("createPlan: %s probe binds no key", what))
	}
	bhs, isBitmap := probe.(*BitmapHeapScan)
	if !isBitmap {
		setNLIProbeKeys(probe, keys)
		return
	}
	bis, ok := bhs.Outer.(*BitmapIndexScan)
	if !ok {
		panic(fmt.Sprintf("createPlan: %s bitmap probe over %T", what, bhs.Outer))
	}
	if len(keys) == 1 {
		bis.Key, bis.Keys = keys[0], nil
	} else {
		bis.Key, bis.Keys = nil, keys
	}
	out := bhs.Output()
	conds := []Expr{}
	if bhs.Cond != nil {
		conds = append(conds, bhs.Cond)
	}
	for k, c := range idxPath.IndexClauses {
		if idxPath.IndexInfo == nil || c.indexCol >= len(idxPath.IndexInfo.Columns) {
			panic(fmt.Sprintf("createPlan: %s bitmap clause names no index column", what))
		}
		name := idxPath.IndexInfo.Columns[c.indexCol]
		col := -1
		for j, sc := range out {
			if strings.EqualFold(sc.Name, name) {
				col = j
				break
			}
		}
		if col < 0 {
			panic(fmt.Sprintf("createPlan: %s bitmap recheck column %s not in the scan", what, name))
		}
		conds = append(conds, &BinaryOp{Op: parser.OpEq,
			Left:  &ColumnRef{Index: col, Name: out[col].Name, Type: out[col].Type},
			Right: keys[k]})
	}
	bhs.BitmapQual = nil
	bhs.Cond = combineAnd(conds)
}
