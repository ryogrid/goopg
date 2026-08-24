Task just completed: M0134-0145 (object_address.sql) — sized live, PARKED,
sizing only, no code shipped. Committed (a3da72645).

What landed: ran `scripts/pg-regress-runner.sh --verbose object_address` live
for the first time (was `not-tried`): 0/1 PASS, 0% parity, 598-line diff.
Dominant gap (majority of ~90 assertions): `pg_get_object_address`/
`pg_identify_object`/`pg_identify_object_as_address`/`pg_describe_object` are
ALL entirely unimplemented catalog functions (name-table rows, zero Go
handlers) — RE-CONFIRMS the standing pg_shdepend-shaped object-enumeration
engine item (standing item 11) a 5th time (prior: M0134-0124/-0132/-0135/
-0142). No single-slice fix flips a meaningful fraction of the diff, so this
loop followed the money.sql (M0134-0143) sizing-only precedent instead of
forcing a narrow fix.

Two independent secondary gaps confirmed, both ledgered, neither contained
enough to ship this loop:
1. `DROP OWNED BY <role>` has ZERO parser AST node (DROP keyword-lookahead
   switch in `internal/parser/ddl.go` has no `owned` arm) — real
   implementation is blocked on the same object-enumeration engine.
2. `CREATE PUBLICATION ... FOR TABLES IN SCHEMA <name>` is unparsed
   (`parseCreatePublicationTail`, `internal/parser/ddl.go:2301`, only accepts
   `FOR ALL TABLES`/`FOR TABLE t1,...`). Investigated as a possible contained
   fix but real support needs a new `pg_publication_namespace` catalog table
   (mirroring `pg_publication_rel`'s per-table journal,
   `upsertPublicationCatalogRow`/`writePublicationMemberRows` in
   `internal/executor/operators_ddl.go:1128-1133`) plus a schema-membership
   filter arm in `internal/replication/logicalwalsender.go:373-377` (today
   only checks `pub.AllTables`/exact `pub.Tables` name membership) — a
   multi-file feature, not a single-loop slice, so left ledgered rather than
   half-shipped.

CSV flipped `not-tried` -> `failed`, pass_required stays `no`. Ledger row
appended (`.ralph/deferral_ledger.md`, 2026-08-25, M0134-0145).

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0146 (oidjoins.sql)**. Size it live first
(`scripts/pg-regress-runner.sh --verbose oidjoins`). No strong prior.

Standing recommendation, carried across several loops (unchanged, no new
item this loop):
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
    now (M0134-0124/-0132/-0135/-0142/-0145). Single most-recurring blocker
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
26. NEW this loop: `DROP OWNED BY` has zero parser AST node (blocked on #11).
27. NEW this loop: `CREATE PUBLICATION ... FOR TABLES IN SCHEMA` unparsed;
    real support needs a new `pg_publication_namespace` catalog +
    `logicalwalsender.go:373-377` schema-membership filter — independent
    bounded-but-nontrivial feature, own candidate slice for a future loop.

Gates run this loop: scripts/pg-regress-runner.sh --verbose object_address
(live sizing, 0/1 PASS, 598-line diff, no code changed so no re-run needed);
go build ./... PASS; RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh
PASS (all packages, cached); pre-commit hook pgbench smoke PASS (TPC-B
328 tps / simple-update 636 tps / select-only 12277 tps, 0 failed); make
regen-testport PASS; make check-testport-inventory PASS (after fixing the
same CSV-quoting mistake as last loop — literal commas in an unquoted
rationale field break the CSV parser, use semicolons); make
ralph-state-guard: self-repaired (standard between-loop marker
reconciliation), passed after repair. tpch-spotcheck NOT re-run — no
planner/executor/codec code changed this loop (docs/CSV/ledger only), so the
practice-card gate doesn't apply; only the mandatory pgbench smoke ran.

In-flight: none.

Note: a concurrent peer session's WIP may still be present in the tree
(.ralph/progress.json, .ralphrc, analysis/postgres-oracle-compatibility-
report.md, ci/logs/launch.log, ci/logs/scheduler.log,
docs/wiki/getting-started.md, internal/executor/operators_recursive_cte.go,
postgres (untracked convenience symlink), third-party/tpcds-postgres,
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt,
docs/wiki/modules/catalog.md) — deliberately left untouched/uncommitted;
only this loop's own files were staged and committed by explicit pathspec.
