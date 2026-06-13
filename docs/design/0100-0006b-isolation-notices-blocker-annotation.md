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

## Remaining work (tracked under M0100-0006b)

The test still SKIPs: `controller_print_speculative_locks` returns no rows in
goopg, while PG shows the `spectoken` ShareLock (s1 waiter) and `spectoken`
ExclusiveLock (s2 holder) via `pg_locks ⋈ pg_stat_activity USING (pid)` filtered
by `application_name`. The `spectoken`/`transactionid` rows are emitted by
`internal/executor/spec_insert_registry.go` but are not surfacing through that
join at the moment the step runs (timing of spec-token hold window vs. the
controller's query, and/or the `pg_locks`↔`pg_stat_activity` pid/application_name
linkage). That is parts (a)/(b) integration of M0100-0006b — see the deferral
ledger entry for the resume point.
