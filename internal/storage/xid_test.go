package storage

import "testing"

// TestXIDPrecedes pins the wraparound-safe modular ordering that
// catalog.DatFrozenXID and the checkpointer TruncateCLOGFn depend on for
// horizon selection. The signed-difference formula (mirror of PG's
// TransactionIdPrecedes) must order XIDs by their position on the modular
// circle, NOT by their raw unsigned value — the whole point of M0117-0001.
func TestXIDPrecedes(t *testing.T) {
	const max = ^TransactionID(0) // 2^32 - 1

	cases := []struct {
		name string
		a, b TransactionID
		want bool
	}{
		// Plain in-range ordering matches unsigned `<`.
		{"small a before larger b", 3, 100, true},
		{"larger a not before smaller b", 100, 3, false},
		{"equal is not before", 42, 42, false},

		// The wraparound cases: where plain unsigned `<` gives the WRONG
		// answer. A freshly-assigned XID just past the 2^32 boundary (e.g. 5)
		// is logically AFTER an older XID near the top of the range (e.g.
		// max-2), even though 5 < max-2 numerically.
		{"new xid after boundary is NOT before old high xid", 5, max - 2, false},
		{"old high xid IS before new xid past boundary", max - 2, 5, true},
		{"adjacent across boundary: max before 0", max, 0, true},
		{"adjacent across boundary: 0 not before max", 0, max, false},
		{"max before 1 (one step past wrap)", max, 1, true},

		// Exactly half the space apart is the degenerate midpoint; int32 of
		// 2^31 is negative, so a precedes b. Documents the boundary behavior.
		{"half-space apart: a precedes b", 0, 1 << 31, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := XIDPrecedes(c.a, c.b); got != c.want {
				t.Errorf("XIDPrecedes(%d, %d) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

// TestXIDPrecedesAntisymmetry verifies that for any two distinct XIDs less than
// 2^31 apart, exactly one precedes the other — the invariant horizon-selection
// (min) relies on. Sweeps a window straddling the wraparound boundary.
func TestXIDPrecedesAntisymmetry(t *testing.T) {
	const max = ^TransactionID(0)
	bases := []TransactionID{0, 1, 3, max - 3, max - 1, max, 1 << 30, 1<<31 - 1}
	for _, base := range bases {
		for d := TransactionID(1); d < 16; d++ {
			a := base
			b := base + d // may wrap; modular arithmetic is intentional
			if a == b {
				continue
			}
			pab := XIDPrecedes(a, b)
			pba := XIDPrecedes(b, a)
			if pab == pba {
				t.Errorf("antisymmetry broken: XIDPrecedes(%d,%d)=%v XIDPrecedes(%d,%d)=%v", a, b, pab, b, a, pba)
			}
			// With b = a + d and 0 < d < 2^31, b is the newer XID, so a precedes b.
			if !pab {
				t.Errorf("XIDPrecedes(%d, %d) = false, want true (b is a+%d)", a, b, d)
			}
		}
	}
}
