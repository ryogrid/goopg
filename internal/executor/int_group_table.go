package executor

import "math/bits"

// intGroupTable is the hashed aggregate's int-lane group table (M0122-0012
// C60, slice 2 of docs/design/0100-0149/0122-0012-c59-int-lane-open-addressing.md):
// interleaved {key, group} entries in a power-of-two array, Fibonacci hash,
// linear probing, doubled and rehashed past a 2/3 load. It replaces a
// map[int64]*groupRuntime, whose swiss table reads a group's control word and
// then a slot in another cache line per lookup; here one 16-byte slot answers
// the common lookup. PG's hashed aggregate keeps its groups in a simplehash.h
// array the same way (TupleHashTable, LookupTupleHashEntry in execGrouping.c,
// sized by build_hash_tables).
//
// Inserts and lookups interleave (a row founds its group, the next row with
// that key finds it), so this is a real table, unlike the hash join's frozen
// probe index. A key is put only after get reported it absent.
type intGroupTable struct {
	entries []intGroupEntry
	mask    uint64
	shift   uint
	n       int
	// m is the map the table replaced, used instead when
	// aggIntTableDisabled is set (A/B and tests).
	m map[int64]*groupRuntime
}

type intGroupEntry struct {
	key int64
	g   *groupRuntime // nil marks an empty slot
}

// aggIntTableDisabled makes the int lane keep its groups in a Go map again.
// Tests and the A/B set it to compare the two.
var aggIntTableDisabled = false

func newIntGroupTable() *intGroupTable {
	if aggIntTableDisabled {
		return &intGroupTable{m: make(map[int64]*groupRuntime)}
	}
	t := &intGroupTable{}
	t.resize(64)
	return t
}

func (t *intGroupTable) resize(c int) {
	old := t.entries
	t.entries = make([]intGroupEntry, c)
	t.mask = uint64(c - 1)
	t.shift = uint(64 - bits.TrailingZeros(uint(c)))
	for _, e := range old {
		if e.g != nil {
			i := (uint64(e.key) * 0x9E3779B97F4A7C15) >> t.shift
			for t.entries[i].g != nil {
				i = (i + 1) & t.mask
			}
			t.entries[i] = e
		}
	}
}

// get returns key k's group, or nil.
func (t *intGroupTable) get(k int64) *groupRuntime {
	if t.m != nil {
		return t.m[k]
	}
	i := (uint64(k) * 0x9E3779B97F4A7C15) >> t.shift
	for {
		e := &t.entries[i]
		if e.g == nil || e.key == k {
			return e.g
		}
		i = (i + 1) & t.mask
	}
}

// put files group g (non-nil) under k, which get reported absent.
func (t *intGroupTable) put(k int64, g *groupRuntime) {
	if t.m != nil {
		t.m[k] = g
		return
	}
	if 3*(t.n+1) > 2*len(t.entries) {
		t.resize(2 * len(t.entries))
	}
	i := (uint64(k) * 0x9E3779B97F4A7C15) >> t.shift
	for t.entries[i].g != nil {
		i = (i + 1) & t.mask
	}
	t.entries[i] = intGroupEntry{key: k, g: g}
	t.n++
}
