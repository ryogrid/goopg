// Package stats provides per-P sharded statistics primitives. The single
// type today is Counter, a 64-bit additive counter sharded across the
// runtime's logical processors via runtimeshim.PinP, eliminating
// cache-line contention on the Add hot path.
//
// Sum and Reset traverse all shards; both are intended for stats consumers
// (snapshot, pg_stat_* readout) rather than the hot path. A concurrent Add
// during a Sum may or may not be observed — counters are eventual-
// consistency by nature.
//
// Per docs/design/perf-optimize/08-runtime-internals.md §4 ("Use case 2")
// and docs/design/0107-0008f-perp-stats-counter.md.
package stats

import (
	"sync/atomic"

	"github.com/goopg/goopg/internal/port/runtimeshim"
)

// maxShards bounds the per-P shard table. PinP returns an index in
// [0, GOMAXPROCS). GOMAXPROCS defaults to runtime.NumCPU() and is
// rarely raised above 256 in practice. The fallback PinP always
// returns 0, which fits even at maxShards == 1; sizing to 256
// gives ample headroom for high-core machines without requiring
// allocation-time GOMAXPROCS introspection (which would race with
// later runtime.GOMAXPROCS() bumps).
const maxShards = 256

// shard is padded to one cache line (64 B) so independent Ps writing
// to neighbouring shards never share a cache line. The 8-byte counter
// plus 56-byte padding sums to exactly 64.
type shard struct {
	n atomic.Int64
	_ [56]byte
}

// Counter is a 64-bit additive counter sharded across logical processors.
// The zero value is a valid empty counter. Counter MUST be passed by
// pointer once any Add has been observed; copying after the first Add
// would split the count and lose contributions from the original.
type Counter struct {
	shards [maxShards]shard
}

// Add increments the counter by delta on the current P's shard. The
// pin/unpin window is short enough that callers may invoke Add from
// any goroutine context, including the hot path.
//
// Within the pinned window the shard's atomic.Int64 is still used
// (not a plain int64) so a concurrent Sum's atomic.LoadInt64 sees a
// well-defined value — the Go memory model otherwise gives no
// guarantee on plain int64 reads across goroutines, and the runtime
// may concurrently sample the shard via GC stack walks.
func (c *Counter) Add(delta int64) {
	pid := runtimeshim.PinP()
	c.shardFor(pid).n.Add(delta)
	runtimeshim.UnpinP()
}

// shardFor maps a P index onto the fixed shard table. PinP returns an index in
// [0, GOMAXPROCS), which is NOT bounded by maxShards: a machine (or a test)
// with GOMAXPROCS > 256 would index past the table and panic in Add. Folding
// the index keeps the counter correct — two Ps then share a shard, which costs
// a little cache-line contention but never a wrong or missing count.
// maxShards is a power of two, so the mask is the modulo.
func (c *Counter) shardFor(pid int) *shard {
	return &c.shards[pid&(maxShards-1)]
}

// Sum returns the aggregate count across all shards. Performed via
// atomic loads so the result is well-defined even with concurrent Adds;
// the returned value is a snapshot that may not reflect Adds in flight
// during the traversal.
func (c *Counter) Sum() int64 {
	var total int64
	for i := range c.shards {
		total += c.shards[i].n.Load()
	}
	return total
}

// Reset zeroes every shard. Like Sum, Reset is not atomic across the
// whole counter — a concurrent Add may land in a shard the reset loop
// has already cleared, in which case the Add survives. Reset is
// intended for test fixtures and end-of-epoch rollovers; production
// stats readouts should use Sum and treat the returned value as a
// monotonically-growing total.
func (c *Counter) Reset() {
	for i := range c.shards {
		c.shards[i].n.Store(0)
	}
}
