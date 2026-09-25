package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestJoinParallelAwareLabel pins PG's generic prefix rule (explain.c:1630) on
// the join node. For a join, `parallel_aware` is set only by the `parallel_hash
// = true` hash join (create_hashjoin_path: parallel_aware = consider_parallel
// && parallel_hash); merge and nested-loop joins are never parallel-aware. So
// "Parallel " follows Join.ParallelHash (M0146-0002), and a partial hash join
// over a COMPLETE inner — goopg's leader-prebuilt shared table, R7's former
// reason for the prefix — prints PG's plain "Hash Join".
func TestJoinParallelAwareLabel(t *testing.T) {
	mk := func(algo optimizer.JoinAlgo, aware, phash bool) string {
		return describePlan(&optimizer.Join{
			Type: optimizer.JoinTypeInner, Algo: algo, ParallelAware: aware, ParallelHash: phash,
		}, nil)
	}
	if got := mk(optimizer.JoinAlgoHash, false, false); got != "Hash Join" {
		t.Fatalf("non-parallel hash join = %q, want %q", got, "Hash Join")
	}
	if got := mk(optimizer.JoinAlgoHash, true, false); got != "Hash Join" {
		t.Fatalf("partial hash join over a complete inner = %q, want %q (PG's parallel_hash = false)", got, "Hash Join")
	}
	if got := mk(optimizer.JoinAlgoHash, true, true); got != "Parallel Hash Join" {
		t.Fatalf("parallel hash join = %q, want %q", got, "Parallel Hash Join")
	}
	if got := mk(optimizer.JoinAlgoMerge, true, false); got != "Merge Join" {
		t.Fatalf("partial merge join = %q, want %q (merge joins are never parallel-aware)", got, "Merge Join")
	}
	if got := mk(optimizer.JoinAlgoNestedLoop, false, false); got != "Nested Loop" {
		t.Fatalf("non-parallel nested loop = %q, want %q", got, "Nested Loop")
	}
}
