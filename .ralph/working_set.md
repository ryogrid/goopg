Task just completed: M0134-0129 (indirect_toast.sql) — sized live, PARKED,
one major contained fix shipped (a real data-corruption bug, not a display
bug).

`scripts/pg-regress-runner.sh indirect_toast`: 0% parity, diff 184→25 lines
(86% reduction). Discovery: `UPDATE ... SET f1 = '-'||f1||'-'` on a TOASTed
column evaluated the RHS against `scanMatching`'s raw (never-detoasted) row
(`internal/executor/operators_storage.go`), so the concatenation
stringified the 12-byte `KindToastPointer` datum via `AppendValueText`'s
`?datum kind=N?` fallback and durably wrote that garbage back as the
column's real new value — confirmed via VACUUM FREEZE + fresh SELECT that
this was on-disk corruption, not just a RETURNING-display artifact.

Fixed four sites (all `internal/executor/operators_storage.go`):
1. `scanMatching`'s WHERE-predicate eval — lazily-detoasted eval-only row.
2. `updateOp`'s non-inherit-child SET-expression loop — same pattern; the
   unset-column passthrough branch still copies the RAW `row[i]`, so an
   untouched out-of-line column keeps its existing TOAST pointer instead of
   being needlessly re-toasted (matches PG's behaviour — the exact
   invariant this test file is named for).
3. `updateOp`'s inheritance-child branch — detoast BEFORE
   `remapChildRowToParent` (pointers are scoped to the child's own TOAST
   relation).
4. `updateOp.appendUpdateRetRowWithFrom` (RETURNING projection) — detoast
   `newRow` before evaluating `o.plan.Returning`; this is what fixed the
   *first* UPDATE in the file (no SET-clause touching f1/f2 at all).

Second-order regression found+fixed mid-loop: once SET-expr results carry
their real (large) value, `tryApplyHOTUpdate`'s in-place HOT-update path
had zero re-toast step (unlike `writeHeapRowReturning`) and hit `ERROR:
tuple too large for line pointer` — added the missing
`ToastLargeColumnsIfNeeded` call there too.

New test: `TestToastUpdateSetSelfReferenceDetoasts`
(`internal/executor/toast_update_selfref_test.go`).

Design `docs/design/m0134-0129-toast-update-selfref.md`, indexed in
README.md. Ledger row: `.ralph/deferral_ledger.md` 2026-08-24 M0134-0129.
CSV flipped `not-tried` → `failed` via `make regen-testport`. fix_plan.md
M0134-0129 marked [x] with full summary.

Deferred (ledger row, resume point recorded): the SAME TOAST-self-reference
bug class was fixed ONLY for the plain (non-FROM, non-index-probe,
may-be-inherit-child) `updateOp` scan path. `updateOp.updateWithFrom`
(UPDATE ... FROM), `updateOp.updateViaIndex` (single-key B-tree probe), and
`deleteOp`'s scan callbacks each have differently-shaped row-handling code
that was NOT audited this loop — a `DELETE ... WHERE substring(toasted_col,
...) = ...` or `UPDATE ... FROM ... SET toasted_col = toasted_col ||
other` likely hits the identical bug today. Resume: grep
`operators_storage.go` for `evalExpr(o.plan.Set[` (the `updateWithFrom`
occurrence) and the `deleteOp` scanMatching callbacks; apply the same
lazily-detoasted-eval-row pattern.

Remaining 25-line diff in indirect_toast.sql itself is the file's
`make_tuple_indirect` LANGUAGE C helper — unchanged standing blocker
(M0134-0106/-0116/-0120 dynamic-extension-loading gap).

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0130 — inet.sql**. Size it live first per the
established pattern (run pg-regress-runner, read the diff, check whether
the root cause is a shared/already-tracked blocker before assuming fresh
work). Also worth a quick look: given this loop found the TOAST-self-
reference bug class is NOT limited to indirect_toast.sql, a future loop
could productively spend a slot auditing `updateWithFrom`/`updateViaIndex`/
`deleteOp` directly (see Deferred above) rather than waiting for another
regress file to surface it.

Standing recommendation, carried across several loops (unchanged this loop):
1. **GIN/GiST/SPGiST physical-index plan integration** — confirmed across
   THREE files (gin.sql M0134-0126, create_index_spgist.sql M0134-0111,
   gist.sql M0134-0127) — every predicate on any of these three index AMs
   EXPLAINs Seq Scan not Index/Index-Only Scan because the AM is
   catalog-only. Strongest candidate for a dedicated milestone.
2. Geometry type-system gap (point/lseg/line/path/polygon typed-literal
   parsing + operator lexer family) — box.sql/circle.sql/geometry.sql/
   gist.sql shared blocker, resume points in
   `docs/design/m0134-0125-geometry-sizing.md`.
3. LANGUAGE C dynamic-extension loading gap — recurs across M0134-0106,
   -0116, -0120, -0129, create_operator/create_type adjacent files.
4. Collation-execution-registry gap recurs across FIVE parked files
   (M0134-0099/-0100/-0101/-0102).
5. BETWEEN-vs-comparison-operator precedence bug (M0134-0113) — silently
   wrong for ANY query today, cross-cutting.
6. RAISE INFO/LOG/DEBUG collapse to hardcoded NOTICE wire severity
   (M0134-0113, ~18 call sites in plpgsql_runtime.go).
7. `::json` cast DETAIL/CONTEXT truncation text (json_errdetail port) —
   M0134-0120, unfixed.
8. pg_shdepend-shaped object-enumeration/CASCADE engine — CONFIRMED A
   THIRD TIME (M0134-0124), after M0134-0117/-0118. Single most-recurring
   blocker across M0134; strong candidate for its own milestone. Resume:
   `catalog.InMemory.RoleDropDependencyDescriptions`
   (`internal/catalog/catalog.go`).
9. `CREATE CONVERSION`-registered procs never consulted by convert_from/
   convert_to (M0122-0008, M0134-0121).
10. DDL-event-trigger firing engine + `session_replication_role` GUC
    (M0134-0122/-0123) — second-most-recurring blocker.
11. `NonSuperuserRole != ""` "is superuser" convention is wrong for any
    non-"postgres"-named `CREATE ROLE ... SUPERUSER` role — worth a
    dedicated sweep.

Gates run this loop: scripts/pg-regress-runner.sh indirect_toast (sizing
run, 0/1, before and after the fix — 184→25 diff lines); minimal repro via
a throwaway cgroup-capped server + psql (verified each of the four fix
sites individually before running the full regress case); go build ./...
PASS; go test ./internal/executor/... PASS (includes 1 new test func);
scripts/tpch-spotcheck.sh PASS (Q12=2 rows 20.4s, Q13=35 rows 7.6s, 29.8s
query-phase wall); RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh
PASS (all packages, cold internal/initdb 428s + cmd/goopg 75s, rest cached);
make check-testport-inventory PASS; make regen-testport PASS; pre-commit
hook's pgbench smoke ran automatically at commit time and PASSED (TPC-B
337 TPS, simple-update 623 TPS, select-only 12446 TPS — all zero failed
transactions); make ralph-state-guard: found the same benign stale
clean-exit-marker status/progress mismatch as prior loops, auto-repaired to
progress=in_progress.

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree again this
loop (.ralph/progress.json, .ralphrc, analysis/*, ci/logs/launch.log,
ci/logs/scheduler.log, docs/wiki/*,
internal/executor/operators_recursive_cte.go, postgres (untracked
convenience symlink), third-party/tpcds-postgres, plus untracked files
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt) and was
deliberately left untouched/uncommitted — only this loop's own files were
staged and committed by explicit pathspec.

M-NIGHTLY: re-checked at loop start — `ci/logs/action-items.md` run
20260824-013441 (2 items) was already filed in fix_plan.md by a prior loop
(confirmed via grep for the run ID at fix_plan.md:1303); nothing new to
file this loop.
