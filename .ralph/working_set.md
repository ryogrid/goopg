# Working Set (carried from loop 1, 2026-06-13)

## Completed this loop

**M0100-0006b part (c) — `(step notices N)` completion-blocker annotations** — DONE
- Isolation runner previously DROPPED parenthesised markers; now parses them.
- isolation.go: `BlockerKind`/`StepBlocker` types; `IsolationSpec.PermutationBlockers`
  (parallel to Permutations); `permTokenize` (char-level, handles glued `mystep(*)`);
  `parsePermutation` + `parseBlockerGroup` replace `parsePermutationTokens`.
- isolation_runner.go: `sessionNoticeQueue.count()` monotonic counter;
  `noticeBaselines`/`blockersSatisfied`/`waitForStepBlockers`; wait invoked before
  every step-completion-report site (immediate-complete, drainWithTimeout,
  drainCompleted, same-session pre-launch drain, final drain). Gated on
  `len(blockers)>0` so the 20 passing specs are unchanged.
- Design doc: docs/design/0100-0006b-isolation-notices-blocker-annotation.md (+ README row).
- Verified: perm-5 diff advanced from NOTICE-interleave region to
  `controller_print_speculative_locks` (L497). Test still SKIP (parts a/b gap).

## Next task (M0100-0006b parts a/b — remaining)

Make `controller_print_speculative_locks` return the spectoken/transactionid rows.
- spec_insert_registry.go ALREADY emits spectoken (s1 ShareLock waiter, s2 ExclusiveLock
  holder) + transactionid rows. But `pg_locks ⋈ pg_stat_activity USING (pid)` filtered by
  `application_name LIKE 'isolation/insert-conflict-specconflict/s%'` returns 0 rows.
- Investigate: (1) spec-token hold-window timing vs. when controller queries (s1 must be in
  speculative-wait, s2 holding token); (2) does the pg_locks row carry the backend pid, and
  does pg_stat_activity join it to application_name? Diff at expected L497+ (perm 5 only).

## Files of interest

- internal/testport/framework/isolation.go — parser + data model (loop 1)
- internal/testport/framework/isolation_runner.go — notices blocker wait (loop 1)
- internal/executor/spec_insert_registry.go — spectoken/transactionid pg_locks rows (parts a/b)
- internal/catalog/catalog.go:2149-2239 — pg_locks view; VirtualSpecLockRowsFunc

## Gates run

- go build ./... : PASS
- go vet ./internal/testport/framework/ : PASS
- framework unit tests : PASS (incl. new TestParsePermutationBlockers*)
- TestPort_IsolationInsertConflictSpecconflict : SKIP (diff advanced to L497, parts a/b gap)
- Regression: EvalPlanQual / InsertConflictDoNothing / MergeUpdate : PASS
- TPC-H spotcheck: N/A (test-framework-only change; no executor/planner/codec touch)
