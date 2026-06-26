# 0118-0114 — intra-grant-inplace enabler: GRANT/ADD-PK serialize on a deadlock-aware pg_class tuple wait (perms 7-8)

Status: accepted
Milestone: M0118-0009 (upstream isolation spec suite pass-through)
Spec: `postgres/src/test/isolation/specs/intra-grant-inplace.spec`
Predecessors: 0118-0109 (GRANT-`xmax` half), 0118-0113 (`pg_class` rowmark half)

## Summary (Enabler, NOT a promotion)

`intra-grant-inplace.spec` exercises PostgreSQL's rule that **GRANT/REVOKE takes no
heavyweight lock on the object whose ACL changes — the lock *is* the catalog tuple's
xmax** — and that an in-place `pg_class` update (`ALTER TABLE … ADD PRIMARY KEY`
flipping `relhasindex` via `heap_inplace_update`) must cope with that by serialising on
the same tuple xmax/rowmark.

Prior loops built the two one-directional waits:

- 0118-0109: `ALTER TABLE … ADD PRIMARY KEY` waits on a concurrent GRANT's recorded
  ACL-change xmax (`waitForTableACLChange`).
- 0118-0113: `ALTER TABLE … ADD PRIMARY KEY` waits on a conflicting explicit rowmark
  (`SELECT … FROM pg_class … FOR …`) via `waitForPgClassRowMarks`.

This loop adds the **reverse direction plus deadlock detection**, which permutations 7
and 8 require:

- **GRANT/REVOKE awaits a conflicting rowmark** (PG's `LockTuple` + await-tuple-xmax
  before its `heap_update`). `execCompatNoop`'s `TableACL` branch now records its
  ACL-change xmax **first** (so a peer `ADD PRIMARY KEY` observes it) and **then** calls
  `waitForPgClassRowMarks` to block behind any conflicting concurrent rowmark.
- **Deadlock detection** on these virtual-tuple waits. The three waits
  (`waitForTableACLChange`, `waitForPgClassRowMarks`, and the new GRANT-side wait) all go
  through a new shared helper `waitPgClassInplaceXID`, which registers the edge
  `myXID → blockingXID` in the **existing process-global wait-for graph**
  (`registerWFGAndCheckCycle`, the same one EPQ row-locks use) and walks it for a cycle
  before blocking. A cycle is a deadlock → the caller raises SQLSTATE **40P01
  "deadlock detected"**.

### Result

| perm | shape | before | after |
|------|-------|--------|-------|
| 7 | `b3 sfu3 b1 grant1(r3) read2 addk2(c1) r3 c1 read2` | grant1 did not wait → diverged at L141 | **byte-exact** |
| 8 | `b2 sfnku2 b1 grant1(addk2) addk2(*) c2 c1 read2` | grant1 did not wait → no deadlock | **deadlock correctly detected**; only residual divergence is the grant1/c2 completion order (see below) |

First divergence advanced **L141 → L184**. Permutations 1–7 and perm 8 *up to and
including* the `ERROR: deadlock detected` line are byte-identical to PG 18.3.

## How perm 7 works (no deadlock)

```
sfu3 (s3)  : rowmark on pg_class, conflicts=true. No ACL xmax yet → no wait.
grant1 (s1): record ACL xmax = s1, THEN wait on sfu3's rowmark (s3). Edge s1→s3. blocks.
read2 (s2) : plain SELECT → f.
addk2 (s2) : waitForTableACLChange sees ACL xmax = s1 (active) → edge s2→s1, blocks;
             waitForPgClassRowMarks sees sfu3 → blocks. Net wait = max(r3, c1) = c1.
r3         : s3 ends → grant1 unblocks, completes.
c1         : s1 ends → addk2 unblocks, completes. read2 → t.
```

Recording the GRANT's xmax **before** waiting is load-bearing: it is what makes `addk2`
serialise after `c1` rather than after `r3`.

## How perm 8 works (intentional deadlock)

```
sfnku2 (s2): rowmark, conflicts=true.
grant1 (s1): record ACL xmax = s1, wait on sfnku2 (s2). Edge s1→s2. blocks.
addk2  (s2): waitForTableACLChange sees ACL xmax = s1 → registers edge s2→s1; the WFG
             now has s1→s2→s1 → cycle → returns deadlock → 40P01. addk2 is the victim.
```

The runner renders the `(*)`-marked victim (`addk2(*)`) in `<waiting ...>` / `<...
completed>` form regardless of how fast the error fires, so a synchronous cycle
detection still matches isolationtester's output for the error line.

## Known residual (deferred) — perm 8 completion ordering

The single remaining perm-8 divergence is an ordering swap:

```
expected: step grant1: <... completed>   ←  unblocks at the deadlock abort (s2 released)
          step c2: COMMIT;
actual:   step c2: COMMIT;
          step grant1: <... completed>   ←  unblocks only at c2
```

In PostgreSQL the deadlock detector **aborts the victim's transaction**, releasing its
locks/xmax immediately, so `grant1` unblocks *before* `c2`. goopg keeps the victim's XID
**active** after a top-level statement error until the explicit `COMMIT`/`ROLLBACK`
(the txn block lingers in *idle in transaction (aborted)*), so `grant1`'s
`WaitForXID(s2)` only returns at `c2`. Matching upstream needs goopg to **deactivate the
victim's XID at deadlock-abort time while keeping the txn block open** — a
txn-lifecycle change (the `AbortTransaction`-releases-resources-but-block-stays-open
semantics), deferred.

## Deferred — perms 9, 10 (separate subsystems)

- **perm 9** (`b1 grant1 b3 sfu3(c1) revoke4(r3) c1 r3`): `revoke4` is a `DO $$ … REVOKE
  … $$` plpgsql block — goopg's plpgsql parser rejects a bare `REVOKE` statement inside a
  DO body (`syntax error … expected ':=' or '=' after "revoke"`). Also needs `lockRowsOp`
  on `pg_class` to await the ACL-change xmax (sfu3-after-grant1 direction).
- **perm 10** (`b1 drop1 b3 sfu3(c1) revoke4(sfu3) c1 r3`): `drop1` is `DELETE FROM
  pg_class WHERE relname = …` — needs DELETE-on-virtual-`pg_class` semantics (deferred
  drop at commit) plus the `SearchSysCacheLocked1` find-then-no-tuple path that emits
  `WARNING: cache lookup failed for relation …`.

## Blast radius

`waitPgClassInplaceXID` is reached only from the three `pg_class`-tuple wait sites, each
of which is already gated on the locked relation being `pg_class` with an `oid = <const>`
filter (rowmark) or on a recorded `TableACLChangeXID` (ACL). With no concurrent GRANT or
rowmark every site is an immediate no-op, so the `ADD PRIMARY KEY` / GRANT hot paths are
byte-unchanged. The wait-for graph is the pre-existing EPQ one; adding/removing an edge
per wait is the same protocol EPQ row-locks already use.

## Gates

- Probe (`intra-grant-inplace.spec`): perms 1–7 byte-exact; perm 8 deadlock line exact;
  first divergence L141 → L184.
- Non-regression: `TestPort_IsolationIntraGrantInplaceDb`, `TestPort_IsolationTruncateConflict`,
  `TestPort_IsolationTuplelockUpgradeNoDeadlock`, `TestPort_IsolationDeadlockHard` strict PASS.
- `TestPgClassRowMarks` (catalog) PASS; `go test -race` on the lock/deadlock executor
  tests green; `go build` + `go vet` clean; pgbench smoke = pre-commit hook.
