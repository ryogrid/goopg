package mmgr

import "testing"

// TestReleaseCascadesToEveryChild is the review/260831-2 UT-4 guard.
// Context.Release ranged over c.children while each child's own Release
// spliced itself out of that very slice, so the shift moved the not-yet-
// visited children down: every second child was never released (its chunks
// were never returned to the pool and its subtree stayed alive) while
// another child was released twice.
func TestReleaseCascadesToEveryChild(t *testing.T) {
	root := Acquire(nil, KindSession)
	var kids []*Context
	for i := 0; i < 5; i++ {
		kid := Acquire(root, KindExpr)
		kid.Alloc(64) // force a chunk so an unreleased child is observable
		kids = append(kids, kid)
	}

	root.Release()

	for i, kid := range kids {
		if kid.id != InvalidContextID {
			t.Errorf("child %d: id=%d, want InvalidContextID (never released)", i, kid.id)
		}
		if len(kid.chunks) != 0 {
			t.Errorf("child %d: %d chunks retained, want 0", i, len(kid.chunks))
		}
		if kid.parent != nil {
			t.Errorf("child %d: parent still set after Release", i)
		}
	}
	if len(root.children) != 0 {
		t.Errorf("root kept %d children after Release", len(root.children))
	}
}
