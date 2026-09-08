package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestJoinParallelAwareLabel (R7, plan-parity-fix-take2) pins PG's generic
// prefix rule (explain.c:1630) on the join node: a node whose parallel_aware
// flag is set prints "Parallel " before its name, and one whose flag is clear
// prints exactly what it printed before R7.
//
// The label states a fact rather than matching PG cosmetically — goopg builds
// the hash cooperatively (parallel_hash_build.go) — so the OFF cases are the
// load-bearing half: a join that is not parallel-aware must never claim to be.
func TestJoinParallelAwareLabel(t *testing.T) {
	mk := func(algo optimizer.JoinAlgo, aware bool) string {
		return describePlan(&optimizer.Join{
			Type: optimizer.JoinTypeInner, Algo: algo, ParallelAware: aware,
		}, nil)
	}
	if got := mk(optimizer.JoinAlgoHash, false); got != "Hash Join" {
		t.Fatalf("non-parallel hash join = %q, want %q", got, "Hash Join")
	}
	if got := mk(optimizer.JoinAlgoHash, true); got != "Parallel Hash Join" {
		t.Fatalf("parallel-aware hash join = %q, want %q", got, "Parallel Hash Join")
	}
	// PG's rule is generic over node kinds, so it is generic here too.
	if got := mk(optimizer.JoinAlgoMerge, true); got != "Parallel Merge Join" {
		t.Fatalf("parallel-aware merge join = %q, want %q", got, "Parallel Merge Join")
	}
	if got := mk(optimizer.JoinAlgoNestedLoop, false); got != "Nested Loop" {
		t.Fatalf("non-parallel nested loop = %q, want %q", got, "Nested Loop")
	}
}
