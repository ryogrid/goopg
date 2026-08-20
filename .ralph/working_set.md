(idle — nothing in flight)

Last completed: M0134-0034 (insert_conflict.sql) PARKED and committed
(14b95ecc) — resolveArbiterIndex now uses PG's exact-set ON CONFLICT arbiter
matching (bms_equal plain-column equality + parserExprStructEqual expression
equality), 539 -> 422 diff lines, 9 -> 6 ^-ERROR. One new ^+ERROR ledgered
(planOnConflict check-ordering gap, out of scope). Design
docs/design/m0134-0034-arbiter-index-exact-set-match.md; four remaining
buckets ledgered in .ralph/deferral_ledger.md.

Next loop: per fix_plan.md banner, select M0134-0035 (interval.sql, status
`failed`) — same sizing pattern as 0006..0034 (researcher sizes at HEAD first,
confirm not stale, bucket root causes CONTAINED vs REFACTOR-tier, ship the
smallest CONTAINED bucket or PARK with ledger rows).

Gates run this loop: RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh
PASS; go build/test PASS; pg-regress-runner insert_conflict PASS (net
improvement, see above); pgbench pre-commit smoke PASS on commit.

In-flight: none.
