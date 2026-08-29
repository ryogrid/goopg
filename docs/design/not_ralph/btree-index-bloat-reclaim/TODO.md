# TODO — B-tree index bloat reclamation

Tracking for [DESIGN.md](DESIGN.md). Tick only when actually done.

## Investigation

- [x] Read the revert commit (`4998c81b9`) and the reverted implementation (`bdaa325a4`)
- [x] Establish that the "~18×" was **index growth, not throughput** (throughput was neutral: 14,766 vs 14,882 tps)
- [x] Trace why the pkey grows: non-HOT updates insert a new entry; nothing reclaims the old one before the page splits
- [x] Trace how dead entries are represented (`ItemIDDead` + `BTHasGarbage`) and who sets/clears them
- [x] Locate deduplication: `deduplicateToRawItemsWithSpans` (`bulkload.go:602`) via `refillDeduplicated` (`btree.go:3480`)
- [x] Establish exactly why LP_DEAD defeats dedup: `KillItems` skips postings (`isPostingRaw` guard); the dedup merger has no dead-awareness
- [x] Read PG's ordering: `_bt_delete_or_dedup_one_page` (`nbtinsert.c:2683`), `_bt_simpledel_pass` (`:2812`), `_bt_dedup_pass` (`nbtdedup.c:58`)
- [x] Inventory reusable primitives (`VacuumIndexPages`, `heapChainDeadToAll`, `storage.TupleDeadToAll`, `OldestXmin`)
- [x] Confirm the reverted commit cannot be cherry-picked onto HEAD (pre-move package layout)

## Design

- [x] Write the initial DESIGN.md
- [x] Evaluate dedup-aware purge vs background btree vacuum vs PG-faithful port
- [x] Select an approach and record the rationale (A: purge inside the page rewrite, before dedup)
- [x] Resolve the layering problem — `nbtree.DeadTIDFilter` injected via `Options.DeadTIDs`; nbtree never imports executor
- [x] Heap-I/O budget: `purgeHeapBlockBudget = 32` distinct blocks, grouped and walked in block order; gate: only after dedup has already failed to avoid the split
- [x] WAL/replay + PG-format parity unaffected (packing density only; TPC-H spot-check read an index built by the OLD binary and passed)
- [x] DESIGN.md updated — the ORIGINAL ROOT CAUSE WAS WRONG; §2a records the correction

## Implementation

- [x] **Second root cause found and fixed**: leaked right-page allocation on every dedup-recovery (`bt.recycleBlock(rightBlk)`) — see DESIGN.md §7a

- [x] Added `BTree.deadTIDs` + `Options.DeadTIDs` (nil ⇒ no-op)
- [x] Purge runs on the EXPANDED list and feeds survivors back through `dedupConsolidate`
- [x] Wired in `indexBTreeOptions` via `indexDeadTIDFilter` (heap-verified, `heapChainDeadToAll` + `OldestXmin`)
- [x] No statement-hot-path work: purge runs only on the split path after dedup has already failed; fill factor is build-time only

## Tests

- [x] Deterministic test reproducing index growth (`TestPurgeReclaimsIndexGrowth`)
- [x] Test that dead entries are reclaimed — 11 -> 3 pages with the purge on
- [x] Dedup-effectiveness guard (`TestPurgePreservesDeduplication`) — survivors still form postings and never need MORE line pointers
- [x] Contract tests: nil filter, wrong-length answer, only-named-entries, never-empties-page
- [x] Oracle-absent default covered by the nil-filter subtest + full nbtree suite green
- [x] `go test ./internal/access/nbtree/` green
- [x] `-race` on nbtree green

## Benchmarks

- [x] Baseline: 152.3 B/txn, 11,085 tps
- [x] New: **0.0 B/txn**, 11,583 tps
- [x] Duplicate-heavy regime covered by `TestPurgeReclaimsIndexGrowth` (11 -> 3 pages) and `TestPurgePreservesDeduplication`; no space regression
- [x] All numbers in DESIGN.md §9, including the purge's ZERO-dead negative result (§2a, §9.3)

## Verification

- [x] units PASS
- [x] race-gate PASS (0 races)
- [x] tpch-spotcheck PASS (Q12=2, Q13=34)
- [x] parser goldens unchanged (units gate)
- [x] pre-commit pgbench smoke PASS (hook ran on commit e0493dd8c)
- [x] Diff reviewed: purge is split-path-only, fill factor build-time-only; per-index-open closure noted as a limitation in DESIGN.md §9.5

## Documentation

- [x] DESIGN.md §2a/§7a/§9 describe the final implementation and the corrected root cause
- [x] TODO.md reflects reality
- [x] Cross-referenced from `perf-optimize-take3/07` (candidate G marked RESOLVED, with the corrected premise)
- [x] Deferral-ledger row appended (2026-08-30) with the four residues
