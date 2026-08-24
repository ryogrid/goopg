Task just completed: M0134-0152 (polymorphism.sql) — sized live, PARKED
sizing-only, NO code change this loop. Committed.

What landed: polymorphism.sql (0% parity, 2293-line diff) was sized live
(`scripts/pg-regress-runner.sh --verbose polymorphism`), then a dedicated
Explore-agent codebase survey confirmed the gap is a genuine multi-file
subsystem, not a single-loop-scoped narrow fix — so nothing was landed
rather than shipping a half-scoped fragment (consistent with "no
placeholder implementations"). Five independent, real gaps found (full
file:line citations in `.ralph/deferral_ledger.md` 2026-08-25 M0134-0152):
1. Polymorphic-argument-resolution for anyelement/anyarray/anynonarray/
   anyenum/anyrange/anymultirange/the anycompatible* family is essentially
   unimplemented for SQL/plpgsql function calls. `pg_proc` stores declared
   pseudo-type strings verbatim; `executeSQLRoutine`
   (`internal/executor/plpgsql_runtime.go:526-625`) coerces args to the
   literal declared name. The one real resolver
   (`resolvePolymorphicReturnType`/`hasPolymorphicArgType`,
   `plpgsql_runtime.go:1174-1185`/`:2781-2825`) handles only 2 of 11
   pseudo-types (anyelement/anyarray) and only fires on an error-message
   branch (`:540`), never on real successful calls.
2. No SQL-function-body inlining (goopg's CONTEXT always reads "statement
   1", never PG's "during inlining"; `wrapSQLFunctionContext`,
   `plpgsql_runtime.go:19`).
3. Range/multirange constructor functions (`int4range(...)`,
   `numrange(...)`, `multirange(...)`) are not callable — no
   `evalFuncCall` dispatch case exists, and goopg has no Datum
   range-value model at all (type-only today).
4. `CREATE FUNCTION`-time polymorphic-return-type validation is missing
   in `execCreateFunction`/`operators_ddl.go`.
5. `pg_statistic` base table is not registered as queryable — only the
   derived `pg_stats` view is (`catalog.go:9417-9441`) — even though a
   REAL per-database heap already physically exists at RelOid 2619
   (written by `persistStatsToPGStatistic`,
   `internal/executor/operators_analyze.go:315-353`). Investigated
   extending the existing pg_type/pg_attribute startup-registration
   pattern (`internal/initdb/open.go:2795` `loadSystemCatalogsIfPresent`)
   but that pattern is DefaultDBOid/startup-time-only, while
   pg_statistic's heap is written lazily PER connected database by
   ANALYZE — a correct fix needs the same per-connection dynamic
   swap-in `pg_stats`/`fetchStatsRows` already uses
   (`internal/executor/pgstat_tables.go:42`), not a copy-paste. Did NOT
   land a half-correct (DefaultDBOid-only) version under time pressure.

CSV: polymorphism.sql flipped `not-tried` → `failed` (via `make
regen-testport`; note the CSV rationale field needed doubled `""` quote
escaping, not backslash `\"` — Go's encoding/csv rejects `\"`).
fix_plan.md M0134-0152 marked PARKED with full findings summary. No
design doc this loop (no subsystem code changed — pure sizing/survey/
ledger work).

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom
— next unworked item is **M0134-0153 (float4.sql)**, already `failed`
status (not `not-tried`) so likely has a live prior diff to re-check
first before assuming full re-sizing is needed.

Standing recommendation carried across many loops (unchanged from last
loop except item #33 added):
1. GIN/GiST/SPGiST physical-index plan integration — Seq Scan not
   Index/Index-Only Scan because the AM is catalog-only.
2. btree v0 opclass generality (`internal/executor/operators_ddl.go:15810`
   `isSupportedBTreeKeyType` + `btree_scalar_keys.go`) — confirmed 8+ times.
3. Memoize plan-node type — entirely unimplemented (M0134-0141).
4. Real parallel-worker query execution — recurs across M0134-0008/-0023/
   -0141.
5. Geometry type-system gap — DONE for all 7 core primitives. The
   operator-lexer family (`<<`/`&<`/`&&`/`&>`/`>>`/`<<|`/`&<|`/`|&>`/
   `|>>`/`<@`/`@>`/`~=`/`<->`/`?-`/`?|`/`?#`/`@@`/`#`) remains open.
6. LANGUAGE C dynamic-extension loading gap.
7. Collation-execution-registry gap (5 parked files).
8. BETWEEN-vs-comparison-operator precedence bug (M0134-0113).
9. RAISE INFO/LOG/DEBUG collapse to hardcoded NOTICE wire severity.
10. `::json` cast DETAIL/CONTEXT truncation text (json_errdetail port).
11. pg_shdepend-shaped object-enumeration/CASCADE engine — confirmed 5
    times. Single most-recurring blocker across M0134.
12. `CREATE CONVERSION`-registered procs never consulted by convert_from/to.
13. DDL-event-trigger firing engine + `session_replication_role` GUC.
14. `NonSuperuserRole != ""` "is superuser" convention wrong for
    non-"postgres" superuser roles.
15. inet.sql (M0134-0130) left 11 undispatched scalar functions.
16. pg_init_privs (M0134-0132) is a reconstruction, not a real snapshot.
17. jsonpath's own grammar entirely unimplemented.
18. Full PostgreSQL Large Object facility (M0134-0135) — own milestone.
19. `coerceTextLikeDatum` never threads `ExecError.Pos` — psql LINE echo
    gap, confirmed 7 times on the geometry family.
20. `evalCast`'s catch-all pass-through hides real validation gaps for
    box/circle/line `::T` casts.
21. `DropTable` on a PARENT never scrubs `inheritanceChildren`/
    `partitionChildren` (only fixed for the child side, M0134-0140).
22. LATERAL outer-column-ref bug (memoize.sql bonus discovery).
23. No generic system-catalog TOAST-table registration.
24. `money`/`cash` type entirely unimplemented (M0134-0143).
25. CREATE SCHEMA sub-element execution gap — blocks 3+ files.
26. `DROP OWNED BY` has zero parser AST node (blocked on #11).
27. `CREATE PUBLICATION ... FOR TABLES IN SCHEMA` unparsed.
28. pg_proc's provariadic/prosqlbody/proargdefaults still not real.
29. `Routine.ArgTypes` conflates IN-only and ALL-args.
30. `tryHandleRoleDDL` has no wire-protocol notice sink.
31. pg-regress-runner.sh's prerequisite block is not schedule-group-aware.
32. `circle`/`point`/`polygon` scalar CONSTRUCTOR functions unregistered.
33. **NEW (M0134-0152):** Polymorphic function type resolution
    (anyelement/anyarray/anycompatible* family) is essentially
    unimplemented for real function calls — see deferral ledger row
    M0134-0152 for the full 5-item breakdown (also covers SQL-function
    inlining, range/multirange constructor functions + value model,
    CREATE-FUNCTION-time polymorphic validation, and pg_statistic
    base-table registration). STRONG candidate for its own dedicated
    milestone — do not attempt a fragment of this without first scoping
    it as multi-loop work; a same-loop half-fix (e.g. pg_statistic
    registration DefaultDBOid-only) was deliberately NOT taken because it
    would silently misbehave for non-default databases.

Gates run this loop: `make regen-testport` (failed once on a CSV
`\"`-vs-`""` quoting bug I introduced, fixed, then PASS); `make
check-testport-inventory` PASS; `make ralph-state-guard`: found+repaired
1 stale-marker inconsistency (same shape as prior loops), then verified
consistent. No go build/test changes this loop (no Go source touched) —
pre-commit hook's mandatory pgbench smoke ran automatically on `git
commit` and PASSed (346/648/12953 TPS across the 3 pgbench modes).
scripts/tpch-spotcheck.sh NOT run this loop (no executor/planner/codec
code changed — doc/ledger-only commit, so the practice-card gate doesn't
apply; only the mandatory pgbench smoke gate does).

In-flight: none.

Note: a concurrent peer session's WIP may still be present in the tree
(.ralphrc, analysis/postgres-oracle-compatibility-report.md,
ci/logs/launch.log, docs/wiki/getting-started.md,
internal/executor/operators_recursive_cte.go, postgres (untracked
convenience symlink), third-party/tpcds-postgres,
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt,
docs/wiki/modules/catalog.md) — deliberately left untouched/uncommitted;
only this loop's own files were staged and committed by explicit pathspec.
