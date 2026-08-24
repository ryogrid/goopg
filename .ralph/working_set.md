Task just completed: M0134-0140 (maintain_every.sql) — CLOSED, real fix,
100% parity (was 0%, 15-line diff). Committed (608a2bb8) and pushed.

What landed: two-part catalog bug in `internal/catalog/catalog.go`:
1. `pg_class.relhassubclass` (virtual pg_class builder, ~line 7418) only
   OR'd `partitionChildren[t.OID]` — never `inheritanceChildren[t.OID]` —
   so a plain `CREATE TABLE ... INHERITS (parent)` never flipped the
   parent's relhassubclass to 't' at all.
2. Fixing #1 surfaced a second, independent bug: `DropTable` (~line 20995)
   never unlinked a dropped child's OID from its parent's
   `inheritanceChildren`/`partitionChildren` list, so relhassubclass stayed
   stuck at 't' forever after `DROP TABLE <child>` (PG's RemoveInheritance
   deletes the pg_inherits row as part of DROP's dependency scan; goopg has
   no pg_inherits heap, so the fix unlinks the in-memory edge directly in
   DropTable). Handled both classic INHERITS (via `InheritsParentOIDs`) and
   ATTACH PARTITION (full sweep of `partitionChildren` by value, since
   `Table.PartitionParentOID` stays 0 for ATTACHed children — same pattern
   `PartitionParentOf` already uses).

No new test file — `scripts/pg-regress-runner.sh maintain_every` is the
coverage; CSV flipped not-tried→pass, pass_required=yes. No deferral-ledger
row (genuine full fix, not a shortcut/partial). Confirmed no regression via
git-stash-diff on inherit.sql (3300-line pre-existing failure byte-identical
with/without the change, zero relhassubclass mentions) plus live re-checks
of alter_table/create_table_like/partition_join/partition_prune (all
pre-existing failures, unrelated).

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0141 (`memoize.sql`)**. Size it live first
(scripts/pg-regress-runner.sh --verbose memoize). No strong prior.

Standing recommendation, carried across several loops (unchanged this loop):
1. GIN/GiST/SPGiST physical-index plan integration — every predicate on
   these three index AMs EXPLAINs Seq Scan not Index/Index-Only Scan
   because the AM is catalog-only. Strongest candidate for a dedicated
   milestone.
2. btree v0 opclass generality (`internal/executor/operators_ddl.go:15810`
   `isSupportedBTreeKeyType` + `btree_scalar_keys.go`) — confirmed a fifth
   time across M0134 macaddr/macaddr8/etc. Strong candidate for a dedicated
   milestone alongside item 1.
3. Geometry type-system gap — box/circle/line/lseg now `*_in`-faithful;
   path/polygon still raw-varlena pass-through. geometry.sql (M0134-0125)
   remains blocked on the unlexed geometric operator family.
4. LANGUAGE C dynamic-extension loading gap — recurs across M0134-0106,
   -0116, -0120, -0129, create_operator/create_type adjacent files.
5. Collation-execution-registry gap recurs across FIVE parked files
   (M0134-0099/-0100/-0101/-0102).
6. BETWEEN-vs-comparison-operator precedence bug (M0134-0113) — silently
   wrong for ANY query today, cross-cutting.
7. RAISE INFO/LOG/DEBUG collapse to hardcoded NOTICE wire severity
   (M0134-0113, ~18 call sites in plpgsql_runtime.go).
8. `::json` cast DETAIL/CONTEXT truncation text (json_errdetail port) —
   M0134-0120, unfixed.
9. pg_shdepend-shaped object-enumeration/CASCADE engine — CONFIRMED A
   THIRD TIME (M0134-0124). Single most-recurring blocker across M0134;
   strong candidate for its own milestone. Resume:
   `catalog.InMemory.RoleDropDependencyDescriptions`.
10. `CREATE CONVERSION`-registered procs never consulted by convert_from/
    convert_to (M0122-0008, M0134-0121).
11. DDL-event-trigger firing engine + `session_replication_role` GUC
    (M0134-0122/-0123).
12. `NonSuperuserRole != ""` "is superuser" convention is wrong for any
    non-"postgres"-named `CREATE ROLE ... SUPERUSER` role.
13. inet.sql (M0134-0130) left 11 pg_proc-seeded-but-undispatched scalar
    functions — low-effort follow-on wiring in evalFuncCall.
14. pg_init_privs (M0134-0132) is a reconstruction, not a real bootstrap
    snapshot.
15. jsonpath's own grammar is entirely unimplemented — REFACTOR-tier, own
    milestone.
16. Full PostgreSQL Large Object facility (M0134-0135) — entirely
    unimplemented, own milestone candidate.
17. `coerceTextLikeDatum` (codec.go) never threads `ExecError.Pos` through
    to its callers, so psql's client-side "LINE N: ...\n  ^" echo never
    fires for box/circle/line/lseg/macaddr/macaddr8/inet/bit(n) literal-
    validation errors raised during INSERT VALUES evaluation.
18-19. Geometry / network-address-family `*_in` closures — see prior loops.
20. `evalCast`'s catch-all `return d, nil` (unknown-type pass-through) has
    now been shown to hide real validation gaps twice (macaddr/macaddr8) —
    worth a systematic audit.
21. NEW this loop: `DropTable` never scrubs `inheritanceChildren`/
    `partitionChildren` entries pointing to the dropped table when it is
    itself a PARENT (only when it's a child, just fixed). If a parent with
    live children is dropped (e.g. via CASCADE), those maps still carry
    dangling child-OID lists keyed by the now-gone parent OID — harmless
    for relhassubclass (parent row is gone) but worth auditing if any other
    consumer of `partitionChildren`/`inheritanceChildren` assumes keys are
    always live tables.

Gates run this loop: go build ./... PASS; go test ./internal/catalog/...
./internal/executor/... PASS; scripts/pg-regress-runner.sh maintain_every
0%→100% parity (verified live before/after); git-stash-diff regression
check on inherit.sql (byte-identical 3300-line diff, confirms pre-existing
not a new break); live re-check of alter_table/create_table_like/
partition_join/partition_prune (all pre-existing failures, no
relhassubclass mentions); make regen-testport PASS; make
check-testport-inventory PASS; RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh PASS (all packages); scripts/tpch-spotcheck.sh
PASS (Q12=2 rows 16.78s, Q13=35 rows 8.05s, 27.5s query-phase wall);
pre-commit hook's pgbench smoke ran automatically at commit time and PASSED
(342 TPS simple-update, 12761 TPS select-only — both "0 failed"); make
ralph-state-guard: to be run before status block.

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree again this
loop (.ralph/progress.json, .ralphrc, ci/logs/launch.log,
ci/logs/scheduler.log, docs/wiki/getting-started.md,
docs/test-port/upstream-isolation-coverage.md,
docs/test-port/upstream-tap-coverage.md,
internal/executor/operators_recursive_cte.go, postgres (untracked
convenience symlink), third-party/tpcds-postgres,
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt,
docs/wiki/modules/catalog.md) and was deliberately left untouched/
uncommitted — only this loop's own files were staged and committed by
explicit pathspec.
