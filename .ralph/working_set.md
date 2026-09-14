Task: M0138-0003 — verify `stadistinct` parity end to end.
**COMPLETE and committed/pushed** this loop (`f60ba92e2`), branch
`plan-parity-with-pg-take2-ralph`.

Files: `docs/design/0100-0149/m0138-0003-stadistinct-parity-verification.md`
(new — full writeup), `docs/design/README.md` (+index row), `.ralph/fix_plan.md`
(M0138-0003 checked off, DONE summary). No `.go` file touched — this was a
verification-only task per its own text ("land a change only where a real
divergence is found").

What was verified:
  - Consumer audit: `catalog.ColumnStats.StaDistinct()` (`catalog.go:1942`) has
    exactly 3 callers (`pg18_user_catalog_rows.go:1591` pg_stats view,
    `pgstats.go:80` pg_statistic heap row, `joinselectivity.go:235` nd2 input)
    — none reads a bare `NDistinct`. Convention was already correct.
  - Re-measured R78's witness (`lineitem.l_orderkey`) on a **HEAD build**
    (carrying M0138-0002) restarted against the persistent TPC-H bench pair
    (goopg :65433 / PG oracle :65432, `GOOPG_ANALYZE_SEED=20260905` pinned via
    `bench/tpch/env_goopg.sh`). Built to a **private** `/tmp/goopg-m0138-0003-bin`
    (not `tmp/goopg-bench-bin`) because `ci/batch/nightly-scheduler.sh` (pid
    849415) was live — see memory `goopg_bench_bin_shared_with_nightly_lane`.
    Result: goopg `l_orderkey` ndistinct moved from pre-M0138-0002's `-0.1956`
    frac (~1.17M, 3.4x off PG) to `327804` absolute — inside PG's own
    unpinned 3-run noise band (`336410`-`366886`, ~9% spread). 5 more spot-check
    columns (l_partkey, l_suppkey, l_linestatus, l_shipdate, customer.c_custkey
    PK) all matched PG within noise; both engines report `-1` for the unique
    PK, confirming the 10%-threshold switch fires identically.
  - Conclusion: R78's divergence WAS the M0138-0001 block-representation gap,
    already fixed by M0138-0002 — not a stadistinct-convention bug. No diff
    earns landing (anti-tuning rule).

Key symbols: `catalog.ColumnStats.StaDistinct()` (`internal/catalog/catalog.go:1942`),
its 3 call sites listed above. PG oracle: `get_variable_numdistinct`
(`postgres/src/backend/utils/adt/selfuncs.c`).

Gates run: `go build ./...` clean (no code changed). Pre-commit hook's
pgbench smoke passed (commit succeeded). `make ralph-state-guard`: same
running/in_progress marker mismatch as recent loops (previous loop's
clean-exit marker), auto-repaired, then OK.

Bench-cluster side effect (intentional, matches M0138-0001 precedent of
leaving clusters running post-measurement): TPC-H goopg bench (:65433) was
stopped and restarted on the private HEAD build above (same PGDATA, no
--reset, no data loss) and had `ANALYZE lineitem;`/`ANALYZE orders;` run
several times against it — this is now the standing state of that cluster
(post-M0138-0002 stats, matches what any future HEAD build would produce).
`tmp/goopg-bench-bin` (the shared nightly-lane artifact) was NOT touched.

In-flight: none.

Next step: Per the banner, M0138's next unchecked task is **M0138-0004**
("MCV, histogram and correlation from the shared sample") — apply PG's
`compute_scalar_stats`/`compute_distinct_stats` selection rule to the sample
M0138-0002 now produces. Re-check the banner in `.ralph/fix_plan.md` fresh
next loop before committing (M0139/M0140 may have become topmost-unblocked
instead) — selection is "topmost milestone (M0138) with an unblocked task"
per the banner text. Also worth a quick look: M0138-0001's correlation
tie-break divergence (36/118 TPC-DS columns vs PG's 7/119) is cited as an
M0138-0004 input.
