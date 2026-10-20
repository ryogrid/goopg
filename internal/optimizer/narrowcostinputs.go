package optimizer

import (
	"os"
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// R121 Slice A — narrow the planner's COST inputs to the columns the statement
// actually needs.
//
// The gap this closes: the planner prices a join at the FULL concatenated
// relation width while the executor builds a NARROWED hash table. Today
// `Path.NCols`/`AvgVarBytes`/`OutputWidth` are written at exactly one
// production site (`pathindexonly.go`, index-only scans); every other path
// falls back to `relNCols` = the leaf's whole schema. Measured on TPC-H Q10
// (R120): goopg's per-group entry is 3888 B against PG's ~224 B, of which
// 1776 B is `48*ncols` over 37 concatenated columns where PG carries ~7.
//
// Three rules make this safe, and each is load-bearing:
//
//  1. **Narrow the Path, never the RelOptInfo.** `buildAvgVarBytes`
//     (entrywidth.go) reads `Rel.AvgVarBytes`/`Rel.ColVarBytes` as its
//     deliberate OVER-charge decline value for the EXECUTOR's hash entry
//     (createplanjoin.go), and rels are per-relset singletons shared across
//     every candidate path and parent. Narrowing the rel would both under-size
//     a real hash build and serve one parent's projection to another.
//
//  2. **All three fields or none.** Hash-join cost consumes `pathWidth` AND
//     `pathNCols`/`pathAvgVarBytes` on the same path (pathgen.go,
//     hashjoin_pgtuplesizing.go). A path narrowed in one currency and full in
//     the other reproduces exactly the defect R120 diagnosed, one level up.
//
//  3. **All-or-none per rel** (among the paths this round writes). If some
//     paths of a rel were narrowed and others not, `addPath` would compare
//     candidate costs computed in different currencies and keep the "cheaper"
//     — a systematic bias toward whichever shape happened to be narrowable,
//     invisible to every values and category gate.
//
// Safety direction: `NeededCols` is a NAME set that deliberately over-states
// (a name needed for one relation counts for all) and abandons wholesale on
// any shape it cannot enumerate, with `NeededColsKnown == false` meaning
// "collector declined". So this can only ever keep TOO MANY columns, never too
// few, and `joinKeepSet ⊆ buildKeepSet ⊆ neededKeepSet` — planner-narrowed is
// a superset of executor-narrowed.
//
// **Default ON since R128** — promoted by the decision round R124 handed
// forward by name ("Promotion is therefore a judgement … and it should be
// taken deliberately rather than as a side effect of this round",
// r124-nontable-leaf-widths/REPORT.md). Measured warrant: TPC-H
// `join-method` 10→9 and `scan-type` 9→8 with no category rising and
// `parallelism` staying 0, artefacts in
// docs/design/not_ralph/plan_parity_fix_take2/r128-parity-over-throughput/.
//
// It is now opt-OUT (`v != "0"`), matching the sibling `GOOPG_NARROW_*`
// family. It remains the only one of them that gates a planner COST input
// rather than executor plan shape — that distinction survives promotion even
// though the opt-in/opt-out split no longer marks it.
//
// The trade is recorded rather than hidden (R128 SCOPE §4): promotion
// re-baselines every plan pin, changes 3 TPC-DS shapes (Q6/Q64/Q75) for no
// TPC-DS gain, and makes R124's accepted planner-publishes-below-executor
// divergence the shipped default rather than an opt-in arm. It is taken
// because the goal counts PG-likeness and disclaims runtime.
var narrowCostInputs = narrowCostInputsFromEnv(os.Getenv("GOOPG_NARROW_COST_INPUTS"))

func narrowCostInputsFromEnv(v string) bool { return v != "0" }

func narrowCostInputsEnabled() bool { return narrowCostInputs }

func setNarrowCostInputsForTest(on bool) func() {
	old := narrowCostInputs
	narrowCostInputs = on
	return func() { narrowCostInputs = old }
}

// narrowedWidths is the width triple a base rel's paths may carry, or a
// decline. `ok == false` means "this rel narrows nothing" and every path of it
// must be left at the relation-wide fallback (rule 3).
type narrowedWidths struct {
	ncols       int
	avgVarBytes float64
	outputWidth int
	ok          bool
}

// relNarrowedWidths computes the triple once per base rel.
//
// It declines — rather than narrowing to something smaller than the truth — on
// every input it cannot attribute:
//
//   - the flag is off, or the rel/leaf is missing;
//   - `NeededColsKnown == false` (the collector declined; NOT the same as an
//     empty set);
//   - an EMPTY keep-set. `SELECT count(*)` needs no column, but `Path.NCols`
//     must be > 0 for `pathAvgVarBytes` to read the narrowed bytes at all
//     (path.go), so "narrowed to zero columns" is unrepresentable. Declining
//     is the honest encoding.
//   - `ColVarBytes` absent (un-ANALYZEd table, subquery, CTE, VALUES), or ANY
//     kept column missing from it. `tableColVarBytes` deliberately omits a
//     column with no statistics rather than mapping it to zero, precisely so
//     this case is distinguishable — declining here fails HIGH, which is the
//     direction that cannot under-size a build.
func relNarrowedColumns(rel *RelOptInfo) ([]keptColumn, bool) {
	if !narrowCostInputsEnabled() || rel == nil || rel.baseLeaf == nil {
		return nil, false
	}
	keep, ok := scanPathTarget(rel)
	if !ok || len(keep) == 0 {
		return nil, false
	}
	out := rel.baseLeaf.Output()

	// R124: a rel with NO per-column map may still narrow, when there is no
	// statistic to lose.
	//
	// The decline below exists to stop us contributing a silent ZERO for a
	// column whose width the map does not carry — it fails HIGH, the only safe
	// direction for a hash build. But on a LEVEL-1 search rel `AvgVarBytes` and
	// `ColVarBytes` are assigned together and only under the table guard
	// (`joinsearch.go:403-411`), so a non-table leaf — a CTEScan, a planned
	// sub-problem subtree, a SetOp, a Filter — arrives with BOTH nil and 0. Its
	// un-narrowed fallback is therefore ALREADY `ncols = full, avgVar = 0`
	// (`relNCols` / `pathAvgVarBytes`, path.go). Narrowing ncols while carrying
	// avgVar = 0 hands the model the SAME variable-payload figure it already
	// uses and only corrects the column count. No estimate is invented.
	//
	// MEASURED REACH (R124, TPC-DS SF0.25): 32 such rels — CTEScan 14,
	// Project 9, SetOp 7, Filter 2, zero ordinary tables. Of those, only 10
	// actually narrow (`kept < full`, the largest a Project 144 -> 28); the
	// other 22 have `kept == full`, where this only replaces the NCols
	// zero-sentinel with the same count. Both are correct; the second is
	// numerically inert.
	//
	// The `AvgVarBytes == 0` guard is load-bearing and is a LIVE trip-wire: a
	// nonzero relation-wide figure with no per-column map means the statistic
	// EXISTS and cannot be attributed, so the original decline is right. Five
	// UPPER-rel sites already assign `AvgVarBytes` alone (upperrel.go,
	// groupingpaths.go, distinctpaths.go, windowsetoppaths.go), so any
	// successor extending narrowing beyond joinrels[1] will trip it on real
	// rels — correct behaviour, not a bug.
	//
	// KNOWN, ACCEPTED DIVERGENCE: avgVar = 0 under-states a leaf that really
	// emits text columns. Pre-existing on both arms for these rels, but via
	// R122's join propagation it now reaches join sums where the rel formerly
	// declined, and there the planner can price below the executor's
	// `buildAvgVarBytes` (which declines to the whole-relation sum).
	// Cost-only: these Path fields never reach the executor, which sizes from
	// `plan.AvgVarBytes` over REL fields (createplanjoin.go).
	noColMap := len(rel.ColVarBytes) == 0
	if noColMap && rel.AvgVarBytes != 0 {
		return nil, false
	}

	cols := make([]keptColumn, 0, len(keep))
	// width is set_rel_width (costsize.c) over the kept columns: each
	// attribute's get_attavgwidth — pg_statistic.stawidth, ANALYZE's average
	// width — and get_typavgwidth only for a column without one (M0146-0234).
	// The type-only sum priced char(n)/varchar columns at their declared
	// sliding-scale width: TPC-DS SF1 Q64's partial-merge outer Sort at width
	// 664 where PG's is 96.
	for _, i := range keep {
		if i < 0 || i >= len(out) {
			return nil, false
		}
		col := out[i]
		kc := keptColumn{name: col.Name}
		if !noColMap {
			// Coordinate conversion, not an aside: neededKeepSet matches column
			// names case-SENSITIVELY, while ColVarBytes is lowercase-keyed
			// (tableColVarBytes).
			w, found := rel.ColVarBytes[strings.ToLower(col.Name)]
			if !found {
				// Unattributed column — decline the whole rel rather than
				// contribute a silent zero.
				return nil, false
			}
			// noColMap leaves varBytes at a literal 0 — see the block
			// comment above.
			kc.varBytes = w
		}
		if aw := rel.ColVarBytes[strings.ToLower(col.Name)]; aw > 0 {
			kc.width = int(aw)
		} else {
			kc.width = typeWidth(col.Type)
		}
		cols = append(cols, kc)
	}
	return cols, true
}

// keptColumn is one column a base rel's narrowed target keeps: its name, its
// set_rel_width width and its variable-width payload.
type keptColumn struct {
	name     string
	width    int
	varBytes float64
}

// relNarrowedWidths computes the triple once per base rel: the sums over
// relNarrowedColumns.
func relNarrowedWidths(rel *RelOptInfo) narrowedWidths {
	cols, ok := relNarrowedColumns(rel)
	if !ok {
		return narrowedWidths{}
	}
	return sumKeptColumns(cols, nil)
}

// sumKeptColumns is a narrowed triple over cols, restricted to the names in
// keep when keep is non-nil. A sum over no column declines: "narrowed to zero
// columns" is unrepresentable (see relNarrowedColumns).
func sumKeptColumns(cols []keptColumn, keep map[string]bool) narrowedWidths {
	var n narrowedWidths
	for _, c := range cols {
		if keep != nil && !keep[c.name] {
			continue
		}
		n.ncols++
		n.avgVarBytes += c.varBytes
		n.outputWidth += c.width
	}
	if n.ncols == 0 {
		return narrowedWidths{}
	}
	if n.outputWidth < 1 {
		n.outputWidth = 1
	}
	n.ok = true
	return n
}

// joinrelNarrowedWidths is build_joinrel_tlist (relnode.c) for the cost
// triple (M0146-0234): a joinrel's target holds a base relation's attribute
// only while attr_needed names a relation outside the joinrel — the query's
// output above the tree, or a join clause that reaches past it. goopg's base
// rels narrow to a statement-wide needed set, and a join used to sum its
// children's triples, so a column read only by a join clause inside the
// joinrel stayed in its width (TPC-DS SF1 Q64: a partial-merge outer Sort at
// width 155 where PG's is 96).
//
// The keep set is a NAME set, as every narrowing input is: the above-tree
// names (outputCols) plus every column name read by a search clause whose
// relids touch both the joinrel and a relation outside it. A name kept for
// one alias keeps every same-named column — the over-keep direction. It
// declines (the caller keeps the children's sum) when the problem is not
// output-eligible, a clause cannot be inventoried, or any published member
// does not narrow. Only published members count: a SEMI/ANTI join emits its
// LHS alone (joinPublishesInner).
func (s *searchCtx) joinrelNarrowedWidths(joinrel *RelOptInfo) narrowedWidths {
	if s == nil || joinrel == nil || !narrowCostInputsEnabled() || !s.outputEligible ||
		!s.outputColsKnown || s.outputCols == nil || s.clauses == nil || len(joinrel.publishedMembers) == 0 {
		return narrowedWidths{}
	}
	keep, ok := s.attrNeededAbove(joinrel.Relids)
	if !ok {
		return narrowedWidths{}
	}
	var cols []keptColumn
	for _, m := range joinrel.publishedMembers {
		mc, ok := relNarrowedColumns(m)
		if !ok {
			return narrowedWidths{}
		}
		cols = append(cols, mc...)
	}
	return sumKeptColumns(cols, keep)
}

// attrNeededAbove is the NAME set of attributes a rel over relids must emit —
// attr_needed restricted to relations outside it (build_base_rel_tlists and
// build_joinrel_tlist, relnode.c): the query's output above the tree plus every
// column read by a search clause whose relids touch both relids and a relation
// outside them. A clause wholly inside relids — a base rel's own restriction,
// or a join clause already applied below — needs nothing emitted. ok=false
// when the problem is not output-eligible or a clause cannot be inventoried.
func (s *searchCtx) attrNeededAbove(relids RelSet) (map[string]bool, bool) {
	if s == nil || !s.outputEligible || !s.outputColsKnown || s.outputCols == nil || s.clauses == nil {
		return nil, false
	}
	keep := make(map[string]bool, len(s.outputCols)+16)
	for name := range s.outputCols {
		keep[name] = true
	}
	for _, ri := range s.clauses.all {
		if ri == nil || ri.clause == nil {
			continue
		}
		if ri.relids&relids == 0 || relsSubset(ri.relids, relids) {
			continue
		}
		if !visitColumnRefsByName(ri.clause, func(name string) { keep[name] = true }) {
			return nil, false
		}
	}
	return keep, true
}

// baseRelNarrowedWidths is a base rel's cost triple over its reltarget
// (M0146-0234): build_base_rel_tlists adds only the Vars a relation outside the
// rel needs, so a column read solely by the rel's own restriction quals is
// evaluated at the scan and never emitted — `date_dim` filtered on d_year and
// d_moy is priced at d_date_sk's width. goopg narrowed a base rel to the
// statement-wide needed set, WHERE included. A reltarget that keeps no
// column (`count(*)` over restrictions) is unrepresentable in the triple (see
// relNarrowedColumns), so it keeps the statement-wide narrowing, as does any
// rel the clause inventory cannot attribute.
func (s *searchCtx) baseRelNarrowedWidths(rel *RelOptInfo) narrowedWidths {
	cols, ok := relNarrowedColumns(rel)
	if !ok {
		return narrowedWidths{}
	}
	if keep, known := s.attrNeededAbove(rel.Relids); known {
		if n := sumKeptColumns(cols, keep); n.ok {
			return n
		}
	}
	return sumKeptColumns(cols, nil)
}

// applyTo stamps the triple onto a path. It is a no-op on a decline, so every
// call site reads as "narrow if this rel narrows", and rule 3 holds by
// construction: one `narrowedWidths` value serves all of a rel's paths.
//
// Index-only paths must NOT be passed here. They already carry a tighter,
// index-derived triple whose `covered` set is a genuinely narrower EMITTED
// schema — a real cost advantage rather than a currency artefact.
func (n narrowedWidths) applyTo(p *Path) {
	if !n.ok || p == nil {
		return
	}
	p.NCols = n.ncols
	p.AvgVarBytes = n.avgVarBytes
	p.OutputWidth = n.outputWidth
}

// narrowBaseRelCostWidths stamps the width triple on every base-rel scan path,
// in one sweep after all of them have been produced.
//
// One sweep rather than an edit in each of the five producers, deliberately:
//
//   - it cannot miss a producer. The prebuilt SeqScan (`buildInitialRels`) is
//     built ~30 lines BEFORE the needed-column set is stamped and so can never
//     narrow at its own constructor — the ordering hazard that made P4-01b's
//     first version silently dormant — while the partial-seqscan, index,
//     bitmap and parameterised-index producers each build fresh `Path` values
//     that set no triple at all.
//   - it makes the all-or-none PER REL rule (rule 3) hold by construction
//     instead of by five call sites agreeing. `relNarrowedWidths` is consulted
//     once per rel and the same verdict is applied to every path of it.
//
// Index-only paths are the one exemption and are skipped by the `NCols > 0`
// test: they already carry a tighter, index-derived triple whose `covered` set
// is a genuinely narrower EMITTED schema, so their cost advantage is real
// rather than a currency artefact. (A consequence worth naming: an un-ANALYZEd
// indexed table can therefore hold an index-only path with a triple while
// every other path of that rel declines — that mix pre-dates this round and is
// why rule 3 is scoped to the paths this round writes.)
//
// Runs before `addBaseRelGatherPaths` so Gather/GatherMerge inherit an
// already-narrowed child (rule: A(ii), `inheritNarrowedWidths`).
func (s *searchCtx) narrowBaseRelCostWidths() {
	if s == nil || !narrowCostInputsEnabled() || len(s.joinrels) < 2 {
		return
	}
	// joinrels[1] is the base-rel level: only base rels have a baseLeaf and a
	// needed-column keep-set to narrow against.
	for _, r := range s.joinrels[1] {
		if r == nil || r.baseLeaf == nil {
			continue
		}
		narrowed := s.baseRelNarrowedWidths(r)
		if !narrowed.ok {
			continue
		}
		for _, p := range r.Pathlist {
			if p == nil || p.NCols > 0 {
				continue // index-only: already narrower, leave it
			}
			narrowed.applyTo(p)
		}
		for _, p := range r.PartialPathlist {
			if p == nil || p.NCols > 0 {
				continue
			}
			narrowed.applyTo(p)
		}
	}
}

// narrowJoinWidths is R122 Slice B: a join path publishes the SUM of its
// children's narrowed widths, so the narrowing survives past the first join.
//
// Slice A narrowed base-rel scans and their single-child wrappers and was
// parity-neutral for precisely this reason: a join path never set its own
// triple, so `pathNCols` fell back to `relNCols(joinrel)` — the full sum of
// both inputs' WHOLE schemas (joinsearchlevel.go) — and only first-level joins
// ever saw a narrowed child.
//
// Called from `addPath`/`addPartialPath`, the single funnel every join path
// passes through. The TIMING looks wrong and is not: the constructor has
// already computed this path's own cost from its CHILDREN's widths before it
// calls addPath, so stamping here cannot affect that cost — which is correct,
// because the triple stamped here is consumed only when this path becomes a
// child one level up.
func narrowJoinWidths(p *Path) {
	if p == nil || !narrowCostInputsEnabled() {
		return
	}
	// Rule 3: idempotent — never overwrite an existing stamp.
	if p.NCols > 0 {
		return
	}
	// An explicit WHITELIST, never "has two children" and never "has a
	// Jointype". `parser.JoinInner` is the ZERO VALUE, and `PathSetOp` has
	// exactly two children, so either of those tests would read a set-op as an
	// inner join and sum two widths that were never joined.
	switch p.Kind {
	case PathHashJoin, PathMergeJoin, PathNestLoop:
	default:
		return
	}
	if len(p.Children) != 2 {
		return
	}
	// M0146-0234: the joinrel's own build_joinrel_tlist triple, the same for
	// every path of the rel (rule 3), when it could be derived.
	if p.Rel != nil && p.Rel.joinNarrow.ok {
		p.Rel.joinNarrow.applyTo(p)
		return
	}
	outer, inner := p.Children[0], p.Children[1]
	if outer == nil || inner == nil {
		return
	}

	// Rule 4: an index-only child's triple is NOT this round's narrowing.
	// pathindexonly.go writes NCols/AvgVarBytes/OutputWidth unconditionally,
	// independent of this flag, so treating `NCols > 0` as "we narrowed it"
	// would launder an IOS triple into a join sum on a rel that declined —
	// while the NLI path for that same joinrel (inner from
	// CheapestParameterized, NCols == 0) declined. Two paths of one joinrel in
	// different currencies is exactly the bias this design exists to prevent;
	// it is R121's wrapper leak one level up.
	if outer.IndexOnly || inner.IndexOnly {
		return
	}

	// Rule 1: both sides or neither. A sum that is narrow on one side and full
	// on the other is not a currency.
	if outer.NCols <= 0 || inner.NCols <= 0 {
		return
	}

	// SEMI/ANTI publish the LHS only — the same rule the rel level applies via
	// `joinPublishesInner`. Keyed on Jointype because the SpecialJoinInfo is
	// not in scope at the constructors. JoinRight publishes BOTH sides;
	// JoinFull never produces a path today, but is deliberately in the
	// publishes-both arm so a future FULL executor cannot silently inherit the
	// SEMI branch by falling through.
	if p.Jointype == parser.JoinSemi || p.Jointype == parser.JoinAnti {
		p.NCols = pathNCols(outer)
		p.AvgVarBytes = pathAvgVarBytes(outer)
		p.OutputWidth = pathWidth(outer)
		return
	}
	// M0146-0005dj: the right forms publish the INNER (the hashed LHS).
	if p.Jointype == parser.JoinRightSemi || p.Jointype == parser.JoinRightAnti {
		p.NCols = pathNCols(inner)
		p.AvgVarBytes = pathAvgVarBytes(inner)
		p.OutputWidth = pathWidth(inner)
		return
	}

	// Rule 2: all three together. Hash cost reads pathWidth AND
	// pathNCols/pathAvgVarBytes off the same path, so a triple narrowed in one
	// currency and full in the other reproduces R120's defect one level up.
	p.NCols = pathNCols(outer) + pathNCols(inner)
	p.AvgVarBytes = pathAvgVarBytes(outer) + pathAvgVarBytes(inner)
	p.OutputWidth = pathWidth(outer) + pathWidth(inner)
}

// inheritNarrowedWidths copies a single-child wrapper's child triple upward.
//
// Gather, GatherMerge, Sort and Memoize project NOTHING, so the row they emit
// is the row their child emits. Without this the narrowing dies at the first
// wrapper — and since goopg's TPC-H bench plans are all parallel, nearly every
// join child is a partial scan under a Gather, so Slice A would reach almost
// nothing.
//
// Copies all three fields together or none (rule 2).
func inheritNarrowedWidths(parent, child *Path) {
	if parent == nil || child == nil || !narrowCostInputsEnabled() {
		return
	}
	if child.NCols <= 0 {
		return
	}
	// Do NOT launder an index-only triple onto a wrapper (rule 3).
	//
	// Index-only paths carry `NCols` unconditionally — they are written by
	// pathindexonly.go whether or not this flag is set, and they are the one
	// documented exemption from the all-or-none rule at the SCAN level. But a
	// Gather is not exempt: it is a path this round writes. Without this
	// guard, a rel that DECLINES to narrow (nil ColVarBytes — an un-ANALYZEd
	// table) but happens to have an index-only path would get a triple on the
	// Gather over that path while its sibling Gather over the partial SeqScan
	// got none, and `addPath` would compare the two in different currencies —
	// exactly the bias rule 3 exists to prevent, re-entering through the
	// wrapper instead of the scan.
	//
	// Confining index-only narrowing to the scan level keeps the wrapper's
	// width at the pre-R121 fallback, which is what the OFF arm does too.
	if child.IndexOnly {
		return
	}
	parent.NCols = child.NCols
	parent.AvgVarBytes = child.AvgVarBytes
	parent.OutputWidth = child.OutputWidth
}
