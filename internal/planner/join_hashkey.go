package planner

import "strings"

// HashKeysAreInt64 reports whether BOTH of this hash join's key expressions are
// statically typed as machine integers, so the executor can represent build-side
// keys as int64 instead of canonical key strings.
//
// M0127-P0.3 (design leftdeep-joins/05 §4, stage E3). The executor used to
// *discover* the key representation at runtime: `lazyHashInsertDatum` populated
// `map[string][]Row` and `map[int64][]Row` simultaneously and `lazyHashFinalize`
// kept whichever survived the whole build. That means every int-keyed join —
// which is nearly every join in TPC-H/TPC-DS — paid for two complete hash
// tables, then threw one away at the moment peak memory had already been
// reached. The plan already holds the answer, because an equi-join between two
// integer columns cannot produce a key that is not int64-representable. Deciding
// here lets the build allocate exactly one map.
//
// Deliberately conservative in three ways:
//
//   - `numeric` is excluded even though `datumToInt64Key` accepts scale-0
//     numerics. For numeric it is the VALUES, not the type, that decide
//     representability, and a type-driven promise that the values then break
//     costs more than it saves.
//   - float/text/date/timestamp and everything else answer false and get the
//     general string map, which is always correct.
//   - an unresolved or unknown type answers false.
//
// The executor keeps a runtime demotion path for the residual case where a
// column typed as an integer nonetheless yields a datum that is not
// int64-representable, so a wrong answer here is a performance question, never a
// correctness one.
func (n *Join) HashKeysAreInt64() bool {
	if n == nil || n.Algo != JoinAlgoHash || n.LeftKey == nil || n.RightKey == nil {
		return false
	}
	return n.hashKeyIsInt64(n.LeftKey) && n.hashKeyIsInt64(n.RightKey)
}

// hashKeyIsInt64 types one side's key expression.
//
// `exprType` is the primary source, but several ColumnRef construction sites
// leave `Type` at its zero value and rely on the schema for type information —
// so a bare ColumnRef falls back to the column it indexes. Hash keys are
// resolved against the MERGED (Left ++ Right) key column space, which is the
// space `mergedKeyColumn` indexes.
func (n *Join) hashKeyIsInt64(key Expr) bool {
	t := exprType(key)
	if t.Name == "" {
		cr, ok := key.(*ColumnRef)
		if !ok {
			return false
		}
		sc, ok := n.mergedKeyColumn(cr.Index)
		if !ok {
			return false
		}
		t = sc.Type
	}
	return isMachineIntTypeName(t.Name)
}

// mergedKeyColumn resolves an index in the merged (Left ++ Right) key column
// space to the schema column it names. Semi/Anti joins emit Left only, so the
// join's own Output() is NOT the space keys are resolved against — the children
// are consulted directly.
func (n *Join) mergedKeyColumn(idx int) (SchemaColumn, bool) {
	if idx < 0 || n.Left == nil || n.Right == nil {
		return SchemaColumn{}, false
	}
	left := n.Left.Output()
	if idx < len(left) {
		return left[idx], true
	}
	right := n.Right.Output()
	if i := idx - len(left); i < len(right) {
		return right[i], true
	}
	return SchemaColumn{}, false
}

// isMachineIntTypeName reports whether a type name denotes one of PostgreSQL's
// binary integer types, every value of which fits an int64. Domain names and
// `oid` are excluded: the former needs base-type resolution, the latter is
// unsigned in PostgreSQL and its executor datum representation is not pinned by
// this milestone.
func isMachineIntTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "int2", "smallint", "int4", "int", "integer", "int8", "bigint":
		return true
	}
	return false
}
