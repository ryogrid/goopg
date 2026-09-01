package transam

// review/260831-2 TA-3 — the two Begin branches must validate identically.
//
// The isolation switch sat AFTER the variadic auto-assign branch's `return`,
// so Begin(garbage) with no explicit procNum stored the garbage level into the
// slot and handed back a Transaction; with an explicit procNum the same call
// errored. Worse, the auto-assign branch had already CAS'd inTxn=1 on a slot,
// so the error path a caller would expect could never release it.

import "testing"

func TestBeginRejectsUnsupportedIsolationOnBothPaths(t *testing.T) {
	const bogus = IsolationLevel(99)

	m := NewManager()
	free := func() int {
		n := 0
		for i := range m.procArray.slots {
			if m.procArray.slots[i].inTxn.Load() == 0 {
				n++
			}
		}
		return n
	}
	before := free()

	if tx, err := m.Begin(bogus); err == nil {
		t.Errorf("Begin(%v) auto-assign = %+v, want error", bogus, tx)
	}
	if tx, err := m.Begin(bogus, 3); err == nil {
		t.Errorf("Begin(%v, 3) = %+v, want error", bogus, tx)
	}
	if got := free(); got != before {
		t.Errorf("free slots %d -> %d: a rejected Begin leaked a slot", before, got)
	}

	// The valid levels must still get through on the auto-assign path.
	for _, iso := range []IsolationLevel{IsolationReadCommitted, IsolationRepeatableRead, IsolationSerializable} {
		tx, err := m.Begin(iso)
		if err != nil {
			t.Fatalf("Begin(%v): %v", iso, err)
		}
		if tx.Isolation != iso {
			t.Fatalf("Begin(%v).Isolation = %v", iso, tx.Isolation)
		}
	}
}
