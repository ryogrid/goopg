package executor

import (
	"sync/atomic"
)

// M0074-0003 forward-compat infrastructure (PARTIAL): arena
// registry + per-process permanent arena. Lays the foundation for
// the Datum struct packed layout (`Buf []byte` + `arena *Arena`
// → `(ArenaRef int32, Offset int32, Length int32)`) deferred to
// M0075. The actual struct flip is NOT in this commit; see
// docs/design/0074-0003-datum-packed-layout.md status note.
//
// The infrastructure here is callable today but unused until M0075
// flips Datum to consult the registry on StringValue / BytesValue.

// arenaRegistrySize bounds the simultaneous live arena count.
// goopg's per-query operator counts (a few seqScan + a few
// indexScan + aggregate / sort / etc.) stay well below 256;
// per-connection multi-tenant deployments would need a different
// strategy (M0075 candidate).
const arenaRegistrySize = 256

// arenaRegistry indexes live Arenas by their registryIdx slot.
// Slot 0 is reserved for permArena (set by init via newPermArena).
// Slots 1..arenaRegistrySize-1 are allocated round-robin via
// registerArena.
//
// Concurrency: each slot is a single pointer; producer-consumer
// patterns rely on Go's atomic pointer reads (single-word writes
// are atomic on the platforms goopg supports). Cross-goroutine
// arena sharing remains a contract violation per Arena's
// non-thread-safe contract.
var arenaRegistry [arenaRegistrySize]*Arena

// arenaRegistryNext is the round-robin slot allocator counter.
// Atomic-incremented on register; modular into the [1,
// arenaRegistrySize) range to skip the reserved permArena slot 0.
var arenaRegistryNext int32

// permArenaSlot is the registry slot reserved for permArena.
// Always 0 so a zero-valued ArenaRef in a future packed Datum
// resolves to permArena (matching the "uninitialised → permArena"
// semantic the M0075 flip will use for literal Datums).
const permArenaSlot int32 = 0

// permArena is the process-global permanent arena. Never resets;
// holds literal Datum payloads, detoasted bytes, spill-reader
// reconstructed strings, and COPY FROM payloads. Bounded by the
// per-session literal/detoast count — TPC-H query texts allocate
// at most a few KB per query. M0075 candidate: per-connection
// scoping with bounded LRU eviction for production multi-tenant
// deployments.
//
// Lifetime: process-wide. Drop() is a no-op (arena always lives).
var permArena = newPermArena()

func newPermArena() *Arena {
	a := NewArena(arenaPageSize)
	a.permanent = true
	a.registryIdx = permArenaSlot
	arenaRegistry[permArenaSlot] = a
	return a
}

// registerArena claims the next free registry slot for a, stores
// the slot index on the arena, and returns it. Skips slot 0
// (reserved for permArena). Wraps round-robin via modulo; collision
// with a still-live arena triggers a re-roll.
func registerArena(a *Arena) int32 {
	if a == nil {
		return -1
	}
	for tries := 0; tries < arenaRegistrySize*2; tries++ {
		// Counter is atomic; slot index is (counter % (size-1)) + 1
		// to skip slot 0.
		c := atomic.AddInt32(&arenaRegistryNext, 1)
		slot := (c % int32(arenaRegistrySize-1)) + 1
		// Try to claim the slot. Race-free without CAS: we only
		// guarantee single-writer per slot — the registry is
		// per-process and Arena.Drop() is the only unregister
		// path, which writes nil. A live arena holding the slot
		// is a contract violation we don't try to detect (the
		// 256-slot cap means we'd need 256+ live arenas to wrap;
		// a TPC-H query has well under 50).
		if arenaRegistry[slot] == nil {
			arenaRegistry[slot] = a
			a.registryIdx = slot
			return slot
		}
	}
	// All slots claimed and not yet released. Falling through
	// without registering means future packed-Datum flip would
	// resolve via slot 0 (permArena) — incorrect for arena-backed
	// payload but at least non-fatal. Return -1 as the "no slot"
	// sentinel; callers should panic or log if registration is
	// load-bearing.
	return -1
}

// unregisterArena releases the slot held by arena. Called from
// Arena.Drop().
func unregisterArena(a *Arena) {
	if a == nil || a.permanent {
		return
	}
	if a.registryIdx <= 0 || int(a.registryIdx) >= arenaRegistrySize {
		return
	}
	arenaRegistry[a.registryIdx] = nil
}

// AllocateString copies s into the arena and returns the (offset,
// length) pair the caller stores in the future packed Datum
// triplet. Offset and length are int32 so they fit the planned
// 4 + 4 = 8-byte sub-budget of the packed layout.
func (a *Arena) AllocateString(s string) (offset, length int32) {
	if len(s) == 0 {
		return 0, 0
	}
	buf, off := a.Allocate(len(s))
	copy(buf, s)
	return int32(off), int32(len(s))
}

// AllocateBytes copies b into the arena and returns the (offset,
// length) pair.
func (a *Arena) AllocateBytes(b []byte) (offset, length int32) {
	if len(b) == 0 {
		return 0, 0
	}
	buf, off := a.Allocate(len(b))
	copy(buf, b)
	return int32(off), int32(len(b))
}
