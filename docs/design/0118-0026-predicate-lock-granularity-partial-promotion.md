# 0118-0026 — Predicate-lock-granularity isolation specs: partial promotion (M0118-0002)

Status: accepted
Date: 2026-06-22
Milestone: M0118-0002 (Upstream Isolation Spec Suite Pass-Through — predicate-lock granularity per access method / scan type)

## Summary

The M0118-0002 group covers seven upstream isolation specs that probe SSI
predicate-lock granularity across different scan types and index access methods.
A probe-first run of all seven through `IsolationRunner.RunAndCompare` showed
**three already match PostgreSQL 18.3 byte-for-byte** and **four require missing
engine features**. This change is a **no-engine-change** promotion of the three
passing specs from soft `runIsoSpec` to strict `runIsoSpecStrict`, plus a
deferral record for the four blocked specs.

### Promoted to pass-required (3)

| spec | dedicated test | what it exercises |
|------|----------------|-------------------|
| predicate-lock-hot-tuple | `TestPort_IsolationPredicateLockHotTuple` | two SERIALIZABLE txns each `SELECT i IN (5,7)` then UPDATE one row — the cross-covering reads form a write-skew structure across **HOT-chain tuple** versions; later committer aborts 40001 |
| partial-index | `TestPort_IsolationPartialIndex` | an UPDATE that moves a row **out of a partial index** (`CREATE INDEX … WHERE val2=1`) must still take the read/write dependency a full-table read would; overlap raises 40001 |
| index-only-scan | `TestPort_IsolationIndexOnlyScan` | write skew across two all-visible tables read via an **index-only scan** of `SELECT min(id)`; overlap forms a rw-cycle so the second committer aborts 40001 |

These ride the SERIALIZABLE pinned snapshot (M0100) and the SSI 40001
dangerous-structure detector (M0104) already in place. goopg's SIREAD predicate
locking is taken at relation grain, which is *coarser* than (but a correct
superset of) PG's finer-grained locks — for these three the coarser grain
produces the identical observable schedule because the spec's reads and writes
genuinely overlap at relation grain too.

## Deferred (4) — ledger 2026-06-22

| spec | first divergence | blocker |
|------|------------------|---------|
| index-only-bitmapscan | `global setup (permutation 0): driver: bad connection` | the setup workload crashes the backend connection — the **bitmap-scan execution path** is not exercised cleanly under this spec's setup; needs investigation + bitmap-scan predicate-lock support |
| predicate-gin | `invalid input syntax for type integer: "{1}"` | needs **int-array literal handling** plus a **GIN access method** with predicate locking |
| predicate-gist | `sum() argument must be numeric` (then full divergence) | needs the **`point` type**, `sum()` over a point subscript, and a **GiST access method** with predicate locking |
| predicate-hash | over-detects: emits `40001 could not serialize access due to read/write dependencies` where PG commits cleanly | goopg takes a **coarser relation-grain SIREAD** predicate lock; PG's hash-index page/tuple predicate locking is finer-grained and does **not** form a dangerous structure for these disjoint hash buckets, so PG commits both txns. Fixing this requires per-page/per-tuple predicate-lock granularity for the hash AM (real SSI granularity work) |

`predicate-hash` is the most interesting of the four: unlike the GIN/GiST/bitmap
cases (missing features), goopg here is *too conservative* — its coarse SIREAD
over-conflicts. This is the canonical "predicate-lock granularity" gap the
milestone is named for and is the natural next slice once finer-grained predicate
locking lands. The other three are gated on index access methods goopg does not
implement (tracked alongside the GIN/GiST/hash AM work in D-002 / M0110-0002's
AM list).

## Why probe-first

Per the M0118 working discipline (memory
`m0118_isolation_specs_often_frontend_gaps`), a throwaway probe test ran
`RunAndCompare` over all seven specs and logged status + first-divergence diff
before any code was touched. This ranked the group cleanly (3 free wins, 4 real
blockers) in one run and avoided speculative engine work on specs that were
already green.

## Verification

```
go test -count=1 -v -run \
  'TestPort_Isolation(PredicateLockHotTuple|PartialIndex|IndexOnlyScan)$' \
  ./internal/testport/
```

All three PASS against PG 18.3 (`./postgres/local_install`). This is a
test-assertion + documentation promotion with no production-code change, so the
executor/MVCC gates are unaffected; the pgbench pre-commit smoke runs via the
`.githooks/pre-commit` hook on commit.

## Status of M0118-0002

**PARTIAL.** 3 of 7 specs are pass-required and green; 4 remain deferred (see
the table above and `.ralph/deferral_ledger.md`). The group stays open until
finer-grained predicate locking (predicate-hash) and the GIN/GiST/bitmap AM
support land.

## Files

- `internal/testport/isolation_port_test.go` — 3 `runIsoSpec` → `runIsoSpecStrict`
  + promotion notes on the three test doc comments.
- `docs/test-port/postgres-oracle-port-status.csv` — D-002 rationale appended.
- `docs/test-port/upstream-isolation-coverage.md`,
  `docs/test-port/postgres-oracle-target-inventory.md` — regenerated.
- `.ralph/deferral_ledger.md` — one row for the 4 deferred specs.
