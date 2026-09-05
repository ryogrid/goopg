package optimizer

// considerparallel.go — Phase 5 slices C-19a / C-19b (take3 08 §8):
// parallelism enters the PATH MODEL.
//
//   - C-19a: `set_rel_consider_parallel` (allpaths.c:589). Every base rel
//     carries whether it is worth generating partial paths for at all, and a
//     join rel inherits the conjunction of its inputs' flags and its own
//     clauses' safety (build_join_rel, relnode.c:842). Every serial path is
//     stamped `ParallelSafe` from its rel — `pathnode->parallel_safe =
//     rel->consider_parallel` in every create_*_path — which is what a partial
//     JOIN path (C-19d/e) will read off its inner side.
//
//   - C-19b: `create_plain_partial_paths` (allpaths.c:806). A partial seq scan
//     is a real path in `RelOptInfo.PartialPathlist`, sized by
//     `compute_parallel_worker` (allpaths.c:4274) and priced by cost_seqscan's
//     `parallel_workers > 0` arm (`costParallelSeqscan`), instead of the
//     post-planning size rule `computeParallelWorkers` (parallel.go) applies
//     to a finished tree.
//
// Why this matters (D-05's finding, analysis/minimize-datum/
// d05-buildcost-20260906 §6): goopg's cost model had NO parallel dimension —
// `MaybeAddGather` runs after the search, and only a hash join can carry a
// Gather — so every correction that made a hash join dearer traded a real 5×
// parallel speedup for a modelled saving. Three corrections failed on exactly
// that. The search cannot weigh parallelism it cannot see; these two slices
// make it visible.
//
// SERIAL CONTROL ARM UNCHANGED. Nothing here CONSUMES a partial path to
// choose a plan: `finalPath` reads `Pathlist` only, the join producers read
// `CheapestTotal` only, and `PartialPathlist` has no reader but the trace.
// That consumer is C-19d (`generate_useful_gather_paths`). The `ParallelSafe`
// stamp cannot move a plan either: it is uniform across every path of one
// rel by construction (base paths copy the rel's flag; a join path's flag is
// the rel's AND its children's, and each child rel is uniform), so the
// `boolDim` axis of `comparePaths` ties on every comparison it used to tie
// on. Gate: plans byte-identical to HEAD on TPC-H (estimate-audit
// -plan-only) and `make plan-gate`.

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// parallelModeOK is the query-wide gate `standard_planner` computes before any
// rel exists (planner.c:339-349): `glob->parallelModeOK`. PG's conditions are
// SELECT (not DML), no modifying CTE, `max_parallel_workers_per_gather > 0`,
// not already inside a worker, and `max_parallel_hazard(parse) != UNSAFE`.
//
// C-19a carries the two the search can answer from its own inputs: the GUC
// (through costParams) and goopg's process kill switch (GOOPG_PARALLEL=off,
// which retires with the post-pass at P5-08). The statement-kind and
// isolation-level halves live at the dispatch seam today
// (`statementIsParallelSafe`, `ParallelSettings.IsSerializable`) and reach
// the search with C-19d, when a partial path can first be CHOSEN — until
// then a partial path over a DML statement's FROM clause is priced and
// discarded, which is harmless.
func parallelModeOK(cp costParams) bool {
	return ParallelEnabled() && cp.maxParallelWorkersPerGather > 0
}

// setBaseRelConsiderParallel is `set_rel_consider_parallel` for every initial
// rel of the search (allpaths.c:589, called from set_rel_size:360 once per
// base rel when parallelModeOK). It runs as its own protocol step after
// `buildInitialRels` and before any path producer but the prebuilt seq scan,
// which it re-stamps: PG sets the flag in set_rel_size, BEFORE set_rel_pathlist
// builds the first path, and goopg's buildInitialRels is both at once.
//
// `cat` resolves user routines for the qual-safety walk (`proparallel`); nil is
// legal and fails closed for any function the builtin table does not know.
func (s *searchCtx) setBaseRelConsiderParallel(cat catalog.Catalog) {
	if s == nil || len(s.joinrels) < 2 {
		return
	}
	s.cat = cat
	s.parallelModeOK = parallelModeOK(s.cp)
	for i, rel := range s.joinrels[1] {
		rel.ConsiderParallel = false
		if s.parallelModeOK && i < len(s.relInfos) {
			rel.ConsiderParallel = relConsiderParallel(rel.baseLeaf, s.relInfos[i].table, cat)
		}
		// The prebuilt path predates the flag (see above). Every path on the
		// rel at this point is a base-rel scan with no children, so the stamp
		// is exact for all of them.
		for _, p := range rel.Pathlist {
			p.ParallelSafe = rel.ParallelSafeForPath()
		}
	}
}

// ParallelSafeForPath is the value `create_*_path` stamps on a childless path
// of this rel: `pathnode->parallel_safe = rel->consider_parallel`
// (pathnode.c, every scan constructor). Paths with children AND their
// children's flags in (`parallelSafeWith`).
func (rel *RelOptInfo) ParallelSafeForPath() bool {
	return rel != nil && rel.ConsiderParallel
}

// parallelSafeWith is the join/wrapper form of the stamp:
// `joinrel->consider_parallel && outer_path->parallel_safe &&
// inner_path->parallel_safe` (create_hashjoin_path, pathnode.c:2740; the
// nestloop and mergejoin constructors are identical). A wrapper over ONE
// child (Sort, Memoize, BitmapHeap over its index child) is the same rule
// with one child.
func parallelSafeWith(rel *RelOptInfo, children ...*Path) bool {
	if !rel.ParallelSafeForPath() {
		return false
	}
	for _, c := range children {
		if c == nil || !c.ParallelSafe {
			return false
		}
	}
	return true
}

// relConsiderParallel is the per-rel body of set_rel_consider_parallel: the
// rtekind switch (allpaths.c:604-737) over what the search leaf IS, then the
// baserestrictinfo and reltarget walks (:748-759). `leaf` is what
// buildInitialRels was handed for the FROM item — a scan under its LeafLocal
// Filter wrappers, or a whole planned subtree for a subquery / CTE / SRF.
func relConsiderParallel(leaf Node, tbl *catalog.Table, cat catalog.Catalog) bool {
	if leaf == nil {
		return false
	}
	// Peel the local-qual wrappers: they are `baserestrictinfo`, checked
	// below, and what sits under them decides the rtekind arm.
	var quals []Expr
	base := leaf
	for {
		f, ok := base.(*Filter)
		if !ok || f.Child == nil {
			break
		}
		quals = append(quals, f.Predicate)
		base = f.Child
	}
	switch x := base.(type) {
	case *SeqScan:
		// RTE_RELATION: temp is parallel-restricted (:610-622), and goopg's
		// virtual catalog relations are backed by per-leader callbacks
		// (tableIsUnsafeForParallel). A TABLESAMPLE pushes down when the
		// method and its arguments are safe (:628-635); both of goopg's
		// methods (bernoulli, system) are PROPARALLEL_SAFE upstream.
		if tableIsUnsafeForParallel(x.Table) {
			return false
		}
		if x.TableSample != nil {
			for _, a := range x.TableSample.Args {
				if !isParallelSafeExpr(a, cat) {
					return false
				}
			}
			if x.TableSample.Repeatable != nil && !isParallelSafeExpr(x.TableSample.Repeatable, cat) {
				return false
			}
		}
	case *IndexScan:
		// The leaf keeps its predicate in Key/Keys/LowKey/HighKey/SAOPKeys,
		// not in a Filter wrapper, so `quals` never sees it. Review
		// finding: `WHERE id = nextval('s')` as an index key must clear the
		// rel too, or the rel's ConsiderParallel flows into join rels and
		// stamps their paths safe.
		if tableIsUnsafeForParallel(x.Table) {
			return false
		}
		if !exprsParallelSafe(cat, x.Key, x.LowKey, x.HighKey) ||
			!exprListParallelSafe(cat, x.Keys) || !exprListParallelSafe(cat, x.SAOPKeys) {
			return false
		}
	case *IndexOnlyScan:
		if tableIsUnsafeForParallel(x.Table) {
			return false
		}
		if !exprsParallelSafe(cat, x.Key, x.LowKey, x.HighKey) ||
			!exprListParallelSafe(cat, x.Keys) {
			return false
		}
	case *BitmapHeapScan:
		if tableIsUnsafeForParallel(x.Table) {
			return false
		}
		if !exprListParallelSafe(cat, x.BitmapQual) {
			return false
		}
		// The bitmap-producing subtree may hold index scans with their own
		// keys; the subtree walk checks them.
		if x.Outer != nil && !subtreeHasNoParallelHazard(x.Outer, cat) {
			return false
		}
	case *CTEScan, *MaterializedCTEScan:
		// RTE_CTE: "CTE tuplestores aren't shared among parallel workers, so
		// we force all CTE scans to happen in the leader" (:706-718).
		return false
	case *Values:
		// RTE_VALUES: the lists must be parallel-safe (:697-702).
		for _, row := range x.Rows {
			for _, e := range row {
				if !isParallelSafeExpr(e, cat) {
					return false
				}
			}
		}
	case *UserSrfScan:
		// RTE_FUNCTION: `is_parallel_safe(rte->functions)` (:685-689) — the
		// routine's own proparallel plus its arguments.
		if !routineIsParallelSafe(x.Routine) {
			return false
		}
		for _, a := range x.Args {
			if !isParallelSafeExpr(a, cat) {
				return false
			}
		}
	case *GenerateSeries, *GenerateSubscripts, *FromUnnest:
		// Builtin SRFs are PROPARALLEL_SAFE; their arguments are checked as
		// part of the expression walk below only when they are quals, so
		// walk them here. The three carry their arguments differently and
		// none is a qual, so the conservative reading is "safe unless a
		// qual says otherwise" — the same as PG, whose generate_series is
		// 's'.
	case *ScalarFuncScan:
		// RTE_FUNCTION over a scalar routine (`FROM myfunc()`): the routine's
		// proparallel and its arguments, exactly as the SRF arm. Review
		// finding: this used to fall to the default arm, whose subtree walk
		// saw no children and read that as "nothing unsafe".
		if !isParallelSafeExpr(x.Func, cat) {
			return false
		}
	case *PgInputErrorInfo, *PgGetPublicationTables, *PgGetSequenceData,
		*PgSequenceParameters, *PgAvailableWalSummaries, *PgGetCatalogForeignKeys,
		*PgPartitionTree, *PgOptionsToTable:
		// Catalog SRFs backed by the same leader-only callbacks a worker
		// context nils (the hazard tableIsUnsafeForParallel refuses for
		// virtual relations). PG's RTE_TABLEFUNC / RTE_NAMEDTUPLESTORE
		// analogues are unsafe too (allpaths.c:720-730).
		return false
	default:
		// A planned subtree — subquery-in-FROM, set operation, a searched
		// sub-problem re-entering as a leaf. RTE_SUBQUERY (:641-677) is
		// parallel-safe unless it needs a LIMIT/OFFSET ("no guarantee that
		// the row order will be fully deterministic"); the subquery's OWN
		// paths then decide, which goopg cannot ask a built Node, so the
		// closest reading is the post-pass's safety walk over the subtree —
		// the same refusals a worker would hit. A Limit anywhere below is
		// `limit_needed`.
		if !subtreeHasNoParallelHazard(base, cat) {
			return false
		}
	}
	// baserestrictinfo (:748): anything parallel-restricted in the rel's own
	// quals gives up on the rel — "postponing application of the restricted
	// quals until we're above all the parallelism … might be tricky".
	for _, q := range quals {
		if !isParallelSafeExpr(q, cat) {
			return false
		}
	}
	// reltarget (:756): a scan leaf emits bare columns, which are safe; the
	// Project a subquery leaf carries is checked by the subtree walk above.
	return true
}

// subtreeHasNoParallelHazard is the subquery arm's reading of "does this
// subquery have parallel-safe paths": no node a worker cannot run
// (`subtreeHasUnsafeNode` — LockRows, temp / virtual relations), no Limit
// (`limit_needed`, allpaths.c:670), and — review finding — no
// parallel-unsafe EXPRESSION anywhere in the body, which is what PG gets for
// free because the subquery's own paths carry `parallel_safe` derived from
// their targets and quals. `FROM (SELECT nextval('s'), x FROM big) q` was
// being marked safe on node kinds alone.
//
// The walk FAILS CLOSED on a node it does not enumerate. It used to descend
// through `parallelChildren`, which answers "no children" for anything it
// does not model, and the SAFETY reading of "no children" is "nothing
// unsafe below" — the opposite of conservative. A WindowAgg, SetOp,
// Memoize or SubqueryScan hiding a temp table or a LockRows therefore
// passed. Now an unmodelled node refuses the whole subquery; the cost is a
// partial path not generated, which is the pre-C-19 regime for that rel.
func subtreeHasNoParallelHazard(n Node, cat catalog.Catalog) bool {
	if subtreeHasUnsafeNode(n) {
		return false
	}
	ok := true
	var walk func(Node)
	walk = func(cur Node) {
		if cur == nil || !ok {
			return
		}
		switch x := cur.(type) {
		case *Limit:
			ok = false
			return
		case *Project:
			if !exprListParallelSafe(cat, x.Targets) {
				ok = false
				return
			}
		case *Filter:
			if !isParallelSafeExpr(x.Predicate, cat) {
				ok = false
				return
			}
		case *Join:
			if x.Predicate != nil && !isParallelSafeExpr(x.Predicate, cat) {
				ok = false
				return
			}
		case *Aggregate:
			if !exprListParallelSafe(cat, x.GroupExprs) {
				ok = false
				return
			}
			for i := range x.Aggs {
				a := &x.Aggs[i]
				if !exprsParallelSafe(cat, a.Arg, a.Arg2) || !exprListParallelSafe(cat, a.ExtraArgs) {
					ok = false
					return
				}
			}
		case *Sort, *Distinct, *DistinctOn, *Gather, *LockRows, *NestedLoopIndexJoin, *BitmapHeapScan:
			// Enumerated by parallelChildren; no expression channel of
			// their own that a worker could not evaluate (LockRows is
			// refused by subtreeHasUnsafeNode above).
		case *SeqScan, *IndexScan, *IndexOnlyScan, *Values, *GenerateSeries,
			*GenerateSubscripts, *FromUnnest, *UserSrfScan, *CTEScan,
			*MaterializedCTEScan, *ScalarFuncScan:
			// A leaf: its own classification is the base-rel arm's job
			// (relConsiderParallel), which a planned subtree's leaves went
			// through when THEIR rels were classified. A leaf that reaches
			// here as a subquery body is re-checked as a rel by the caller's
			// arm, so nothing is lost by stopping.
			return
		default:
			// Unmodelled — WindowAgg, SetOp, Memoize, OrdinalityWrap,
			// RowsFrom, the Pg* SRFs, anything future. Refuse.
			ok = false
			return
		}
		for _, c := range parallelChildren(cur) {
			walk(c)
		}
	}
	walk(n)
	return ok
}

// exprsParallelSafe / exprListParallelSafe are isParallelSafeExpr over a
// variadic / slice of possibly-nil expressions.
func exprsParallelSafe(cat catalog.Catalog, es ...Expr) bool {
	for _, e := range es {
		if e != nil && !isParallelSafeExpr(e, cat) {
			return false
		}
	}
	return true
}

func exprListParallelSafe(cat catalog.Catalog, es []Expr) bool {
	for _, e := range es {
		if e != nil && !isParallelSafeExpr(e, cat) {
			return false
		}
	}
	return true
}

// joinrelConsiderParallel is build_join_rel's propagation (relnode.c:829-845):
//
//	if (inner_rel->consider_parallel && outer_rel->consider_parallel &&
//	    is_parallel_safe(root, (Node *) restrictlist) &&
//	    is_parallel_safe(root, (Node *) joinrel->reltarget->exprs))
//	    joinrel->consider_parallel = true;
//
// A join rel's target is the concatenation of its inputs' columns (bare
// Vars), so the last conjunct is vacuous here.
func joinrelConsiderParallel(s *searchCtx, rel1, rel2 *RelOptInfo, clauses []*restrictInfo) bool {
	if s == nil || !s.parallelModeOK || !rel1.ConsiderParallel || !rel2.ConsiderParallel {
		return false
	}
	for _, ri := range clauses {
		if ri == nil || !isParallelSafeExpr(ri.clause, s.cat) {
			return false
		}
	}
	return true
}

// isParallelSafeExpr is `is_parallel_safe` (clauses.c:706) reduced to the
// hazards goopg's expression language can carry: `max_parallel_hazard_walker`
// (clauses.c:774) reports PARALLEL_UNSAFE / RESTRICTED for
//
//   - a function whose proparallel is not 's' (user routines default to 'u',
//     as in CREATE FUNCTION; builtins from the restricted table below);
//   - a SubPlan (:857) — every sublink form here; the exprwalk `scopeVeto`
//     policy ABORTS on them, which is exactly the answer;
//   - a PARAM_EXEC Param (:894) — an outer-scope reference or an executor
//     parameter, both of which only the leader can supply;
//   - an expression type the walker does not enumerate: fail-closed, the
//     walker's own rule.
//
// Everything else (Vars, Consts, operators, casts, CASE, IS NULL, …) is safe.
// Operators are functions too and PG checks the underlying proc; goopg's
// builtin operators are all parallel-safe, so BinaryOp / UnaryOp pass.
func isParallelSafeExpr(e Expr, cat catalog.Catalog) bool {
	if e == nil {
		return true
	}
	safe := true
	ok := walkExprRefs(e, scopeVeto, exprVisitor{
		Visit: func(x Expr) bool {
			if !safe {
				return false
			}
			switch v := x.(type) {
			case *FuncCall:
				if !funcCallIsParallelSafe(v, cat) {
					safe = false
				}
			case *OuterColumnRef, *ExecParamRef:
				safe = false
			}
			// A SubPlan in any of its forms: an expression that owns an
			// inner-scope plan slot. Decided by the slot table rather than
			// a type list so a sublink form added later is restricted by
			// construction (the veto below is the walker's own backstop).
			if safe {
				if slots, ok := exprChildSlots(x); ok {
					for _, sl := range slots {
						if sl.kind == slotInnerPlan || sl.kind == slotSubqRow {
							safe = false
							break
						}
					}
				}
			}
			return safe
		},
		OnUnknown: func(Expr) { safe = false },
	})
	return ok && safe
}

// funcCallIsParallelSafe resolves one call: a user routine by name through the
// catalog (any parallel-unsafe overload refuses — the search does not resolve
// overloads, so it must not guess), else a builtin against the restricted
// table. A name the catalog cannot answer and the table does not list is a
// builtin, and PG's builtins are overwhelmingly 's'.
func funcCallIsParallelSafe(fc *FuncCall, cat catalog.Catalog) bool {
	if fc == nil {
		return true
	}
	name := strings.ToLower(fc.Name)
	// Strip a schema qualifier before the builtin lookup: `pg_catalog.nextval`
	// must hit the table the same as `nextval` (review finding).
	bare := name
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		bare = name[i+1:]
	}
	if parallelRestrictedBuiltins[bare] {
		return false
	}
	if cat != nil {
		if rs := cat.Routines(); rs != nil {
			on := parser.ObjectName{Name: name}
			if i := strings.LastIndexByte(name, '.'); i >= 0 {
				on = parser.ObjectName{Schema: name[:i], Name: name[i+1:]}
			}
			for _, r := range rs.LookupByName(on) {
				if !routineIsParallelSafe(r) {
					return false
				}
			}
		}
	}
	return true
}

// routineIsParallelSafe reads `proparallel`. CREATE FUNCTION's default is
// UNSAFE ('u'), and goopg stores the unset value as "" (routines.go:80), so
// only an explicit 's' passes.
func routineIsParallelSafe(r *catalog.Routine) bool {
	return r != nil && r.Parallel == "s"
}

// parallelRestrictedBuiltins lists the builtin functions whose upstream
// `proparallel` is 'r' or 'u' and that a WHERE clause plausibly names: the
// sequence functions (nextval/currval/setval/lastval — 'u'), transaction and
// backend introspection ('r': they read leader-only state), advisory locks
// ('r'), and set_config/pg_notify ('r'/'u'). The list is a transcription of
// pg_proc.dat's non-'s' entries filtered to those goopg implements; a
// builtin missing from it is treated as 's', which is the value all but ~200
// of PG's ~3000 builtins carry.
var parallelRestrictedBuiltins = map[string]bool{
	"nextval": true, "currval": true, "setval": true, "lastval": true,
	"txid_current": true, "txid_current_if_assigned": true, "txid_status": true,
	"pg_current_xact_id": true, "pg_current_xact_id_if_assigned": true, "pg_xact_status": true,
	"pg_current_snapshot": true, "txid_current_snapshot": true,
	"pg_backend_pid": true, "pg_stat_get_activity": true, "pg_stat_get_backend_pid": true,
	"pg_advisory_lock": true, "pg_advisory_unlock": true, "pg_advisory_lock_shared": true,
	"pg_advisory_unlock_shared": true, "pg_advisory_unlock_all": true,
	"pg_try_advisory_lock": true, "pg_try_advisory_lock_shared": true,
	"pg_advisory_xact_lock": true, "pg_advisory_xact_lock_shared": true,
	"pg_try_advisory_xact_lock": true, "pg_try_advisory_xact_lock_shared": true,
	"set_config": true, "pg_notify": true, "pg_listening_channels": true,
	"pg_cursor": true, "pg_prepared_statement": true, "pg_get_viewdef": true,
	"currtid2": true, "pg_export_snapshot": true, "pg_log_backend_memory_contexts": true,
	"pg_sleep": true, "pg_sleep_for": true, "pg_sleep_until": true,
	// 'r' in pg_proc.dat (:3488-3507): a worker drawing its own stream gives
	// "different rows per run", the canonical parallel wrong answer. Review
	// finding — these were missing.
	"random": true, "random_normal": true, "setseed": true,
}

// addBaseRelPartialPaths is `create_plain_partial_paths` (allpaths.c:806) for
// every initial rel that is a plain relation: the protocol step
// set_plain_rel_pathlist (:768) runs right after the serial seq scan and
// before create_index_paths, gated on `rel->consider_parallel &&
// required_outer == NULL` (:783). C-19b.
//
// The page count is `rel->pages` — `baseRelPages`, the same figure every
// index producer in this search prices against — so a partial scan and the
// serial scans it will one day compete with share one page model. The
// post-pass (`computeParallelWorkers`) reads a LIVE smgr block count instead;
// C-19h reconciles the two when the post-pass retires, and until then the
// two can disagree only in the worker count of a path nothing consumes.
func (s *searchCtx) addBaseRelPartialPaths() {
	if s == nil || !s.parallelModeOK || len(s.joinrels) < 2 {
		return
	}
	for i, rel := range s.joinrels[1] {
		if !rel.ConsiderParallel || i >= len(s.relInfos) {
			continue
		}
		tbl := s.relInfos[i].table
		scan, ok := leafBaseScan(rel.baseLeaf).(*SeqScan)
		if !ok || tbl == nil || scan.Table == nil {
			// Only RTE_RELATION leaves get a plain partial path; an index or
			// bitmap leaf is the legacy rule-based planner's choice standing
			// in for the relation, and a subtree leaf is not a relation.
			continue
		}
		// WORKER COUNT is sized on `baserel->pages` proper — baseRelPages,
		// the physical-size figure every index producer in this search
		// reads (pathbitmap.go:49, pathparamindex.go:274) and the closest
		// the path model has to the post-pass's live block count.
		relTuples := float64(s.relInfos[i].baseRows)
		if relTuples < 1 {
			relTuples = 1
		}
		workers := computeParallelWorkerForRel(s.cp, baseRelPages(tbl, relTuples), tableParallelWorkersReloption(tbl))
		if workers <= 0 {
			continue
		}
		// COST is priced on the SAME inputs the serial seq scan of this rel
		// was priced on — `costSeqscan(cp, estScanPages(rows, width), rows,
		// 0)` in buildInitialRels, with rows = rel.Rows and width =
		// rel.Width — so the partial path is exactly "the serial scan with
		// its CPU share divided", which is the relationship C-19d's
		// Gather-vs-serial comparison must see. The serial prebuilt scan
		// does NOT read baseRelPages (a pre-C-19 split: it prices pages from
		// rows × width even on an ANALYZEd relation, while the index rivals
		// read relpages); unifying it onto rel->pages is a plan-moving
		// change of its own and is C-19d's to land, not this slice's.
		pages := estScanPages(rel.Rows, rel.Width)
		addPartialSeqScanPath(rel, s.cp, pages, rel.Rows, 0, workers)
	}
}

// computeParallelWorkerForRel is `compute_parallel_worker` (allpaths.c:4274)
// with `index_pages = -1`, the create_plain_partial_paths call. It is the
// PATH-MODEL twin of `computeParallelWorkers` (parallel.go): the same log3
// ladder over min_parallel_table_scan_size, capped by
// max_parallel_workers_per_gather, with the table's parallel_workers
// reloption winning outright. `reloptionWorkers` is 0 when unset (goopg's
// ParallelWorkersSet convention; PG's -1).
//
// The post-pass twin stays until P5-08 retires it; the arithmetic is kept
// identical on purpose so the two answer the same number for the same
// relation, which is what lets C-19d's Gather paths be compared against the
// post-pass's placement while both exist.
func computeParallelWorkerForRel(cp costParams, heapPages int64, reloptionWorkers int) int {
	maxWorkers := cp.maxParallelWorkersPerGather
	var workers int
	if reloptionWorkers > 0 {
		workers = reloptionWorkers
	} else {
		if heapPages < cp.minParallelTableScanBlocks {
			return 0
		}
		threshold := cp.minParallelTableScanBlocks
		if threshold < 1 {
			threshold = 1
		}
		workers = 1
		for heapPages >= threshold*3 {
			workers++
			threshold *= 3
			if threshold > (1<<31-1)/3 { // INT_MAX / 3, upstream's overflow break
				break
			}
		}
	}
	// "In no case use more than max_workers" (:4356).
	if workers > maxWorkers {
		workers = maxWorkers
	}
	return workers
}

// addPartialSeqScanPath adds the partial seq scan `create_seqscan_path(root,
// rel, NULL, parallel_workers)` builds for create_plain_partial_paths: the
// serial scan's shape with `parallel_workers > 0`, priced by cost_seqscan's
// parallel arm. A partial path is always parallel-safe (`parallel_safe =
// rel->consider_parallel`, and the caller has checked the rel).
func addPartialSeqScanPath(rel *RelOptInfo, cp costParams, relPages int64, relTuples float64, numQualOps, workers int) {
	cost, rows := costParallelSeqscan(cp, relPages, relTuples, rel.Rows, numQualOps, workers)
	tgt, tgtKnown := scanPathTarget(rel)
	addPartialPath(rel, &Path{
		Kind: PathSeqScan,
		Rel:  rel,
		Rows: rows,
		Cost: cost,
		// B-17d: `path->disabled_nodes = enable_seqscan ? 0 : 1`
		// (costsize.c:356) — the partial scan counts the flag too.
		DisabledNodes:   disabledNodesFor(!cp.enableSeqScan),
		ParallelSafe:    true,
		ParallelWorkers: workers,
		Target:          tgt,
		TargetKnown:     tgtKnown,
	}, "scan.seq.partial")
}
