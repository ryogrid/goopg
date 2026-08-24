Task just completed: M0134-0143 (money.sql) — sized live, PARKED, no code
shipped. Committing now.

What landed: ran `scripts/pg-regress-runner.sh --verbose money` live for the
first time (was `not-tried`): 0/1 PASS, 0% parity, 691-line diff. Root cause:
the `money`/`cash` OID (790) is registered in `pg_type`/`pg_proc` for
catalog-lookup purposes (all `cash_*` builtins appear in
`pg_proc_names_generated.go`'s name table) but has ZERO real Go
implementation behind any of them — `cash_in`/`cash_out`/arithmetic/
`cashlarger`/`cashsmaller`/`cash_words` are bare name-table rows with no
handler. A `money` value falls through `evalCast`'s catch-all pass-through
(standing item 20) as an undecorated int8/numeric: `'123'::money` stores/
prints bare `123` not `$123.00` (no cent scaling, no `$`/comma cash_out
formatting), `m + '123'` raises `operator + requires numeric operands`
(zero arithmetic operators), no overflow detection at the documented
+92233720368547758.07/-92233720368547758.08 int64-cents bounds, no
pg_input_is_valid/pg_input_error_info wiring, no rounding-to-nearest-cent on
extra-precision input.

Decision: PARK, not implement. This is a from-scratch type implementation
(storage/parsing/output/arithmetic/overflow/functions with locale-shaped
semantics) — same size/shape class as the already-parked geometry-type gap
(standing item 5), not a bounded single-slice fix like box/circle/line/lseg/
macaddr/macaddr8 were (those only needed I/O-function + cast wiring on top
of an EXISTING partial type; money has no partial type to extend). Resume
point recorded in the ledger: `parseCashLiteral`/`cashOut` in
`internal/executor/expr.go` (port of `cash_in`/`cash_out`,
`postgres/src/backend/utils/adt/cash.c`) plus the full `cash_*`
arithmetic/comparison/function family dispatched the same way point/box/
circle/macaddr8 operator families are (runtime Kind-sniffing, no static
type tag on Datum).

CSV flipped `not-tried` -> `failed`, pass_required stays `no`. Ledger row
appended (`.ralph/deferral_ledger.md`, 2026-08-25, M0134-0143). No dedicated
design doc — this is a sizing-only park with zero code shipped (same
precedent as M0134-0141's memoize.sql park).

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0144 (namespace.sql)**. Size it live first
(`scripts/pg-regress-runner.sh --verbose namespace`). No strong prior.

Standing recommendation, carried across several loops (unchanged except new
item 24):
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
11. pg_shdepend-shaped object-enumeration/CASCADE engine — confirmed 4 times
    (M0134-0142). Single most-recurring blocker across M0134; strongest
    candidate for its own milestone.
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
24. NEW this loop: `money`/`cash` type entirely unimplemented — no
    `cash_in`/`cash_out`/arithmetic/comparison/functions behind the
    catalog-registered OID; falls through evalCast's catch-all pass-through
    as bare int8/numeric with zero cent scaling. Same size/shape as the
    geometry-type gap (item 5); resume point in the M0134-0143 ledger row.

Gates run this loop: scripts/pg-regress-runner.sh --verbose money (live
sizing, 691-line diff, 0% parity); go build ./... PASS; make regen-testport
PASS; make check-testport-inventory PASS; make ralph-state-guard: to run
before final status. No Go source touched this loop (docs/CSV/ledger/
fix_plan only) so ralph-precommit-test.sh/tpch-spotcheck.sh were not
re-run (no executable code changed; last run PASS in M0134-0142's loop).

In-flight: none.

Note: a concurrent peer session's WIP may still be present in the tree
(.ralph/progress.json, .ralphrc, analysis/postgres-oracle-compatibility-
report.md, ci/logs/launch.log, ci/logs/scheduler.log,
docs/wiki/getting-started.md, internal/executor/operators_recursive_cte.go,
postgres (untracked convenience symlink), third-party/tpcds-postgres,
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt,
docs/wiki/modules/catalog.md) — deliberately left untouched/uncommitted;
only this loop's own files were staged and committed by explicit pathspec.
