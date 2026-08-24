Task just completed: M0134-0151 (polygon.sql) — sized live, PARKED, real fix
shipped (not sizing-only). Committing this loop.

What landed: `polygon` was a raw-varlena pass-through with zero validation,
the last-unaudited of the 7-type core geometry family (box/circle/line/lseg
/path/point all graduated first via M0134-0094/-0098/-0136/-0137/-0149/
-0150) — this loop closes the family out. New `parsePolygonLiteral`/
`polygonCanonicalText` (`internal/executor/expr.go`), a faithful port of
`poly_in`/`poly_out` (`postgres/src/backend/utils/adt/geo_ops.c`): `poly_in`
computes `npts` via the same `pair_count` as `path_in`, then calls
`path_decode` with `opentype=false` (a leading `'['` is rejected outright —
always closed, unlike path's open form) and `endptr_p=NULL` (whole string
must be consumed, matching point_in's strictness); unlike `path_in`, `poly_in`
does NOT strip a single leading paren first (that "quick entry" unwrap is
path_in-specific). `polygonCanonicalText` reuses the existing
`pathCanonicalText(points, true)` verbatim — no new formatting logic. Wired
into `coerceTextLikeDatum` (`codec.go`), `pg_input_is_valid`/
`pg_input_error_info` (`operators_pg_input_error_info.go`), `evalCast`'s
`::polygon` arm, `evalTypedStringLit`'s `polygon '...'` arm, and the parser's
typed-literal keyword whitelist (`internal/parser/select.go` `tryTypedLiteral`)
— same sibling gap `lseg`/`path` had before M0134-0150 (bare `polygon '...'`
in expression context previously parsed as two unrelated tokens).

Verified live: polygon.sql 405-line diff -> 354-line diff. Like point.sql,
NOT zero-residual: dominated by the already-known geometric operator lexer/
dispatch gap (`<<`/`&<`/`&&`/`&>`/`>>`/`<<|`/`&<|`/`|&>`/`|>>`/`<@`/`@>`/
`~=`/`<->`) plus GiST/SPGiST plan-integration (Seq Scan not Index Scan,
standing item #1). One new, narrow, out-of-scope gap: `polygon(circle(
point(...)))` 3-function scalar constructor chain errors `function circle
does not exist` — the TYPE I/O now works but the constructor-style scalar
functions of the same names are unregistered. Confirmed no regression on
box.sql(722)/circle.sql(51)/line.sql(55)/lseg.sql(27)/path.sql(31)/
point.sql(451), all unchanged.

CSV: polygon.sql flipped `not-tried` → `failed`, `pass_required` stays `no`.
fix_plan.md M0134-0151 marked PARKED with full landed/deferred summary.
Design: `docs/design/m0134-0151-polygon-typed-literal.md` (new), indexed in
`docs/design/README.md`. Ledger row: `.ralph/deferral_ledger.md`
2026-08-25 M0134-0151.

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0152 (polymorphism.sql)**. Size it live first
(`scripts/pg-regress-runner.sh --verbose polymorphism`). All 7 core geometry
primitives (box/circle/line/lseg/path/point/polygon) are now individually
audited with real *_in-faithful validate+canonicalize chokepoints — item #5
in the standing list below is now FULLY COMPLETE and can be retired/
downgraded next loop. geometry.sql itself (M0134-0125, already PARKED) may
be worth a re-size to see how much of its 51%-unlexed-operator diff has
shrunk now that 7/7 primitive parsers exist (its own remaining blocker was
schedule-group table creation + operator lexer, both still open).

Standing recommendation, carried across several loops (item #5 now COMPLETE
this loop — ALL SEVEN core geometry primitives have real validate+
canonicalize chokepoints; consider retiring/replacing this item next loop
with "geometric operator lexer/dispatch family", which is now the sole
remaining geometry blocker):
1. GIN/GiST/SPGiST physical-index plan integration — Seq Scan not
   Index/Index-Only Scan because the AM is catalog-only.
2. btree v0 opclass generality (`internal/executor/operators_ddl.go:15810`
   `isSupportedBTreeKeyType` + `btree_scalar_keys.go`) — confirmed 8+ times.
3. Memoize plan-node type — entirely unimplemented (M0134-0141).
4. Real parallel-worker query execution — recurs across M0134-0008/-0023/
   -0141.
5. Geometry type-system gap — DONE for all 7 core primitives (box/circle/
   line/lseg/path/point/polygon all have real *_in-faithful validate+
   canonicalize chokepoints, M0134-0094/-0098/-0136/-0137/-0149/-0150/
   -0151). The operator-lexer family (`<<`/`&<`/`&&`/`&>`/`>>`/`<<|`/`&<|`/
   `|&>`/`|>>`/`<@`/`@>`/`~=`/`<->`/`?-`/`?|`/`?#`/`@@`/`#`) and several
   cross-type operator-dispatch gaps (point<@path, polygon<<, etc.) remain
   entirely open — this is now the SOLE remaining geometry gap, not
   per-type parsing. Strong candidate for its own dedicated milestone next.
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
    shared by box/circle/line/lseg/path/point/polygon/macaddr/macaddr8/inet/
    bit(n). CONFIRMED 7 TIMES on the geometry family alone. Strong candidate
    for the next contained slice: thread `Pos` through the INSERT/UPDATE/
    COPY literal-coercion call path into `coerceTextLikeDatum`'s error
    returns.
20. `evalCast`'s catch-all pass-through hides real validation gaps — box/
    circle/line's own `::box`/`::circle`/`::line` CAST arms are STILL
    missing per earlier grep — only their `T 'lit'` typed-literal forms
    exist (their assignment-coercion path is fine; only bare `::T` casts
    are affected).
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
    (M0134-0147's 5 remaining-gap items).
29. `Routine.ArgTypes` conflates IN-only and ALL-args (OUT/INOUT included) —
    makes `proargtypes` wrong for any OUT-param function.
30. `tryHandleRoleDDL` has no wire-protocol notice sink — blocks 3 role-DDL
    NOTICE/WARNING messages in password.sql (M0134-0148's deferral; item #9
    covers a related but distinct RAISE-severity gap).
31. pg-regress-runner.sh's prerequisite block is not schedule-group-aware —
    always runs create_index.sql/create_misc.sql/create_view.sql/
    create_aggregate.sql as prerequisites regardless of the named test's
    real position in PG's own parallel_schedule (M0134-0150's point.sql
    sizing note: a 10-vs-11 row false-positive on POINT_TBL, ruled out as a
    goopg bug, not yet fixed in the harness). Same underlying gap
    M0134-0125's geometry.sql sizing already flagged from the opposite
    direction (missing companion-table creation).
32. `circle`/`point`/`polygon` scalar CONSTRUCTOR functions (not the TYPE
    I/O, which now works for all 3) are unregistered — `polygon.sql`'s
    `polygon(circle(point(x,y), r))` errors `function circle does not
    exist` (M0134-0151's deferral). Narrow, not yet its own ledger row.

Gates run this loop: go build ./... PASS; go test -timeout 10m
./internal/executor/ ./internal/parser/ PASS (6.9s); RALPH_PRECOMMIT_SCOPE=
units scripts/ralph-precommit-test.sh PASS (all packages, some cached,
initdb 421.0s); scripts/tpch-spotcheck.sh PASS (Q12=2 rows 16.95s, Q13=35
rows 7.57s); scripts/pg-regress-runner.sh --verbose polygon box circle line
lseg path point (live, before/after — polygon 405→354, box/circle/line/
lseg/path/point all unchanged); make regen-testport / make
check-testport-inventory PASS; make ralph-state-guard: found+repaired 1
stale-marker inconsistency (same shape as prior loops — progress.json's
"completed" was the previous loop's clean-exit marker, reconciled to
"in_progress"), then verified consistent. Pre-commit hook pgbench smoke will
run automatically on `git commit` (not separately invoked this loop; hook is
mandatory and machine-enforced).

In-flight: none.

Note: a concurrent peer session's WIP may still be present in the tree
(.ralphrc, analysis/postgres-oracle-compatibility-report.md,
ci/logs/launch.log, docs/wiki/getting-started.md,
internal/executor/operators_recursive_cte.go, postgres (untracked
convenience symlink), third-party/tpcds-postgres,
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt,
docs/wiki/modules/catalog.md) — deliberately left untouched/uncommitted;
only this loop's own files were staged and committed by explicit pathspec.
