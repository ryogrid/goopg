package executor

import "testing"

// TestSsiGinKeyPage pins the GIN key-grain SSI page mapping (design 0118-0140):
//   - equal search keys always map to the same page (so a reader's SIREAD and a
//     later insert of the same key collide → rw-conflict);
//   - distinct keys map to distinct pages with overwhelming probability (so
//     disjoint keys do NOT collide → reduced false positives), which the spec's
//     "different parts of the index" permutations rely on;
//   - no key page ever equals the whole-index sentinel page (fastupdate=on), so a
//     per-key reader and a fastupdate=on reader stay distinguishable.
func TestSsiGinKeyPage(t *testing.T) {
	// Determinism: equal keys → equal page.
	a, b := ssiGinKeyPage("1"), ssiGinKeyPage("1")
	if a != b {
		t.Fatal("ssiGinKeyPage not deterministic for equal keys")
	}
	// The spec's four keys must land on four distinct pages.
	keys := []string{"1", "2", "800", "2000"}
	seen := make(map[uint32]string, len(keys))
	for _, k := range keys {
		p := uint32(ssiGinKeyPage(k))
		if prev, dup := seen[p]; dup {
			t.Fatalf("ssiGinKeyPage collision: %q and %q both → %d", prev, k, p)
		}
		seen[p] = k
		if uint32(ssiGinKeyPage(k)) == uint32(ssiGinSentinelPage) {
			t.Fatalf("ssiGinKeyPage(%q) collided with the fastupdate sentinel page", k)
		}
	}
}
