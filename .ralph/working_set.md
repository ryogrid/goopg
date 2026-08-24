Task just completed: M0134-0149 (path.sql) — sized live, PARKED, real fix
shipped (not sizing-only). Committed (64b9f9a24).

What landed: `path` was a raw-varlena pass-through with zero validation, the
same state box/circle/line/lseg were in before their own M0134 slices
(M0134-0094/-0098/-0136/-0137). Added `parsePathLiteral`
(`internal/executor/expr.go`), a faithful port of `path_in`/`path_decode`/
`pair_count` (`postgres/src/backend/utils/adt/geo_ops.c`) — point count
pre-computed from the total comma count exactly as `pair_count` does (an
even/zero count rejects immediately, explaining `'[]'`'s error before any
point parsing starts), a single true-leading `'('` stripped as `path_in`'s
own outer wrapper (tracked as a SEPARATE `outerDepth` from `path_decode`'s
own `'['`/doubled-`'(('` wrapper-depth variable — conflating the two was
the first draft's bug, caught before commit), reusing the already-shared
`linePairDecode`/`lineSingleDecode` primitives (same ones `parseLineLiteral`/
`parseLsegLiteral` use via `pathDecodeTwoPoints`) for the per-point float8
decode since path's coordinate-pair grammar is npts-count-agnostic. New
`pathCanonicalText` mirrors `path_out`'s `path_encode`. Wired into the same
4 chokepoints as every prior geometry type: `coerceTextLikeDatum`
(`internal/executor/codec.go`), `pg_input_is_valid`/`pg_input_error_info`
(`expr.go` switch on `name` + `operators_pg_input_error_info.go`), and the
function-call dispatch switch in `evalFuncCall` (also keyed on `name`, NOT
`funcName` — a wrong-variable-name typo caught by the build before commit)
for `isopen`/`isclosed`/`pclose`/`popen`, which `pg_proc` already had OIDs
1430/1431/1433/1434 for but zero dispatch (`function isopen does not
exist` etc.).

Verified live: path.sql 111-line diff -> 31-line diff (0% -> still `failed`
but every remaining line, without exception, is the box.sql/circle.sql/
line.sql/lseg.sql-shared psql LINE-position-echo gap — `coerceTextLikeDatum`
never threads `ExecError.Pos` through to the wire-protocol error position,
so INSERT-time syntax errors lack the `LINE N: ...\n  ^` echo psql renders).
No new ledger row: this is the SAME gap the prior four geometry slices
already recorded (standing recommendation item #19 below).

CSV: path.sql flipped `not-tried` → `failed`, `pass_required` stays `no`.
fix_plan.md M0134-0149 marked PARKED with full landed/deferred summary.
Design: `docs/design/m0134-0149-path-typed-literal.md` (new), indexed in
`docs/design/README.md`.

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0150 (point.sql)**. Size it live first
(`scripts/pg-regress-runner.sh --verbose point`). Strong prior: point is the
MOST FUNDAMENTAL geometry type (every other geometry type's parser —
box/circle/line/lseg/path — calls into point-pair decoding), so point.sql
may already be closer to green than the compound types were, OR it may
reveal that `point` itself still has gaps the compound-type parsers papered
over with their own local point-pair decoders. Check whether `point` has a
`coerceTextLikeDatum` chokepoint case yet (grep tname == "point" in
codec.go) before assuming — unlike box/circle/line/lseg/path, `point` was
NOT in the box/circle/line/lseg/path parity-audit list in this loop.

Standing recommendation, carried across several loops (item #19 grew this
loop's remaining-gap explanation; unchanged otherwise):
1. GIN/GiST/SPGiST physical-index plan integration — Seq Scan not
   Index/Index-Only Scan because the AM is catalog-only.
2. btree v0 opclass generality (`internal/executor/operators_ddl.go:15810`
   `isSupportedBTreeKeyType` + `btree_scalar_keys.go`) — confirmed 8+ times.
3. Memoize plan-node type — entirely unimplemented (M0134-0141).
4. Real parallel-worker query execution — recurs across M0134-0008/-0023/
   -0141.
5. Geometry type-system gap — SHRINKING: box/circle/line/lseg/path now all
   have real validate+canonicalize chokepoints (M0134-0094/-0098/-0136/
   -0137/-0149); point/polygon still need auditing (next task).
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
    shared by box/circle/line/lseg/path/macaddr/macaddr8/inet/bit(n). NOW
    CONFIRMED 5 TIMES on the geometry family alone (box/circle/line/lseg/
    path) — every one of those 5 cases' ENTIRE residual diff is exactly
    this gap. Strong candidate for the next contained slice: thread `Pos`
    through the INSERT/UPDATE/COPY literal-coercion call path into
    `coerceTextLikeDatum`'s error returns, then wire `Pos` into the
    wire-protocol ErrorResponse (dispatch.go's existing error-send path
    already understands `ExecError.Pos` — grep confirmed for a prior similar
    case, e.g. parser-level errors already echo LINE N correctly).
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
    (M0134-0147's 5 remaining-gap items).
29. `Routine.ArgTypes` conflates IN-only and ALL-args (OUT/INOUT included) —
    makes `proargtypes` wrong for any OUT-param function.
30. `tryHandleRoleDDL` has no wire-protocol notice sink — blocks 3 role-DDL
    NOTICE/WARNING messages in password.sql (M0134-0148's deferral; item #9
    covers a related but distinct RAISE-severity gap).

Gates run this loop: go build ./... PASS; GOOPG_CG_UNIT=path-test
scripts/goopg-test-run.sh go test -timeout 10m ./internal/executor/ PASS
(7.0s); RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh PASS
(all packages, some cached, initdb 439.9s); scripts/pg-regress-runner.sh
--verbose path (live, before/after — 111→31-line diff, 0 residual
^ERROR/^-ERROR); make regen-testport / make check-testport-inventory PASS;
pre-commit hook pgbench smoke PASS (select-only 12697 tps, 0 failed);
make ralph-state-guard: found+repaired 1 stale-marker inconsistency
(progress.json's "completed" was the prior loop's clean-exit marker, not
project completion — reconciled to "in_progress"), then verified consistent.

In-flight: none.

Note: a concurrent peer session's WIP may still be present in the tree
(.ralph/progress.json, .ralphrc, analysis/postgres-oracle-compatibility-
report.md, ci/logs/launch.log, ci/logs/scheduler.log,
docs/wiki/getting-started.md, internal/executor/operators_recursive_cte.go,
postgres (untracked convenience symlink), third-party/tpcds-postgres,
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt,
docs/wiki/modules/catalog.md) — deliberately left untouched/uncommitted;
only this loop's own files were staged and committed by explicit pathspec.

M-NIGHTLY note: `ci/logs/action-items.md` (run 20260824-013441, 2 items) was
checked this loop — both already filed in fix_plan.md (§"Nightly run
20260824-013441"): AI-...-001 already closed [x] by a prior loop;
AI-...-002 is a duplicate of the still-open AI-20260822-001356-003
(`TestSyntax_AdvisoryLock_SessionUnlockAcrossBeginBoundary`, now re-failed 3
nights running) — left unselected per the banner (M0134 outranks M-NIGHTLY
selection while M0134 has remaining unparked top-to-bottom tasks and this
item doesn't break a build/gate M0134 depends on).
