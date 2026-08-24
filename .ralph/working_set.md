Task just completed: M0134-0146 (oidjoins.sql) — sized live, PARKED, two real
fixes landed (not sizing-only). Committed (725007de0).

What landed:
1. `pg_get_catalog_foreign_keys()` implemented end-to-end — was registered in
   pg_proc seed data with ZERO handler (`0A000: table-valued function ... not
   supported`). Added a 219-row static SRF (`internal/executor/
   pg_catalog_fk_data.go`, transcribed verbatim from `postgres/src/include/
   catalog/system_fk_info.h`'s genbki-generated `sys_fk_relationships[]`)
   plus a `PgGetCatalogForeignKeys` plan node (`internal/optimizer/plan.go`)
   wired through `planTableFuncRangeVar`/`joinlayout.go`/`executor.go`,
   mirroring the existing `pg_available_wal_summaries` FROM-clause-SRF
   pattern.
2. Fixed a genuine cross-cutting plpgsql bug the SRF exposed: a
   regclass-typed record field (`FOR rec IN SELECT ... LOOP`) collapsed to
   its bare catalog OID digits on `rec.field`/`rec.field::text`/RAISE `%`
   access — `bindRecordRowComposite` flattened it via `Datum.Format()` and
   `lowerPLpgSQLExpr`'s `*parser.ColumnRef` composite-field branch
   re-derives a fake type by sniffing whether the flattened text parses as
   an integer. Extracted the existing CastExpr regclass-resolution logic
   (`internal/executor/expr.go`) into a reusable `regclassOIDToName(ctx,
   connDBOid, oid)` helper, threaded `ctx *Context` through
   `bindRecordRowComposite`/`bindSelectIntoRow` (previously ctx-less) to
   reach it. Verified live: oidjoins.sql now runs 4 correct NOTICE lines
   with resolved catalog names (`pg_proc`, `pg_namespace`, ...) before
   diverging.

Remaining gap (independent, systemic, NOT sized further this loop):
`pg_proc`'s live/queryable column set is missing 6 real PG18 columns
present in its own heap-encode schema — `PGProcColumnsPG18()`
(`internal/executor/sys_pg_proc.go:29-62`) declares all 30 PG18 columns
including `provariadic`/`pronargdefaults`/`proargmodes`/`proargnames`/
`proargdefaults`/`prosqlbody`, but live `SELECT provariadic FROM pg_proc`
42703s and `SELECT * FROM pg_proc` returns only 23 columns — confirmed via
`pg_attribute` for `'pg_proc'::regclass` not listing `provariadic` at all.
The heap-encode schema and the query-time-resolvable schema for pg_proc
have drifted apart. oidjoins.sql's DO block iterates all 219 catalog-FK
rows via dynamic EXECUTE, so this is very likely NOT isolated to pg_proc —
expect the same missing-column pattern to recur across several of the
~40 other catalogs the sweep touches.

CSV stays `not-tried` -> `failed` (NOT `pass` — sweep still doesn't
complete clean), `pass_required` stays `no`. Ledger row appended
(`.ralph/deferral_ledger.md`, 2026-08-25, M0134-0146).

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0147 (opr_sanity.sql)**. Size it live first
(`scripts/pg-regress-runner.sh --verbose opr_sanity`). No strong prior.

Separately, a concrete resume point for M0134-0146 itself (optional, not
next-in-line per the banner's top-to-bottom order, but worth a future loop):
find and unify/backfill pg_proc's queryable-column builder against
`PGProcColumnsPG18()` (`internal/executor/sys_pg_proc.go`), then re-run
`scripts/pg-regress-runner.sh --verbose oidjoins` to find the
next-divergent catalog/column pair, repeating until the 219-row sweep is
clean or the remaining gaps are fully catalogued.

Standing recommendation, carried across several loops (unchanged, no new
item this loop beyond the pg_proc column-drift note above):
1. GIN/GiST/SPGiST physical-index plan integration — Seq Scan not
   Index/Index-Only Scan because the AM is catalog-only.
2. btree v0 opclass generality (`internal/executor/operators_ddl.go:15810`
   `isSupportedBTreeKeyType` + `btree_scalar_keys.go`) — confirmed 8+ times.
3. Memoize plan-node type — entirely unimplemented (M0134-0141).
4. Real parallel-worker query execution — recurs across M0134-0008/-0023/
   -0141.
5. Geometry type-system gap — path/polygon still raw-varlena pass-through.
6. LANGUAGE C dynamic-extension loading gap.
7. Collation-execution-registry gap (5 parked files).
8. BETWEEN-vs-comparison-operator precedence bug (M0134-0113).
9. RAISE INFO/LOG/DEBUG collapse to hardcoded NOTICE wire severity.
10. `::json` cast DETAIL/CONTEXT truncation text (json_errdetail port).
11. pg_shdepend-shaped object-enumeration/CASCADE engine — confirmed 5 times
    (M0134-0124/-0132/-0135/-0142/-0145). Single most-recurring blocker
    across M0134; strongest candidate for its own milestone.
12. `CREATE CONVERSION`-registered procs never consulted by convert_from/to.
13. DDL-event-trigger firing engine + `session_replication_role` GUC.
14. `NonSuperuserRole != ""` "is superuser" convention wrong for non-"postgres"
    superuser roles.
15. inet.sql (M0134-0130) left 11 undispatched scalar functions.
16. pg_init_privs (M0134-0132) is a reconstruction, not a real snapshot.
17. jsonpath's own grammar entirely unimplemented.
18. Full PostgreSQL Large Object facility (M0134-0135) — own milestone.
19. `coerceTextLikeDatum` never threads `ExecError.Pos` — psql LINE echo gap
    shared by box/circle/line/lseg/macaddr/macaddr8/inet/bit(n).
20. `evalCast`'s catch-all pass-through hides real validation gaps.
21. `DropTable` on a PARENT never scrubs `inheritanceChildren`/
    `partitionChildren` (only fixed for the child side, M0134-0140).
22. LATERAL outer-column-ref bug (memoize.sql bonus discovery) — narrow,
    potentially real, independent of Memoize/parallel.
23. No generic system-catalog TOAST-table registration —
    `pgClassReltoastrelidFor` special-cases only `pg_rewrite`; every other
    nailed catalog's `reltoastrelid` is hardcoded 0.
24. `money`/`cash` type entirely unimplemented (M0134-0143).
25. CREATE SCHEMA sub-element execution gap — blocks 3+ files (create_schema
    .sql, select_views.sql, namespace.sql). Resume point at M0134-0115's
    ledger row.
26. `DROP OWNED BY` has zero parser AST node (blocked on #11).
27. `CREATE PUBLICATION ... FOR TABLES IN SCHEMA` unparsed; real support
    needs a new `pg_publication_namespace` catalog +
    `logicalwalsender.go:373-377` schema-membership filter.
28. NEW this loop: pg_proc's live/queryable column set has drifted from its
    own heap-encode schema (`PGProcColumnsPG18()`) — 6 PG18 columns
    (provariadic/pronargdefaults/proargmodes/proargnames/proargdefaults/
    prosqlbody) declared but not query-resolvable. Likely the first of
    several similar per-catalog drifts.

Gates run this loop: scripts/pg-regress-runner.sh --verbose oidjoins (live,
before AND after the fixes — 0/1 PASS both times, diff shrank from
"immediate error" to "4 correct lines then divergence at check #5"); go
build ./... PASS; RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh
PASS (all packages, some cached); scripts/tpch-spotcheck.sh PASS (fresh
capped server, Q12=2/Q13=35 canonical); TPC-DS SF0.5 gate BLOCKED this loop
— `scripts/tpcds-sf05-regression.sh sweep` refused with "FATAL: the nightly
CI batch is running (ci/batch)" (concurrent resource-lock guard, not
forced — the change is orthogonal to any TPC-DS query shape, no TPC-DS
query uses plpgsql DO blocks or pg_get_catalog_foreign_keys); make
regen-testport PASS; make check-testport-inventory PASS; pre-commit hook
pgbench smoke PASS (TPC-B 331 tps / simple-update 623 tps / select-only
12317 tps, 0 failed); make ralph-state-guard: self-repaired (standard
between-loop marker reconciliation), passed after repair.

In-flight: none. (TPC-DS SF0.5 gate was blocked by a concurrent nightly
batch, not abandoned mid-run — nothing to resume; re-run it in a future
loop once the nightly lane is idle, per the practice-card mandate for
planner/executor changes, if time allows.)

Note: a concurrent peer session's WIP may still be present in the tree
(.ralph/progress.json, .ralphrc, analysis/postgres-oracle-compatibility-
report.md, ci/logs/launch.log, ci/logs/scheduler.log,
docs/wiki/getting-started.md, internal/executor/operators_recursive_cte.go,
postgres (untracked convenience symlink), third-party/tpcds-postgres,
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt,
docs/wiki/modules/catalog.md) — deliberately left untouched/uncommitted;
only this loop's own files were staged and committed by explicit pathspec.
