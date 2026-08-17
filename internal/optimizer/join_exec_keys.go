package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// JoinExecKeys is the executor's view of a hash join's key list: the pairs
// the hash table is actually keyed on, plus the predicate work that is left
// once those pairs are enforced by the hash itself.
//
// M0127-P2.2 (design `leftdeep-joins/05` §5, stage E4 — the executor half of
// the sibling pair P2.1 opened). `HashKeys` is the PLAN's truth: every usable
// equi-pair, which is what EXPLAIN renders as `Hash Cond:` and what PG carries
// in `hashclauses`. This is the narrower question the executor has to answer —
// *which* of those pairs may be folded into the key encoding, given that
// goopg's key encoding is `datumKey`, not a type's hash opclass.
//
// The distinction matters because PG's hash join is allowed to treat a
// hashclause as fully discharged by the hash (`ExecScanHashBucket` re-checks
// the clause with the operator, but only against rows the hash already
// selected), whereas goopg's `datumKey` is one canonicalisation shared by every
// type. Where `datumKey` equality and `=` provably agree, folding a pair into
// the key is exact and its conjunct can leave the residual. Where they might
// not — a bpchar's trailing-space equality, float's `-0.0 = 0.0`, an array or
// composite datum, an unresolved type — the pair stays out of the key and its
// conjunct stays in the residual, which is precisely today's behaviour for
// every conjunct after the first.
type JoinExecKeys struct {
	// Keys are the pairs the executor hashes on, in plan order. Keys[0] is
	// always the join's existing (LeftKey, RightKey) — P2.2 does not change
	// which pair leads, only how many follow it.
	Keys []JoinKeyPair

	// Residual is the conjunction the executor must still evaluate per hash
	// match. It is Predicate minus the conjuncts of the hash-safe pairs.
	Residual Expr

	// Int64Keys reports that every pair in Keys is int-typed on both sides,
	// so the executor may use its fixed-width int64 encoding instead of
	// per-column datumKey strings.
	Int64Keys bool
}

// ExecHashKeyPlan derives the executor's key/residual split for this join.
//
// Three deliberate conservatisms, each of which costs only performance:
//
//   - Keys[0] is used unconditionally, because it is the pair goopg has
//     hashed on since M0003; changing whether it is a key is not this task's
//     business. It is dropped from the residual only when it is hash-safe,
//     so an exotic-typed lead key keeps its per-match re-check exactly as
//     today.
//   - a NullAware (`NOT IN`) join stays single-key. Its three-valued-NULL
//     bookkeeping (`antiBuildHasNull` / `antiBuildRows` in the executor) is
//     defined over ONE key column; multi-column `NOT IN` needs the row-wise
//     NULL rule, which is a separate piece of work (deferral ledger).
//   - a pair whose types cannot be resolved statically is not folded in.
func (n *Join) ExecHashKeyPlan() JoinExecKeys {
	out := JoinExecKeys{}
	if n == nil {
		return out
	}
	out.Residual = n.Predicate
	if n.LeftKey == nil || n.RightKey == nil {
		return out
	}
	// Left/Right may be nil on a Join assembled outside Plan(); the two
	// helpers that need them (residualExcluding for the left width,
	// mergedKeyColumn for the schema type fallback) both degrade safely, so
	// this stays a key plan rather than an early return.
	pairs := n.HashKeys
	if len(pairs) == 0 {
		// A join built after fillJoinHashKeys ran (or a plan that never
		// went through Plan()) still has its single pair; behave exactly
		// as the pre-P2.2 executor did.
		pairs = []JoinKeyPair{{Left: n.LeftKey, Right: n.RightKey}}
	}
	if n.NullAware && len(pairs) > 1 {
		pairs = pairs[:1]
	}
	keys := make([]JoinKeyPair, 0, len(pairs))
	safe := make([]JoinKeyPair, 0, len(pairs))
	for i, p := range pairs {
		hashSafe := n.pairIsHashSafe(p)
		if i == 0 || hashSafe {
			keys = append(keys, p)
		}
		if hashSafe {
			safe = append(safe, p)
		}
	}
	out.Keys = keys
	out.Residual = n.residualExcluding(safe)
	out.Int64Keys = true
	for _, p := range keys {
		if !n.hashKeyIsInt64(p.Left) || !n.hashKeyIsInt64(p.Right) {
			out.Int64Keys = false
			break
		}
	}
	return out
}

// ExecMergeKeyPlan derives the MERGE join's key/residual split from the same
// published `HashKeys` list (M0127-P2.3, design `leftdeep-joins/07` §2).
//
// goopg's merge join has been the single-pair shape's worst case. It sorts both
// sides on the ONE key, then evaluates the whole `Predicate` against every pair
// in an equal-key group — so a two-conjunct join whose leading column is
// low-cardinality builds one enormous group and runs the group's cartesian
// product through the interpreted evaluator. That is the same degeneracy P2.2
// removed from hash join, except merge pays it as O(n·m) work rather than as
// one long bucket chain. PG never faces it: `mergeclauses` is a list and
// `MJCompare` walks all of them before declaring two tuples equal
// (postgres/src/backend/executor/nodeMergejoin.c), leaving `joinqual` with the
// genuinely non-equijoin conjuncts only.
//
// Why the hash-safety predicate governs here too. The executor's merge
// comparator is `compareDatum`, not a type's btree opclass, so the fold-in
// question is again "does goopg's one canonicalisation answer the same question
// as `=`". `pairIsHashSafe`'s rule — both sides resolve to the SAME whitelisted
// scalar type, with the machine-integer family treated as one — is exactly the
// set for which `compareDatum` returns 0 iff the operator is true: numeric goes
// through `numericCmp`, the int lane through `Int`, bytea through
// `bytes.Compare`, timestamps through the `KindTime` arm. The types that would
// break it (float's `-0.0`, bpchar's trailing spaces, arrays, composites) are
// already excluded there for the identical reason, so a second whitelist would
// be a second thing to keep in sync rather than a sharper rule.
//
// `Int64Keys` is left false: it selects the hash path's fixed-width packing,
// which has no analogue in a comparator-driven merge.
func (n *Join) ExecMergeKeyPlan() JoinExecKeys {
	out := JoinExecKeys{}
	if n == nil {
		return out
	}
	out.Residual = n.Predicate
	if n.LeftKey == nil || n.RightKey == nil {
		return out
	}
	pairs := n.HashKeys
	if len(pairs) == 0 {
		// A merge join assembled outside Plan()'s tail never saw
		// fillJoinHashKeys; fall back to the pair it has always sorted on.
		pairs = []JoinKeyPair{{Left: n.LeftKey, Right: n.RightKey}}
	}
	if n.NullAware && len(pairs) > 1 {
		// Same conservatism as the hash plan: the three-valued-NULL
		// bookkeeping of `NOT IN` is defined over one key column.
		pairs = pairs[:1]
	}
	keys := make([]JoinKeyPair, 0, len(pairs))
	safe := make([]JoinKeyPair, 0, len(pairs))
	for i, p := range pairs {
		mergeSafe := n.pairIsHashSafe(p)
		if i == 0 || mergeSafe {
			// Keys[0] leads unconditionally: it is the pair goopg has
			// sorted on since M0003, and an exotic-typed lead key keeps
			// its per-pair residual re-check exactly as today.
			keys = append(keys, p)
		}
		if mergeSafe {
			safe = append(safe, p)
		}
	}
	out.Keys = keys
	out.Residual = n.residualExcluding(safe)
	return out
}

// pairIsHashSafe reports whether folding this equi-pair into the hash key is
// equality-exact — i.e. whether `datumKey(l) == datumKey(r)` is the same
// question as `l = r`. `ExecMergeKeyPlan` reuses it for the merge comparator;
// the doc comment there explains why the same set answers both questions.
//
// The rule is "both sides resolve to the SAME scalar type, and that type is on
// the whitelist", with the machine-integer family treated as one type because
// int2/int4/int8 datums all canonicalise through `canonicalNumericKey(v, 0)`.
// Requiring identity rather than assignability is what keeps the cross-type
// coercion cases out: `int = numeric` is `=`-true for 5 and 5.0 and datumKey
// agrees, but `int = float8` and `text = bpchar` do not, and enumerating which
// cross-type combinations happen to agree is exactly the kind of table that
// rots. A declined pair is not a lost result — it stays in the residual and is
// evaluated by the ordinary expression evaluator, as it is today.
func (n *Join) pairIsHashSafe(p JoinKeyPair) bool {
	lt, lok := n.keyExprType(p.Left)
	rt, rok := n.keyExprType(p.Right)
	if !lok || !rok || lt.IsArray || rt.IsArray {
		return false
	}
	ln, rn := strings.ToLower(lt.Name), strings.ToLower(rt.Name)
	if isMachineIntTypeName(ln) && isMachineIntTypeName(rn) {
		return true
	}
	return ln == rn && isHashSafeTypeName(ln)
}

// isHashSafeTypeName whitelists the types whose datumKey encoding is a
// faithful equality encoding.
//
// Excluded on purpose, with the reason each one is a wrong-results risk rather
// than a missed optimisation:
//
//   - float4/float8: `-0.0 = 0.0` is true and NaN's behaviour is operator-
//     specific, but their datumKey forms differ.
//   - bpchar (`character(n)`): PG's bpchareq ignores trailing spaces.
//   - json, arrays, composites, ranges: no equality operator at all, or one
//     that is structural rather than byte-wise.
//   - domains: need base-type resolution first.
//   - oid: unsigned in PG and its datum representation is not pinned yet
//     (the same exclusion `isMachineIntTypeName` already makes).
func isHashSafeTypeName(name string) bool {
	switch name {
	case "numeric", "decimal",
		"text", "varchar", "character varying", "name",
		"bool", "boolean",
		"date",
		"timestamp", "timestamp without time zone",
		"timestamptz", "timestamp with time zone",
		"bytea", "uuid":
		return true
	}
	return false
}

// keyExprType resolves a key expression's static type in the MERGED
// (Left ++ Right) key column space, with the same schema fallback
// `hashKeyIsInt64` uses: several ColumnRef construction sites leave `Type` at
// its zero value and rely on the schema.
func (n *Join) keyExprType(key Expr) (catalog.Type, bool) {
	t := exprType(key)
	if t.Name != "" {
		return t, true
	}
	cr, ok := key.(*ColumnRef)
	if !ok {
		return catalog.Type{}, false
	}
	sc, ok := n.mergedKeyColumn(cr.Index)
	if !ok || sc.Type.Name == "" {
		return catalog.Type{}, false
	}
	return sc.Type, true
}
