# 0118-0025 — SERIALIZABLE / SSI anomaly isolation specs promoted to pass-required (M0118-0001)

Status: accepted
Date: 2026-06-22
Milestone: M0118-0001 (Upstream Isolation Spec Suite Pass-Through — SERIALIZABLE / SSI anomaly specs)

## Summary

The nineteen isolation specs in the M0118-0001 group already matched
PostgreSQL 18.3 **byte-for-byte** when run through
`IsolationRunner.RunAndCompare`. This change contains **no engine change**: it
formally promotes the entire group from observed-pass to `pass_required=yes` by
switching each dedicated Go test function from the soft `runIsoSpec` helper
(which `t.Skip()`s a non-`pass` result) to the strict `runIsoSpecStrict` helper
(which turns any non-`pass` result into a hard test failure), and recording the
promotion in the D-002 inventory rationale.

Promoted specs (all 19):

| spec | dedicated test |
|------|----------------|
| simple-write-skew | `TestPort_IsolationSimpleWriteSkew` |
| matview-write-skew | `TestPort_IsolationMatviewWriteSkew` |
| read-only-anomaly | `TestPort_IsolationReadOnlyAnomaly` |
| read-only-anomaly-2 | `TestPort_IsolationReadOnlyAnomaly2` |
| read-only-anomaly-3 | `TestPort_IsolationReadOnlyAnomaly3` |
| read-write-unique | `TestPort_IsolationReadWriteUnique` |
| read-write-unique-2 | `TestPort_IsolationReadWriteUnique2` |
| read-write-unique-3 | `TestPort_IsolationReadWriteUnique3` |
| read-write-unique-4 | `TestPort_IsolationReadWriteUnique4` |
| two-ids | `TestPort_IsolationTwoIds` |
| total-cash | `TestPort_IsolationTotalCash` |
| receipt-report | `TestPort_IsolationReceiptReport` |
| project-manager | `TestPort_IsolationProjectManager` |
| classroom-scheduling | `TestPort_IsolationClassroomScheduling` |
| multiple-row-versions | `TestPort_IsolationMultipleRowVersions` |
| update-conflict-out | `TestPort_IsolationUpdateConflictOut` |
| serializable-parallel | `TestPort_IsolationSerializableParallel` |
| serializable-parallel-2 | `TestPort_IsolationSerializableParallel2` |
| serializable-parallel-3 | `TestPort_IsolationSerializableParallel3` |

## Why no code was needed

These specs are the canonical SSI test battery from
`postgres/src/test/isolation`. They construct dangerous structures (rw-antidependency
cycles) across two or three SERIALIZABLE transactions and assert that PostgreSQL
aborts exactly one transaction with SQLSTATE `40001` (and, for the read-only-anomaly
family, that a read-only transaction is correctly pivoted or allowed through). The
underlying capabilities all landed in earlier milestones:

- **REPEATABLE READ** txn-level pinned snapshot (M0100) — every snapshot in a
  SERIALIZABLE transaction is taken once at first statement and reused, so the
  write-skew structure is observable.
- **Real SSI** — predicate locks (SIREAD), rw-conflict tracking, and the
  `OutConflict`/`InConflict` dangerous-structure detector that raises
  `40001 could not serialize access due to read/write dependencies among
  transactions` with the upstream reason code in DETAIL (M0104). The bare
  upstream `errmsg` text and DETAIL wording were already aligned during
  M0118-0001's earlier `generateAllPermutations` work (see the D-002 rationale),
  which is why the diff is now empty for the whole group.

`multiple-row-versions` and `receipt-report` additionally exercise multi-version
visibility under concurrent updates; both reproduce upstream's per-permutation
output exactly. `serializable-parallel{,-2,-3}` confirm the detector behaves the
same whether or not a parallel-worker plan would have been chosen (goopg runs
them serially, which produces the identical observable schedule).

## Verification

```
go test -count=1 -run 'TestPort_Isolation(SimpleWriteSkew|MatviewWriteSkew|TwoIds|TotalCash|\
ReceiptReport|ProjectManager|ClassroomScheduling|ReadOnlyAnomaly|ReadOnlyAnomaly2|\
ReadOnlyAnomaly3|SerializableParallel|SerializableParallel2|SerializableParallel3|\
UpdateConflictOut|MultipleRowVersions|ReadWriteUnique|ReadWriteUnique2|ReadWriteUnique3|\
ReadWriteUnique4)$' ./internal/testport/ -v
```

All 19 PASS against PG 18.3 (`./postgres/local_install`). Because this is a
test-assertion + documentation promotion with no production-code change, the
executor/MVCC gates are unaffected; the pgbench pre-commit smoke runs via the
`.githooks/pre-commit` hook on commit.

## Status of M0118-0001

With this promotion **M0118-0001 is COMPLETE** — every spec listed under the
milestone item is pass-required and green. No specs in the group were deferred.

## Files

- `internal/testport/isolation_port_test.go` — 19 `runIsoSpec` → `runIsoSpecStrict`.
- `docs/test-port/postgres-oracle-port-status.csv` — D-002 rationale appended.
- `docs/test-port/upstream-isolation-coverage.md`,
  `docs/test-port/postgres-oracle-target-inventory.md` — regenerated.
