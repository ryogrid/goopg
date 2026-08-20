Task: M0134-0059 (rangefuncs.sql) — PARTIAL this loop, landed & committed
(c3ce58b8). Case stays `failed`. CSV row unchanged. Next: select
M0134-0060 (rangetypes.sql).

Files this loop: `internal/executor/plpgsql_runtime.go`
(`evalSQLFunctionSetof` — SQL-language `RETURNS SETOF <composite-type>`
functions were collapsing multi-column result rows to `row[0]` only;
fixed by packing them into composite text via `rowToCompositeText` so
`userSrfScanOp`/`decomposeCompositeText` can decompose on read),
`internal/executor/ordinality_composite_srf_test.go` (new,
`TestOrdinalityCompositeSRFSelectStar`), `.ralph/deferral_ledger.md` (new
row, M0134-0059 breakdown of remaining gaps), `.ralph/fix_plan.md`
(M0134-0059 entry rewritten with PARTIAL verdict + next-task pointer).

Key symbols: `evalSQLFunctionSetof` (plpgsql_runtime.go) — the actual bug
site; NOT `userRoutineColumnSchema`/`wrapOrdinality`
(internal/optimizer/planner.go) or the `expr.go:390` slot-width guard —
those were red herrings the researcher's sizing pass flagged as candidate
files but all three were confirmed correct on inspection; the guard at
expr.go:390 was working as designed and is what surfaced the bug.

Hypothesis/Findings: rangefuncs.sql's diff was ~2330 lines almost
entirely because the goopg server CRASHED partway through the file
(`server closed the connection unexpectedly`, no panic in the structured
slog) on `CREATE TEMPORARY VIEW ... JOIN rngfunct(1) WITH ORDINALITY ...
ON (n=ord)`, truncating everything after (~2280 of the 2330 lines were
"missing due to truncation" not independent mismatches). Root cause
traced to ONE bug: `evalSQLFunctionSetof` fed `userSrfScanOp` malformed
single-column rows for every composite-returning SETOF tuple. Fixing it
BOTH resolved the preceding "out of Slot range" error AND made the crash
trigger itself succeed cleanly (same root cause, confirmed live).
Re-diffing post-fix (2524 lines now, further into the file, no longer
crash-truncated) surfaces the file's OTHER independent gaps, first of
which: `select a,b,ord from rngfunct(1) with ordinality as z(a,b,ord)`
(explicit `ord` column NAME in the SELECT list, not `select *`) still
raises `column "ord" does not exist` — a distinct, un-landed bug, related
to but not the same as the already-ledgered M0122-0002 scalar-SRF
ordinality-naming gap. Other remaining buckets (JSON constructors —
already ledgered M0134-0038; pg_views; ROWS FROM column-flattening;
table-valued-function column-alias-list parsing; OUT-param RETURNS
inference; whole-row-Var lateral args) are each their own gap, none sized
in detail this loop.

Next step: select **M0134-0060 (rangetypes.sql)** per the fix_plan
task-ID-ascending selection rule. Size it via `scripts/pg-regress-runner.sh
--verbose rangetypes` (delegate to researcher) before deciding
fix/split/park, same pattern as M0134-0049..0059.

Gates run this loop: `go build ./...` PASS; `go test ./internal/executor/...
./internal/optimizer/...` PASS (implementer, targeted); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (via tester, ~421s cold internal/initdb
+ 73s cmd/goopg, rest cached — expected on fresh/switched tree); pre-commit
pgbench smoke PASS (376/694/12935 TPS across the 3 builtin scripts, 0
failed transactions); live regress re-diff via `pg-regress-runner.sh
--verbose rangefuncs` confirmed the fix's effect and the case's continued
`failed` status; `make ralph-state-guard` PASS (auto-repaired a stale
status/progress mismatch from the previous loop's clean-exit marker, same
pattern as last loop).

Delegation: researcher agent `a0ee295ef7d2217ea` (1 round, sizing from
cached CI diffs, found the crash-truncation pattern + Bucket 1/2/3
breakdown — accepted). implementer agent `a6aede0bc03ac82c3` (1 round,
traced the brief's 3 candidate files, found none of them was the actual
bug, correctly identified the real root cause one layer below
(plpgsql_runtime.go) and fixed it there, also verified the crash-fix side
effect live — DONE, no further round needed, flagged the scope deviation
clearly in its report per protocol). tester agent `a3286273c47ed44c7`
(precommit units gate, PASS). tester agent `a7a5990cdce080536` (live
regress re-diff verification, confirmed case stays `failed` with a
smaller/different remaining diff).

In-flight: none. Commit `c3ce58b8` pushed to `regress-renumbering`. No
server left running (regress runner + pgbench smoke + package tests all
self-start/stop their own throwaway goopg instances via the cgroup
wrapper). Handoff dir `tmp/ralph-handoffs/m0134-0059-rangefuncs-ordinality/`
and `tmp/regress-diffs/rangefuncs.diff` cleaned up (scratch, folded into
the ledger/fix_plan already).
