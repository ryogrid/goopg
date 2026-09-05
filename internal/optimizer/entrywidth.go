package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// Build-side ENTRY WIDTH — the variable-width half of `hashsize.EntryBytes`,
// measured over the columns a hash join actually RETAINS.
//
// `hashsize.EntryBytes(ncols, avgVarBytes)` prices one resident build row as
// `48·ncols + 24 + avgVarBytes`, and the planner and the executor must agree
// on it or one of them prices a geometry the other will not build (the
// invariant the whole `hashsize` package exists for). They did not:
//
//   - `ncols` reaches the executor as `len(schema)` of the build child, which
//     `narrowBuildInput` (narrowoutput.go) has already cut down to the columns
//     the statement needs — 2 of `orders`' 9 on TPC-H Q9.
//   - `avgVarBytes` reached it as `RelOptInfo.AvgVarBytes`, which
//     `buildInitialRels` sums over EVERY column of the relation
//     (joinsearch.go) — including the ones narrowing just dropped.
//
// So the entry was half priced on the narrowed row and half on the full one.
// On Q9's `orders` build that is 2·48 + 24 + 74 = 194 B/row modelled against
// 120.2 B/row measured (`Memory Usage: 44026kB` over the 375 k rows one of 4
// batches holds), and the 74 is entirely `o_comment` (50), `o_clerk` (15),
// `o_orderpriority` (8) and `o_orderstatus` (1) — four columns the build does
// not retain. The retained pair (`o_orderkey`, `o_orderdate`) has an ANALYZEd
// payload of exactly 0.
//
// buildAvgVarBytes re-sums the statistic over the schema the build side
// EMITS. Every case it cannot attribute falls back to the relation-wide sum,
// which is the pre-existing value and an OVER-count: understating an entry
// makes the geometry believe a build fits in memory when it does not, which
// costs residency rather than a batch, so the fallback errs high on purpose.
//
// WHAT THIS DOES NOT BUY (measured 2026-09-05, analysis/minimize-datum/
// d04-pack-prototype-20260905 predicted otherwise). Correcting the entry does
// NOT change Q9's batch count: at 1.5 M rows and a 128 MB budget
// (`work_mem` 64 MB × `hash_mem_multiplier` 2), `Choose` sizes the bucket
// array for a memory-full table and charges MapSlotBytes = 48 per slot, so
// 1,048,576 buckets take 50.3 MB and leave 83.9 MB for rows. Two batches
// would need an entry of 111.8 B/row; two retained Datums plus their slice
// header are already 120. `nbatch` is 4 at entry 112..194 alike, drops to 2
// only in the narrow 96..111 window, and returns to 4 below 96 — the bucket
// array then doubles to 100.7 MB and takes back more than the rows gave up.
// The lever on this witness is MapSlotBytes, not the entry.
func buildAvgVarBytes(build *Path, retained Schema) float64 {
	if build == nil || build.Rel == nil {
		return 0
	}
	full := build.Rel.AvgVarBytes
	widths := build.Rel.ColVarBytes
	if len(widths) == 0 || len(retained) == 0 {
		return full
	}
	var sum float64
	for _, c := range retained {
		w, ok := widths[strings.ToLower(c.Name)]
		if !ok {
			// A column the relation's statistics do not describe: a
			// computed Project target, a subquery output, a name a union
			// lost. Decline to the whole-relation sum rather than charge
			// it zero — an unattributed column must not shrink the entry.
			return full
		}
		sum += w
	}
	return sum
}

// tableColVarBytes is `RelOptInfo.ColVarBytes` for one base table: column name
// → that column's average variable-width payload.
//
// Statistics are positionally aligned with `Table.Columns`, the same
// correspondence `coveredAvgVarBytes` (pathindexonly.go) reads. A column with
// no statistics entry is left OUT of the map rather than mapped to zero, so
// buildAvgVarBytes declines on it instead of silently discounting it.
func tableColVarBytes(tbl *catalog.Table) map[string]float64 {
	if tbl == nil || tbl.Stats == nil || len(tbl.Stats.Columns) == 0 {
		return nil
	}
	n := len(tbl.Columns)
	if len(tbl.Stats.Columns) < n {
		n = len(tbl.Stats.Columns)
	}
	m := make(map[string]float64, n)
	for i := 0; i < n; i++ {
		m[strings.ToLower(tbl.Columns[i].Name)] = tbl.Stats.Columns[i].AvgWidth
	}
	return m
}

// unionColVarBytes merges two rels' column-width maps for the join rel above
// them, the unsummed counterpart of `joinrel.AvgVarBytes = rel1 + rel2`.
//
// A name in both inputs (a self-join's aliases, or two tables that happen to
// share a column name) keeps the WIDER of the two widths: the map is consumed
// by name, so a collision must resolve in the direction that over-states the
// entry.
func unionColVarBytes(a, b map[string]float64) map[string]float64 {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	m := make(map[string]float64, len(a)+len(b))
	for k, v := range a {
		m[k] = v
	}
	for k, v := range b {
		if prev, dup := m[k]; !dup || v > prev {
			m[k] = v
		}
	}
	return m
}
