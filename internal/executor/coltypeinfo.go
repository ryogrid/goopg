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

	// D-01 (MD-01): the PG TupleDesc descriptor fields the packed-row work
	// needs — `attlen`, `attbyval`, `attstorage`, mirroring
	// postgres/src/include/catalog/pg_attribute.h. They are DERIVED here,
	// not transcribed: `catalog.TypeNameToOID` maps the type name onto its
	// PG OID and `userTypeAttrsForOID` reads the single in-tree
	// transcription of pg_type.dat.
	//
	// That indirection is the point. The D-02 audit found FOUR independent
	// transcriptions of the same type list already in tree
	// (`encodeValuePGCtx`, `decodePhysicalPGValueLowered`,
	// `pgPhysicalTypeIsVarlena`, `catalog.PhysicalTypeAlign`), which
	// already disagree about the bare `float` spelling — a fifth written
	// beside them would be a drift hazard, not a convenience (03 §5,
	// REVIEW M-goopg-2).
	//
	// Nothing reads these yet; they are additive payload for D-03 onward.
	// The alignment answer deliberately stays on `align` above: unifying
	// alignment is D-09's job and it changes the on-disk format, which this
	// item does not.
	attLen     int16 // -1 == variable length (varlena)
	attByVal   bool
	attStorage byte // 'p' plain | 'e' external | 'x' extended | 'm' main
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
		ta := colTypeDescriptor(t)
		info[i] = colTypeInfo{
			lower:      lower,
			align:      physicalPGTypeAlignLowered(t, lower),
			isTSTZ:     isTimestampTZTypeName(t.Name),
			attLen:     ta.TypLen,
			attByVal:   ta.TypByVal,
			attStorage: ta.TypStorage,
		}
	}
	return info
}

// colTypeDescriptor resolves one column type to its PG pg_type descriptor
// through the existing name -> OID -> attrs bridge, so this file adds no new
// transcription of pg_type.dat (D-01, REVIEW M-goopg-2).
//
// Arrays short-circuit: `catalog.Type` carries the ELEMENT name with
// `IsArray` beside it, so the OID lookup would answer with the element's
// descriptor. Every PG array type is a varlena (`typlen = -1`,
// `typbyval = false`, `typstorage = 'x'`) whatever its element is
// (postgres/src/include/catalog/pg_type.dat, the `_`-prefixed array rows),
// which is what an array column's stored form actually looks like here.
func colTypeDescriptor(t catalog.Type) userTypeAttrs {
	if t.IsArray {
		return userTypeAttrs{TypLen: -1, TypByVal: false, TypAlign: 'i', TypStorage: 'x'}
	}
	return userTypeAttrsForOID(catalog.TypeNameToOID(t.Name))
}

// isTSTZ is retained on colTypeInfo for callers outside the decode switch that
// need the answer without re-deriving it. The decode path itself no longer
// consults it: inside `case "timestamp", "timestamptz"` the lowered name is
// already exactly one of those literals, so a direct comparison is both
// cheaper and provably the same answer.
var _ = colTypeInfo{}.isTSTZ

// D-03 consumed two thirds of the D-01 payload: NewTupleDescFromColumns
// (packedtuple.go) reads `attLen` for PG's attcacheoff walk and for
// HEAP_HASVARWIDTH, and `attStorage` to reject a TOAST pointer packed into a
// plain column.
//
// `attByVal` still has no consumer, and the reason is structural rather than
// an oversight: PG reads attbyval in `fetchatt` (execTuples.c:1085) to decide
// whether the Datum IS the value or points at it, and goopg's decoder has no
// such fork — `decodePhysicalPGValueLowered` always materialises a Datum. It
// gains one at D-09/MD-1x, when the alignment codec unifies with PG's; keep it
// referenced until then so `unused` does not flag it.
var _ = colTypeInfo{}.attByVal
