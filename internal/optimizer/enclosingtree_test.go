package optimizer

// M0127-P5.5-f-ii-b — the enclosing-tree tripwire and the pinned spine's
// consumption of the boundary map (enclosingtree.go, predp.go).
//
// Both are LIVE in production since M0127-P5.9 (2026-08-06):
// `GOOPG_PGSHAPED_DP` defaults ON and `planSelect` calls the search, so these
// tests are no longer their only observer — enclosingtree.go's own header
// already records the correction. The unusual one is
// `TestEnclosingTripwireRefusesToPassVacuously`: it asserts that the tripwire
// FAILS on a tree it cannot check, which is the lesson P5.5-f-ii-a paid for —
// an assertion that abstains silently is a false green, and a partial tree walk
// is the same failure one level up.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// etSearched builds a tagged searched subtree in the ELIDED shape: the standard
// two-rel fixture already in binding order, so `createPlanAtSearchRoot` emits no
// boundary Project and the root is a bare `*Join` publishing
// `a0 a1 b0 b1 b2`.
//
// The elided shape is used deliberately — it is the one the tag exists for
// (searchedtree.go), and it is the one whose root is a node kind the tripwire
// walk would otherwise happily descend into.
func etSearched(t *testing.T) Node {
	t.Helper()
	a, b := cpjTwoRel()
	n := createPlanAtSearchRoot(stHashRoot(a, b), 5)
	if !isSearchedTree(n) {
		t.Fatalf("fixture root %T is not tagged; these tests would prove nothing", n)
	}
	return n
}

// etCol is a named same-scope reference at a given index.
func etCol(idx int, name string) *ColumnRef {
	return &ColumnRef{Index: idx, Name: name, Type: catalog.Type{Name: "int4"}}
}

// etScan is a plain leaf for the side of a spine join that is not the searched
// subtree.
func etScan(prefix string, width int) *SeqScan {
	return &SeqScan{Table: &catalog.Table{Name: prefix}, Alias: prefix, schema: cpjSchema(prefix, width)}
}

// etRecoverContains runs fn and requires it to panic with a message containing
// want. Used rather than a bare recover so a test that stops panicking fails
// loudly instead of passing on a nil recover.
func etRecoverContains(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("no panic; expected one mentioning %q", want)
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, want) {
			t.Fatalf("panic %q does not mention %q", r, want)
		}
	}()
	fn()
}

// etPinnedSpine builds the tree `runJoinSearchBelowPinned` descends: a retained
// Filter over a pinned Semi join whose Left is `Filter{pred}(origChain)`.
//
// `origChain` is handed in already tagged, which models the post-P5.5-f wiring:
// with `ctx == nil` the historical DP block inside `runJoinSearchBelowPinned` is
// a no-op, so what the spine sees spliced beneath it is exactly the searched
// root. That is the only part of the future wiring these tests need, and it is
// the part the two assertions are about.
func etPinnedSpine(t *testing.T, retained Expr) (root Node, chain Node, semi *Join) {
	t.Helper()
	chain = etSearched(t)
	below := &Filter{Child: chain, Predicate: etCol(0, "a0")}
	semi = &Join{
		Type:     JoinTypeSemi,
		Algo:     JoinAlgoHash,
		Left:     below,
		Right:    etScan("c", 2),
		LeftKey:  etCol(2, "a0"), // deliberately mis-bound: `a0` lives at 0
		RightKey: etCol(5, "c0"),
		schema:   append(Schema(nil), below.Output()...),
	}
	return &Filter{Child: semi, Predicate: retained}, chain, semi
}
