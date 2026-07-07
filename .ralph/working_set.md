(idle — nothing in flight)

M-NIGHTLY (tpch/Q13-regression) FIXED, committed (75394478), and pushed this
loop. Task was fully resolved — no follow-up required unless a future
regression reopens it.

Two independent latent bugs, both surfaced by TPC-H Q13's `customer LEFT
JOIN orders ON c_custkey = o_custkey AND o_comment NOT LIKE
'%special%requests%'` shape:

1. **Crash** — `internal/planner/planner.go`'s LEFT JOIN inner-only-conjunct
   split (~line 1899, the M0063-0005 design) wraps `o_comment NOT LIKE ...`
   in a `Filter` over the inner (orders) plan and correctly shifts its
   `ColumnRef.Index` to leaf-local coordinates, but never set
   `LeafLocal: true` (the M0077-0001 convention on `Filter` in `plan.go`).
   Two post-rewrite posMap passes (`applyJoinTreePosMap` /
   `remapPosMapAfterRewrite`, both `bushy.go`) mistook the already-local
   index for a stale FROM-cumulative offset and remapped it a second time
   — with this dataset's non-canonical orders column order, index 8
   (o_comment) got remapped to 0 (o_orderdate), producing a 42883 Kind
   mismatch. Fixed by setting `LeafLocal: true` on that Filter.

2. **Silent row loss** (found immediately after fixing #1, via the
   mandatory `tpch-spotcheck.sh` re-run: Q13 ran but returned 32 rows not
   33, missing the `c_count=0` bucket for the ~50k customers with zero
   orders) — `internal/planner/nl_index_join.go`'s `tryBuildNLI` /
   `pickInnerSide` falls back to using `j.Left` (customer) as the NLI's
   indexed inner side whenever `j.Right` (orders) isn't a bare `*SeqScan`
   (exactly what fix #1's Filter wrapper produces), silently making
   customer the null-extended side and orders the loop-driver — correct
   for INNER joins, WRONG for LEFT JOIN (flips which side is preserved).
   Fixed by declining that fallback branch whenever `j.Type !=
   JoinTypeInner`, falling back to the (already-correct) Hash Join path.

Verification this loop: minimal planner-only repro test (a throwaway
`zz_probe_q13_test.go`, deleted after use) isolated bug #1 down to the exact
line via instrumented prints (also deleted); a small `zz_a pk / zz_b`
psql-level repro isolated bug #2 (NLI picked customer_pk as inner, orders
as outer SeqScan driver, returning zz_b's own rows under zz_a's column
names). Both fixes verified via `go build ./...`, `go test
./internal/planner/... ./internal/executor/...`, a full `go test -short
./...` sweep (excluding `internal/testutil/tpch` — heavy scale-load tests
gated behind `-short`/explicit `-run` per their own doc comment — and
`internal/testport` — ported oracle tests that must be invoked explicitly
per `.ralph/PROMPT.md`; DO NOT run these two via a blanket `go test ./...`,
they hang/take hours), and `scripts/tpch-spotcheck.sh` (Q12=2, Q13=33 —
full parity restored). New regression test:
`internal/planner/left_join_inner_only_leaflocal_test.go`
(`TestLeftJoinInnerOnlyConjunctFilterIsLeafLocal`, confirmed it fails
without the fix). Design doc `docs/design/0063-0005-q13-left-join-not-
like-rewrite.md` updated (status draft→accepted, new §8 post-mortem),
already indexed in `docs/design/README.md`. `.ralph/fix_plan.md`'s
`tpch/Q13-regression` item checked off with full detail.

Next step: pick up the next queued M-NIGHTLY item from `ci/logs/action-
items.md` / `fix_plan.md` — `tpch/Q15b-MAIN-explain` (AI-20260707-000712-006,
EXPLAIN errored during plan-capture), then `tpch/Q9-timeout`
(AI-20260707-000712-007) and `tpch/Q20-timeout` (AI-20260707-000712-008) —
all three need the same port-65433 TPC-H runner server setup per
`ci/design/05-tpch-stage.md` §A (see `bench/tpch/env_goopg.sh` for the
canonical PGDATA/port/superuser env, and `scripts/goopg-test-run.sh` for
the memory-capped launch wrapper — remember to stop the server / systemd
scope cleanly when done, never bare `pkill`).

In-flight: none. All manually-started servers this loop (repro/verify
instances on `bench/tpch/runtime_goopg/data`, port 65433, cgroup scopes
goopg-q13-repro/-repro2/-repro3) were stopped via `systemctl --user stop`;
the real-PostgreSQL oracle instance on `bench/tpch/runtime/pgdata` (port
65432, started manually for an oracle-comparison attempt that turned out
unnecessary — no TPC-H data was loaded there) was stopped via `pg_ctl
stop`. The `scripts/tpch-spotcheck.sh` gate's own server + temp data dir
were stopped/cleaned by the script itself. A stray `go test
./internal/testutil/tpch/...` and a stray blanket `go test ./...` each hit
one of the two heavy/excluded packages above and hung to their 10-30 min
timeout (expected/known behavior per those packages' own doc comments, not
a regression) — their processes exited on their own with the timeout; no
process was left running (verified via `ps aux` — only the pre-existing,
unrelated `goopg-wp.scope` WordPress instance and a pre-existing
`pgtsconfig_test` postgres instance remain, both sanctioned/unrelated to
this work).
