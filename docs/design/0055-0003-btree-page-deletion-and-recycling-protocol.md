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

- **Implemented 2026-07-07** (M-NIGHTLY AI-20260706-201855-001,
  `internal/access/btree/btree_vacuum.go`): a non-root internal page is
  collapsed the moment a downlink removal drops its own item count to
  0. `maybeCascadeEmptyInternal` is invoked after every leaf unlink
  (from both `unlinkEmptyLeaf` and `unlinkEmptyLeafFPI`) with the
  `ancestorPath` (root..parent, captured before the parent's item
  count could reach 0 and its key-based lookup became impossible) and
  loops upward: it unlinks the empty internal page from its own parent
  via `unlinkEmptyInternalPage`/`unlinkEmptyInternalPageFPI` (relink
  live level-siblings, remove the downlink one level up, flag
  `BTDeleted`, recycle the block — mirroring `unlinkEmptyLeaf`'s shape
  and reusing the same WAL emitter/replay records, which are page-
  structure-agnostic), then repeats one level higher if that removal
  also emptied the grandparent. Stops at the root (root emptiness is a
  distinct, pre-existing case: `VacuumIndexPages`'s `isTreeEmpty`/
  `resetToEmptyRoot` collapse the whole tree to a single empty leaf
  root once every leaf is gone).
- Before this, an internal page vacuumed down to 0 items stayed
  linked-but-contentless in the tree; any later descent whose
  separator range still routed through it hit
  `findChildBlockDirect`'s `count == 0` guard and raised "btree: empty
  internal page" — reproducible via repeated small-table
  insert/delete/vacuum churn (e.g. `pgbench_branches` at scale=10, no
  crash needed).
- **Known gap (deferred, see `.ralph/deferral_ledger.md` 2026-07-07):**
  the cascade is not crash-safe across its own recursion levels — it
  has no phase-1 (`BTHalfDead`-style) marker of its own, unlike leaf
  deletion's two-phase protocol (§2.1), so a crash between cascading
  level N and N+1 can leave level N's now-empty page exposed to the
  same bug one level higher. `CompleteDeferredDeletions` does not yet
  scan for this case. Root invariants and fast-root metadata are
  unaffected by this gap (cascading never touches the root itself).

## 3. Tests

- Delete-heavy and vacuum-heavy workloads with repeated cycles.
- Crash points between mark and unlink.
- Recycle/reuse verification (physical page reappearance in later growth).

## 4. Acceptance

- No orphaned-live-path corruption across replay tests.
- Predictable index size stabilization under churn.
- Reuse of deletable pages confirmed by functional and storage-level checks.