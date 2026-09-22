# B-tree Multi-Writer Split Protocol (M0055)

| field      | value |
|------------|-------|
| status     | draft |
| date       | 2026-05-06 |
| supersedes | — |

## 1. Problem

Current split-path structural updates rely on simplified serialization. This limits write scalability and leaves gaps compared to upstream nbtree split-completion lifecycle.

## 2. Goals

- Enable safe concurrent writers for non-conflicting insert paths.
- Provide explicit incomplete-split lifecycle and completion.
- Maintain reader correctness via existing move-right/high-key semantics.

## 3. Protocol

### 3.1 Incomplete split marker

- Introduce explicit page state to mark split not fully propagated upward.
- Writers encountering such state must run split-completion routine before proceeding.

### 3.2 Lock ordering

- Use deterministic sibling/parent lock ordering to avoid deadlocks.
- Keep latch hold times minimal and bounded.

### 3.3 Parent insertion completion

- Ensure parent downlink insertion and marker clear are atomic from protocol perspective.
- Define retry/idempotency rules for crash/recovery and concurrent writer races.

### 3.4 Sibling invariant restoration

- Update both forward and backward sibling metadata where required by protocol.
- Add validation checks in maintenance paths.

## 4. Transition Plan

- Stage 1: add metadata and completion routine while still allowing compatibility fallback.
- Stage 2: remove splitMu as steady-state structural gate.
- Stage 3: enforce completion protocol across all writer paths.

### 4.1 Cross-handle structural gate \(M0122-0010, 2026-09-22\)

The pre-Stage-2 gate now lives in `storage.Pool`, keyed by the full
`RelFileNode`. Every ordinary `Open`, `Create`, and bulk-build BTree handle
obtains the same mutex for one relation. The three existing structural critical
sections \(split insert, deferred split completion, and vacuum unlink\) use
that shared mutex; unrelated relations remain independent.

This closes the correctness hole where each backend's separate BTree instance
had its own `splitMu`, allowing a split on one connection to interleave with a
structural mutation on another. It deliberately does not claim Stage 2:
removing the gate still needs the buffer-pool reader/writer guarantee described
above. `TestOpenedHandlesShareStructuralLock` is the pin: it asserts both handles
receive the SAME mutex and that one holding it blocks the other, and it fails
without the change \("opened handles do not share a relation structural
lock"\).

`TestOpenedHandlesConcurrentlyInsertAcrossSplits` drives 900 distinct keys
through two independently opened handles and verifies every pointer after real
leaf splits, but it is a **functional smoke test, not a race pin**, and the
file now says so. Measured 2026\-09\-22: with `sharedSplitMu` removed from the
constructors it still passes under `-race -count=3`, because its per\-handle
keys ascend and rightmost\-page splits stay consistent under the buffer\-pool
page locks alone. **No existing test reproduces the interleaved
structural\-change failure itself**; writing one is open work, and the gate is
justified by the structural argument \(two handles, two mutexes, one tree\)
rather than by a failing reproducer.

**Known limitation.** `Pool.BTreeStructuralLock`'s map has no eviction, so one
mutex per relation ever opened accumulates for the process lifetime. Pruning it
in `InvalidateRel` is the obvious place and is deliberately NOT done: a
concurrent holder releasing a mutex that has already left the map, followed by
a relfilenode reuse, would hand the next handle a fresh mutex and defeat the
gate for that window. Ledgered.

## 5. Tests

- Multi-writer split stress with random keys.
- Concurrent split on adjacent leaves.
- Crash/restart during split propagation.
- Deadlock and livelock regression tests.

## 6. Acceptance

- No structural corruption under stress/recovery tests.
- Writer throughput scales beyond single structural writer baseline.
- splitMu no longer governs normal structural write flow.
