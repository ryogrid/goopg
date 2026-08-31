# 0021-0011 — Tuple-Level Locking: Multi-Holder FOR SHARE via Lockmgr Modes

**Status:** accepted (step 4 — multi-holder FOR SHARE without
MultiXact infrastructure; persistent xmax bookkeeping deferred)
**Milestone:** [0021 — Pessimistic Row Locking](../../milestones/0021-pessimistic-lock-select-for-update.md)
(deferred follow-up: tuple-level locking on top of M0012).
**Spans seam:** lockRowsOp tuple-tag mode selection, lockmgr
mode-conflict matrix.
**Cross-links:**
[0021-0007](0021-0007-tuple-locking-producer-wiring.md)
(producer wiring),
[0021-0008](0021-0008-tuple-locking-blocking-enforcement.md)
(blocking enforcement),
[0012-0001](0012-0001-lock-manager-architecture.md) (lockmgr
core).

## Context

Steps 2a/2b/2c/2d gave SELECT FOR UPDATE working tuple-level
blocking. SELECT FOR SHARE was wired through the same
`stampLock` path, but with a hidden bug: lockRowsOp acquired
`ExclusiveLock` on the tuple tag regardless of strength. Two
concurrent SELECT FOR SHARE sessions on the same row
serialised — the second blocked waiting for the first.

This slice fixes that to upstream's "multiple FOR SHARE
holders coexist" semantics. The natural next step would be to
introduce a MultiXact infrastructure (the upstream way of
recording multiple holders per tuple), but the lockmgr's
existing mode-conflict matrix already supplies what we need at
the lockmgr level: `RowShareLock` is compatible with itself
and conflicts with `ExclusiveLock`. By picking the right mode
for each strength, multi-holder FOR SHARE works without
MultiXact.

## Lockmgr mode mapping

```go
func (o *lockRowsOp) tupleLockMode() lockmgr.Mode {
    if o.lockStrength == storage.HeapXmaxShrLock || o.lockStrength == storage.HeapXmaxKeyShrLock {
        return lockmgr.RowShareLock
    }
    return lockmgr.ExclusiveLock
}
```

| SQL form         | HeapXmax* bit set | tuple-tag lockmgr mode |
| ---------------- | ----------------- | ---------------------- |
| `FOR UPDATE`     | HeapXmaxExclLock  | ExclusiveLock          |
| `FOR SHARE`      | HeapXmaxShrLock   | RowShareLock           |
| `FOR KEY SHARE`  | HeapXmaxKeyShrLock| RowShareLock           |
| UPDATE / DELETE  | (foreign-lock branch in scanMatching) | ExclusiveLock |

Per the lockmgr's `conflictTab`:

- RowShareLock conflicts with ExclusiveLock + AccessExclusiveLock only.
- ExclusiveLock conflicts with everything except itself? Actually
  `bit(RowShareLock) | bit(RowExclusiveLock) | … | bit(ExclusiveLock) | …` — it conflicts with itself too (one writer at a time) and with all shared modes.

So:

- Two FOR SHARE on same row: each takes RowShareLock; compatible
  → both proceed concurrently.
- FOR SHARE + FOR UPDATE on same row: ExclusiveLock conflicts
  with RowShareLock → second waits.
- Two FOR UPDATE on same row: ExclusiveLock self-conflicts →
  second waits.
- UPDATE / DELETE encountering a FOR SHARE-locked tuple:
  ExclusiveLock blocks until all RowShareLock holders release.

Transaction-scoped release via `LockMgr.ReleaseAll(backendID)`
(the existing dispatch.go hook) cleans every holder up at
commit/abort. No MultiXact registry required.

## On-page xmax bookkeeping

The on-page `xmax` field can only hold one xid. With multiple
FOR SHARE holders, the second session's stamp overwrites the
first's xmax bytes. This is harmless because:

- Visibility: `mvcc.TupleVisible` short-circuits when
  `HeapXmaxLockOnly` is set, ignoring the actual xmax value.
- Lockmgr: each holder is tracked separately by backend ID;
  ReleaseAll cleans them up regardless of the on-page xmax.
- WAL: each lock emission records its own (xid, lockStrength)
  via `xl_heap_lock`; recovery re-applies them in order.

The remaining gap — accurate post-crash reconstruction of "who
holds this row's lock" — is what MultiXact would solve. v0
defers it because (a) the lockmgr's transaction-scoped state is
in-memory only, so post-crash all locks are lost anyway, and
(b) the on-page LOCK_ONLY bit lets readers continue without
needing to know the locker's identity.

## Tests

`internal/executor/operators_lockrows_test.go`:

- `TestForShareCompatibleMultipleHolders` — NEW. Two sessions
  concurrently take FOR SHARE on the same row; second succeeds
  without blocking. Verifies both backends appear as holders
  in `lm.Holders(tag)`. Then a third session UPDATE blocks
  waiting for both, and unblocks only after BOTH holders
  release. Pins the multi-holder semantics + the
  conflict-blocking semantics.

Full `go test ./...` green; race-mode targeted runs across
executor + lockmgr all green.

## Out of scope

- True MultiXact infrastructure (multixact id minting,
  xl_multixact_create WAL, persistent membership map). v0
  achieves multi-holder semantics at the lockmgr level
  without it.
- Lock-strength promotion semantics: a session that holds
  FOR SHARE then issues FOR UPDATE on the same row would
  upgrade its hold to ExclusiveLock. The lockmgr supports
  this via Acquire being idempotent within a backend (a
  backend already holding RowShareLock can also grant
  itself ExclusiveLock); the test suite doesn't yet
  exercise it.
- Persistent on-page MultiXact identifier in xmax — needed
  for crash-recovery accuracy when multiple FOR SHARE
  holders existed at crash time.
- Tuple-level NOWAIT/SKIP LOCKED beyond what step 0021-0004
  already gates at the relation level.
- Streaming per-row stamping + pg_locks-style introspection.
