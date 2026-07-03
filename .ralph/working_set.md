(idle — nothing in flight)

DU-002 slice 436 (`COMMENT ON <object>` now raises PostgreSQL's own
`does not exist` SQLSTATE/message instead of a silent no-op, M0110-0001)
landed, tested, documented, and committed+pushed this loop. Closed the
systemic deferral recorded since slice 386 in one pass across all ~19
execCommentOn object kinds. All gates green (build/vet, internal/executor
+internal/parser+internal/catalog suites, TestPort_PgDumpConnectionSetup,
tpch-spotcheck Q12=2/Q13=33, pgbench pre-commit hook, ralph-state-guard).
One small pre-existing/DROP-FUNCTION-shared deferral noted (function
comment-not-found message doesn't schema-qualify the name) — ledger row
appended, not a blocker.

Next step: resume M0110-0001 DU-002 slice-by-slice pg_dump gap sweep —
run TestPort_PgDumpConnectionSetup, do a fresh candidate sweep (parser
grammar vs pg_dump getter battery) for the next uncovered gap the same way
slices 434/435/436 were found.
