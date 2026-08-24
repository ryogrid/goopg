Task just completed: M0134-0132 (init_privs.sql) — FULL PASS. 100% parity
(0% → 100%, diff 15→0 lines).

`scripts/pg-regress-runner.sh init_privs`: single-row diff —
`SELECT count(*) > 0 FROM pg_init_privs` returned `f` instead of `t`.
Root cause: `pg_init_privs.VirtualRows` (`internal/catalog/catalog.go`, OID
3394) was a hardcoded `return nil` since M0110-0001 (DU-002), deliberately
empty because goopg has no bootstrap-time ACL-snapshot mechanism. Real PG's
`initdb setup_privileges()` (`postgres/src/bin/initdb/initdb.c:1802-1935`)
seeds every pg_catalog/information_schema relation's `relacl` at bootstrap
(world-readable + owner-full default) and copies it into `pg_init_privs` as
an immutable day-zero snapshot `pg_dump` diffs live ACLs against.

Fixed: added `PGInitPrivsRowsForDBOid` (`internal/catalog/catalog.go`, next
to `PGClassRowsForDBOid`), which reconstructs the same row set ON EVERY
READ (real objoid, classoid=1259, objsubid=0, privtype='i', synthesized
`{=r/postgres,postgres=arwdDxtm/postgres}`/`...rwU...` initprivs text) for
every relation in schema pg_catalog/information_schema — NOT a captured-once
snapshot, so a future `pg_dump --binary-upgrade`-style differential dump of
only-changed system-catalog ACLs is not yet byte-faithful (ledgered).

Design `docs/design/m0134-0132-init-privs-population.md`, indexed in
README.md. Ledger row: `.ralph/deferral_ledger.md` 2026-08-24 M0134-0132
(resume: a real one-time snapshot map, e.g. `c.initPrivs` keyed like
`tableACLs`, taken once at catalog bootstrap, read by
`PGInitPrivsRowsForDBOid` instead of recomputing defaults every call). New
tests `TestPGInitPrivsRowsForDBOidNonEmpty`/
`TestPGInitPrivsRowsForDBOidExcludesUserTables`
(`internal/catalog/pg_init_privs_test.go`). CSV flipped `not-tried` →
`pass`/`pass_required=yes` via `make regen-testport`. fix_plan.md
M0134-0132 marked [x] with full summary. Committed 60be1c8b, pushed to
origin/regress-renumbering.

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0133**. Size it live first per the established
pattern (run pg-regress-runner, read the diff, check whether the root cause
is a shared/already-tracked blocker before assuming fresh work).

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
13. pg_init_privs (M0134-0132) is a reconstruction, not a real bootstrap
    snapshot — if a future task needs byte-faithful pg_dump
    --binary-upgrade differential ACL dumps for system catalogs, see the
    resume point above (c.initPrivs one-time snapshot map).

Gates run this loop: scripts/pg-regress-runner.sh init_privs (0/1 → 1/1,
100% parity after the fix); go build ./... PASS; go test
./internal/catalog/... ./internal/executor/... PASS; scripts/tpch-spotcheck.sh
PASS (Q12=2 rows 18.8s, Q13=35 rows 7.9s, 28.4s query-phase wall);
RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh PASS (all
packages, cold internal/initdb 430s + cmd/goopg 79s, rest cached); make
check-testport-inventory PASS; make regen-testport PASS; pre-commit hook's
pgbench smoke ran automatically at commit time and PASSED (TPC-B 340 TPS,
simple-update 629 TPS, select-only 12662 TPS — all zero failed
transactions); make ralph-state-guard: found the same benign stale
clean-exit-marker status/progress mismatch as prior loops, auto-repaired to
progress=in_progress.

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree again this
loop (.ralph/progress.json, .ralphrc, analysis/*, ci/logs/launch.log,
ci/logs/scheduler.log, docs/wiki/*, internal/executor/
operators_recursive_cte.go, postgres (untracked convenience directory/
symlink), third-party/tpcds-postgres, plus untracked files
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt,
docs/wiki/modules/catalog.md) and was deliberately left untouched/
uncommitted — only this loop's own files were staged and committed by
explicit pathspec.

M-NIGHTLY: re-checked at loop start — `ci/logs/action-items.md` run
20260824-013441 (2 items) is the same run ID a prior loop already confirmed
filed in fix_plan.md (grep for the run ID at fix_plan.md:1303); nothing new
to file this loop.
