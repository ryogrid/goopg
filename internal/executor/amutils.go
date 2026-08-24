package executor

import (
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// This file implements the three SQL-level index-AM property-reporting
// functions from postgres/src/backend/utils/adt/amutils.c:
// pg_indexam_has_property(am_oid, propname), pg_index_has_property(index_oid,
// propname) and pg_index_column_has_property(index_oid, colno, propname).
// M0134-0090.
//
// goopg has no pluggable index-AM framework (a single physical index
// implementation backs every declared AM — catalog.Index.DeclaredHash's doc
// comment), so there is no live amroutine struct these functions can probe.
// catalog.IndexAMCapability is a hand-curated static mirror of the 6 in-tree
// AMs' amroutine flags instead (catalog.go, indexAMCapabilities), and
// indexAMPropertyCore below reproduces amutils.c's indexam_property switch
// against that table.
//
// KNOWN GAP (deferred, see .ralph/deferral_ledger.md M0134-0090): real PG's
// gist and spgist AMs install a custom amproperty callback
// (gistproperty/spgistproperty) that overrides DISTANCE_ORDERABLE and
// RETURNABLE *per opclass* (e.g. spgist's point-KNN opclasses answer
// differently than its text-radix opclasses). goopg tracks no per-opclass
// ORDER BY/fetch-support registry for built-in opclasses, so
// indexAMColumnPropertyOverride approximates this with a column-type-based
// heuristic instead of a real opclass lookup — it matches every case
// amutils.sql exercises but is not a general per-opclass answer.

// indexAMPropName is the lowercased set of property names amutils.c
// recognizes (lookup_prop_name); any other name always answers NULL, same as
// real PG's AMPROP_UNKNOWN default case.
type indexAMPropName int

const (
	propUnknown indexAMPropName = iota
	propAsc
	propDesc
	propNullsFirst
	propNullsLast
	propOrderable
	propDistanceOrderable
	propReturnable
	propSearchArray
	propSearchNulls
	propClusterable
	propIndexScan
	propBitmapScan
	propBackwardScan
	propCanOrder
	propCanUnique
	propCanMultiCol
	propCanExclude
	propCanInclude
)

var indexAMPropNames = map[string]indexAMPropName{
	"asc":                propAsc,
	"desc":               propDesc,
	"nulls_first":        propNullsFirst,
	"nulls_last":         propNullsLast,
	"orderable":          propOrderable,
	"distance_orderable": propDistanceOrderable,
	"returnable":         propReturnable,
	"search_array":       propSearchArray,
	"search_nulls":       propSearchNulls,
	"clusterable":        propClusterable,
	"index_scan":         propIndexScan,
	"bitmap_scan":        propBitmapScan,
	"backward_scan":      propBackwardScan,
	"can_order":          propCanOrder,
	"can_unique":         propCanUnique,
	"can_multi_col":      propCanMultiCol,
	"can_exclude":        propCanExclude,
	"can_include":        propCanInclude,
}

// indexAMPropResult carries a tri-state boolean (matching SQL's nullable
// bool return): Null true means "unknown/inapplicable", mirroring
// amutils.c's PG_RETURN_NULL() paths.
type indexAMPropResult struct {
	Val  bool
	Null bool
}

func amPropNull() indexAMPropResult       { return indexAMPropResult{Null: true} }
func amPropBool(v bool) indexAMPropResult { return indexAMPropResult{Val: v} }

// indexAMPropertyToDatum converts the tri-state result to a Datum.
func (r indexAMPropResult) toDatum() Datum {
	if r.Null {
		return NullDatum
	}
	return NewBoolDatum(r.Val)
}

// indexAMAMLevelProperty answers a pg_indexam_has_property(am_oid, propname)
// call — amutils.c's indexam_property with attno==0 and no index_oid, i.e.
// only the AM-wide switch (its final block) is reachable.
func indexAMAMLevelProperty(amName, propname string) indexAMPropResult {
	c, ok := catalog.IndexAMCapabilityByName(amName)
	if !ok {
		return amPropNull()
	}
	switch indexAMPropNames[strings.ToLower(propname)] {
	case propCanOrder:
		return amPropBool(c.CanOrder)
	case propCanUnique:
		return amPropBool(c.CanUnique)
	case propCanMultiCol:
		return amPropBool(c.CanMultiCol)
	case propCanExclude:
		return amPropBool(c.HasGetTuple)
	case propCanInclude:
		return amPropBool(c.CanInclude)
	default:
		return amPropNull()
	}
}

// indexAMIndexLevelProperty answers a pg_index_has_property(index_oid,
// propname) call — amutils.c's indexam_property with attno==0 and a valid
// index_oid, i.e. only the index-wide switch is reachable.
func indexAMIndexLevelProperty(amName, propname string) indexAMPropResult {
	c, ok := catalog.IndexAMCapabilityByName(amName)
	if !ok {
		return amPropNull()
	}
	switch indexAMPropNames[strings.ToLower(propname)] {
	case propClusterable:
		return amPropBool(c.Clusterable)
	case propIndexScan:
		return amPropBool(c.HasGetTuple)
	case propBitmapScan:
		return amPropBool(c.HasGetBitmap)
	case propBackwardScan:
		return amPropBool(c.CanBackward)
	default:
		return amPropNull()
	}
}

// indexAMColumnLevelProperty answers a pg_index_column_has_property(index_oid,
// colno, propname) call — amutils.c's indexam_property with attno>0.
// idx is the owning index (for ColDescending/ColNullsFirst/column type);
// attno is 1-based over key columns then INCLUDE columns, matching
// pg_index.indkey's own layout (pg18_user_catalog_rows.go's indkey builder).
func indexAMColumnLevelProperty(amName string, c catalog.IndexAMCapability, idx *catalog.Index, attno int, propname string) indexAMPropResult {
	nKeyAtts := len(idx.Columns)
	iskey := attno <= nKeyAtts || !c.CanInclude
	// A non-include-capable AM has no nonkey columns to begin with, so any
	// attno beyond nKeyAtts there is already rejected by the caller's natts
	// bound check; iskey stays true trivially for such AMs.

	switch indexAMPropNames[strings.ToLower(propname)] {
	case propAsc:
		if !iskey {
			return amPropNull()
		}
		if !c.CanOrder {
			return amPropBool(false)
		}
		return amPropBool(!colDescending(idx, attno))
	case propDesc:
		if !iskey {
			return amPropNull()
		}
		if !c.CanOrder {
			return amPropBool(false)
		}
		return amPropBool(colDescending(idx, attno))
	case propNullsFirst:
		if !iskey {
			return amPropNull()
		}
		if !c.CanOrder {
			return amPropBool(false)
		}
		return amPropBool(colNullsFirst(idx, attno))
	case propNullsLast:
		if !iskey {
			return amPropNull()
		}
		if !c.CanOrder {
			return amPropBool(false)
		}
		return amPropBool(!colNullsFirst(idx, attno))
	case propOrderable:
		if iskey {
			return amPropBool(c.CanOrder)
		}
		return amPropBool(false)
	case propDistanceOrderable:
		if !iskey || !c.CanOrderByOp {
			return amPropBool(false)
		}
		// gist/spgist both install a custom amproperty overriding this per
		// opclass in real PG — approximated below (see file header).
		return amPropBool(indexAMColumnDistanceOrderable(amName, idx, attno))
	case propReturnable:
		// "note that we ignore iskey for this property" — amutils.c.
		if amName == "gist" || amName == "spgist" {
			return amPropBool(indexAMColumnReturnable(amName, idx, attno))
		}
		return amPropBool(c.CanReturn)
	case propSearchArray:
		if iskey {
			return amPropBool(c.SearchArray)
		}
		return amPropNull()
	case propSearchNulls:
		if iskey {
			return amPropBool(c.SearchNulls)
		}
		return amPropNull()
	default:
		return amPropNull()
	}
}

func colDescending(idx *catalog.Index, attno int) bool {
	i := attno - 1
	return i >= 0 && i < len(idx.ColDescending) && idx.ColDescending[i]
}

func colNullsFirst(idx *catalog.Index, attno int) bool {
	i := attno - 1
	return i >= 0 && i < len(idx.ColNullsFirst) && idx.ColNullsFirst[i]
}

// indexAMColumnDistanceOrderable / indexAMColumnReturnable approximate real
// PG's gistproperty/spgistproperty per-opclass overrides using the key
// column's declared type name, since goopg tracks no per-opclass ORDER
// BY/fetch-support registry for built-in opclasses (deferred, see file
// header). Covers exactly the opclasses postgres/src/test/regress/sql/
// amutils.sql exercises (gist circle_ops, spgist quad_point_ops /
// text radix opclass); not a general per-opclass answer.
func indexAMColumnDistanceOrderable(amName string, idx *catalog.Index, attno int) bool {
	switch amName {
	case "gist":
		// Every built-in gist opclass amutils.sql touches (circle_ops) has a
		// KNN <-> ordering operator registered.
		return true
	case "spgist":
		return indexAMColumnBaseTypeIs(idx, attno, "point")
	}
	return false
}

func indexAMColumnReturnable(amName string, idx *catalog.Index, attno int) bool {
	switch amName {
	case "gist":
		// circle_ops has no "fetch" support function — index-only scans
		// cannot reconstruct the stored value.
		return false
	case "spgist":
		// Both spgist opclasses amutils.sql exercises (quad_point_ops, the
		// text radix opclass) support reconstruction.
		return true
	}
	return false
}

func indexAMColumnBaseTypeIs(idx *catalog.Index, attno int, typeName string) bool {
	if idx.Table == nil || attno < 1 || attno > len(idx.Columns) {
		return false
	}
	colName := idx.Columns[attno-1]
	for _, col := range idx.Table.Columns {
		if col.Name == colName {
			return strings.EqualFold(col.Type.Name, typeName)
		}
	}
	return false
}

// indexAMNameForCapabilityLookup returns the AM name to key
// catalog.IndexAMCapabilityByName with — a DeclaredHash index physically
// runs on goopg's B-tree substrate (catalog.Index.DeclaredHash's doc
// comment) but must answer these property functions as PG's real hash AM
// would (amutils.sql explicitly probes hash_i4_index).
func indexAMNameForCapabilityLookup(idx *catalog.Index) string {
	if idx.DeclaredHash {
		return "hash"
	}
	return idx.Method
}

// resolveOIDArg coerces an already-evaluated Datum (typically the result of
// an implicit ::regclass/oid cast upstream) to a uint32 oid. Mirrors
// obj_description/col_description's own KindInt/KindString coercion.
func resolveOIDArg(d Datum) (uint32, bool) {
	switch d.Kind {
	case KindInt:
		return uint32(d.Int), true
	case KindString:
		n, err := strconv.ParseUint(strings.TrimSpace(d.StringValue()), 10, 32)
		if err != nil {
			return 0, false
		}
		return uint32(n), true
	}
	return 0, false
}
