# root-0038 — `ORDER BY … FOR UPDATE` over a join silently took no row lock

**Status:** landed 2026-08-06.
**Area:** executor / row-level locking (`internal/executor/operators_lockrows.go`).
**Found by:** M-NIGHTLY triage of `TestPort_IsolationEvalPlanQual`
(AI-20260806-011323-001). See §5 — the nightly failure itself is **not** closed
by this; the bug below was found while investigating it and is a distinct,
independently reproducible defect.

## 1. Symptom

```sql
-- session 2
BEGIN;
UPDATE accounts SET balance = balance + 450 WHERE accountid = 'checking';
-- (held open)

-- session 1
SELECT a.accountid, a.balance
  FROM accounts a, small s
 WHERE a.accountid = s.k
 ORDER BY a.accountid
   FOR UPDATE OF a;
```

PostgreSQL blocks session 1 until session 2 commits, then returns `1050`.
goopg returned `600` — the stale pre-update row — **in 4 ms, without
blocking**. `FOR UPDATE` was a silent no-op: no tuple lock was taken and the
EvalPlanQual recheck never ran.

Removing the `ORDER BY` made the identical query behave correctly (blocked
6005 ms, returned `1050`). A single-relation `ORDER BY … FOR UPDATE` was also
correct. Only *Sort above a join* was broken.

## 2. Why goopg is exposed here and PG is not

PG carries the row mark as a **resjunk `ctid` column** added by
`preprocess_targetlist` for every rowmark; `nodeLockRows.c` reads it back out of
the tuple. A Sort preserves it exactly as it preserves any other column, so the
plan shape above the scan is irrelevant to locking.

goopg has no resjunk-ctid column. `lockRowsOp` **reconstructs** the TID at
runtime by two routes:

1. **`lockRowsOp.scan`** — a `currentTIDProvider` located by walking the
   operator tree (`findScanLeafForRel`, falling back to `findScanLeaf`), then
   read per row via `currentTID()` in `drainAndStamp`.
2. **the slot side-channel** — `MaterializedSlot.hasCTID/ctidBlock/ctidOff`,
   consumed by `drainAndStamp`'s fallback when route 1 yields nothing.

Route 1 **cannot** work under a Sort, and both walkers correctly return `nil`
at a `sortOp`: once the sort has drained and reordered its input, the scan
cursor no longer points at the row being emitted, so reading `currentTID()`
there would stamp an arbitrary tuple. Route 2 exists precisely to cover that
case — `sortOp.ctids` carries `(block, off, has)` in lockstep with its rows and
re-attaches it in `Next` (M0118-0003, whose comment names
`ORDER BY … FOR UPDATE` as the motivating case).

## 3. Root cause

Route 2 only fires if the slot **entering** the sort already has `hasCTID`.

- A `seqScanOp` stamps `slot.hasCTID` itself (`operators_storage.go:1754`), so
  a single-relation sort was always fine.
- A `joinOp` does **not**. It forwards the heap ctid only when
  `preserveCTIDRel` is set — and the function that sets it,
  `markJoinPreserveCTID`, recursed through `projectOp` / `filterOp` / `joinOp`
  only. It **stopped dead at the `sortOp`** and never reached the join.

So for `LockRows → Sort → Hash Join`:

| route | result |
|---|---|
| 1 — `o.scan` | `nil` (correct by design at a Sort) |
| 2 — slot ctid | never populated (join untagged) |

`drainAndStamp` therefore found neither a scan nor a slot TID, and `lockRowsOp`
fell through to its unlocked pass-through path — returning rows with only the
relation-level `RowShareLock` taken and no tuple lock at all.

## 4. Fix

One arm, in `markJoinPreserveCTID`:

```go
case *sortOp:
	markJoinPreserveCTID(v.child, targetRel)
```

The walkers `findScanLeaf` / `findScanLeafForRel` are deliberately **left
alone** — returning `nil` at a Sort is correct, and teaching them to descend
would reintroduce the arbitrary-tuple stamp that route 2 was built to avoid.

**A/B, two binaries differing only in that arm**, identical plan shape
(`LockRows → Sort → Hash Join`) on both sides:

| | elapsed | balance | blocked? |
|---|---|---|---|
| pre-fix | 4 ms | 600 (stale) | no |
| fixed | 4008 ms | 1050 | yes |

**Guard:** `TestPort_LockRowsSortOverJoinTakesRowLock`
(`internal/testport/lockrows_sort_ctid_test.go`), two subtests —
`sort_over_join` (the regression) and `join_no_sort` (control, route 1, always
worked). Verified non-vacuous: with the arm removed, `sort_over_join` fails
with `balance=600` and no block while `join_no_sort` still passes, so the guard
discriminates the exact defect rather than the feature at large.

The guard drives its two sessions as plain `BEGIN`/`COMMIT` statements on
pinned `*sql.Conn`s, the way `framework.IsolationRunner` does. `db.Begin()`
cannot be used: lib/pq asserts the `ReadyForQuery` transaction status flips to
`T`, which goopg does not report, and the test dies with "unexpected
transaction status idle" before reaching any of its semantics.

## 5. What this does NOT close

`TestPort_IsolationEvalPlanQual` (AI-20260806-011323-001, six nights) remains
**open**. Its failing steps (`lockwithvalues`, `partiallock_ext`) carry no
`ORDER BY`, so they do not take this path. This loop additionally established
about that item:

- The recorded diagnosis in `fix_plan.md` was **wrong**. It attributed the
  20260806 failure to `partiallock_ext` not blocking at L1027; the actual diff
  starts at **L1001 on `lockwithvalues`**, and `partiallock_ext` blocks
  correctly at L1024 in the very same log. The signature is the same class
  though — no `<waiting …>` **and** a stale read (`600` where PG gives `1050`).
- It is **not reproducible at HEAD** by any of: isolation (5 runs), the
  nightly's cgroup env (`GOOPG_MEM_HIGH=6G/MAX=8G/GOMEMLIMIT=5GiB`), synthetic
  12-way CPU load, or the whole `TestPort_Isolation*` family in nightly order
  (404 s). So it is order-dependent on something **outside** the isolation
  family, and the trigger is still unidentified.

## 6. Deferred

`sortOp` drops the side-channel once it **spills** (`ctidsDisabled` — the N-way
merge cannot carry it), so a row-locking query whose sort exceeds `work_mem`
still loses its tuple lock silently. The bounded fix is not available here; the
real fix is the resjunk-ctid column PG uses. Ledger row appended 2026-08-06.

More broadly, every remaining consumer of the positional reconstruction is
fragile in the same way: any operator between `LockRows` and its scan leaves
that is absent from the two walkers' switches degrades to "no lock", silently.
Today that switch knows 8 operator types out of ~70.

## 7. See also

- `docs/design/root-0030-lockrows-rescan-state.md` — `lockRowsOp.Open` as the
  ExecReScan entry point.
- `docs/design/0100-0005-lockrows-partition-tid-stamp.md`,
  `0100-0005-lockrows-committed-update-chain-follow.md`.
- `postgres/src/backend/executor/nodeLockRows.c`,
  `postgres/src/backend/optimizer/prep/preptlist.c`
  (`preprocess_targetlist` rowmark junk attributes).
