package planner

import "github.com/goopg/goopg/internal/catalog"

// Relation size and width estimation — the inputs the cost model's per-node cost
// functions consume. See docs/design/cost-model/ chapter 05. These are pure
// helpers in phase C2; nothing in the live planner calls them yet (selection is
// still the integer DP), so they cannot change a plan. They are wired into path
// generation in C3/C4.
//
// The load-bearing piece is the estimate_rel_size ROW FALLBACK (§4): column
// statistics survive a restart but TableStats.RowCount does not (ledger pq-P6),
// so on a freshly started server the model would be blind. PostgreSQL faces the
// same problem for a never-analysed relation and solves it in estimate_rel_size
// (plancat.c:1075) by deriving a row count from the live block count and the
// tuple width. Reproducing that here makes the milestone persistence-independent.

// Page geometry, PG-fixed. Mirrors storage.BlockSize (8192) and
// storage.SizeOfPageHeaderData (24); hardcoded with citation to avoid a planner
// -> storage import for two constants that never change.
const (
	blockSizeBytes      = 8192
	pageHeaderSizeBytes = 24
	usableBytesPerBlock = blockSizeBytes - pageHeaderSizeBytes // 8168

	// Per-row heap overhead: the heap-tuple header (23, MAXALIGN'd to 24) plus
	// one item pointer (4). PG's density formula charges the same.
	perTupleOverhead = 24 + 4

	// varlenaDefaultWidth is PG's get_typavgwidth fallback for a varlena column
	// with no usable typmod ("we don't have a clue" — selfuncs/lsyscache). 32.
	varlenaDefaultWidth = 32
)

// typeWidth estimates the average stored width of one value of type t, in bytes.
// Fixed-width types return their typlen; varlena types with a typmod (char(n),
// varchar(n), numeric(p,s)) derive a width from it; unbounded varlena falls back
// to varlenaDefaultWidth. This is the type-derived path of design ch. 05 §2, which
// needs no statistics and so works on a cold-started server.
func typeWidth(t catalog.Type) int {
	if t.IsArray {
		return varlenaDefaultWidth
	}
	switch t.Name {
	case "bool", "boolean":
		return 1
	case "int2", "smallint":
		return 2
	case "int4", "integer", "int", "date", "float4", "real", "oid":
		return 4
	case "int8", "bigint", "float8", "double precision", "double",
		"money", "time", "timestamp", "timestamptz", "timestamp with time zone",
		"timestamp without time zone":
		return 8
	case "name":
		return 64
	case "numeric", "decimal":
		// ~2 bytes per 4 decimal digits of precision plus a small header.
		if len(t.Args) >= 1 && t.Args[0] > 0 {
			return int((t.Args[0]+3)/4)*2 + 8
		}
		return 16
	case "char", "bpchar", "varchar", "text", "character", "character varying":
		// char(n) / varchar(n): n characters plus the 4-byte varlena header.
		if len(t.Args) >= 1 && t.Args[0] > 0 {
			return int(t.Args[0]) + 4
		}
		return varlenaDefaultWidth
	default:
		return varlenaDefaultWidth
	}
}

// tupleWidth estimates the average width of a row with the given output columns,
// the analogue of PG's set_rel_width (costsize.c). Floored at 1. Consumed by
// cost_sort, the EXPLAIN width column, and the row fallback below.
func tupleWidth(cols []SchemaColumn) int {
	w := 0
	for _, c := range cols {
		w += typeWidth(c.Type)
	}
	if w < 1 {
		w = 1
	}
	return w
}

// estimateRelSizeRows reproduces PG's estimate_rel_size density formula
// (plancat.c / table_block_relation_estimate_size): from a live block count and a
// tuple width, derive how many rows the relation holds. Returns 0 for an unknown
// (zero) block count. This is the cold-start fallback (design ch. 05 §4); it is a
// floor under the real value (uniform packing, blind to dead tuples), so a warm
// ANALYZE'd RowCount always wins when present.
func estimateRelSizeRows(blocks int64, width int) float64 {
	if blocks <= 0 {
		return 0
	}
	perTuple := width + perTupleOverhead
	if perTuple < 1 {
		perTuple = 1
	}
	density := float64(usableBytesPerBlock) / float64(perTuple)
	rows := float64(blocks) * density
	if rows < 1 {
		rows = 1
	}
	return rows
}

// baseRelRows is the set_baserel_size_estimates analogue for a base relation's
// pre-filter row count (design ch. 05 §1, §4). A warm ANALYZE'd RowCount wins;
// otherwise the estimate_rel_size fallback derives rows from the live block count
// and width, so the estimate is a coarse number rather than 0 on a cold-started
// server. Local-filter selectivity is applied by the caller on top of this, only
// when reliable (design ch. 05 §1) — kept separate so the two concerns do not
// entangle.
func baseRelRows(rowCount, blocks int64, width int) float64 {
	if rowCount > 0 {
		return float64(rowCount) // warm: ANALYZE ran this session
	}
	return estimateRelSizeRows(blocks, width) // cold: block-derived, no persistence
}
