//go:build linux

package aio

// review/260831-2 ST-9 — pokeWake wrote its NOP without checking that the
// submission queue had room.
//
// Every other submission path takes a slot from m.slots, which is sized to
// sqEntries, so it cannot outrun the ring. The close-time wake NOP takes no
// slot: with the ring full, `tail & sqMask` aliases an SQE the kernel has not
// read yet, and the NOP would overwrite a real read/write whose completion
// then never arrives. The ring itself needs a kernel; the arithmetic that
// decides this does not, so it is pinned here directly.

import "testing"

func TestSQRingFull(t *testing.T) {
	var head, tail uint32
	m := &methodIOUring{sqHead: &head, sqTail: &tail, sqEntries: 4, sqMask: 3}

	for _, tc := range []struct {
		name       string
		head, tail uint32
		want       bool
	}{
		{name: "empty", head: 0, tail: 0, want: false},
		{name: "one queued", head: 0, tail: 1, want: false},
		{name: "one free", head: 0, tail: 3, want: false},
		{name: "exactly full", head: 0, tail: 4, want: true},
		{name: "full after consuming two", head: 2, tail: 6, want: true},
		{name: "drained", head: 4, tail: 4, want: false},
		// Both counters are free-running uint32s; the kernel wraps them
		// rather than resetting, so the check must survive the wrap.
		{name: "full across the uint32 wrap", head: ^uint32(0) - 1, tail: 2, want: true},
		{name: "room across the uint32 wrap", head: ^uint32(0) - 1, tail: 0, want: false},
	} {
		head, tail = tc.head, tc.tail
		if got := m.sqRingFull(tail); got != tc.want {
			t.Errorf("%s: sqRingFull(head=%d tail=%d) = %v, want %v", tc.name, tc.head, tc.tail, got, tc.want)
		}
	}
}

func TestPokeWakeSkipsWhenSQRingIsFull(t *testing.T) {
	var head, tail uint32 = 2, 6 // 4 entries queued in a 4-entry ring
	m := &methodIOUring{
		sqHead:    &head,
		sqTail:    &tail,
		sqEntries: 4,
		sqMask:    3,
		sqes:      make([]ioSqe, 4),
	}
	live := ioSqe{Opcode: iorOpRead, UserData: 99}
	m.sqes[6&m.sqMask] = live

	if err := m.pokeWake(); err != nil {
		t.Fatalf("pokeWake on a full ring: %v", err)
	}
	if tail != 6 {
		t.Errorf("tail advanced to %d on a full ring", tail)
	}
	if m.sqes[6&m.sqMask] != live {
		t.Errorf("the wake NOP overwrote an unconsumed SQE: %+v", m.sqes[6&m.sqMask])
	}
}
