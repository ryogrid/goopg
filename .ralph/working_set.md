(idle — nothing in flight)

Loop #42 landed and committed: fixed the VARIADIC function call-site
argument-collapsing gap recorded as an open item in loop #41's 2026-07-15
VARIADIC-array signature-matching deferral-ledger row. `SELECT
sum_variadic(1, 2, 3)` against `CREATE FUNCTION sum_variadic(VARIADIC arr
integer[]) ...` failed `function ... does not exist` — a materially
different mechanism from loop #41's DDL identity-resolution fix (this is
call resolution). Root cause: `resolveRoutineOverload`
(`internal/executor/plpgsql_runtime.go`, sole call-resolution path for
`evalStoredRoutineFuncCall`) required exact `len(c.ArgTypes)==len(args)`,
with zero VARIADIC awareness — unlike `operators_call.go`'s `callOp.Open`
(`CALL <procedure>(...)`), which already had VARIADIC-aware count matching
+ array bundling (M0097-0022) on its own, entirely separate code path.

Fix: new `callArgTypesForCandidate` (accepts `n >= variadicPos` call-arg
counts when the routine's last ArgMode is "v", type-checks excess positions
against the VARIADIC parameter's element type — stripped `"[]"` suffix,
since `Routine.ArgTypes[i].Name` bakes the array suffix directly into the
string per the established storage convention) + `bundleVariadicArgs`
(collapses trailing args into one array `Datum` via the existing
`buildArrayDatum` helper before dispatch, since every dispatch path —
`executeSQLRoutine`/`executePLpgSQLRoutine` — binds `args[i]` to
`r.ArgTypes[i]` positionally). Also hardened `evalStoredRoutineFuncCall`'s
"use CALL not SELECT" error branch with an index guard (first path that can
reach it with `len(x.Args) > len(r.ArgTypes)`).

Files: internal/executor/plpgsql_runtime.go (resolveRoutineOverload +
2 new helpers + evalStoredRoutineFuncCall), internal/executor/
variadic_call_test.go (new — 2 tests, plpgsql + sql language routines),
docs/design/0119-0004-variadic-call-argument-collapsing.md (new) +
README.md index row 0119-0004dc, .ralph/fix_plan.md (new [x] entry after
the VARIADIC-array signature-matching item), .ralph/deferral_ledger.md
(new `resolved` row closing the open item).

Key symbols: resolveRoutineOverload, callArgTypesForCandidate (new),
bundleVariadicArgs (new), evalStoredRoutineFuncCall, executeSQLRoutine,
executePLpgSQLRoutine (all in plpgsql_runtime.go); buildArrayDatum
(operators_call.go, reused).

Nightly triage: `ci/logs/action-items.md` run 20260715-010036 was already
fully triaged and closed by prior loops (all 11 AI items resolved, see
fix_plan.md's "M-NIGHTLY triage — run 20260715-010036" block) — queue was
empty at this loop's start, no new items to add.

Next DU-002 resume point (milestone-scale, not one-loop): per-database
catalog namespace — thread DBOid through `catalog.InMemory`'s remaining
server-wide object stores (`c.tables`/`c.schemas`/etc. have no partition
key at all) so pg_dump restore into a fresh, empty database doesn't
collide with the original database's own objects. See the 2026-07-06
deferral-ledger row for the established 4e-series pattern to extend. This
is now the ONLY thing still blocking the DU-002 round-trip probe further —
the function/routine signature-matching AND call-resolution sub-series are
both exhausted as of this loop.

Housekeeping note (unchanged from loop #41, re-verified): a concurrent
interactive `claude` session (pid 872994, pts/20, started loop #41) is
still running in this same working tree and has now ALSO produced a new
untracked `tools/mdtablefix/` directory alongside the pre-existing
`Markdown_Table_Repair_Design_Doc.md` (both unrelated Python markdown-
table-repair tooling, not this loop's work). Left untouched again — do not
delete without checking with the user first; excluded from this loop's
commit via explicit pathspec (only .ralph/*, docs/design/*, and
internal/executor/{plpgsql_runtime.go,variadic_call_test.go} were staged).
Also left untouched: `analysis/tpch-explain-baseline.md` and
`ci/logs/launch.log`, both modified since before this loop started (not by
this loop's work) — excluded from the commit for the same reason.

Gates run this loop (all green): `go build ./...`/`go vet ./...` clean
repo-wide; `go test ./internal/executor/... ./internal/catalog/...
./internal/parser/...` PASS; `go test -short $(go list ./... | grep -v
/internal/testport)` (full repo, short mode, 0 FAIL, ~230s incl.
internal/initdb's 228s); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS (0
failed, all 3 workloads); `make ralph-state-guard` clean (auto-repaired the
same benign stale-progress-marker pattern as loops #36-#42).

In-flight: none. git push status (unrelated to this loop, carried from
loops #38-#41): local `wal-format-mod` was ahead of `origin/wal-format-mod`
by increasing amounts each loop; this loop adds one more commit on top. Do
NOT attempt to auto-resolve any push conflict — loop #38's working_set
(readable via `git log`) already spelled out 3 resolution options for the
user; wait for explicit human direction before any rebase/force-push.
