package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// colTypeInfo is the once-per-COLUMN resolution of everything the per-VALUE
// decode path used to re-derive from the column's type name on every row.
//
// The pattern is PostgreSQL's TupleDesc: `heap_deform_tuple` reads `attlen`,
// `attbyval` and `attalignby` from a descriptor resolved when the relation is
// opened, and touches no string
// (postgres/src/backend/access/common/heaptuple.c). goopg instead asked "what
// type is this?" by lowercasing `catalog.Type.Name` — a string — once in
// decodePhysicalPGValueMctxStyled, once in physicalPGTypeAlign and once in
// isTimestampTZTypeName, i.e. three scans of the same string per value. That
// measured 4.64 % of TPC-H Q14's CPU and 6.13 % of Q3's, across the scan, join
// and aggregate paths alike.
//
// This is memoisation of a pure function of catalog.Type, so the only hazard is
// staleness: it MUST be derived wherever the column list itself is resolved
// (an operator's Open), never cached against a table across DDL. An ALTER TABLE
// that changes a column's type re-resolves the column list, and therefore this.
type colTypeInfo struct {
	// lower is strings.ToLower(Type.Name). Note for an array column
	// catalog.Type.Name is the ELEMENT type name and IsArray carries the
	// array-ness, so `lower` alone never decides array handling — every
	// consumer checks t.IsArray first, exactly as before.
	lower string
	// align is physicalPGTypeAlign(Type).
	align int
	// isTSTZ is isTimestampTZTypeName(Type.Name).
	isTSTZ bool
}

// resolveColTypeInfo resolves one colTypeInfo per column. Call it once, from
// the operator's Open, and pass the slice down beside the column list.
func resolveColTypeInfo(cols []catalog.Column) []colTypeInfo {
	if len(cols) == 0 {
		return nil
	}
	info := make([]colTypeInfo, len(cols))
	for i := range cols {
		t := cols[i].Type
		lower := strings.ToLower(t.Name)
		info[i] = colTypeInfo{
			lower:  lower,
			align:  physicalPGTypeAlignLowered(t, lower),
			isTSTZ: isTimestampTZTypeName(t.Name),
		}
	}
	return info
}

// isTSTZ is retained on colTypeInfo for callers outside the decode switch that
// need the answer without re-deriving it. The decode path itself no longer
// consults it: inside `case "timestamp", "timestamptz"` the lowered name is
// already exactly one of those literals, so a direct comparison is both
// cheaper and provably the same answer.
var _ = colTypeInfo{}.isTSTZ
