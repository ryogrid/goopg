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

// bufmapBucket is one slot of the open-addressing hash table. Every
// field is mutated only via atomic operations so concurrent lock-free
// Lookups never observe a torn key or val.
//
// Layout encoding for key:
//
//	key0 = uint64(DBOid)<<32 | uint64(RelOid)
//	key1 = uint64(Block)<<32 | uint64(uint8(Fork))
//
// val:
//
//	0  = empty   (sentinel; treat as "not present, terminate probe")
//	1  = tombstone (sentinel; continue probing past)
//	>1 = packVal(slotIdx, gen) = ((slotIdx+1)<<32) | gen
//	    — biased by +1 so the live range never collides with sentinels.
type bufmapBucket struct {
	key0 atomic.Uint64
	key1 atomic.Uint64
	val  atomic.Uint64
}

// bufmapInner is the backing store of a bufmap. Insert/Delete mutate
// individual buckets in place under bufmap.mu; compact() never mutates
// an inner that is concurrently visible to readers — it builds a fresh
// inner and publishes it via bufmap.inner.Store, so each Lookup observes
// a single, self-consistent snapshot.
type bufmapInner struct {
	mask    uint64
	buckets []bufmapBucket // len == mask+1
}

// bufmap is an open-addressing hash table mapping BufferTag to a packed
// (slotIdx<<32 | gen) value.
//
// Lookup is lock-free (atomic loads only). Insert and Delete take mu
// (release-ordered with readers via the atomic store on val).
//
// compact() replaces the entire inner atomically so that no concurrent
// reader observes a partially-rewritten bucket array.
type bufmap struct {
	mu    sync.Mutex
	inner atomic.Pointer[bufmapInner]
}

// newBufmap constructs a table sized for nSlots active entries at ≤50%
// load factor (next power of two ≥ 2×nSlots).
func newBufmap(nSlots int) *bufmap {
	size := uint64(1)
	for size < uint64(nSlots)*2 {
		size <<= 1
	}
	bm := &bufmap{}
	bm.inner.Store(&bufmapInner{
		mask:    size - 1,
		buckets: make([]bufmapBucket, size),
	})
	return bm
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

// packKey encodes a BufferTag into the (key0, key1) pair stored in a
// bufmapBucket. The encoding is unique per tag.
func packKey(t BufferTag) (uint64, uint64) {
	key0 := uint64(t.Rel.DBOid) | uint64(t.Rel.RelOid)<<32
	// Fork is int8; cast through uint8 to avoid sign extension into the
	// upper bits of key1.
	key1 := uint64(t.Block)<<32 | uint64(uint8(t.Rel.Fork))
	return key0, key1
}

// Lookup returns (slotIdx, gen) for tag, or (-1, 0) if absent. Lock-free.
// Tombstones do NOT terminate probing; only true-empty buckets do.
//
// Note: Insert uses plain linear probing (no Robin-Hood displacement), so
// Lookup cannot use the Robin-Hood "dist > residentDist" early-exit
// optimisation here — that would only be correct if Insert also reordered
// entries by probe distance. We rely on the empty-bucket terminator plus
// the table-size safety bound.
//
// Each bucket is read with a seqlock-style snapshot: load val, load key0
// and key1, then re-load val. If val is unchanged across the read, the
// snapshot is self-consistent (no Insert/Delete tore the key mid-read).
// If val changed, retry the same bucket.
func (m *bufmap) Lookup(tag BufferTag) (int32, uint32) {
	in := m.inner.Load()
	wantKey0, wantKey1 := packKey(tag)
	h := bufTagHash(tag) & in.mask
	size := in.mask + 1
	dist := uint64(0)
	// Safety bound: probe at most table_size times before giving up.
	// Prevents infinite loops if the table is fully occupied by
	// tombstones under concurrent insert/compact races.
	for dist <= size {
		b := &in.buckets[h]
		// Seqlock snapshot of (val, key0, key1).
		v1 := b.val.Load()
		switch {
		case v1 == bufmapEmpty:
			return -1, 0
		case v1 == bufmapTombstone:
			h = (h + 1) & in.mask
			dist++
			continue
		}
		k0 := b.key0.Load()
		k1 := b.key1.Load()
		v2 := b.val.Load()
		if v1 != v2 {
			// Bucket mutated mid-read — retry the same slot.
			continue
		}
		if k0 == wantKey0 && k1 == wantKey1 {
			return unpackVal(v1)
		}
		h = (h + 1) & in.mask
		dist++
	}
	return -1, 0
}

// Insert publishes (tag, slotIdx, gen) under mu. Returns true on success,
// false if an entry for the same tag already exists. Callers should call
// Lookup first (lock-free) to avoid unnecessary lock acquisition.
func (m *bufmap) Insert(tag BufferTag, slotIdx int32, gen uint32) bool {
	val := packVal(slotIdx, gen)
	wantKey0, wantKey1 := packKey(tag)
	m.mu.Lock()
	defer m.mu.Unlock()
	in := m.inner.Load()
	h := bufTagHash(tag) & in.mask
	size := in.mask + 1
	for i := uint64(0); i < size; i++ {
		b := &in.buckets[h]
		v := b.val.Load()
		switch {
		case v == bufmapEmpty || v == bufmapTombstone:
			// Park val at tombstone first (we already aren't "empty
			// terminator" to readers, since v was empty/tombstone — but
			// we want to be explicit about the protocol). Writing keys
			// while val is tombstone is safe because Lookup skips
			// tombstone buckets without reading keys.
			b.val.Store(bufmapTombstone)
			b.key0.Store(wantKey0)
			b.key1.Store(wantKey1)
			// Release-store the live val. Concurrent readers that
			// observe `live` will retry their snapshot if they raced
			// the key write, and on the retry will see val == live
			// alongside the new keys.
			b.val.Store(val)
			return true
		default:
			k0 := b.key0.Load()
			k1 := b.key1.Load()
			if k0 == wantKey0 && k1 == wantKey1 {
				return false // already present
			}
			h = (h + 1) & in.mask
		}
	}
	// Table full — should not happen at ≤50% load.
	return false
}

// Delete marks the entry for (tag, slotIdx) as tombstone under mu.
func (m *bufmap) Delete(tag BufferTag, slotIdx int32) {
	wantKey0, wantKey1 := packKey(tag)
	m.mu.Lock()
	defer m.mu.Unlock()
	in := m.inner.Load()
	h := bufTagHash(tag) & in.mask
	size := in.mask + 1
	for i := uint64(0); i < size; i++ {
		b := &in.buckets[h]
		v := b.val.Load()
		switch {
		case v == bufmapEmpty:
			return // not present
		case v == bufmapTombstone:
			h = (h + 1) & in.mask
			continue
		}
		existingSlotIdx, _ := unpackVal(v)
		k0 := b.key0.Load()
		k1 := b.key1.Load()
		if k0 == wantKey0 && k1 == wantKey1 && existingSlotIdx == slotIdx {
			b.val.Store(bufmapTombstone)
			return
		}
		h = (h + 1) & in.mask
	}
}

// compact builds a fresh inner with all tombstones eliminated and
// atomically publishes it. The old inner remains live (immutable from
// now on) until in-flight lock-free Lookups release their references
// and the GC reclaims it.
//
// Concurrent Insert/Delete are blocked by mu. Concurrent Lookups on the
// old inner are safe because (a) compact only reads the old inner via
// atomic loads, never mutates it, and (b) the inner pointer swap
// publishes the new inner atomically, so a Lookup observes one
// self-consistent snapshot.
func (m *bufmap) compact() {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.inner.Load()
	size := cur.mask + 1
	newInner := &bufmapInner{
		mask:    cur.mask,
		buckets: make([]bufmapBucket, size),
	}
	for i := uint64(0); i < size; i++ {
		b := &cur.buckets[i]
		v := b.val.Load()
		if v == bufmapEmpty || v == bufmapTombstone {
			continue
		}
		k0 := b.key0.Load()
		k1 := b.key1.Load()
		// Re-derive the hash from the unpacked tag bits.
		tag := unpackKey(k0, k1)
		h := bufTagHash(tag) & cur.mask
		for {
			nb := &newInner.buckets[h]
			if nb.val.Load() == bufmapEmpty {
				nb.key0.Store(k0)
				nb.key1.Store(k1)
				nb.val.Store(v)
				break
			}
			h = (h + 1) & cur.mask
		}
	}
	m.inner.Store(newInner)
}

// unpackKey reverses packKey. Used by compact() to re-hash live entries
// into the new inner.
func unpackKey(k0, k1 uint64) BufferTag {
	return BufferTag{
		Rel: RelFileNode{
			DBOid:  uint32(k0),
			RelOid: uint32(k0 >> 32),
			Fork:   ForkNumber(int8(uint8(k1))),
		},
		Block: BlockNumber(k1 >> 32),
	}
}
