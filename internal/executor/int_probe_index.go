package executor

import "math/bits"

// intProbeIndex is the hash join's int-lane probe table (M0122-0012 C59,
// design docs/design/0100-0149/0122-0012-c59-int-lane-open-addressing.md):
// a flat open-addressing copy of lazyIntHash that the per-row probe reads
// instead of the Go map. Each probe touches one {key, rows} slot of a
// power-of-two array (Fibonacci hash, linear probing), where the map's swiss
// table first reads a group's control word and then a slot in another cache
// line. PG probes its hash join's bucket array the same way
// (ExecScanHashBucket, nodeHash.c) and its grouping tables are simplehash.h's
// linear-probe arrays.
//
// The map stays the source of truth for every other site — batching, spill,
// the shared parallel build, the outer-join fill and demotion. The index is
// a frozen view: it records the map it was built from (holding it, so the
// map cannot be freed and its address reused) and that map's length, and
// intProbeRows rebuilds it whenever the current lazyIntHash is another map or
// has a different length. Every site that replaces, clears or shrinks the map
// changes one of the two; rows are appended to existing keys only while the
// table is being built, before the first probe.
type intProbeIndex struct {
	entries []intProbeEntry
	mask    uint64
	shift   uint
	src     map[int64][]Row
	n       int
}

type intProbeEntry struct {
	key  int64
	rows []Row // nil marks an empty slot: every key in the map has a row
}

// intProbeIndexMaxKeys bounds the index: a larger table keeps the map lookup,
// so parallel participants probing one shared table do not each hold a large
// copy (PG's shared hash table is one array for all of them).
const intProbeIndexMaxKeys = 1 << 20

// intProbeIndexDisabled turns the index off. Tests and the A/B set it to
// compare it with the map lookup.
var intProbeIndexDisabled = false

// intProbeRows returns the build rows for probe key ik — exactly
// o.lazyIntHash[ik].
func (o *joinOp) intProbeRows(ik int64) []Row {
	m := o.lazyIntHash
	x := &o.intProbe
	if !sameIntMap(x.src, m) || x.n != len(m) {
		if intProbeIndexDisabled || len(m) == 0 || len(m) > intProbeIndexMaxKeys {
			x.entries, x.src = nil, nil
			return m[ik]
		}
		x.build(m)
	}
	if x.entries == nil {
		return m[ik]
	}
	i := (uint64(ik) * 0x9E3779B97F4A7C15) >> x.shift
	for {
		e := &x.entries[i]
		if e.rows == nil {
			return nil
		}
		if e.key == ik {
			return e.rows
		}
		i = (i + 1) & x.mask
	}
}

// sameIntMap reports whether a and b are the same map (not equal contents).
func sameIntMap(a, b map[int64][]Row) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	// Two map values are the same map when an insert through one is seen
	// through the other; comparing that would mutate them. A map value is a
	// pointer to its header, so compare those instead.
	return mapHeaderPtr(a) == mapHeaderPtr(b)
}

func (x *intProbeIndex) build(m map[int64][]Row) {
	c := 2
	for c < len(m)+len(m)/2 {
		c <<= 1
	}
	if cap(x.entries) >= c {
		x.entries = x.entries[:c]
		clear(x.entries)
	} else {
		x.entries = make([]intProbeEntry, c)
	}
	x.mask = uint64(c - 1)
	x.shift = uint(64 - bits.TrailingZeros(uint(c)))
	for k, rows := range m {
		if len(rows) == 0 {
			// A key with no rows matches nothing, as the map's empty
			// slice does; leaving it out keeps nil as the empty marker.
			continue
		}
		i := (uint64(k) * 0x9E3779B97F4A7C15) >> x.shift
		for x.entries[i].rows != nil {
			i = (i + 1) & x.mask
		}
		x.entries[i] = intProbeEntry{key: k, rows: rows}
	}
	x.src, x.n = m, len(m)
}
