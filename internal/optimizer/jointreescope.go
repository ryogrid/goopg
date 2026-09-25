package optimizer

// M0145-0005 slice 3 — IR-direct leaf materialisation.
//
// `extractSearchLeaves` re-derives the search problem's leaf scans and link
// quals by walking the built NODE chain — a second traversal of structure
// `planFromItem` just walked, re-deriving leaf order, leaf ranges, link
// types, and the `realWidth`/`belowNullable`/`preserved` bookkeeping that
// every coordinate decision below the seam reads. The walk exists because
// the information was never recorded: `planFromItem` resolves each ON qual
// and attaches each leaf node, then throws the structure away and leaves a
// tree of *Join nodes for the seam to re-flatten.
//
// jtScopeTable is that information recorded at construction time, in the
// exact order and shape the walk produces it. `planFromItem` emits one leaf
// record per chain attachment and one link record per join; `planFromClause`
// concatenates the per-item tables in FROM order and attaches the result to
// the resolveContext. The jointree-arm seam then builds the SAME
// (scans, widths, onQuals, outerLinks, semiAnti) tuple the walk builds —
// `extractScopeLeaves` is a transcription of `extractSearchLeaves`'s
// arithmetic onto table reads, not a reimplementation of its semantics:
//
//   - leaf order is attachment order, which IS the walk's DFS order
//     (planFromItem builds left-deep chains in FROM order);
//   - a descendable join (Inner/Cross/Left/Right) records a link and
//     appends its right subtree as a leaf — the right side is always one
//     leaf because planScanRangeVar products are opaque node types (the
//     seam declines if a descendable node ever shows up as a leaf record,
//     the same fail-closed answer the count gates give today);
//   - a Semi/Anti join (the reduce_outer_joins demotion — the only
//     Semi/Anti a planFromClause chain can carry) records a link and
//     appends its right node as a SYNTHETIC leaf, matching the walk's
//     opaque-RHS append;
//   - any other join type (FULL) folds the accumulated chain into ONE
//     opaque leaf — exactly the walk's "not an admitted join type" arm,
//     which treats the whole node as a leaf;
//   - link leaf ranges are recorded as indices into the leaf table;
//     `realWidth`/`width` become prefix sums over it, `belowNullable` and
//     the `preserved` decline become range-containment tests, and
//     `rebaseChainQual`/`rebaseSemiAntiChainQual` run unchanged on the
//     recorded preds — the translation work is inherent (a Join node's
//     Predicate is in its own concat coordinates, which the executor
//     contract requires), only the discovery changes.
//
// `root` pins the table to the exact chain it was built beside: the seam
// uses the table only when `ctx.jtScope.root == chain`, so any pre-search
// rewrite that grafts a different subtree (the S5a post-unnest Phase B
// chain is the reachable one) falls back to the node walk unchanged.

type jtScopeLinkKind uint8

const (
	jtLinkInner jtScopeLinkKind = iota
	jtLinkOuter
	jtLinkSemiAnti
)

// jtScopeLeaf is one chain leaf in walk order. `synthetic` marks a Semi/Anti
// RHS leaf — the out-of-band span `buildLeafSpans` relocates, and the leaf
// `realWidth` skips. Emitting-vs-pulled is not recorded here: pulled bodies
// are spliced into scans by splicePulledLeaves exactly as before — this
// table covers only the planFromClause chain.
type jtScopeLeaf struct {
	node      Node
	synthetic bool
}

// jtScopeLink is one join the walk would record, with the leaf-range
// endpoints it would compute from its position. `jn` is the built Join node
// itself — the extraction reads Predicate/LeftKey/RightKey/SJInfo/Type off
// it lazily, at the same moment the walk would read them, so any mutation a
// pre-seam pass makes is observed identically. The parser join type the
// walk records (JoinLeft for every outer link, JoinSemi/JoinAnti for a
// semi/anti one) is a pure function of jn.Type and is derived at
// extraction, not stored.
type jtScopeLink struct {
	kind    jtScopeLinkKind
	loLeft  int // leaf index where the left subtree's leaves begin
	loRight int // leaf index where the right subtree's leaves begin
	hiRight int // leaf index where the right subtree's leaves end
	jn      *Join
}

type jtScopeTable struct {
	leaves   []jtScopeLeaf
	links    []jtScopeLink
	root     Node
	poisoned bool
}

// jtScopeAddLeaf appends the leaf for a chain attachment point. It is called
// for the FROM item's base node and, by jtScopeAddJoin, for each right-side
// node; a leaf that is itself a descendable join poisons the table — the
// walk would split it into multiple leaves and every index below would be
// wrong, so the extraction declines instead of mis-numbering.
func (t *jtScopeTable) addLeaf(n Node, synthetic bool) {
	t.leaves = append(t.leaves, jtScopeLeaf{node: n, synthetic: synthetic})
}

// jtScopeAddJoin records one built join node and appends its right subtree's
// leaf. Mirrors the walk's dispatch on `jn.Type`:
//
//   - Inner/Cross: descendable — right side is one leaf; a link is recorded
//     only when the join carries a qual (a nil-Predicate inner link is a
//     pass-through, and a CROSS carrying one is a shape planFromItem never
//     builds — the walk declines it, so the table poisons);
//   - Left/Right: descendable — right side is one leaf, link recorded
//     unconditionally (an outerChainLink exists even for a nil pred);
//   - Semi/Anti: right side is a SYNTHETIC leaf, link recorded;
//   - anything else (FULL): the whole accumulated chain folds into one
//     opaque leaf — the walk treats the node as a leaf and everything
//     inside it disappears from the flat problem.
func (t *jtScopeTable) addJoin(jn *Join) {
	switch jn.Type {
	case JoinTypeInner, JoinTypeCross, JoinTypeLeft, JoinTypeRight:
		t.addLeaf(jn.Right, false)
		loRight := len(t.leaves) - 1
		if jn.Type == JoinTypeCross && jn.Predicate != nil {
			t.poisoned = true
			return
		}
		if jn.Type == JoinTypeLeft || jn.Type == JoinTypeRight {
			t.links = append(t.links, jtScopeLink{kind: jtLinkOuter, loLeft: 0, loRight: loRight, hiRight: len(t.leaves), jn: jn})
			return
		}
		if jn.Predicate != nil {
			t.links = append(t.links, jtScopeLink{kind: jtLinkInner, loLeft: 0, loRight: loRight, hiRight: len(t.leaves), jn: jn})
		}
	case JoinTypeSemi, JoinTypeAnti:
		t.addLeaf(jn.Right, true)
		t.links = append(t.links, jtScopeLink{kind: jtLinkSemiAnti, loLeft: 0, loRight: len(t.leaves) - 1, hiRight: len(t.leaves), jn: jn})
	default:
		// Fold: the whole node is one opaque leaf (the walk's
		// "not an admitted join type" arm) — every leaf and link
		// accumulated inside it is discarded with the subtree, and
		// any poison they carried is erased with them.
		t.leaves = []jtScopeLeaf{{node: jn}}
		t.links = nil
		t.poisoned = false
	}
}

// shift adds `delta` to every link's leaf indices — planFromClause applies
// it when concatenating per-item tables, the same shift it applies to the
// item's binding offsets.
func (t *jtScopeTable) shift(delta int) {
	for i := range t.links {
		t.links[i].loLeft += delta
		t.links[i].loRight += delta
		t.links[i].hiRight += delta
	}
}

// appendTable concatenates `other` (an item-local table) onto the receiver,
// shifting the other's link indices by the receiver's current leaf count.
func (t *jtScopeTable) appendTable(other *jtScopeTable) {
	if other == nil {
		return
	}
	other.shift(len(t.leaves))
	t.leaves = append(t.leaves, other.leaves...)
	t.links = append(t.links, other.links...)
	t.poisoned = t.poisoned || other.poisoned
}
