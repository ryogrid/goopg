Task just completed: M0134-0139 (macaddr8.sql) — CONTAINED fix shipped, PARKED.
diff 420 lines (0% parity) → 29 lines, 2 residual `^+ERROR` (both the
already-ledgered btree-opclass-generality gap). Committed and pushed.

What landed: `parseMacaddr8Literal` (`internal/executor/expr.go`) — a
faithful port of `macaddr8_in` (`mac8.c:96-232`), structurally different
from `macaddr_in`'s 7-format sscanf cascade: a single greedy scanner reading
exactly 2 hex digits per byte, tracking one optional consistent separator
(`:`/`-`/`.`), accepting 6 or 8 bytes with 6-to-8 EUI-64 auto-widening
(FF/FE inserted as the 4th/5th octets). Sizing also surfaced a previously-
unknown, second gap `macaddr.sql` never exercised: `::macaddr`/`::macaddr8`
CAST were BOTH unvalidated pass-throughs in `evalCast` (no case existed for
either target — confirmed live, `evalCast`'s trailing `return d, nil`
silently accepted garbage). Fixed both directions via new
`macaddr8ToMacaddrOctets`/`macaddrToMacaddr8Octets` helpers (port
`macaddr8tomacaddr`/`macaddrtomacaddr8`, mac8.c:523-566). Wired into
column-assignment coercion (codec.go), `pg_input_is_valid`/
`pg_input_error_info`, `~`/`&`/`|` bitwise ops and `trunc()` (tried after the
6-octet macaddr form in each shared dispatch site), new `macaddr8_set7bit()`
function. Confirmed no regression on macaddr.sql(33)/box.sql(722)/
circle.sql(51)/line.sql(55)/lseg.sql(27)/point.sql(531)/inet.sql(1298). New
test `internal/executor/macaddr8_literal_test.go` (3 subtests, all PASS).

Design `docs/design/m0134-0139-macaddr8-cast-and-literal.md`, indexed in
README.md. Ledger row: `.ralph/deferral_ledger.md` 2026-08-24 M0134-0139
(residual gap #1 = same coerceTextLikeDatum Pos-threading LINE-echo gap as
M0134-0136/-0137/-0138; residual gap #2 = btree v0 opclass generality on
macaddr8, already tracked under M0134-0060/-0067/-0138's ledger rows, not
newly discovered here).

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0140 (`maintain_every.sql`)**. Size it live
first (scripts/pg-regress-runner.sh --verbose maintain_every). No strong
prior — unrelated to the geometry/network-address-family cluster just
closed (box/circle/line/lseg/point/inet/macaddr/macaddr8, 8 files in a row);
expect a fresh root-cause investigation.

Standing recommendation, carried across several loops (unchanged this loop):
1. GIN/GiST/SPGiST physical-index plan integration — every predicate on
   these three index AMs EXPLAINs Seq Scan not Index/Index-Only Scan
   because the AM is catalog-only. Strongest candidate for a dedicated
   milestone.
2. btree v0 opclass generality (`internal/executor/operators_ddl.go:15810`
   `isSupportedBTreeKeyType` + `btree_scalar_keys.go`) — NOW CONFIRMED a
   FIFTH time (M0134-0060/-0067/-0138/this-loop's macaddr8.sql), hard-codes
   a fixed type set with no generic per-type comparator dispatch. Strong
   candidate for a dedicated milestone alongside item 1.
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
   THIRD TIME (M0134-0124), after M0134-0117/-0118. Single most-recurring
   blocker across M0134; strong candidate for its own milestone. Resume:
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
    validation errors raised during INSERT VALUES evaluation. Cross-cutting,
    touches every INSERT/UPDATE/COPY call site.
18. Geometry-family `*_in` closure now covers box/circle/line/lseg (4/6 core
    geo types). path/polygon remain raw-varlena pass-through.
19. Network-address-family `*_in` closure now covers inet/cidr/macaddr/
    macaddr8 — ALL FOUR network types now have real validation. This
    cluster is CLOSED as a recurring item.
20. `evalCast`'s catch-all `return d, nil` (unknown-type pass-through) has
    now been shown to hide real validation gaps twice (macaddr/macaddr8) —
    worth a systematic audit of which type names still fall through it
    silently rather than raising 42704/22P02.

Gates run this loop: go build ./... PASS; go test -run TestMacaddr8
./internal/executor/ PASS (3 subtests); scripts/pg-regress-runner.sh
macaddr8 420→29 diff lines, 2 `^+ERROR` (both pre-existing, sizing +
fix verified live); scripts/pg-regress-runner.sh macaddr/box/circle/line/
lseg/point/inet re-checked for regression, all held steady
(33/722/51/55/27/531/1298); make regen-testport PASS; make
check-testport-inventory PASS; RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh PASS (all packages); scripts/tpch-spotcheck.sh
PASS (Q12=2 rows 18.46s, Q13=35 rows 7.92s, 28.6s query-phase wall);
pre-commit hook's pgbench smoke ran automatically at commit time and PASSED
(339 TPS simple-update, 12520 TPS select-only — both "0 failed"); make
ralph-state-guard: ran, PASS.

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree again this
loop (.ralph/progress.json, .ralphrc, analysis/deferral-ledger-summary-*,
ci/logs/launch.log, ci/logs/scheduler.log, docs/wiki/getting-started.md,
docs/wiki/modules/catalog.md, internal/executor/operators_recursive_cte.go,
postgres (untracked convenience symlink), third-party/tpcds-postgres,
dl_summary_session.txt, docs/test-port/upstream-isolation-coverage.md,
docs/test-port/upstream-tap-coverage.md) and was deliberately left
untouched/uncommitted — only this loop's own files were staged and
committed by explicit pathspec.

M-NIGHTLY: re-checked at loop start — `ci/logs/action-items.md`'s 2 items
(20260824-013441-001/-002) were already filed in fix_plan.md by a prior
loop (confirmed via grep, "Nightly run 20260824-013441 ... filed
2026-08-24" section present); nothing new to file this loop.
