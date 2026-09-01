package stats

import "testing"

// TestCounterShardForFoldsHighPIndexes is the review/260831-2 UT-5 guard. Add
// indexed the shard table directly with runtimeshim.PinP(), whose result is
// bounded by GOMAXPROCS, not by maxShards — so on a host (or under a test)
// running with GOMAXPROCS above 256 the very first Add panicked with an index
// out of range.
func TestCounterShardForFoldsHighPIndexes(t *testing.T) {
	var c Counter
	for _, pid := range []int{0, 1, maxShards - 1, maxShards, maxShards + 7, 1023} {
		c.shardFor(pid).n.Add(1)
	}
	if got := c.Sum(); got != 6 {
		t.Errorf("Sum() = %d, want 6: every P index must land on some shard", got)
	}
}
