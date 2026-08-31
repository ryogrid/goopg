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

## 5. Tests

- Multi-writer split stress with random keys.
- Concurrent split on adjacent leaves.
- Crash/restart during split propagation.
- Deadlock and livelock regression tests.

## 6. Acceptance

- No structural corruption under stress/recovery tests.
- Writer throughput scales beyond single structural writer baseline.
- splitMu no longer governs normal structural write flow.