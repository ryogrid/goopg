(idle — nothing in flight)

M0127-P0.1 is CLOSED (loop #42, 2026-08-03) — the first M0127 task, and the
first code of the leftdeep-joins bundle.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P0.2` (single-pass build: fold
`drainRowsBounded`'s budget into `buildLazyHashTable`'s build loop, delete
the re-iteration's per-row `MaterializedSlot`; keep the M0097-0058
owned-copy discipline). Bar: UNITS + SPOT + RACE.**

Carry-over facts a next loop should not re-derive:

- **P0.1 shape:** `mergedKeySlotCache` in
  `internal/executor/operators_join_agg.go` (type + `rebind`, next to
  `mergedKeySlot`), two instances per `joinOp` (`lazyBuildKeySlot`,
  `lazyProbeKeySlot`). `rebind` swaps `slot.sources[realIdx]` — one
  interface word, no alloc — and rebuilds only when
  `(realWidth, nullWidth, realOnLeft)` changes.
- **Widths are known at `Open`**, from `len(o.left.Schema())` /
  `len(o.right.Schema())` at the top of `buildLazyHashTable`. The
  `width == 0 && len(row) > 0` lines inside the build loops are an
  empty-child-schema fallback, not the normal source of the width — that is
  why the hoist is legal and why the cache still needs a shape guard.
- **Microbench numbers (this host):** cached 4.10 ns/op, 0 allocs; uncached
  185.8 ns/op, 344 B, 5 allocs. `BenchmarkMergedKeySlotSeam[Uncached]` in
  `internal/executor/join_merged_key_slot_test.go` — reuse this file as the
  seam-microbench home for P1.1's 0-alloc bar.
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified
  (including its IMPLEMENTATION-TODO checkboxes). Landed-task tracking goes
  in `docs/design/0127-pg-shaped-join-search.md` §6 (progress log, added
  this loop) + the fix_plan checkbox + the README index status.
- **Deliberately out of scope at P0.1:** `fused_hash_join.go:186,:280` keep
  the per-row `mergedKeySlot` (05 §3 — fusion dies at P6.1);
  `buildHashRightWithCTID`'s per-row `SlotFromRow` (FOR-UPDATE-only path).
- Nightly triage: the 20 `AI-20260803-013955-*` subjects are all already
  filed under M-NIGHTLY. Nothing new to file this loop.

Gates run this loop: UNITS precommit PASS; SPOT `scripts/tpch-spotcheck.sh`
PASS (Q12 rows=2, Q13 rows=35, 32.3 s query phase, peak 10,767 MB); BENCH as
above; pgbench smoke via the commit hook; `make ralph-state-guard` OK
(auto-repaired a stale progress marker).

In-flight: none.
