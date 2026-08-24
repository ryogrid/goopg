Task just completed: M0134-0130 (inet.sql) — sized live, PARKED, one
contained fix shipped (a real correctness/type-system gap, not a display
bug).

`scripts/pg-regress-runner.sh inet`: 0% parity, diff 1397→1298 lines.
Discovery: `inet`/`cidr` columns had ZERO input canonicalization or output
formatting anywhere — a literal like `'10'::cidr` was stored and re-emitted
verbatim as raw text, never expanded to PG's canonical `10.0.0.0/8`
(classful-default-mask expansion), and cidr's "bits set to right of mask"
validation didn't exist at all (`'192.168.1.2/30'` was silently accepted
instead of raising `22P02`).

Fixed: added `normalizeInetCidrText`/`formatInetAddr`/`formatInetV4`/
`formatInetV6` (`internal/executor/operators_ddl.go`), a faithful Go port
of PG's `network_in` (postgres/src/backend/utils/adt/network.c) +
`pg_inet_net_ntop` (postgres/src/port/inet_net_ntop.c), reusing the
existing btree-key parser (`parseInetKeyText`/`cidrDefaultV4Mask`/
`maskInetAddr`, M0134-0002 C5). Go's `net.IP.String()` could NOT be
reused for IPv6: PG's embedded-dotted-decimal-tail rule fires for any
6-word leading zero run (or 7 with nonzero last word, or 5 with
word[5]==0xffff), not just Go's recognized `::ffff:a.b.c.d` — confirmed
live Go collapses `::ffff:1.2.3.4`→`1.2.3.4` and renders
`::4.3.2.1`→`::403:201` (pure hex groups), both wrong for PG.

Wired into `coerceTextLikeDatum` (`internal/executor/codec.go`) — the
box/circle canonicalize-on-assignment chokepoint (M0134-0094/-0098) —
deliberately NOT into `evalCast` (matching that same precedent: explicit
`::inet`/`::cidr` casts on bare constants stay unvalidated, same as
box/circle never validating their own explicit-cast boundary).

New test: `TestNormalizeInetCidrText` (14 subtests,
`internal/executor/inet_cidr_normalize_test.go`).

Design `docs/design/m0134-0130-inet-cidr-normalization.md`, indexed in
README.md. Ledger row: `.ralph/deferral_ledger.md` 2026-08-24 M0134-0130.
CSV flipped `not-tried` → `failed` via `make regen-testport`. fix_plan.md
M0134-0130 marked [x] with full summary.

Deferred (ledger row, resume points recorded):
1. 11 `pg_proc`-seeded-but-undispatched scalar functions dominate the
   remaining diff: `host`/`abbrev`/`broadcast`/`network`/`masklen`/
   `netmask`/`hostmask`/`inet_merge`/`inet_same_family`/`cidr(text)`/
   `inet(text)`. Same shape as hash_func.sql's gap (M0134-0128) — each
   reduces to the primitives THIS loop already built
   (`formatInetAddr`/`parseInetKeyText`/`maskInetAddr`), so it's low-effort
   follow-on wiring in `evalFuncCall` (`internal/executor/expr.go`),
   following the `evalHashFunc` pattern exactly.
2. `<<`/`<<=`/`>>`/`>>=`/`&&`/`~`/`&` inet/cidr operators are entirely
   unparsed — no lexer tokens exist yet (`internal/parser/token.go`/
   `lexer.go`), not just missing backend functions.
3. No cidr↔inet implicit comparison coercion (`WHERE c = i` raises
   "operator = has incompatible operand types"; PG coerces both to inet).

Remaining 99 diff lines beyond what #1-#3 would fix are smaller/unsized —
not audited in detail this loop.

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0131 — infinite_recurse.sql**. Size it live
first per the established pattern (run pg-regress-runner, read the diff,
check whether the root cause is a shared/already-tracked blocker before
assuming fresh work). Also worth a quick look: item #1 above (11 inet
scalar functions) is comparatively low-effort and self-contained — a
future loop could productively spend a slot wiring it directly rather
than waiting for another regress file to surface it, similar to how
M0134-0128's hash-function wiring was scoped.

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

Gates run this loop: scripts/pg-regress-runner.sh inet (sizing run, 0/1,
before and after the fix — 1397→1298 diff lines); minimal repro via a
throwaway cgroup-capped server + psql against the real
postgres/src/test/regress/sql/inet.sql (confirmed byte-match canonical
form + correct 22P02 errors); go build ./... PASS; go test
./internal/executor/... PASS (includes 1 new test func, 14 subtests);
scripts/tpch-spotcheck.sh PASS (Q12=2 rows 18.4s, Q13=35 rows 7.5s, 27.9s
query-phase wall); RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh
PASS (all packages, cold internal/initdb 429s + cmd/goopg 76s, rest
cached); make check-testport-inventory PASS; make regen-testport PASS;
pre-commit hook's pgbench smoke ran automatically at commit time and
PASSED (TPC-B 312 TPS, simple-update 633 TPS, select-only 12665 TPS — all
zero failed transactions); make ralph-state-guard: found the same benign
stale clean-exit-marker status/progress mismatch as prior loops,
auto-repaired to progress=in_progress.

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree again this
loop (.ralph/progress.json, .ralphrc, analysis/*, ci/logs/launch.log,
ci/logs/scheduler.log, docs/wiki/*,
internal/executor/operators_recursive_cte.go, postgres (untracked
convenience symlink), third-party/tpcds-postgres, plus untracked files
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt) and
was deliberately left untouched/uncommitted — only this loop's own files
were staged and committed by explicit pathspec.

M-NIGHTLY: re-checked at loop start — `ci/logs/action-items.md` run
20260824-013441 (2 items) was already filed in fix_plan.md by a prior loop
(confirmed via grep for the run ID at fix_plan.md:1303); nothing new to
file this loop.
