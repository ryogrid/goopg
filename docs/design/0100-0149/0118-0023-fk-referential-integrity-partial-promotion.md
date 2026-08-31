# 0118-0023 — FK / referential-integrity isolation specs: partial promotion (M0118-0005)

**Status:** accepted
**Date:** 2026-06-22
**Milestone:** M0118-0005 (Upstream Isolation Spec Suite Pass-Through — FK /
referential-integrity concurrency group)
**Related:** [0118-0022](0118-0022-merge-insert-conflict-promotion.md) (the same
`runIsoSpecStrict` promotion mechanism), M0118-0003/0004 (multixact lock-only +
no-key-update producer that makes the FK KEY SHARE non-conflict work).

## Summary

The M0118-0005 group targets nine FK / referential-integrity isolation specs:
`fk-contention`, `fk-deadlock`, `fk-deadlock2`, `fk-partitioned-1`,
`fk-partitioned-2`, `referential-integrity`, `ri-trigger`,
`temporal-range-integrity`, plus the adjacent `fk-snapshot`.

A probe (throwaway `zz_probe_test.go` driving `IsolationRunner.RunAndCompare` per
spec, ranked by first-divergence cost — the established discipline) showed **five
already match PG 18.3 byte-for-byte with no engine change**, and **four still
diverge and need real engine work**. This loop **promotes the five passing specs
to pass-required** and records the four deferrals; the group cannot fully close.

### Promoted to pass-required (5)

| spec | dedicated test | why it already passes |
|------|----------------|-----------------------|
| `referential-integrity` | `TestPort_IsolationReferentialIntegrity` | SERIALIZABLE write-skew across two tables standing in for an app-enforced FK; the empty read on `b` takes a relation-grain SIREAD so the dangerous structure closes with 40001 (landed in the SSI milestones). |
| `temporal-range-integrity` | `TestPort_IsolationTemporalRangeIntegrity` | range-predicate read/write across two tables → rw-dependency cycle → 40001. |
| `fk-snapshot` | `TestPort_IsolationFkSnapshot` | FK check observes the correct snapshot; no extra wait. |
| `fk-contention` | `TestPort_IsolationFkContention` | child INSERT takes `FOR KEY SHARE` on the parent row; a concurrent **non-key** parent UPDATE does not conflict with KEY SHARE, so neither session blocks. Rides the M0118-0003/0004 multixact lock-only + no-key-update producer. |
| `fk-deadlock2` | `TestPort_IsolationFkDeadlock2` | two child INSERTs both take a non-conflicting KEY SHARE on the shared parent (multixact lock set); the subsequent parent UPDATEs touch disjoint rows so no lock cycle forms — both commit. |

Mechanism: the three pre-existing dedicated tests were switched from the soft
`runIsoSpec` (a non-`pass` status is a `t.Skip`) to `runIsoSpecStrict` (a
non-`pass` status is a `t.Errorf` — a regression surfaces red, not silently
skipped), and two new dedicated tests (`TestPort_IsolationFkContention`,
`TestPort_IsolationFkDeadlock2`) were added directly on the strict helper. No
production code changed — this is a pass-required promotion of behavior that
already landed in prior milestones.

### Deferred (4) — ledger 2026-06-22

| spec | first divergence | required work |
|------|------------------|---------------|
| `fk-deadlock` | goopg's `s2i` INSERT-into-`child` (FK `FOR KEY SHARE` on the shared parent) **waits and times out** where PG proceeds; the real deadlock in PG is on the subsequent parent UPDATEs. goopg's FK row-lock wait **over-conflicts**. | make the FK-check KEY SHARE acquisition non-conflicting against another session's KEY SHARE on the same parent row (multixact lock-set join on the *wait* path, the producer twin of `fk-deadlock2`'s grant path), then let the cycle form on the parent UPDATEs so the deadlock detector fires symmetrically with PG. |
| `ri-trigger` | goopg does not raise `ERROR: child row exists`; the RI enforcement trigger never fires. | user-visible RI constraint-trigger firing (the spec installs an explicit `CREATE TRIGGER` enforcing the relationship). |
| `fk-partitioned-1` | `ERROR: table "pfk1" does not exist` after `ALTER TABLE pfk ATTACH PARTITION pfk1 …`. | `ALTER TABLE … ATTACH PARTITION` + FK enforcement that spans the partitioned parent. |
| `fk-partitioned-2` | same `table "pfk1" does not exist`. | partitioned-table FK enforcement (shares the `fk-partitioned-1` enabler). |

All four are genuine engine gaps (FK-wait lock semantics, RI trigger firing,
partition attach/FK), not output-format quibbles — out of scope for a single
promotion loop, hence the ledger row and the fix_plan item stays unchecked.

## Gates

- `TestPort_Isolation{ReferentialIntegrity,TemporalRangeIntegrity,FkSnapshot,FkContention,FkDeadlock2}`
  — all 5 PASS under the strict gate (~12s wall).
- `go build ./...` clean; `go vet ./internal/testport/`.
- pgbench smoke via the pre-commit hook at commit (mandatory on every commit).

## Files

- `internal/testport/isolation_port_test.go` — 3 soft→strict switches + 2 new
  strict dedicated tests.
- `docs/test-port/postgres-oracle-port-status.{csv,md}` — D-002 rationale + regen.
- `docs/design/0118-0023-*.md` (this doc) + `docs/design/README.md` index row.
- `.ralph/deferral_ledger.md` — one row for the four deferred specs.
