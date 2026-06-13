# Deferral Ledger

One line per deferral. Never close a task silently with a forward reference —
record what landed, what was deferred, and where to resume.

| date | task-id | landed | deferred | resume point | why |
|------|---------|--------|----------|--------------|-----|
| 2026-06-13 | M0100-0006b | part (c): `(step notices N)` blocker parsing + runner wait (`waitForStepBlockers`, `PermutationBlockers`); perm-5 NOTICE interleave now matches | parts (a)/(b): spectoken/transactionid rows not surfacing in `controller_print_speculative_locks` | `internal/executor/spec_insert_registry.go` emits spectoken/transactionid rows, but the `pg_locks ⋈ pg_stat_activity USING (pid)` join (filtered by application_name) returns 0 rows at the controller's query moment — investigate (1) spec-token hold-window timing vs. the query, (2) whether pg_locks rows carry the correct pid and pg_stat_activity links it to application_name. Diff at expected L497+. | spectoken pg_locks reporting integration is a separate deeper gap from the runner annotation; bounded scope this loop = part (c) |
