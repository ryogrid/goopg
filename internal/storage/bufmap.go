package storage

import (
	"sync"
	"sync/atomic"
)

// sentinels for bufmap vals array.
const (
	bufmapEmpty     uint64 = 0
	bufmapTombstone uint64 = 1
)

// bufmap is an open-addressing hash table mapping BufferTag to a packed
// (slotIdx<<32 | gen) value.
//
// Lookup is lock-free (only atomic loads). Insert and Delete take mu
// (write operations that touch both keys[] and vals[] must be serialized
// to prevent partial-write races on the 16-byte BufferTag key).
//
// The table size is always a power of two at load factor ≤ 50%.
type bufmap struct {
	mu   sync.Mutex
	mask uint64
	keys []BufferTag // len == size
	vals []uint64    // len == size; 0=empty, 1=tombstone, else (slotIdx<<32)|gen
}

// newBufmap constructs a table sized for nSlots active entries at ≤50%
// load factor (next power of two ≥ 2×nSlots).
func newBufmap(nSlots int) *bufmap {
	size := uint64(1)
	for size < uint64(nSlots)*2 {
		size <<= 1
	}
	return &bufmap{
		mask: size - 1,
		keys: make([]BufferTag, size),
		vals: make([]uint64, size),
	}
}

// bufTagHash computes a 64-bit hash for t, mixing every byte of BufferTag
// (DBOid, RelOid, Fork, Block). Based on MurmurHash3 fmix64.
// CRITICAL: Fork must feed the hash to avoid cross-fork collisions.
func bufTagHash(t BufferTag) uint64 {
	h := uint64(t.Rel.DBOid) | uint64(t.Rel.RelOid)<<32
	h ^= uint64(t.Rel.Fork) * 0xBF58476D1CE4E5B9
	h ^= uint64(t.Block) * 0x9E3779B97F4A7C15
	h ^= h >> 33
	h *= 0xFF51AFD7ED558CCD
	h ^= h >> 33
	h *= 0xC4CEB9FE1A85EC53
	h ^= h >> 33
	return h
}

// packVal packs (slotIdx, gen) into a single uint64 value.
// (slotIdx + 1) occupies the high 32 bits; gen occupies the low 32 bits.
// We shift slotIdx by +1 so that live entries always have the upper 32
// bits non-zero, which guarantees the packed value is > UINT32_MAX and
// therefore cannot collide with bufmapEmpty (0) or bufmapTombstone (1).
func packVal(slotIdx int32, gen uint32) uint64 {
	return uint64(uint32(slotIdx+1))<<32 | uint64(gen)
}

// unpackVal extracts (slotIdx, gen) from a packed value, reversing the
// +1 bias applied by packVal.
func unpackVal(v uint64) (int32, uint32) {
	return int32(v>>32) - 1, uint32(v)
}

// Lookup returns (slotIdx, gen) for tag, or (-1, 0) if absent. Lock-free.
// Tombstones do NOT terminate probing; only true-empty buckets do.
//
// Note: Insert uses plain linear probing (no Robin-Hood displacement), so
// Lookup cannot use the Robin-Hood "dist > residentDist" early-exit
// optimisation here — that would only be correct if Insert also reordered
// entries by probe distance.  We rely on the empty-bucket terminator plus
// the table-size safety bound.
func (m *bufmap) Lookup(tag BufferTag) (int32, uint32) {
	h := bufTagHash(tag) & m.mask
	dist := uint64(0)
	// Safety bound: probe at most table_size times before giving up.
	// Prevents infinite loops if the table is fully occupied by
	// tombstones or under concurrent insert/compact races.
	size := m.mask + 1
	for dist <= size {
		v := atomic.LoadUint64(&m.vals[h])
		switch {
		case v == bufmapEmpty:
			// True empty bucket: tag is not present.
			return -1, 0
		case v == bufmapTombstone:
			// Tombstone: continue probing (entry may be beyond).
			h = (h + 1) & m.mask
			dist++
			continue
		}
		// Live entry. Under Go's memory model, the atomic load on vals[h]
		// observing a live value implies seeing the prior keys[h] write.
		if m.keys[h] == tag {
			return unpackVal(v)
		}
		h = (h + 1) & m.mask
		dist++
	}
	return -1, 0
}

// Insert publishes (tag, slotIdx, gen) under mu. Returns true on success,
// false if an entry for the same tag already exists. Callers should call
// Lookup first (lock-free) to avoid unnecessary lock acquisition.
func (m *bufmap) Insert(tag BufferTag, slotIdx int32, gen uint32) bool {
	val := packVal(slotIdx, gen)
	m.mu.Lock()
	defer m.mu.Unlock()
	h := bufTagHash(tag) & m.mask
	size := m.mask + 1
	for i := uint64(0); i < size; i++ {
		v := m.vals[h]
		switch {
		case v == bufmapEmpty || v == bufmapTombstone:
			m.keys[h] = tag
			// Atomic store so concurrent lock-free Lookups see the
			// key before the live value (release semantics).
			atomic.StoreUint64(&m.vals[h], val)
			return true
		default:
			if m.keys[h] == tag {
				return false // already present
			}
			h = (h + 1) & m.mask
		}
	}
	// Table full — should not happen at ≤50% load.
	return false
}

// Delete marks the entry for (tag, slotIdx) as tombstone under mu.
func (m *bufmap) Delete(tag BufferTag, slotIdx int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := bufTagHash(tag) & m.mask
	size := m.mask + 1
	for i := uint64(0); i < size; i++ {
		v := m.vals[h]
		switch {
		case v == bufmapEmpty:
			return // not present
		case v == bufmapTombstone:
			h = (h + 1) & m.mask
			continue
		}
		existingSlotIdx, _ := unpackVal(v)
		if m.keys[h] == tag && existingSlotIdx == slotIdx {
			atomic.StoreUint64(&m.vals[h], bufmapTombstone)
			return
		}
		h = (h + 1) & m.mask
	}
}

// compact rebuilds the table in place eliminating all tombstones.
// Called under compactMu (cold path, rare). Takes mu internally.
func (m *bufmap) compact() {
	m.mu.Lock()
	defer m.mu.Unlock()
	size := m.mask + 1
	// Build a clean copy.
	newKeys := make([]BufferTag, size)
	newVals := make([]uint64, size)

	for i := uint64(0); i < size; i++ {
		v := m.vals[i]
		if v == bufmapEmpty || v == bufmapTombstone {
			continue
		}
		tag := m.keys[i]
		// Re-insert into new table.
		h := bufTagHash(tag) & m.mask
		for {
			if newVals[h] == bufmapEmpty {
				newKeys[h] = tag
				newVals[h] = v
				break
			}
			h = (h + 1) & m.mask
		}
	}

	// Overwrite in place with atomic stores (allow readers to transition).
	for i := uint64(0); i < size; i++ {
		m.keys[i] = newKeys[i]
		atomic.StoreUint64(&m.vals[i], newVals[i])
	}
}
