# B-tree Page Deletion and Recycling Protocol (M0055)

| field      | value |
|------------|-------|
| status     | draft |
| date       | 2026-05-06 |
| supersedes | — |

## 1. Problem

Current empty-leaf handling is intentionally simplified and does not yet provide full two-phase deletion lifecycle, replay-oriented unlink semantics, or robust page recycling policy.

## 2. Design

### 2.1 Two-phase deletion

- Phase A: mark candidate leaf/subtree state for deletion intent.
- Phase B: unlink sibling/parent references under protocol-safe lock order.

### 2.2 WAL and replay

- Add dedicated deletion-phase record kinds.
- Replay must preserve reader correctness at every intermediate state.
- Interrupted deletion must be resumable by subsequent maintenance pass.

### 2.3 Recycle policy

- Deleted pages become recyclable only after safety horizon criteria.
- Recycled pages return to free space management and are reused by later growth.

### 2.4 Internal cleanup scope

- Define when internal ancestors are collapsed.
- Preserve root invariants and fast-root metadata consistency.

## 3. Tests

- Delete-heavy and vacuum-heavy workloads with repeated cycles.
- Crash points between mark and unlink.
- Recycle/reuse verification (physical page reappearance in later growth).

## 4. Acceptance

- No orphaned-live-path corruption across replay tests.
- Predictable index size stabilization under churn.
- Reuse of deletable pages confirmed by functional and storage-level checks.