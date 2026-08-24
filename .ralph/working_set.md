Task just completed: M0134-0142 (misc_sanity.sql) — PARKED, two contained
fixes shipped. Committing now.

What landed: ran `scripts/pg-regress-runner.sh --verbose misc_sanity` live for
the first time (was `not-tried`): 0/1 PASS, 0% parity, 72-line diff. Two
independent CONTAINED bugs found and fixed:
1. `pg_attribute.attoptions`/`attfdwoptions` were declared scalar `text`
   (typid 25) in `pgAttrColDefs` (`internal/initdb/initdb.go:6205-6206`)
   instead of PG's actual `text[]` (typid 1009) per
   `postgres/src/include/catalog/pg_attribute.h:175,178`
   (`text attoptions[1]`/`text attfdwoptions[1]`) — a real catalog-shape bug,
   not cosmetic.
2. `oidToBuiltinTypeName` (`internal/executor/expr.go:78`), the table backing
   `::regtype` name resolution, had no entries for OID 194 (`pg_node_tree`)
   or OID 2277 (`anyarray`) — both fell through to the raw-numeric-OID
   fallback. Added both as bare pseudo-type names (matching PG's
   `format_type` convention for other pseudo-types like `internal`).

Diff went 72 -> 69 lines (both fixes confirmed live in the diff shrink).
Remaining 69-line diff, all REFACTOR-tier / cross-file gaps, PARKED:
- `pg_shdepend` relation doesn't exist at all (42P01) — standing item #11
  (pg_shdepend-shaped object-enumeration engine), confirmed a 4th time.
- No generic system-catalog TOAST-table registration: `pgClassReltoastrelidFor`
  (`internal/initdb/initdb.go:6130`) special-cases ONLY `pg_rewrite`; every
  other nailed catalog hardcodes `reltoastrelid=0`. Real PG's `pg_type` HAS a
  toast table (DECLARE_TOAST), so its `typacl`/`typdefault`/`typdefaultbin`
  varlena columns don't show in this sanity check — goopg's `pg_type` shows
  them spuriously. NEW discovery this loop, not previously ledgered as its
  own item (candidate #23 for the standing recommendation list below).
- `pg_authid.rolpassword`/`pg_largeobject.data`/`pg_largeobject_metadata.lomacl`/
  `pg_replication_origin.roname` are PG's accepted no-toast exceptions and
  expected in the output; goopg is missing `pg_largeobject`/
  `pg_largeobject_metadata`/`pg_replication_origin` catalogs entirely
  (already ledgered under M0134-0135) and may be missing
  `pg_authid.rolpassword`.

CSV flipped `not-tried` -> `failed`, pass_required stays `no`. Ledger row
appended (`.ralph/deferral_ledger.md`, 2026-08-25, M0134-0142). No dedicated
design doc — this is a two-line catalog-shape correction, not a new
subsystem (same precedent as M0134-0140's un-designed catalog fix).

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0143 (money.sql)**. Size it live first
(`scripts/pg-regress-runner.sh --verbose money`). No strong prior.

Standing recommendation, carried across several loops (unchanged except new
item 23):
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
11. pg_shdepend-shaped object-enumeration/CASCADE engine — CONFIRMED A 4TH
    TIME (M0134-0142, this loop). Single most-recurring blocker across
    M0134; strongest candidate for its own milestone.
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
23. NEW this loop: no generic system-catalog TOAST-table registration —
    `pgClassReltoastrelidFor` special-cases only `pg_rewrite`; every other
    nailed catalog's `reltoastrelid` is hardcoded 0, which will keep
    surfacing as spurious "missing toast table" rows in any future sanity
    check that cross-references `pg_class.reltoastrelid` with `pg_attribute`
    varlena columns (misc_sanity.sql's `pg_type` case this loop).

Gates run this loop: scripts/pg-regress-runner.sh --verbose misc_sanity
(live sizing + verification, 72->69 lines); go build ./... PASS;
RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh PASS (all
packages); scripts/tpch-spotcheck.sh PASS (Q12=2 rows 17.75s, Q13=35 rows
7.53s); make regen-testport PASS; make check-testport-inventory PASS;
make ralph-state-guard: to run before final status.

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree again this
loop (.ralph/progress.json, .ralphrc, analysis/postgres-oracle-compatibility-
report.md, ci/logs/launch.log, ci/logs/scheduler.log,
docs/wiki/getting-started.md, internal/executor/operators_recursive_cte.go,
postgres (untracked convenience symlink), third-party/tpcds-postgres,
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt,
docs/wiki/modules/catalog.md) and was deliberately left untouched/
uncommitted — only this loop's own files were staged and committed by
explicit pathspec.
