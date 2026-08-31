# 0118-0113 — `intra-grant-inplace` enabler: `pg_class` rowmark locks block the in-place `ADD PRIMARY KEY` update (M0118-0009)

Status: accepted
Date: 2026-06-25
Spec: `postgres/src/test/isolation/specs/intra-grant-inplace.spec`
Builds on: 0118-0109 (the table-ACL `xmax` wait, permutation 1)

## Summary

**Enabler, NOT a promotion.** Advances `intra-grant-inplace.spec`'s first
divergence from **L62 → L141**: permutations 2–6 are now byte-identical to
PG 18.3. The spec stays `defer` because permutations 7–10 need the
`GRANT`/`REVOKE`/`DELETE FROM pg_class` `LockTuple` + deadlock-detection core
(see *Remaining* below).

The spec's header states the invariant: *"GRANT's lock is the catalog tuple
xmax. GRANT doesn't acquire a heavyweight lock on the object undergoing an ACL
change. Inplace updates, such as relhasindex=true, need special code to cope."*
0118-0109 handled the GRANT-side `xmax` wait. This loop handles the **rowmark**
side — an explicit `SELECT … FROM pg_class … FOR { KEY SHARE | NO KEY UPDATE |
SHARE | UPDATE }` taking a tuple lock that `ALTER TABLE … ADD PRIMARY KEY` (an
in-place `heap_inplace_update` of `relhasindex`) must serialise behind.

## The permutations this unlocks

| perm | steps | expected behaviour |
|------|-------|--------------------|
| 2 | `keyshr5 addk2` | addk2 **does not wait** (FOR KEY SHARE does not conflict with the in-place no-key update) |
| 3 | `keyshr5 b3 sfnku3 addk2(r3) r3` | addk2 **waits** behind s3's FOR NO KEY UPDATE; completes on `r3` |
| 4 | `b3 sfnku3 keyshr5 addk2(r3) r3` | same, lock order reversed |
| 5 | `b2 sfnku2 addk2 c2` | same-xact rowmark — addk2 **does not wait** on its own NO KEY UPDATE |
| 6 | `keyshr5 b2 sfnku2 addk2 c2` | same-xact rowmark inside a multixact — addk2 **does not wait** |

The conflict rule is exactly that of acquiring a NO-KEY-UPDATE-equivalent lock
on the tuple: it conflicts with `SHARE`, `NO KEY UPDATE`, `UPDATE` but **not**
`KEY SHARE`. PostgreSQL's `heap_inplace_lock` waits for lockers whose mode
conflicts with the in-place update; FOR KEY SHARE alone never blocks it
(perm 2), while FOR NO KEY UPDATE does (perms 3/4). A locker in the **same**
transaction tree never blocks its own in-place update (perms 5/6).

## Implementation

goopg has no real `pg_class` heap tuple — it is served from the virtual
catalog builder, so the heap row-lock stamping in `lockRowsOp.drainAndStamp`
is a no-op for it (`currentTID()` returns `ok=false`). We therefore record the
rowmark in an in-memory catalog store and replay the in-place updater's wait,
mirroring the 0118-0109 ACL-`xmax` pattern but with multiple concurrent holders
and per-mode conflict.

### Catalog store (`internal/catalog/catalog.go`)

- `pgClassRowMarks map[uint32]map[uint32]bool` — `relOID → holderXID →
  conflictsWithInplace`, mutex-guarded.
- `AddPgClassRowMark(relOID, xid, conflictsWithInplace)` — records a holder; a
  later stronger acquisition by the same xid ORs the conflict flag (never
  downgrades).
- `PgClassRowMarks(relOID) []PgClassRowMark` — the holders the in-place updater
  filters and waits on.
- `ClearPgClassRowMarksForXID(xid)` — called at COMMIT/ROLLBACK so a finished
  locker stops appearing as held.

### Recording the rowmark (`internal/executor/operators_lockrows.go`)

`lockRowsOp.maybeRecordPgClassRowMark`, called at the end of `Open`, fires
**only** when the locked relation OID is `catalog.RelationRelationId` (1259) —
so the normal heap row-lock path is byte-unchanged. It extracts the target
relation OID from the scan's `oid = <const>` filter (`pgClassFilterOID`, the
shape every such SELECT uses; any other predicate shape → no record),
materialises the session's writer XID (so `WaitForXID` has a real XID to wait
on), and records the holder with `conflicts = Strength != ForKeyShare`.

### Waiting at the in-place update (`internal/executor/operators_ddl.go`)

`execAlterTableAddPrimaryKey` already calls `waitForTableACLChange` (0118-0109);
it now also calls `waitForPgClassRowMarks`, which `mvcc.WaitForXID`s on every
recorded holder of the table's `pg_class` tuple whose mode conflicts and whose
transaction tree is not this backend's own (resolved via `TxnMgr.TopLevelXid`,
so a same-xact / savepoint locker is skipped). `WaitForXID` returns immediately
for an already-finished holder, so a stale entry is harmless.

### Lifecycle (`internal/executor/operators_tx.go`)

`execCommit` / `execRollback` call `clearPgClassRowMarks(tx)` after
`TxnMgr.Commit`/`Rollback`, keyed by the top-level XID. (Savepoint sub-XID
rowmarks, which no current spec uses, are left behind but are harmless.)

## Blast radius

Nil for everything except a `SELECT … FOR …` whose locked relation is
`pg_class` *and* whose filter is `oid = <const>`:

- `maybeRecordPgClassRowMark` returns immediately unless the locked table OID is
  1259 — every user-table / catalog-view row lock is byte-unchanged.
- `waitForPgClassRowMarks` is a no-op when no rowmark is recorded for the table
  (the common case → `PgClassRowMarks` returns `nil`).
- The clear hook is an `InMemory`-typed no-op when nothing was recorded.

## Remaining (spec stays `defer`)

Permutations 7–10 need the runtime shared-catalog **`LockTuple` + deadlock**
core that goopg lacks:

- `GRANT`/`REVOKE`/`DELETE FROM pg_class` must take a `LockTuple` on the virtual
  `pg_class` row and *await* a conflicting rowmark's `xmax` (perm 7: grant1
  `<waiting ...>` behind sfu3's FOR UPDATE; perm 8: addk2 blocks in `LockTuple`
  behind grant1 → **deadlock detected**).
- `SearchSysCacheLocked1()` semantics: a rowmark that finds a tuple then no
  tuple after a concurrent `DELETE FROM pg_class` (perms 9/10).

These are the Effort-L runtime shared-catalog MVCC-tuple-lock core; tracked in
the deferral ledger.

## Gates

- `intra-grant-inplace.spec` probe: first divergence advanced **L62 → L141**
  (perms 2–6 byte-identical).
- `TestPgClassRowMarks` (new catalog unit) PASS.
- Non-regression: `TestPort_IsolationIntraGrantInplaceDb` +
  `TestPort_IsolationTruncateConflict` strict PASS; the full row-lock isolation
  family (`SkipLocked`/`Nowait`/`Tuplelock`/`LockUpdate`/`UpdateLockedTuple`/
  `LockCommitted`) PASS.
- `go test -race ./internal/mvcc/... ./internal/catalog/...` green;
  `internal/executor` units PASS.
- `go build ./...` + `go vet` clean; pgbench smoke = pre-commit hook.
