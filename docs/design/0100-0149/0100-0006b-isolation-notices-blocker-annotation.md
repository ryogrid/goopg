# 0100-0006b — Isolation runner: `(step notices N)` completion-blocker annotations

**Status:** accepted (part c of M0100-0006b)
**Milestone:** M0100 — RC Isolation Suite runtime correctness & spec pass
**Spec:** `postgres/src/test/isolation/specs/insert-conflict-specconflict.spec` (perm 5)

## Problem

The 5th permutation of `insert-conflict-specconflict.spec` annotates a step with
a PostgreSQL isolationtester *completion blocker*:

```
s1_upsert s2_upsert (s1_upsert notices 10)
```

Per `postgres/src/test/isolation/README` ("Dealing with race conditions"), a
parenthesised marker delays *reporting* a step as completed until its blocking
condition is met. The three marker forms are:

| form | meaning |
|------|---------|
| `*` | report the step as waiting as soon as it is launched |
| `<other step name>` | do not report this step complete until that step has completed |
| `<other step name> notices <n>` | do not report this step complete until that step's session has emitted ≥ n NOTICE messages, counted from this step's launch |

The marker only delays **reporting** completion; it never delays **launching**
a step.

goopg's isolation runner previously **dropped** these annotations during parsing
(`parsePermutationTokens` skipped any `( … )` group, with a comment incorrectly
claiming "PG isolationtester ignores these annotations"). As a result perm 5's
`s2_upsert` was reported before `s1`'s ten `blurt_and_lock_123` NOTICEs, so the
NOTICE interleaving diverged from `expected/insert-conflict-specconflict.out`.

## Approach

Two layers, both additive and gated so the 20 already-passing specs are
byte-for-byte unchanged (none of them carry markers).

### Parser (`internal/testport/framework/isolation.go`)

* New value types `BlockerKind` (`BlockerStepComplete` / `BlockerNotices` /
  `BlockerStar`) and `StepBlocker{Kind, StepName, Count}`.
* `IsolationSpec.PermutationBlockers [][][]StepBlocker` — parallel to
  `Permutations`: `PermutationBlockers[p][i]` holds the markers attached to
  `Permutations[p][i]` (nil when none). Same outer/inner length as
  `Permutations`, so the runner can index in lockstep.
* `permTokenize` — a char-level scanner that emits `(`, `)`, `,` as standalone
  tokens, returns double-quoted identifiers unquoted, and otherwise splits on
  whitespace. This handles the glued `mystep(*)` form that a field split cannot.
* `parsePermutation(raw) ([]string, [][]StepBlocker)` — replaces
  `parsePermutationTokens`. Markers attach to the most recently seen step.
  `parseBlockerGroup` parses comma-separated markers between `(` and `)`.
* The multi-line permutation accumulator now joins continuation lines into one
  raw string before parsing, so a marker can attach across a line break.

The step list still **excludes** markers (pinned by the pre-existing
`TestParseIsolationSpecNoticesAnnotationStripped`).

### Runner (`internal/testport/framework/isolation_runner.go`)

* `sessionNoticeQueue` gains a monotonic `total` counter (`count()`), never
  reset by `drain()`, so "notices `n`" can be measured from a baseline.
* At step launch, `noticeBaselines` snapshots the referenced session's notice
  count for each `notices` marker.
* `waitForStepBlockers` blocks (bounded by `drainWindow`) until
  `blockersSatisfied` — i.e. each referenced session has emitted ≥ `Count`
  notices since the baseline. It is a **no-op when the step has no blockers**.
* The wait is invoked immediately before every site that reports a step's
  completion: the immediate-complete branch and all blocked/pending-drain sites
  (`drainWithTimeout`, `drainCompleted`, the same-session pre-launch drain, and
  the final drain loop). `drainWithTimeout`/`drainCompleted` gained `spec` +
  `queues` parameters to reach the helper.

Only `BlockerNotices` is *enforced*; `BlockerStepComplete` and `BlockerStar`
are parsed and stored but treated as already-satisfied (no ported spec
exercises them yet — adding enforcement is a localized follow-up in
`blockersSatisfied`).

## Verification

* `go test ./internal/testport/framework/` — parser/runner unit tests pass,
  including new `TestParsePermutationBlockers`, `TestParsePermutationBlockerKinds`,
  `TestParseIsolationSpecPopulatesBlockers`, and the unchanged
  annotation-stripping / drain tests.
* `TestPort_IsolationInsertConflictSpecconflict` — perm 5's NOTICE interleaving
  now matches; the diff advanced past the `s1_upsert`/`s2_upsert` notice region
  to the `controller_print_speculative_locks` step (expected L497+).
* Regression: `TestPort_IsolationEvalPlanQual`, `…InsertConflictDoNothing`,
  `…MergeUpdate` (notice-heavy specs that use the pending/drain paths) still
  PASS — confirming the `len(blockers)==0` gating leaves them untouched.

## Parts (a)/(b) — speculative locks surface through pg_locks ⋈ pg_stat_activity

Loop 2 (2026-06-13) made both `controller_print_speculative_locks` steps match
PG. The `spectoken`/`transactionid` rows were already emitted by
`internal/executor/spec_insert_registry.go`, but the controller's
`pg_locks pl JOIN pg_stat_activity pa USING (pid)` query returned 0 rows. Three
defects, all now fixed:

1. **Empty backend PID.** The registry stamped each synthetic `pg_locks` row with
   `ExecContext.ActivityPID`, a field deprecated to the empty string ("use ProcNum
   + Activity instead"). With `pid=""` the rows could never join `pg_stat_activity`.
   Fix: `activity.(*Registry).PIDForProcNum(procNum)` resolves the live PID from the
   per-backend slot, exposed via a new `ExecContext.backendPID()` helper that the
   three spec-registry call sites now use.

2. **`Activity` not wired into the executor context.** `backendPID()` still saw
   `ctx.Activity == nil` because `internal/server/dispatch.go` never set
   `ectx.Activity` (only `ProcNum`). Fix: `ectx.Activity = s.cfg.Activity` in the
   simple-query dispatch path (the path the isolation runner uses; it also enables
   the relation-lock WaitEvent markers, which the view still renders as NULL).

3. **`pg_stat_activity.pid` typed as `text`, `pg_locks.pid` as `int4`.** A
   text↔int4 `USING (pid)` join silently yields zero rows. In PostgreSQL both are
   `int4`. Fix: `internal/initdb/pg_stat_activity_view.go` types `pid`/`leader_pid`
   as `int4`; non-numeric internal pids (background workers such as `cp-0`) are
   emitted as NULL via `numericPIDOrNull` so the `int4` decode never fails. This is
   the kind of representation alignment the `align-data-structure-with-pg` branch
   targets and a cheap PG parity win (`psql \d pg_stat_activity` now shows
   `pid integer`).

The row **model** was also completed: every transaction holding an XID owns a
`transactionid ExclusiveLock` on its own XID. A pure waiter (blocked before its
own speculative insert) is absent from the `active` map, so `LockRows` now emits
the self-lock from each spec-/xid-waiter's `ownXID` (deduped against the active
entries). This yields PG's exact 4-row / 3-row prints.

### Verification (parts a/b)

* `TestPort_IsolationInsertConflictSpecconflict` — both `print_speculative_locks`
  steps now match (4 rows: s1/s2 × {spectoken, transactionid}; then 3 rows once
  s2 releases the token and s1 waits on s2's XID). Diff advanced from L496 to L533.
* `TestPort_PgStatActivity`, `…WaitEventsNull`, `TestSyntax_Catalog_PgStatActivity`,
  plpgsql/syntax-catalog pid scans, all dedicated isolation specs
  (`EvalPlanQual*`, `InsertConflictDoNothing`, `InsertConflictDoUpdate[2-4]`,
  `LockCommitted*`, `ReadWriteUnique`, `FkSnapshot`), `internal/initdb`,
  `internal/activity`, `internal/catalog`, `internal/executor` — all PASS.

## Remaining work (tracked under M0100-0006b)

The test still SKIPs, now for an **unrelated** reason: a +2-line offset that
cascades to EOF. After `s2_commit`, s1 wakes from the XID wait and completes its
`ON CONFLICT DO UPDATE`; goopg re-evaluates the **non-unique** index expression
`blurt_and_lock_4(key)` on that retry/update path, emitting two extra NOTICEs
(`blurt_and_lock_4() called …` / `acquiring advisory lock on 4`) that PG does not
emit at completion (PG evaluates it only once, during the speculative insert).
This is an executor index-maintenance issue on the ON CONFLICT update path, not
part of the speculative-lock infrastructure (parts a/b/c, now complete). See the
deferral ledger for the resume point.
