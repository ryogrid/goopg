Task just completed: M0134-0131 (infinite_recurse.sql) — FULL PASS, not a
PARK. 100% parity (0% → 100%).

`scripts/pg-regress-runner.sh infinite_recurse`: was 0% parity ("server
closed the connection unexpectedly" — a Go process crash). Root cause: a
self-recursive `LANGUAGE sql` function (`create function infinite_recurse()
as 'select infinite_recurse()'`) recursed unbounded through
`evalStoredRoutineFuncCall` → `executeSQLRoutine` → `optimizer.Plan`/`Build`
→ `evalFuncCall` → back to `evalStoredRoutineFuncCall`, smashing the Go
goroutine stack. A Go "stack overflow" is a FATAL, unrecoverable process
crash (not a catchable panic via recover()) — it takes the whole server
down, unlike PG's clean `54001 stack depth limit exceeded` (PG's
`check_stack_depth`, `postgres/src/backend/utils/misc/stack_depth.c`, polls
the C stack pointer on every function call — goopg has no equivalent
primitive since Go doesn't expose a cheap current-stack-depth read).

Fixed: added `Context.RoutineDepth` (`internal/executor/context.go`),
incremented/checked/decremented around every `executeStoredRoutine` call
(`internal/executor/plpgsql_runtime.go`, the single dispatch point both
`executeSQLRoutine` and `executePLpgSQLRoutine` go through) — capped at
`maxRoutineCallDepth=2000`, raising the same `54001`/"stack depth limit
exceeded" PG emits. Both LANGUAGE sql and plpgsql build their child
`Context` via `*child = *ctx`, so the running depth threads through
recursion by value at every level, mirroring a real stack depth without
any extra plumbing needed.

Design `docs/design/m0134-0131-routine-call-depth-guard.md`, indexed in
README.md. Ledger row: `.ralph/deferral_ledger.md` 2026-08-24 M0134-0131
(the guard only covers the executeStoredRoutine call path — other
potential unbounded-recursion sites, e.g. trigger recursion, remain
unguarded; apply the same ctx.RoutineDepth++ / defer decrement / threshold
pattern if one is found to actually crash). CSV flipped `not-tried` →
`pass`/`pass_required=yes` via `make regen-testport`. fix_plan.md
M0134-0131 marked [x] with full summary.

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0132 — init_privs.sql**. Size it live first
per the established pattern (run pg-regress-runner, read the diff, check
whether the root cause is a shared/already-tracked blocker before assuming
fresh work).

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
12. inet.sql (M0134-0130) left 11 pg_proc-seeded-but-undispatched scalar
    functions (host/abbrev/broadcast/network/masklen/netmask/hostmask/
    inet_merge/inet_same_family/cidr()/inet()) — low-effort follow-on
    wiring in evalFuncCall, following evalHashFunc's pattern exactly.

Gates run this loop: scripts/pg-regress-runner.sh infinite_recurse (0/1 →
1/1, 100% parity after the fix); go build ./... PASS; go test
./internal/executor/... PASS; scripts/tpch-spotcheck.sh PASS (Q12=2 rows
18.4s, Q13=35 rows 7.3s, 27.7s query-phase wall); RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh PASS (all packages, cold internal/initdb
435s + cmd/goopg 76s, rest cached); make check-testport-inventory PASS;
make regen-testport PASS; pre-commit hook's pgbench smoke ran automatically
at commit time and PASSED (TPC-B 340 TPS, simple-update 622 TPS, select-only
12549 TPS — all zero failed transactions); make ralph-state-guard: found the
same benign stale clean-exit-marker status/progress mismatch as prior loops,
auto-repaired to progress=in_progress.

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree again this
loop (.ralphrc, analysis/*, ci/logs/launch.log, ci/logs/scheduler.log,
docs/wiki/*, internal/executor/operators_recursive_cte.go, postgres
(untracked convenience symlink), third-party/tpcds-postgres, plus untracked
files analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt,
docs/wiki/modules/catalog.md) and was deliberately left untouched/
uncommitted — only this loop's own files were staged and committed by
explicit pathspec.

M-NIGHTLY: re-checked at loop start — `ci/logs/action-items.md` run
20260824-013441 (2 items) was already filed in fix_plan.md by a prior loop
(confirmed via grep for the run ID at fix_plan.md:1303); nothing new to
file this loop.
