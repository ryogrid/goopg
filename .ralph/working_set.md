(idle — nothing in flight)

Loop #21 landed and pushed M0119-0004 pg_dump slice 446 (`CREATE TEXT SEARCH
CONFIGURATION` + `ALTER ... ADD MAPPING` round-trip), which loop #19/#20 had
implemented but left uncommitted. Commit `d7092f9b` on
`align-data-structure-with-pg` (pushed to origin). Gates this loop: `go build
./...` clean; `go test -count=1 -run TestPort_PgDumpConnectionSetup
./internal/testport/ -v` PASS; `go test -count=1 ./internal/catalog/...
./internal/parser/... ./internal/executor/... ./internal/planner/...
./internal/analyzer/...` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
pgbench pre-commit smoke PASS (0 failed transactions across all 3 workloads).
Also added the missing `docs/design/README.md` slice-446 addendum (row 571,
0110-0001) that loop #19/#20 had omitted — the design-doc-index-update rule
requires this in the same commit as a design-doc change.

Next candidate (not started): pick the next pg_dump slice per the M0119-0004
series — natural candidates surfaced by slice 446's own "deliberately left out"
list are ALTER-form completions (ALTER MAPPING REPLACE/DROP MAPPING/RENAME
TO/SET SCHEMA for TEXT SEARCH CONFIGURATION) or a fresh probe of
`CREATE TEXT SEARCH TEMPLATE`/`CREATE TEXT SEARCH PARSER` (both still
CompatNoopStmt-discarded — check `docs/test-port/postgres-oracle-port-status.csv`
and re-scan `.ralph/fix_plan.md` for current priority first, since a live
Ralph loop may have already picked something up since this snapshot).
