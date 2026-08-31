# Transam (clog / mvcc / snapshot / ssi / subxact) — Bug Review 2026-08-31

Files: clog.go, clog_bufferpool.go, clog_statuscache.go, combocid.go, manager.go,
partition_detach_epoch.go, predlock.go, procarray.go, snapshot.go, ssi.go,
ssi_conflict.go, ssi_precommit.go, subxact.go, subxact_slru.go,
subxact_visibility.go, visibility.go, xidgen.go, multixact/multixact.go,
multixact/store.go, control/control.go, control/pgcontrol.go

Findings count: 6

---

### `visibility.go:TupleVisible` — HeapXmaxCommitted hint-bit branch returns `false` unconditionally (sibling-path disagreement with TupleVisibleSubxact)
- **Bug**: When `HEAP_XMAX_COMMITTED` is set, `TupleVisible` returns `false` (invisible) without consulting the snapshot:
  ```go
  if h.Infomask&storage.HeapXmaxCommitted != 0 {
      return false
  }
  ```
  The subxact twin `TupleVisibleSubxact` (subxact_visibility.go:398-403) correctly returns `!snap.SeesCommittedXID(effXmax)`, and its own comment explains why: the hint bit only says "xmax committed NOW", it says nothing about whether *this snapshot* predates that commit. If the snapshot was taken while xmax was still in-progress, the tuple must remain visible. PG's `HeapTupleSatisfiesMVCC` (heapam_visibility.c:1144-1153) does exactly the snapshot check even in the committed-hint branch: `if (XidInMVCCSnapshot(xmax, snapshot)) return true;`.
- **When it triggers**: A REPEATABLE READ / SERIALIZABLE transaction captures a snapshot while the deleter is in-progress; a later scan (by any session) sets the persistent `HEAP_XMAX_COMMITTED` hint bit on the page; the RR/SSI transaction then re-scans the page (later statement, or EPQ re-check) and `TupleVisible` wrongly reports the tuple deleted. Sibling paths documented as "must agree" (pattern_sibling_paths_must_agree) diverge, so the two call sites (operators_storage.go:350,702,865 / operators_index.go:59,103 vs the seq-scan path at :1999,7754) can disagree on the same tuple.
- **Fix**: Mirror the subxact path: `return !snap.SeesCommittedXID(effXmax)` instead of `return false`.
- **Severity**: high (correctness bug; wrong visibility for snapshots that predate a concurrent delete)

### `manager.go:AssignXID` — non-atomic read-check-allocate-store allows XID leak/double-assignment under concurrency
- **Bug**: `AssignXID` does `if existing := s.xid.Load(); existing != 0 { return }` and then `m.xidgen.Allocate()` + `s.xid.Store(...)` without any lock/CAS tying the check to the store. Two goroutines calling `AssignXID` concurrently for the same handle can both read `xid == 0`, both `Allocate()` distinct XIDs, and both `Store()`; one XID is clobbered and silently leaked (consumed from the generator, recorded nowhere, never stamped in CLOG).
- **When it triggers**: Any code path that can materialise a writer XID concurrently for the same transaction handle (e.g. parallel DML fragments sharing one `Transaction`, background workers, or future parallelism on one backend slot). The doc comment itself warns callers to refresh `Transaction.XID` with the return value, but a race makes the two callers disagree.
- **Fix**: Use a compare-and-swap / single-flight so only one allocator proceeds: e.g. re-check under the store, or do the allocation+store under a per-slot mutex and have the loser adopt the winner's stored XID.
- **Severity**: medium (needs concurrent use of a single handle; otherwise the allocate-then-store race is latent)

### `manager.go:Begin` — auto-assign path skips isolation-level validation
- **Bug**: The isolation validation `switch iso { case ... default: return error }` is only reached on the explicit-`procNum` branch; the variadic auto-assign branch returns before it, so an invalid `iso` value is silently accepted (and, if it happens to equal `IsolationSerializable`, registers SSI bookkeeping; any other garbage value produces a `Transaction` with an invalid isolation stored into the slot).
- **When it triggers**: Any caller passing an unsupported isolation level without an explicit procNum (internal/test callers). Production passes only valid levels, so impact is low, but the two `Begin` branches should validate identically.
- **Fix**: Hoist the `switch iso` validation to the top of `Begin` so both branches enforce it.
- **Severity**: low

### `manager.go:AcquireConnSlot` — int32 cursor overflow wraps to negative modulo → out-of-range slot index
- **Bug**: `start := m.connSlotCursor.Add(1)` uses an `atomic.Int32`; after 2^31 cumulative acquisitions it wraps negative, and `1 + (start+off)%int32(sz-1)` can become a negative or OOB index into `m.procArray.slots` → panic.
- **When it triggers**: Only after ~2.1 billion connection acquisitions over a Manager lifetime (never in practice; remote/incremental). Note the sibling `WaitForSlotsToCommit` etc. all use int32 slot indexes safely for realistic counts.
- **Fix**: Use `atomic.Uint64` for the cursor, or compute `i := 1 + int((uint64(start)+uint64(off))%uint64(sz-1))` with a uint64 mask.
- **Severity**: low (theoretical long-horizon overflow)

### `clog.go:GetStatus` — nil `pool` dereferences instead of the nil-safe contract used elsewhere
- **Bug**: `c.pool.Load().getStatus(xid)` will panic with a nil-pointer dereference if `GetStatus` is ever called before `EnablePGSLRUMirror` (which installs `pool`). `FlushAll`, `SetFlushWALHook`, `SetFsyncDisabled` all explicitly guard `p == nil`, but `GetStatus` does not.
- **When it triggers**: Out-of-order startup / tests that consult status before the pool is wired; production always calls `EnablePGSLRUMirror` immediately after `OpenCLog`, so the live server is unaffected — but the inconsistency is a latent panic on a code path that is otherwise nil-tolerant.
- **Fix**: Guard with `if p := c.pool.Load(); p != nil { ... }` (treat as Unknown on nil, matching the "unwritten lane faults in as all-zero" contract).
- **Severity**: low

### `multixact/multixact.go:StatusesConflict` — invalid `Status` values index out of bounds (panic)
- **Bug**: `StatusesConflict(held, req)` indexes `statusHWLock[held]` / `statusHWLock[req]`; `statusHWLock` is `[MaxStatus + 1]` (6 entries). `Status` is a `uint8` with 8-bit range, so any status > 5 (e.g. a corrupted on-disk status, or a caller passing a raw byte) causes a runtime index-out-of-range panic. The store validates statuses on `CreateFromMembers`/`Expand`, but `MembersConflict` and `StatusesConflict` are exported and do not validate; `StatusesConflict` is also reached from `MembersConflict` for members that bypass the store's validation.
- **When it triggers**: A corrupted member status reaching the conflict matrix, or any external caller passing an out-of-range `Status`. Defensive bounds-checking is warranted on a uint8-indexed table in a leaf package.
- **Fix**: Clamp or return false for `held > MaxStatus || req > MaxStatus` (or validate in `MembersConflict` before calling).
- **Severity**: low (needs corrupted/out-of-range input; the in-tree paths validate)

---

## Files reviewed with no findings
- **clog_statuscache.go** — pack/lookup layout (valid bit 8, xid bits 32..63, status bits 0..7) is self-consistent; only terminal statuses cached; `invalidate` on truncation is correct.
- **combocid.go** — 1-based combo CID indexing and bounds checks (`int(comboCID) > len(cs.array)`) are correct.
- **partition_detach_epoch.go** — trivial atomic counter, correct.
- **predlock.go** — granularity sentinels, `Covers`, coarsening/promotion logic are consistent.
- **procarray.go** — slot layout/regions consistent with the manager's usage.
- **snapshot.go** — window tests, `HasInProgress`/`HasAborted` (linear vs binary search threshold), `SeesCommittedXID`/`SeesCommittedXIDHinted` ordering are correct.
- **ssi.go** — lifecycle, seq-no stamps, retention/purge overlap test, safe-snapshot wait are correct.
- **ssi_conflict.go** — conflict-edge directionality, covering-tag walk, victim selection match upstream shapes.
- **ssi_precommit.go** — dangerous-structure scan and prepared-pivot handling correct.
- **subxact.go** — stack push/find/release/rollback semantics consistent.
- **subxact_slru.go** — segment/page/offset arithmetic and truncation page-precede logic correct.
- **subxact_visibility.go** — subxact resolution and visibility (except the sibling-path divergence already noted in `visibility.go`) correct.
- **xidgen.go** — atomic allocate/peek/set-next correct.
- **multixact/multixact.go** — lock-mode matrix, `GetUpdateXid`, `HasLockers`, `HintBits` match upstream; see the one defensive-indexing note above.
- **multixact/store.go** — dedup, expand survivor logic, canonical sort/key all correct.
- **control/control.go** — pidfile/socket lifecycle and command handling correct.
- **control/pgcontrol.go** — all decode/encode offsets were checked against the in-tree PG 18.3 `pg_control.h`/`CheckPoint` struct and match (incl. `data_checksum_version` at 252 and CRC at 292); CRC32C coverage and durable write path correct.
