package optimizer

import (
	"math/big"
	"regexp"
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// PartitionDesc order (M0148-0001c3a9 slice 1).
//
// PG keeps a partitioned table's partitions in its PartitionDesc in BOUND
// order, not creation order: RelationBuildPartitionDesc
// (partitioning/partdesc.c) asks partition_bounds_create
// (partitioning/partbounds.c) for a canonical index per partition, and
// expand_partitioned_rtentry (optimizer/util/inherit.c) expands the
// partitions in that order, depth-first through sub-partitioned children.
// The appendrel's members, their RT indexes (`t1_1`, `t1_2`, …) and the
// Append's subplan order all follow it, and partitionwise join pairs
// partitions by that same index.
//
// The canonical order per strategy (partition_bounds_create):
//
//   - RANGE (create_range_bounds): by lower bound, MINVALUE lowest; the
//     bounds of distinct partitions never overlap, so the lower bound alone
//     orders them.
//   - LIST (create_list_bounds): every non-NULL value of every partition
//     is sorted, and a partition's index is the position at which its first
//     value appears — i.e. partitions by their smallest value. A partition
//     holding only NULL comes after all of them.
//   - HASH (create_hash_bounds): by (modulus, remainder).
//   - The DEFAULT partition is last.
//
// goopg stores bound values as raw text; partitionDescOrder compares them by
// the partition key's type. A value it cannot compare (an unrecognised type,
// an expression key over non-numeric values) leaves that level in the
// catalog's order rather than guess.

// partitionDescOrder returns children in PartitionDesc order, or children
// unchanged when the order cannot be decided.
func partitionDescOrder(parent *catalog.Table, children []*catalog.Table) []*catalog.Table {
	if len(children) < 2 || parent == nil {
		return children
	}
	type entry struct {
		t       *catalog.Table
		def     bool
		nullOnl bool
		key     []string // RANGE lower bound / LIST smallest value
		unb     []int    // RANGE: -1 MINVALUE, +1 MAXVALUE, 0 value
		mod     int64
		rem     int64
	}
	method := strings.ToUpper(parent.PartitionMethod)
	nkeys := len(parent.PartitionKey)
	kinds := make([]boundKind, nkeys)
	for i := range kinds {
		kinds[i] = partitionKeyBoundKind(parent, i)
	}
	ents := make([]entry, 0, len(children))
	for _, c := range children {
		b, ok := ownPartitionBound(c)
		if !ok {
			return children
		}
		e := entry{t: c}
		switch {
		case b.IsDefault:
			e.def = true
		case method == "HASH":
			if !b.IsHash {
				return children
			}
			e.mod, e.rem = b.Modulus, b.Remainder
		case method == "RANGE":
			if len(b.FromValues) < nkeys {
				return children
			}
			e.key = b.FromValues[:nkeys]
			e.unb = make([]int, nkeys)
			for i := 0; i < nkeys; i++ {
				if i < len(b.FromUnbounded) && b.FromUnbounded[i] {
					e.unb[i] = -1
					if i < len(b.FromUnboundMax) && b.FromUnboundMax[i] {
						e.unb[i] = 1
					}
				}
			}
		case method == "LIST":
			if nkeys != 1 {
				return children
			}
			var vals []string
			for i, raw := range b.InValues {
				if listValueIsNull(b, i, raw) {
					continue
				}
				vals = append(vals, raw)
			}
			if len(vals) == 0 {
				e.nullOnl = true
				break
			}
			lo := vals[0]
			for _, v := range vals[1:] {
				c, ok := compareBoundValue(kinds[0], v, lo)
				if !ok {
					return children
				}
				if c < 0 {
					lo = v
				}
			}
			e.key = []string{lo}
		default:
			return children
		}
		ents = append(ents, e)
	}
	decidable := true
	rank := func(e entry) int {
		switch {
		case e.def:
			return 2
		case e.nullOnl:
			return 1
		}
		return 0
	}
	sort.SliceStable(ents, func(i, j int) bool {
		a, b := ents[i], ents[j]
		if ra, rb := rank(a), rank(b); ra != rb || ra != 0 {
			return ra < rb
		}
		if method == "HASH" {
			if a.mod != b.mod {
				return a.mod < b.mod
			}
			return a.rem < b.rem
		}
		for k := range a.key {
			if method == "RANGE" && (a.unb[k] != 0 || b.unb[k] != 0) {
				if a.unb[k] != b.unb[k] {
					return a.unb[k] < b.unb[k]
				}
				continue
			}
			c, ok := compareBoundValue(kinds[k], a.key[k], b.key[k])
			if !ok {
				decidable = false
				return false
			}
			if c != 0 {
				return c < 0
			}
		}
		return false
	})
	if !decidable {
		return children
	}
	out := make([]*catalog.Table, len(ents))
	for i, e := range ents {
		out[i] = e.t
	}
	return out
}

// ownPartitionBound is c's own FOR VALUES bound. A sub-partitioned child
// also lists its own partitions' bounds in PartitionBounds (DDL registers
// each child's bound on its parent), so the entry naming c is taken; a lone
// entry is c's own.
func ownPartitionBound(c *catalog.Table) (catalog.PartitionBound, bool) {
	for _, b := range c.PartitionBounds {
		if b.ChildName != "" && strings.EqualFold(b.ChildName, c.Name) {
			return b, true
		}
	}
	if len(c.PartitionBounds) == 1 {
		return c.PartitionBounds[0], true
	}
	return catalog.PartitionBound{}, false
}

// listValueIsNull reports whether the i-th LIST value is the NULL literal.
// The literal form tells a NULL from a text value 'null'; the raw form is
// the fallback for bounds stored without literals.
func listValueIsNull(b catalog.PartitionBound, i int, raw string) bool {
	if i < len(b.InValueLiterals) && b.InValueLiterals[i] != "" {
		return strings.EqualFold(b.InValueLiterals[i], "null")
	}
	return strings.EqualFold(raw, "null")
}

type boundKind int

const (
	boundUnknown boundKind = iota
	boundNumeric
	boundText
	boundBool
	boundISODate
)

// partitionKeyBoundKind classifies the i-th partition key's type for bound
// comparison. An expression key has no column type; numeric comparison is
// still tried value by value (compareBoundValue falls back to it).
func partitionKeyBoundKind(parent *catalog.Table, i int) boundKind {
	if i < len(parent.PartitionKeyExprs) && parent.PartitionKeyExprs[i] != nil {
		return boundUnknown
	}
	var typ string
	for _, c := range parent.Columns {
		if strings.EqualFold(c.Name, parent.PartitionKey[i]) {
			typ = strings.ToLower(c.Type.Name)
			if c.Type.IsArray {
				return boundUnknown
			}
			break
		}
	}
	switch typ {
	case "int2", "int4", "int8", "smallint", "integer", "bigint", "int", "oid",
		"numeric", "decimal", "float4", "float8", "real", "double precision":
		return boundNumeric
	case "text", "varchar", "character varying", "bpchar", "char", "character", "name":
		return boundText
	case "bool", "boolean":
		return boundBool
	case "date", "timestamp", "timestamp without time zone":
		return boundISODate
	}
	return boundUnknown
}

var isoDateBound = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}( \d{2}:\d{2}:\d{2}(\.\d+)?)?$`)

// compareBoundValue compares two raw bound values of one key column.
func compareBoundValue(k boundKind, a, b string) (int, bool) {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch k {
	case boundText:
		// The partition key's collation; byte order is the C collation the
		// regress databases use.
		return strings.Compare(a, b), true
	case boundBool:
		av, aok := parseBoundBool(a)
		bv, bok := parseBoundBool(b)
		if !aok || !bok {
			return 0, false
		}
		switch {
		case av == bv:
			return 0, true
		case !av:
			return -1, true
		}
		return 1, true
	case boundISODate:
		if isoDateBound.MatchString(a) && isoDateBound.MatchString(b) {
			return strings.Compare(a, b), true
		}
		return 0, false
	}
	ar, aok := new(big.Rat).SetString(a)
	br, bok := new(big.Rat).SetString(b)
	if !aok || !bok {
		return 0, false
	}
	return ar.Cmp(br), true
}

func parseBoundBool(s string) (bool, bool) {
	switch strings.ToLower(s) {
	case "t", "true", "yes", "on", "1":
		return true, true
	case "f", "false", "no", "off", "0":
		return false, true
	}
	return false, false
}
