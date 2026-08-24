Task just completed: M0134-0147 (opr_sanity.sql) — sized live, PARKED, one real
fix landed (not sizing-only). Committed (bc6a1ff04).

What landed:
Fixed the exact resume point M0134-0146 left: pg_proc's live/queryable schema
(`registerPgProcView`, `internal/initdb/pg_proc_view.go`) was missing 7 real
PG18 columns present in its heap-encode twin (`PGProcColumnsPG18()`,
`internal/executor/sys_pg_proc.go`) — `provariadic`/`pronargdefaults`/
`proallargtypes`/`proargmodes`/`proargnames`/`proargdefaults`/`prosqlbody`.
Added all 7 to the virtual table's `Columns` list and populated them across
all 4 row-building blocks (builtin stubs, user routines,
`catalog.BuiltinProcs()`, user aggregates). `proargmodes`/`proargnames`/
`proallargtypes` carry REAL per-routine data (from `Routine.ArgModes`/
`ArgNames`/`ArgTypes`) via 3 new helpers in pg_proc_view.go
(`pgArgModesLiteral`/`pgArgNamesLiteral`/`pgAllArgTypesLiteral`).
`provariadic` stays constant 0 and `prosqlbody` constant NULL everywhere
(both real remaining gaps, ledgered). `pronargdefaults`/`proargdefaults` are
a real count / non-NULL placeholder pair kept mutually consistent (NOT the
real parsed pg_node_tree — a placeholder to satisfy opr_sanity.sql's
NULL-ness invariant).

Verified live, TWO independent regress files improved by the same fix:
- opr_sanity.sql: every "column ... does not exist" divergence gone (only
  unrelated `amvalidate` gap remains). Diff 1886→1833 lines.
- oidjoins.sql (M0134-0146's file): 219-row FK sweep went from diverging at
  check #5 to running ALL 219 checks clean — new unrelated divergence at the
  very LAST check (pg_subscription_rel.srrelid, "operator = has incompatible
  operand types oid and oid[]" — 42804, NOT a pg_proc issue, not yet
  triaged). CSV row for oidjoins.sql updated in place (rationale only, no
  status change — still `failed`/`pass_required=no`, sweep still doesn't
  complete 100% clean).

Remaining pg_proc gaps (ledgered, NOT this loop's scope):
1. `provariadic` always 0 — no real variadic-element-type resolution
   (ANYOID/ANYELEMENTOID/ANYCOMPATIBLEOID/array-element special cases). Will
   make opr_sanity's "variadic type ⟺ variadic argument" check fail once a
   user VARIADIC function is exercised (proargmodes now correctly says 'v',
   provariadic never does).
2. `prosqlbody` always NULL — no SQL-body node-tree serializer (matches
   standing gap already noted at `internal/executor/expr.go:13838`).
3. `proargdefaults` placeholder text, not real parsed node-tree.
4. `proallargtypes` = same OIDs as `proargtypes` (since `Routine.ArgTypes`
   already isn't IN-only — a SEPARATE pre-existing divergence from PG's
   real IN-only `proargtypes` semantics, not touched this loop).
5. opr_sanity.sql's remaining ~1833-line diff covers ~90 OTHER
   catalog-consistency assertions (opclass/opfamily/amop/amproc
   completeness, pg_amvalidate, index sanity) not yet triaged individually.

CSV: oidjoins.sql rationale updated (no status change). opr_sanity.sql
flipped `not-tried` → `failed` (NOT `pass`), `pass_required` stays `no`.
Ledger row appended (`.ralph/deferral_ledger.md`, 2026-08-25, M0134-0147).

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0148 (password.sql)**. Size it live first
(`scripts/pg-regress-runner.sh --verbose password`). No strong prior.

Separately, a concrete resume point for M0134-0147 itself (optional, not
next-in-line per the banner's top-to-bottom order): re-run
`scripts/pg-regress-runner.sh --verbose oidjoins` and chase the new
pg_subscription_rel.srrelid oid/oid[] type-mismatch error (last of the 219
FK checks) — likely a small, isolated bug distinct from the pg_proc
column-drift work.

Standing recommendation, carried across several loops (unchanged, no new
item this loop beyond the pg_proc sub-gaps noted above):
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
28. pg_proc's provariadic/prosqlbody/proargdefaults still not real
    (see the 5 remaining-gap items above) — resolved-enough for now, but
    a real fix needs a node-tree serializer + variadic-element resolver.
29. `Routine.ArgTypes` conflates IN-only and ALL-args (OUT/INOUT included) —
    makes `proargtypes` wrong for any OUT-param function; would need a
    parse-time split at `internal/executor/operators_ddl.go` ~16540/17330.

Gates run this loop: scripts/pg-regress-runner.sh --verbose opr_sanity (live,
before AND after — 1886→1833-line diff, all column-does-not-exist errors
gone); scripts/pg-regress-runner.sh --verbose oidjoins (live, before/after —
219/219 checks now execute, new unrelated last-check divergence); go build
./... PASS; go test ./internal/initdb/... -run TestPgProcView PASS (16/16);
RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh PASS (all
packages, some cached); scripts/tpch-spotcheck.sh PASS (fresh capped server,
Q12=2/Q13=35 canonical); TestPort_PgDumpConnectionSetup FAILED but confirmed
PRE-EXISTING via git-stash bisect (unrelated CREATE CAST bytea->text 42P17
error, not a regression from this loop's change); TPC-DS SF0.5 gate BLOCKED
this loop — `scripts/tpcds-sf05-regression.sh sweep` refused with "FATAL: the
nightly CI batch is running" (concurrent resource-lock guard, not forced —
change is orthogonal to any TPC-DS query shape); make regen-testport PASS
(after fixing a CSV-quoting mistake — commas in an unquoted rationale field
broke the parser, fixed by switching to semicolons per the file's existing
convention); make check-testport-inventory PASS; pre-commit hook pgbench
smoke PASS (select-only 12379 tps, 0 failed); make ralph-state-guard:
self-repaired (standard between-loop marker reconciliation), passed after
repair.

In-flight: none. (TPC-DS SF0.5 gate was blocked by a concurrent nightly
batch, not abandoned mid-run — nothing to resume; re-run it in a future loop
once the nightly lane is idle, per the practice-card mandate for
planner/executor changes, if time allows.)

Note: a concurrent peer session's WIP may still be present in the tree
(.ralph/progress.json, .ralphrc, analysis/postgres-oracle-compatibility-
report.md, ci/logs/launch.log, ci/logs/scheduler.log,
docs/wiki/getting-started.md, internal/executor/operators_recursive_cte.go,
postgres (untracked convenience symlink), third-party/tpcds-postgres,
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt,
docs/wiki/modules/catalog.md) — deliberately left untouched/uncommitted;
only this loop's own files were staged and committed by explicit pathspec.
