# Take 6 results — candidates A and B

**Status:** implemented and measured
**Date:** 2026-08-30
**Branch:** `perf-opt-take6`
**Baseline:** `b91732783`
**Plan:** [README.md](README.md) (agent-reviewed; §8 there)

---

## 1. Summary

Both shipped candidates target work that **every** operator pays, so the win is
cross-query rather than Q14-specific. Alternating A/B, fresh server per arm, two
rounds, warm (second of two runs):

| query | baseline | A + B | speedup |
|---|---:|---:|---:|
| **Q14** (the plan-match join query) | 664.2 / 713.6 ms | **531.9 / 559.9 ms** | **1.26×** |
| **Q3** (3-way join, 11,415 groups) | 2720.5 / 2710.5 ms | **2358.4 / 2358.0 ms** | **1.15×** |
| **Q10** (4-way join, 20,451 groups) | 2198.9 / 2197.9 ms | **1890.0 / 1880.6 ms** | **1.17×** |

Ranges disjoint in every round. **Row counts and result bytes identical on
every query in every round** (`cmp` on the full result set: Q14 1 row, Q3 11,415
rows, Q10 20,451 rows).

Mechanisms confirmed by re-profiling, not inferred from the clock:

| | baseline | after |
|---|---:|---:|
| `sync/atomic.(*Int32).Add` | 10.86 % | **0.54 %** |
| `strings.ToLower` | 4.64 % | **absent** |
| `physicalPGTypeAlign`, `isTimestampTZTypeName` | present | **absent** |
| `sync.(*RWMutex).RLock` / `RUnlock` | 7.37 % / — | **absent** |

---

## 2. Candidate A — resolve each column's type once

`resolveColTypeInfo` computes the lowered type name and the physical alignment
once per column, in the operator's `Open`. `decodeRowRangeInfo` and
`decodePhysicalPGValueLowered` take the lowered name as a parameter instead of
recomputing it per value. Inside `case "timestamp", "timestamptz"` the lowered
name is already one of those two literals, so `isTimestampTZTypeName` — the
third scan of the same string — becomes `tname == "timestamptz"`.

This is goopg's `TupleDesc`: PG's `heap_deform_tuple` reads `attlen`,
`attbyval` and `attalignby` from a descriptor resolved once and touches no
string.

**The hazard, and why the implementation avoids it.** A "small integer type
code derived from the type name" — which the first draft of the plan
proposed — would be **wrong**: for an array column `catalog.Type.Name` holds the
*element* type and `IsArray` carries the array-ness, so `int4[]` would get
`int4`'s alignment and decode arm; `len(t.Args)` likewise separates internal
`"char"` (align 1) from `char(N)` (align 4). What shipped memoises the *lowered
name* and still passes the full `catalog.Type`, so every `IsArray`/`Args` branch
sits exactly where it did. `TestColTypeInfoArraySafety` pins it: `int4[]` and
`text[]` must align 4, and `char` must not collapse into `char(10)`.

Measured alone (before B): Q14 1.07×, Q3 ~1.13×, Q10 1.11×, Q1 neutral.

## 3. Candidate B — no lock per tuple for visibility

Two per-tuple lookups each took a process-global `sync.RWMutex`, together ~94 %
of the atomic traffic:

1. **`CLog.oldestClogXid` → `atomic.Uint32`.** Readers `Load()`. Writers still
   serialise on `oldestMu` for the monotonic compare-and-store, which also still
   guards `truncateLogger`.
2. **`SubxactMap.IsSubxact` → `atomic.Int64` count.** A zero count means the map
   is empty, so the guarded lookup could only have returned false. Maintained in
   step with the map under `mu`, read without it.

**What was NOT done, and why it matters.** The plan's first draft proposed
hoisting the CLOG horizon into the snapshot. That is unsafe:
`AdvanceOldestClogXid` publishes the new horizon *before* `TruncateCLOG` deletes
the SLRU bytes, precisely so concurrent readers short-circuit before the bytes
disappear. A reader holding a stale, older horizon would fail the `XIDPrecedes`
check and fault in a truncated page as all-zero (= Unknown), which `statusCache`
would then memoise — and a snapshot under `REPEATABLE READ` lives for the whole
transaction, so the window is unbounded rather than scan-bounded. The atomic
keeps the value **live**, which is the whole difference.

## 4. What this did not fix — the larger half of B

goopg's `HEAP_XMIN_COMMITTED` branch still calls `clogSaysNotAborted`, where
PostgreSQL's branch does only the snapshot test. The hint bits are set on a
bulk-loaded table and buy nothing (README §3.4). Removing that call would drop
`GetStatus` as well and is the largest remaining single item — but it changes
visibility semantics, so it needs its own design and differential testing rather
than being folded in here.

`cloneRowOwned` remains the top allocator (36–41 % of objects) and is the
sequential scan, not the join. `evalFastExpr` and `memmove` are now the top two
CPU items, both real work.

## 5. Verification

| check | result |
|---|---|
| Q14 / Q3 / Q10 results vs baseline | **byte-identical**, both rounds |
| `scripts/tpch-spotcheck.sh` | **PASS** — exit 0, Q12 = 2, Q13 = 34 |
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | pass — exit 0, 43 packages, 0 failures |
| `go test -race ./internal/executor/ ./internal/access/transam/` | pass |
| `TestColTypeInfoArraySafety` | pass — arrays and `char(N)` unaffected |
| `go test ./internal/access/...` | pass |

## 6. Changed files

| file | change |
|---|---|
| `internal/executor/coltypeinfo.go` | **new** — `colTypeInfo`, `resolveColTypeInfo` |
| `internal/executor/coltypeinfo_test.go` | **new** — array / `char(N)` safety |
| `internal/executor/codec.go` | `decodePhysicalPGValueLowered`, `physicalPGTypeAlignLowered`, `decodeRowRangeInfo`; the timestamptz check uses the lowered name |
| `internal/executor/operators_storage.go` | `seqScanOp.colInfo` resolved in `Open`, threaded into both decode paths |
| `internal/access/transam/clog.go` | `oldestClogXid` → `atomic.Uint32`; `oldestMu` now guards `truncateLogger` only |
| `internal/access/transam/subxact_visibility.go` | `SubxactMap.nParents atomic.Int64` + empty-map fast path |

## 7. Reproduction

```bash
go build -o tmp/take6/goopg-b ./cmd/goopg
QUERIES="14 03 10" tmp/take6/ab.sh tmp/take6/goopg-base tmp/take6/goopg-b 2
TAG=t6q14AB SECS=30 tmp/take6/profile.sh
GOOPG_BIN=$PWD/tmp/take6/goopg-spot scripts/tpch-spotcheck.sh
```

---

## 8. Follow-up — the hint-bit branch no longer consults the CLOG

§4 left this as "the largest remaining single item". It is now fixed.

### 8.1 What was wrong

`HEAP_XMIN_COMMITTED` is supposed to be a fast path. `SeesCommittedXID`'s own
comment says so — *"the hint bits … are what keep this off the repeat-scan hot
path, exactly as upstream"* — but **no caller realised it**. Both siblings did:

```go
if h.Infomask&storage.HeapXminCommitted == 0 {
    if !SeesCommittedXIDWithSubxacts(snap, h.Xmin, r) { return false }
} else if !snap.SeesCommittedXID(h.Xmin) {   // hint bit SET — same call
    return false
}
```

so the hinted branch ran the identical predicate, including
`clogSaysNotAborted` → `OldestClogXid()` + `GetStatus()`. The hint bit saved
nothing.

PostgreSQL's branch does only the snapshot test:

```c
else {  /* xmin is committed, but maybe not according to our snapshot */
    if (!HeapTupleHeaderXminFrozen(tuple) &&
        XidInMVCCSnapshot(HeapTupleHeaderGetRawXmin(tuple), snapshot))
        return false;
}
```

### 8.2 Why dropping the CLOG consult is sound

The invariant was verified by auditing every site that sets the bit — there is
exactly one (`seqScanOp.Next`, M0115-0004), and it fires only **after** the
tuple was confirmed visible through `SeesCommittedXID` (so the CLOG already
answered "not aborted") and only when xmin is **not** the current transaction or
an ancestor (a self-visible tuple can still be rolled back to a savepoint, so it
is deliberately never hinted).

Commit is terminal. An XID the CLOG did not report aborted then cannot report
aborted later. So for a hinted tuple the consult is guaranteed to return true
and skipping it cannot change an answer.

What still must be checked is the **snapshot window** — the bit records "xmin
committed", not "committed before *my* snapshot" — which is exactly what PG's
branch checks and what `SeesCommittedXIDHinted` retains. The in-memory
`HasAborted` check is also retained, although PG has no equivalent, as a cheap
backstop for the crash-recovery case `SeesCommittedXID`'s comment describes.

### 8.3 Measured

Both arms already carry candidates A and B, so this isolates the follow-up.
The headline 7.97 % from README §3.4 was measured *before* candidate B, which
had already converted the expensive half (the two RWMutexes) into atomic loads
— so what remained for this change to remove was the `GetStatus` lookup, not
the lock:

| | after A+B | after this fix |
|---|---:|---:|
| `Snapshot.SeesCommittedXID(Hinted)` | 2.13 % cum | **1.58 %** |
| `Snapshot.clogSaysNotAborted` | 0.58 % | **absent** |
| `TupleVisibleSubxact` | 4.00 % cum | **3.21 %** |

Wall clock, alternating A/B, fresh server per arm, two rounds:

| query | after A+B | after this fix | |
|---|---:|---:|---:|
| Q14 | 589.5 / 547.8 ms | 515.8 / 527.3 ms | ~1.08× |
| Q3 | 2343.1 / 2375.6 ms | 2330.0 / 2318.5 ms | ~1.01× |
| Q10 | 1892.8 / 1923.6 ms | 1880.4 / 1862.9 ms | ~1.01× |

**Stated plainly: this is a small win, not the 7.97 % the plan implied**, because
candidate B had already taken the costly part. It is worth having anyway — it
removes the CLOG from the per-tuple path entirely and makes the hint bit
actually be the fast path its own comment claims.

### 8.4 Verification

| check | result |
|---|---|
| `TestSeesCommittedXIDHintedMatchesUnhinted` | pass — the two predicates agree on **every** xid the CLOG does not report aborted, across five snapshot-window shapes; the single permitted divergence (a CLOG-aborted xid, unreachable for a hinted tuple) is pinned explicitly and confirmed to be exercised |
| Q14 / Q3 / Q10 results | byte-identical, both rounds |
| `scripts/tpch-spotcheck.sh` | **PASS** — Q12 = 2, Q13 = 34 |
| units | pass — 43 packages, 0 failures |
| `go test -race ./internal/access/transam/ ./internal/executor/` | pass |
