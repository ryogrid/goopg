package planner

// M0127-P5.4c-ii-a — `build_index_pathkeys` (pathkeys.c:740): the ordering a
// B-tree index path delivers, recorded on the path.
//
// PG oracle: `build_index_pathkeys` (pathkeys.c:740), reached from
// `build_index_paths` (indxpath.c:750-800) which stores the result as the index
// path's `pathkeys` for BOTH the plain and the parameterised construction.
// Design: leftdeep-joins 03 §5.3, 04 §2.1.
//
// Why this is a slice of its own, ahead of the merge arm that motivates it.
// P5.4c-i landed `try_mergejoin_path`'s sort-skip branch (joinpath.c:1091-1097)
// as a tested but UNREACHABLE consumer: no path in the search carried an
// ordering, so `pathkeysContainedIn` was always false and every merge candidate
// paid for two sorts. Closing that needs two independent things — a function
// that says what order an index delivers (here), and a producer of an
// UNPARAMETERISED ordered index path for that order to be usable by a merge
// outer (`build_index_paths`' `useful_pathkeys` arm, which needs `cost_index`
// and is P5.4c-ii-b). Landing them together would make neither falsifiable on
// its own, and the cost model half is a genuine design question — 04 §1 forbids
// a second, independently-calibrated index cost model, so it gets its own slice
// rather than being smuggled in beside a pathkey builder.
//
// What this one DOES change today: `addOneParameterizedIndexPath` now records
// the ordering, so `addPath`'s pathkey dimension (`comparePathkeysDim`,
// pathkeys.go) stops being a constant `dimEqual` for the first time. A
// parameterised index path that is more ordered than a cheaper rival now
// survives instead of being pruned on cost alone — which is exactly what PG's
// `add_path` does, since `create_index_path` passes the same `useful_pathkeys`
// to the parameterised path.
//
// No longer inert (M0127-P5.9, 2026-08-06): `GOOPG_PGSHAPED_DP` is ON by
// default and `planSelect` calls the search, so this IS on the production path.
// The isolation coverage in `pathkeysindex_test.go` is now a floor, not the
// whole story — the DS05 fixed-binary A/B moved 86 of 99 plans (09 §3.15).

import "github.com/goopg/goopg/internal/catalog"

// indexIsOrderable is PG's `index->sortopfamily == NULL` test
// (`build_index_pathkeys`, pathkeys.c:748): an access method that does not
// return tuples in key order has no pathkeys to offer, whatever its columns say.
//
// goopg's catalogue has no `amcanorder`, so the test is made of the two facts it
// does record. `isBTreeIndex` excludes GiST/GIN/BRIN/SP-GiST, which are all
// non-orderable in PG too. `DeclaredHash` excludes `USING hash`: goopg builds a
// hash index on the B-tree substrate, so `Method` stays "btree" and the scan
// would in fact come back in key order — but PG's hash AM is not orderable, so a
// pathkey here would be an ordering PG never claims and could hand goopg a merge
// join PG would not plan. Matching the ORACLE is what matters at this seam, not
// exploiting an implementation accident.
func indexIsOrderable(idx *catalog.Index) bool {
	return idx != nil && isBTreeIndex(idx) && !idx.DeclaredHash
}

// buildIndexPathkeys reproduces `build_index_pathkeys` (pathkeys.c:740): the
// pathkey list a forward (or, with `backward`, a reverse) scan of `idx`
// delivers.
//
// `colExprs` maps an index key column NAME to the planner expression that names
// that column of the scanned relation. It is the stand-in for PG's
// `index->indextlist` + `make_pathkey_from_sortinfo`: PG resolves each index
// column to a canonical EquivalenceClass member and drops the key when the
// column is in no class the query cares about, while goopg's pathkeys are
// syntactic (pathkeys.go, design 04 §2.1) and so must be handed the very
// `*ColumnRef` the join clauses were written with — a freshly minted ColumnRef
// would carry a different `Index`/`SourceTableIdx` and `exprEqual` would read it
// as a different column. The caller therefore supplies the expressions it
// already holds rather than this function synthesising them.
//
// Three of PG's loop details are reproduced because each is load-bearing:
//
//   - INCLUDE columns contribute NOTHING (`i >= index->nkeycolumns` break,
//     :763-764): they are stored unordered. goopg keeps them in a separate
//     `IncludeColumns` field, so iterating `Columns` is that break.
//   - A column the caller cannot name STOPS the list rather than skipping to the
//     next one (:815-822). An index on (a, b, c) whose `b` is unusable delivers
//     (a) — NOT (a, c), because rows are ordered by c only within equal b.
//     PG's one exception, `indexcol_is_bool_constant_for_query`, needs the
//     boolean-index special case goopg does not have; ledgered.
//   - A backward scan inverts BOTH the direction and the null placement
//     (:770-774), which is what makes a DESC-index scan equivalent to a forward
//     scan of an ASC index.
//
// The redundancy check is PG's `pathkey_is_redundant` (:800) reduced to its
// reachable half. PG drops a key whose EquivalenceClass is constant — goopg has
// no EC-constant fact at this seam, ledgered — and a key already in the list;
// only the second can fire here, on a pathological `(a, a)` index. goopg
// compares expressions where PG compares canonical pathkey pointers, so it also
// drops a repeat that differs only in direction; that is sound rather than
// merely close, since every row in a group equal on `a` is equal on `a` and no
// second key over it can reorder anything.
func buildIndexPathkeys(idx *catalog.Index, colExprs map[string]Expr, backward bool) []PathKey {
	if !indexIsOrderable(idx) || len(colExprs) == 0 {
		return nil
	}
	var keys []PathKey
	for i, name := range idx.Columns {
		if name == "" {
			// An expression index column (`Columns[i] == ""`, the expression
			// living in `ColExprs[i]`). PG builds a pathkey from the index
			// tlist's expression; goopg would have to translate a parser.Expr
			// into the planner expression the query's own clauses carry, which
			// is a resolution step this seam has no binding context for. Stop
			// here for the same reason an unnameable column stops the list.
			// Ledgered.
			break
		}
		e, ok := colExprs[name]
		if !ok || e == nil {
			break
		}
		// `index->reverse_sort[i]` / `index->nulls_first[i]`, which goopg
		// mirrors from pg_index.indoption in `ColDescending`/`ColNullsFirst`. An
		// empty (or short) slice means the PG default for that column, ASC NULLS
		// LAST — the same convention `BuildIndexDef` reads them under.
		desc := i < len(idx.ColDescending) && idx.ColDescending[i]
		nullsFirst := i < len(idx.ColNullsFirst) && idx.ColNullsFirst[i]
		if backward {
			desc = !desc
			nullsFirst = !nullsFirst
		}
		if pathkeyExprIndex(keys, e) >= 0 {
			continue
		}
		keys = append(keys, PathKey{Expr: e, SortAsc: !desc, NullsFirst: nullsFirst})
	}
	return keys
}

// pathkeyExprIndex finds the pathkey already sorting on `expr`, or -1.
func pathkeyExprIndex(keys []PathKey, expr Expr) int {
	for i := range keys {
		if exprEqual(keys[i].Expr, expr) {
			return i
		}
	}
	return -1
}
