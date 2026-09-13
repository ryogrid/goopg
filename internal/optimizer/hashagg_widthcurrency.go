package optimizer

import (
	"os"

	"github.com/goopg/goopg/internal/executor/hashsize"
)

// R120 — the HashAggregate spill arm's byte currency.
//
// `costAgg`'s hashed spill arm (cost_funcs.go) historically supplied
// `inAvgVarBytes` — the VARIABLE payload only — to two places that mean the
// input TUPLE WIDTH: `hashAggEntrySize`, whose parameter is literally named
// `tupleWidth`, and the `pages` term whose own comment cites
// `relation_byte_size(input_tuples, input_width)`.
//
// PG 18.3 passes one and the same `input_width` to both
// (costsize.c:2801-2802 and :2824), and its SORTED rival prices in that
// identical currency (`cost_tuplesort`, costsize.c:1903, via
// `relation_byte_size`, costsize.c:6452-6456). Goopg's sorted rival likewise
// pays the full per-row footprint `hashsize.EntryBytes = 48*ncols + 24 +
// avgVar`. Supplying only the variable payload to the hashed rival therefore
// priced the two aggregation candidates in DIFFERENT currencies for the same
// input rows — a large discount to hash (12.4x on a 9-column row, unbounded
// when avgVar is 0), which additionally made `hashAggSetLimits` early-return
// and left the whole spill arm inert exactly where PG spills.
//
// This switch is default-off: only the exact value "1" elects the corrected
// currency, mirroring R113's GOOPG_PG_SORT_RELATION_BYTES_COST. OFF is
// bit-identical to the pre-R120 arithmetic.
//
// SCOPE NOTE (r120-hashagg-width-currency/SCOPE.md §1a): this restores
// Goopg-INTERNAL consistency between the two rivals. It does NOT achieve PG
// alignment — `hashsize.DatumBytes` (48) per column still dwarfs PG's real
// per-column bytes, leaving roughly a 6-7x residue — so on WIDE inputs it can
// convert the old under-charge into an over-charge. Closing that residue is
// the K65/K66 ncols-narrowing family, deliberately not this round.
var hashAggWidthCurrency = hashAggWidthCurrencyFromEnv(os.Getenv("GOOPG_HASHAGG_WIDTH_CURRENCY"))

func hashAggWidthCurrencyFromEnv(v string) bool { return v == "1" }

func hashAggWidthCurrencyEnabled() bool { return hashAggWidthCurrency }

func setHashAggWidthCurrencyForTest(on bool) func() {
	old := hashAggWidthCurrency
	hashAggWidthCurrency = on
	return func() { hashAggWidthCurrency = old }
}

// hashAggTupleWidth is the BARE input tuple width the corrected arm hands to
// `hashAggEntrySize` — PG's `input_width` analogue.
//
// It is `hashsize.EntryBytes` MINUS `hashsize.RowSliceBytes`: PG passes a bare
// path-target width to `hash_agg_entry_size`, which adds its own
// MAXALIGN(SizeofMinimalTupleHeader) (nodeAgg.c:1706-1707), so passing the
// full EntryBytes would double-count a header term. Goopg's RowSliceBytes (24)
// is precisely its per-row header analogue and lines up with PG's
// MAXALIGN(SizeofHeapTupleHeader) = 24, which is why the same decomposition
// serves both arms: EntryBytes = hashAggTupleWidth + 24 is the
// `relation_byte_size` per-row analogue used for the `pages` term.
func hashAggTupleWidth(ncols int, avgVarBytes float64) float64 {
	if ncols < 0 {
		ncols = 0
	}
	if avgVarBytes < 0 {
		avgVarBytes = 0
	}
	return float64(ncols)*hashsize.DatumBytes + avgVarBytes
}
