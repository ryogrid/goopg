package optimizer

import (
	"os"
	"strings"
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
// Default-off pending the round's A/B. Note the sibling `GOOPG_NARROW_*` flags
// are opt-OUT (default ON) and gate executor plan shape; this is the first to
// gate a planner COST input.
var narrowCostInputs = narrowCostInputsFromEnv(os.Getenv("GOOPG_NARROW_COST_INPUTS"))

func narrowCostInputsFromEnv(v string) bool { return v == "1" }

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
func relNarrowedWidths(rel *RelOptInfo) narrowedWidths {
	if !narrowCostInputsEnabled() || rel == nil || rel.baseLeaf == nil {
		return narrowedWidths{}
	}
	keep, ok := scanPathTarget(rel)
	if !ok || len(keep) == 0 {
		return narrowedWidths{}
	}
	out := rel.baseLeaf.Output()
	if len(rel.ColVarBytes) == 0 {
		return narrowedWidths{}
	}
	kept := make([]SchemaColumn, 0, len(keep))
	var avg float64
	for _, i := range keep {
		if i < 0 || i >= len(out) {
			return narrowedWidths{}
		}
		col := out[i]
		// Coordinate conversion, not an aside: neededKeepSet matches column
		// names case-SENSITIVELY, while ColVarBytes is lowercase-keyed
		// (tableColVarBytes).
		w, found := rel.ColVarBytes[strings.ToLower(col.Name)]
		if !found {
			// Unattributed column — decline the whole rel rather than
			// contribute a silent zero.
			return narrowedWidths{}
		}
		avg += w
		kept = append(kept, SchemaColumn{Name: col.Name, Type: col.Type})
	}
	return narrowedWidths{
		ncols:       len(keep),
		avgVarBytes: avg,
		outputWidth: TupleWidth(kept),
		ok:          true,
	}
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
		narrowed := relNarrowedWidths(r)
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
