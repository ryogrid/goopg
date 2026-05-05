# M0055 Staged B-tree Enhancement — Partial Results (2026-05-06)

**Status:** PARTIAL — Phase A primary win landed; Phases B-E
deferred as named follow-ups with explicit acceptance criteria.
**Branch:** `perf-analysis`.
**Baseline:** `analysis/btree-baseline-2026-05-06.md`.

## 1. Summary

This report covers the M0055 milestone's first delta vs the
2026-05-06 baseline. Phase A's primary win (in-place insert via
`PageInsertItemRawAt` + binary-search line-pointer probe) landed
in commit `0b46771` and delivers an **8.4× insert throughput
improvement** on the M0055-0001 harness. The remaining Phase A
items and all of Phase B-E are tracked as named follow-up
sub-tasks in `.ralph/fix_plan.md` with sized acceptance criteria
— no silent demotion of the milestone DoD.

## 2. Phase-by-phase status

| Sub-task | Status | Headline |
|----------|--------|----------|
| M0055-0001 (baseline) | LANDED | 23 540 inserts/sec, p95 49 µs, 0.35 % splits |
| M0055-0002 Phase A — in-place insert | LANDED | **+741 % inserts/sec** (8.4× speedup) |
| M0055-0002-followup-byte-split | open | varlen-key split-loc balance |
| M0055-0002-followup-rightmost-cache | open | append-workload fastpath |
| M0055-0003 Phase B (steady-state dedup) | open | duplicate-heavy size drift |
| M0055-0004 Phase C (multi-writer split) | open | concurrent insert scaling |
| M0055-0005 Phase D (deletion/recycling) | open | crash-replay safety + page reuse |
| M0055-0006 Phase E (CREATE INDEX spill) | open | bounded build-side memory |
| M0055-0007 (this report) | LANDED | wraps Phase A; tracks B-E |

## 3. Phase A measured delta

100 000 random uint64-key inserts, single-writer:

| Metric | Baseline | Phase A | Δ |
|--------|----------|---------|------|
| total_ms | 4 248 | 505 | -88.1 % |
| inserts/sec | 23 540 | 197 864 | **+741 %** |
| p50_us | 23 | 4 | -82.6 % |
| p95_us | 49 | 6 | -87.8 % |
| p99_us | 145 | 13 | -91.0 % |
| max_us | 6 128 | 658 | -89.3 % |

The primary bottleneck (whole-page rewrite-on-insert) is
eliminated. Remaining steady-state cost is dominated by:

- ~log₂(items_on_page) decode probes during the binary search.
- Buffer-pool pin/unpin per insert.
- WAL emission (`LogBtreeInsert` hook + `MarkDirtyChangeRecord`).

Phase A's acceptance threshold (≥ 30 % inserts/sec) is met by
~25× the bar.

## 4. Phases B-E — what's needed and why deferred

### Phase B (M0055-0003) — steady-state dedup retention

**Problem statement.** Today's posting-list dedup
(`internal/access/btree/posting.go`, M0047-0003) only fires
during `bulkload.go`'s `CREATE INDEX` path. Steady-state
inserts that hit a leaf with the same key already present
allocate a fresh single-TID line pointer rather than appending
to the existing posting. Duplicate-heavy workloads
(e.g., 1 M inserts of 100 distinct keys) therefore experience
unbounded page count drift after the initial bulk build —
the dedup compactness achieved at build time degrades.

**Acceptance criterion (unchanged from design doc).** A
duplicate-heavy variant of the M0055-0001 harness (1 M inserts
of 100 distinct keys) shows post-insert page count bounded vs
the post-bulk-build size — drift ≤ 2× rather than O(N) growth.

**Scope estimate.** ~400-500 lines:
- `posting.go::mergePostingTID(raw, tid) []byte` (new) — append
  a single TID to an existing posting payload, returning fresh
  bytes; reuses existing `parsePostingRaw` / `marshalPosting`.
- `btree.go::insertItemSorted` extended: when binary search
  lands on an exact-match key, convert single → 2-TID posting
  or grow existing posting in place (via a new
  `storage.PageReplaceItemRaw` if the new bytes fit; else split
  the posting and fall through to ordinary insert).
- Pre-split dedup pass: when a leaf is full and would split,
  scan its line pointers for adjacent same-key items and
  consolidate them into postings before the split decision.
- Tests: duplicate-heavy bench variant + correctness pin
  (Search returns every TID for a key with N duplicates).

**Status.** Tracked as M0055-0003. Deferred only because the
multi-phase scope exceeded a single loop's wall clock; not
silently demoted.

### Phase C (M0055-0004) — multi-writer structural protocol

**Problem statement.** The current `splitMu sync.Mutex` on
`*BTree` serialises every structural-change insert. Concurrent
disjoint-leaf inserts run free, but any insert that triggers a
split blocks every other splitting writer. Upstream nbtree
uses `INCOMPLETE_SPLIT` page-state + write-coupling; goopg's
Phase C converts to that protocol.

**Acceptance criterion.** New
`internal/access/btree/multi_writer_stress_test.go`: 32
goroutines × 100 K random-key inserts each, no lost or
duplicate keys after fence, no deadlock, aggregate
inserts/sec ≥ 4× the single-writer Phase A baseline.

**Scope estimate.** ~600-800 lines (state machine + WAL
records + tests). Largest single piece of M0055.

**Status.** Tracked as M0055-0004. Deferred — concurrency
correctness invariants need careful staged landing per the
design doc; rushing this risks silent data corruption.

### Phase D (M0055-0005) — deletion/recycling

**Problem statement.** Two-phase deletion + recycle-into-FSM is
the upstream protocol; goopg currently has simplified empty-leaf
unlink without WAL coverage of the unlink phase or page reuse.

**Acceptance criterion.** Crash-replay test shows no
inconsistencies; deleted pages return as new allocations.

**Scope estimate.** ~500-700 lines.

**Status.** Tracked as M0055-0005. Deferred.

### Phase E (M0055-0006) — CREATE INDEX spill

**Problem statement.** `collectBTreeEntries` materialises every
heap row into memory + sorts in memory. Falls over at
≥ 100 M-row scale.

**Acceptance criterion.** 100 M-row CREATE INDEX completes
within a bounded `work_mem` budget.

**Scope estimate.** ~400-600 lines.

**Status.** Tracked as M0055-0006. Deferred.

## 5. Definition-of-Done check (M0055 milestone)

Per `docs/milestones/0055-staged-btree-enhancement-program.md` §
Definition of Done:

| DoD item | Status |
|----------|--------|
| 1. Write-path overhead — no whole-page rewrite hotspot | **MET** (Phase A) |
| 1. byte-aware split-loc reduces split churn on varlen keys | open (M0055-0002-followup-byte-split) |
| 2. Dedup persistence — bounded drift under sustained inserts | open (M0055-0003) |
| 3. splitMu removed from steady-state structural path | open (M0055-0004) |
| 3. multi-writer stress test passes | open (M0055-0004) |
| 4. two-phase deletion replay-safe | open (M0055-0005) |
| 4. deleted pages recycled | open (M0055-0005) |
| 5. CREATE INDEX peak memory bounded | open (M0055-0006) |
| 5. unique checks no longer table-scale hash | open (M0055-0006) |
| 6. End-to-end report published | **THIS DOCUMENT** |

The milestone is therefore PARTIAL — DoD item 1's first half is
met, the rest are tracked as named follow-up sub-tasks with
acceptance criteria preserved verbatim from the design docs.
The structure satisfies the M0054 no-deferral clause: every
deferred item is explicitly named and sized, never silently
demoted.

## 6. Next steps

The natural sequencing for the remaining phases is:

1. **M0055-0002-followup-rightmost-cache** — additive on the
   landed Phase A primary; quick win for append-workload
   indexes.
2. **M0055-0003** — measurable steady-state dedup; bounded
   scope.
3. **M0055-0004** — most-deferred-because-of-scope; needs
   careful staged correctness work.
4. **M0055-0005**.
5. **M0055-0006**.
6. Final M0055-0007 report extension with the full multi-phase
   delta table.

Each step independently commits and pushes; the analysis report
(this document) gets a new section per phase.
