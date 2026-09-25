# M0143-0010 — index probes must resolve the heap update chain

Status: implemented (this loop).
Task: `.ralph/fix_plan.md` M0143-0010. Resolves M-NIGHTLY items
AI-20260917-004357-008 / -009 / -012 (`fk-contention`, `fk-deadlock`,
`update-locked-tuple` isolation specs).

## Bug

A non-key UPDATE in goopg is HOT: the new version lands on the same heap
page and every index entry keeps referencing the chain **root** line
pointer. Once the updater commits, the root member is dead and the live
row sits deeper in the same-page `t_ctid` chain. Any index probe that
fetches `ptr.Offset` verbatim and applies its predicate to only that
tuple reports a false no-match for a perfectly live row.

Concrete instances found this loop:

- `scanIndexForFKMatch` (`internal/executor/operators_fk.go`) — the
  index-accelerated FK existence probe added by `a53c5b807`
  (M0142-0003g). Regression window
  `48cf54f8..1b54b00f` between nightlies `20260916-035206` (all three
  specs PASS) and `20260917-004357` (all FAIL): `INSERT INTO child`
  raised 23503 after a committed non-key parent UPDATE, or failed to
  wait on an in-flight parent update. Live repro on a scratch cluster:
  `UPDATE foo SET b='y'; INSERT INTO bar VALUES (42)` → spurious
  `bar_a_fkey` violation.
- `uniqueCheckWithWait`'s `scanOnce` (`internal/executor/
  operators_storage.go`) — the plain `INSERT`/`UPDATE` unique-enforcement
  probe, pre-existing and worse: `CREATE TABLE uq(a int PRIMARY KEY,
  b text); INSERT INTO uq VALUES (7,'x'); UPDATE uq SET b='y'; INSERT
  INTO uq VALUES (7,'dup')` **succeeded** — duplicate PRIMARY KEY rows.
- `findInProgressConflictKey` and `probeSpeculativeConflict`
  (`internal/executor/operators_upsert.go`) — the upsert arbiter's
  in-flight-conflict scan and the post-Phase-B speculative recheck.
- `exclusionCheckOnce` (`operators_storage.go`) and
  `recheckDeferredExclusionEq` (`internal/executor/deferred_exclusion.go`)
  — btree exclusion probes with the same raw-pointer fetch.

Already-correct siblings, unchanged: the `followHOTChain` /
`followHOTChainNoCopy` probes (regular index scans, index-only scans,
update-via-index scans, the `:673` upsert arbiter arm — M0100-0005
Bug A), `resolveDeferredUniqueChainTail` (deferred-unique recheck,
M0134-0005e), and the exact-ctid refetches (EPQ, rowmark, catalog
`oldTID` verification), which deliberately name one physical version and
must NOT walk.

## Design

New shared iterator `eachHeapChainMember`
(`internal/executor/operators_index.go`, next to `followHOTChainNoCopy`):

```go
func eachHeapChainMember(page storage.Page, startSlot uint16,
    fn func(t storage.HeapTuple, slot uint16) bool)
```

- `ItemIDRedirect` stubs are followed transparently; every
  `ItemIDNormal` member is yielded to `fn` in chain order.
- `fn` returns `false` to stop; `true` continues to the member's HOT
  successor (`IsHotUpdated` → `CTID.Offset`) when one exists.
- Terminates on a missing slot, a non-normal non-redirect item, a
  self-referencing link, or `MaxHeapTuplesPerPage` hops — the same bound
  `followHOTChain` uses (M0131-S32).

Each probe keeps its own predicate — the iterator only fixes *which
tuples the predicate sees*:

| probe | per-member predicate |
|---|---|
| `scanIndexForFKMatch` | first member visible under `TupleVisibleSubxact`; that member's slot feeds `fkPendingOutcome` (whose `pending.slot` coordinate stays exact for `epqChainCheckMovedPartition`) |
| `uniqueCheckWithWait.scanOnce` | in-flight other-xact xmin → wait (`inflightXmin`); `isLiveForUniqueCheck` → `liveConflict`, `conflictPtr` = member slot (SSI tuple walk gets the committed member's location) |
| `findInProgressConflictKey` | the existing Case 1/2/3 in-flight checks, per member |
| `probeSpeculativeConflict` | skip self-xmin; `isLiveForUniqueCheck`; decode the matched member |
| `exclusionCheckOnce` | `isLiveForUniqueCheck` per member |
| `recheckDeferredExclusionEq` | chain = one logical row: count once if ANY member is live — never per member, or an in-flight update (still-live root + in-flight successor) double-counts one row into a false 23P01 |

Per-member evaluation in chain order (rather than tail-only resolution
like `resolveDeferredUniqueChainTail`) is load-bearing for the wait
paths: a chain root carrying an in-flight xmax must still trigger the
wait/conflict decision even though a successor exists — matching what
the raw-root read did, extended to the members the raw read never saw.

## Verification

- `TestFKInsertAfterParentHotUpdate` (operators_fk_test.go): parent
  PK + two non-key UPDATEs → child INSERT of the key must succeed, a
  non-existent key must still raise 23503. Verified red→green by
  stashing the production change.
- `TestUniqueInsertAfterHotUpdate` (insert_unique_constraint_test.go):
  duplicate key after non-key UPDATEs → 23505; distinct key succeeds.
  Verified red→green the same way.
- Isolation specs `fk-contention`, `fk-deadlock`, `update-locked-tuple`:
  all PASS (were FAIL at `ad778446e`).
- Gates: executor package suite, units, tpch-spotcheck, tpcds-sf025
  sweep, tpch-acceptance-arm.
