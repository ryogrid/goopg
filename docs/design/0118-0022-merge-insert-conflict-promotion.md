# 0118-0022 — MERGE & INSERT ON CONFLICT isolation specs promoted to pass-required (M0118-0006)

Status: accepted
Date: 2026-06-22
Milestone: M0118-0006 (Upstream Isolation Spec Suite Pass-Through — MERGE & INSERT ON CONFLICT output parity)

## Summary

The ten isolation specs in the M0118-0006 group already matched PostgreSQL 18.3
**byte-for-byte** when run through `IsolationRunner.RunAndCompare`. This change
contains **no engine change**: it formally promotes the group from observed-pass
to `pass_required=yes` by hard-asserting the match in the dedicated Go test
functions and recording the promotion in the D-002 inventory rationale.

Promoted specs:

| spec | dedicated test |
|------|----------------|
| merge-update | `TestPort_IsolationMergeUpdate` |
| merge-delete | `TestPort_IsolationMergeDelete` |
| merge-insert-update | `TestPort_IsolationMergeInsertUpdate` |
| merge-match-recheck | `TestPort_IsolationMergeMatchRecheck` |
| merge-join | `TestPort_IsolationMergeJoin` |
| insert-conflict-do-update-2 | `TestPort_IsolationInsertConflictDoUpdate2` |
| insert-conflict-do-update-3 | `TestPort_IsolationInsertConflictDoUpdate3` |
| insert-conflict-do-update-4 | `TestPort_IsolationInsertConflictDoUpdate4` |
| insert-conflict-specconflict | `TestPort_IsolationInsertConflictSpecconflict` |
| insert-conflict-do-nothing-2 | `TestPort_IsolationInsertConflictDoNothing2` |

## Why no code was needed

These specs exercise the MERGE executor's WHEN MATCHED / NOT MATCHED arms with
concurrent-update rechecks, and `INSERT … ON CONFLICT DO {UPDATE,NOTHING}`
arbiter behavior under REPEATABLE READ and SERIALIZABLE. The underlying
capabilities landed in earlier milestones:

- MERGE execution and EvalPlanQual recheck of the matched row version
  (so `merge-match-recheck` re-evaluates the join condition after a concurrent
  update, and `merge-join` produces the same per-action counts as upstream).
- `ON CONFLICT DO UPDATE`'s speculative-insertion arbiter, including the
  `pg_advisory_xact_lock`-driven specconflict ordering
  (`insert-conflict-specconflict`).
- `ON CONFLICT DO NOTHING` never raising a serialization failure even when a
  concurrent committed insert created the conflicting key — the arbiter simply
  skips the row (`insert-conflict-do-nothing-2`).

Running each spec through the runner reproduced PG 18.3's expected output with
zero diff across every generated permutation, so the only work remaining was to
make the existing observability anchors into enforced gates.

## Mechanism: `runIsoSpecStrict`

`runIsoSpec` (the pre-existing soft anchor) calls `t.Skip` when a spec's output
does not yet match — appropriate while a spec is still `defer`, because an
unported spec should not turn the suite red. A `pass_required` spec needs the
opposite: a regression must surface as a **red test**.

This change adds `runIsoSpecStrict`, identical to `runIsoSpec` except that any
status other than `pass` is a `t.Errorf` (hard failure) carrying the captured
diff. The ten test functions above were switched from `runIsoSpec` to
`runIsoSpecStrict`. No other isolation test changed behavior; specs still
legitimately deferring keep the soft anchor.

## Oracle

`postgres/src/test/isolation/specs/{merge-*,insert-conflict-*}.spec` and their
`expected/*.out`, compared against `./postgres/local_install` PostgreSQL 18.3.

## Verification

- All ten promoted tests green under the strict gate
  (`go test -run 'TestPort_IsolationMerge…|TestPort_IsolationInsertConflict…'`),
  total ~55 s.
- Probe harness (throwaway) confirmed every spec returns `status=pass` before
  promotion; the harness was removed after use.
- pgbench CI-parity smoke via the pre-commit hook at commit.

## Follow-ups

Remaining M0118 groups untouched. The next-closest isolation candidate observed
while probing is `intra-grant-inplace` (output already matches except a single
`<waiting>` divergence on `ALTER TABLE … ADD PRIMARY KEY`), but it requires
modeling pg_class catalog-tuple xmax row locks for GRANT/REVOKE/ALTER across nine
permutations — a large semantic gap against goopg's virtual pg_class, deferred.
