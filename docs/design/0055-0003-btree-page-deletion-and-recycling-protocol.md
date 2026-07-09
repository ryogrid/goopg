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

### 2.5 Internal-page sibling-relink cross-connection race

- **Fixed 2026-07-09** (M-NIGHTLY AI-20260709-010336-082 follow-up,
  `internal/access/btree/btree_vacuum.go`): `unlinkEmptyInternalPage`
  (WAL path) and `unlinkEmptyInternalPageFPI` (FPI fallback) had the
  identical stale-sibling-relink bug already fixed for LEAF pages in
  `unlinkEmptyLeaf`/`unlinkEmptyLeafFPI` (§2.4's sibling counterpart,
  same root cause as the pgbench-reopen thread's block-678 finding)
  — both computed `leftLive`/`rightLive` via an unlocked `liveSibling`
  pre-pass in `maybeCascadeEmptyInternal`/`unlinkEmptyInternalPage`,
  then wrote those captured values verbatim into the sibling pages.
  `bt.splitMu` (held across the whole cascade via the caller,
  `unlinkEmptyLeaf`) only serialises within one `*BTree` Go-instance —
  each backend opens its own instance per statement — so a concurrent
  Insert-driven split on a DIFFERENT connection's instance for the
  SAME relation could splice a new live internal page into the exact
  chain segment between the walk and the write, and this cascade's
  later blind write would stomp that splice back to the stale
  neighbour. Fixed by re-deriving the live neighbour via a fresh
  `liveSibling` walk from the sibling's CURRENT on-disk link, executed
  INSIDE the same `pinW` hold that performs the write — mirrors §2.4's
  leaf-level fix exactly. Regression test:
  `TestUnlinkEmptyInternalPagePreservesConcurrentSplice`
  (`internal/access/btree/btree_vacuum_internal_race_test.go`) —
  deterministically simulates the race (no goroutines needed) by
  capturing a real internal page's live prev/next, splicing a
  synthetic live page in between, then invoking the unlink with the
  stale pre-splice prev/next; confirmed non-vacuous via `git stash`
  (fails pre-fix with the exact stomp symptom).
- **New gap found while fixing the above, NOT yet fixed (deferred, see
  `.ralph/deferral_ledger.md` 2026-07-09):** `applyParentDownlinkRemoval`
  (shared by both the leaf and internal-page unlink paths) removes the
  parent's downlink purely by a previously-captured slot INDEX
  (`resolveParentDownlink`'s / `findDownlinkSlotInParent`'s return
  value), with no re-validation at write time that the item still at
  that index is actually the intended child's downlink. This is the
  SAME index-drift race AI-20260706-201855-001 fixed for the
  intra-instance case (there, `splitMu` closed the gap because both
  racing operations shared one `*BTree` instance) — but for a
  DIFFERENT connection's instance racing via a concurrent split on
  the SAME parent page, `splitMu` provides no protection at all (same
  limitation as §2.5 above), so the index can still drift cross-
  connection and `applyParentDownlinkRemoval` will delete an unrelated
  live child's downlink instead of the intended one.

## 3. Tests

- Delete-heavy and vacuum-heavy workloads with repeated cycles.
- Crash points between mark and unlink.
- Recycle/reuse verification (physical page reappearance in later growth).

## 4. Acceptance

- No orphaned-live-path corruption across replay tests.
- Predictable index size stabilization under churn.
- Reuse of deletable pages confirmed by functional and storage-level checks.