# Row-estimate collapse — reproducers and probe

Companion artefacts for `docs/design/planner-rowest-collapse/DESIGN.md`.
Read that document first; this directory is only the evidence.

```
date:     2026-09-06
goopg:    f7a345e32 (built from a clean detached worktree — the working tree
          carried five agents' WIP, including in-flight C-19f)
clusters: throwaway :5533 (synthetic tables + a private copy of SF0.5 store_sales)
          bench/tpcds/runtime_goopg/data-sf05 on :65437, S-cold, under
          flock /tmp/goopg-65437.lock
oracle:   ./postgres (PG 18.3, read-only) and bench/tpcds/plans-pg/*.txt
```

| file | what it shows |
|---|---|
| `repro-a1-range-nullfrac.sql` | **A1.** Two synthetic tables differing only in null fraction. `BETWEEN` on the 0-null table estimates 24 827 against 25 000 actual; the same predicate on the 4.4 %-null table estimates 3 800, and a narrower band lands on the flat 0.005 `DEFAULT_RANGE_INEQ_SEL`. Root cause of Q28's six `rows=1` scans. |
| `repro-a2-no-histogram.sql` | **A2 (latent).** A 100-distinct column gets an MCV list and no histogram, and `rangeOpSelectivity` bails before reading the MCV list: `BETWEEN` estimates 1 000 against 10 000 actual. The 500-distinct twin, which does get a histogram, is accurate. |
| `repro-a3-antijoin.sql` | **A3.** `LEFT JOIN … WHERE d.id IS NULL` estimates 1 against 50 000 actual; the semantically identical `NOT EXISTS` becomes a `Hash Anti Join` and estimates 83 333. Root cause of Q78's `rows=1`. |
| `repro-b1-groupvars.sql` | **B1 control.** Group-by across hash joins, nested loops and `UNION ALL` — all estimates correct. This is what rules out "joins break group estimates" as the explanation and forces the isolation in the next file. |
| `sf05-probe-q99-isolate.sql` | **B1 isolation.** Six probes over the real SF0.5 data taking Q99 apart: the single-table group-by is right, `substr` is right, the whole five-way join without `ORDER BY … LIMIT` is right (90), and adding the LIMIT alone takes it to 720 657 — because the LIMIT changes the plan shape to `NestedLoopIndexJoin` + `Memoize`, and `resolveBaseColumn` has no arm for that node. |
| `sf05-probe-ab.sql` | The A/B probe run base-vs-patched on SF0.5: Q28's decomposition plus Q99 / Q62 / Q76 / Q22. |
| `probe-patch.diff` | **The instrument, not a proposed patch.** Two edits — the `nulltestsel` term in `rangequery.go` and the `*NestedLoopIndexJoin` arm in `joinkeyproof.go` — used to confirm that each mechanism is sufficient to explain its witnesses. The real cuts are scoped in the design doc's §4 and are deliberately larger than this (the sibling `WithSource` twins, the stale comment, the tests). |

## The measured A/B

Base vs `probe-patch.diff`, on `data-sf05`, S-cold, against the PG 18.3 plans
in `bench/tpcds/plans-pg/`:

| node | base | probe | PG 18.3 | actual |
|---|---:|---:|---:|---:|
| Q28 `Seq Scan on store_sales` (B1 arm) | 1 | 14 932 | 5 337 | 15 410 |
| Q28 `ss_quantity BETWEEN 0 AND 5` alone | 1 | 57 945 | — | 68 801 |
| Q28 `ss_coupon_amt BETWEEN 1319 AND 2319` | 7 198 | 31 527 | — | 30 142 |
| Q99 `HashAggregate` | 720 657 | 90 | 72 | 90 |
| Q62 `HashAggregate` | 359 432 | 150 | 120 | 150 |
| Q22 grouping-sets `HashAggregate` | 9 460 201 | 72 001 | 71 857 | 11 987 |
| Q12 `WindowAgg` | 107 310 | 4 572 | — | 932 |
| Q76 `HashAggregate` | 67 352 | 67 352 | 6 810 | 470 |

Q99 and Q62 become exact; Q22 lands within 0.2 % of PostgreSQL's own estimate.
Q76 does not move — it is the `*Append` residue (design §3.4), and PG is 14×
over on it too.

## The one that is not a bug

Q47 and Q57 were the census's second- and fifth-largest misses (`CTE Scan on v2`
estimated 1 against 43 626 and 17 189). PostgreSQL 18.3 estimates **1** on the
identical node:

```
bench/tpcds/plans-pg/Q47.txt:   ->  Sort  (cost=745.30..745.30 rows=1 width=400)
bench/tpcds/plans-pg/Q57.txt:   ->  Sort  (cost=384.96..384.97 rows=1 width=690)
```

Seven mutually-implied equi-conditions multiplied as independent events, floored
by `clamp_row_est`. goopg reproducing upstream's answer is plan parity, not a
defect. See design §2.4.

## Running them

The synthetic four need no benchmark data at all. Note that the data directory
path must be **short** — a scratchpad-length path overflows the unix control
socket's `sun_path` and the server starts without a control listener:

```bash
export PATH="$PWD/postgres/local_install/bin:$PATH"
git worktree add /tmp/wt-rowest --detach HEAD
( cd /tmp/wt-rowest && go build -o bin/goopg ./cmd/goopg )
/tmp/wt-rowest/bin/goopg init -D /tmp/rowest-5533 --no-sync
GOMEMLIMIT=8GiB GOOPG_CG_UNIT=goopg-rowest scripts/goopg-test-run.sh \
    /tmp/wt-rowest/bin/goopg start -D /tmp/rowest-5533 --listen 127.0.0.1:5533 &
until pg_isready -h 127.0.0.1 -p 5533 -U postgres; do sleep 1; done
psql -h 127.0.0.1 -p 5533 -U postgres -d postgres -f repro-a1-range-nullfrac.sql
/tmp/wt-rowest/bin/goopg stop -D /tmp/rowest-5533
```

`repro-b1-groupvars.sql` and `repro-a3-antijoin.sql` are self-contained;
`repro-a3-antijoin.sql` expects `dim` from `repro-b1-groupvars.sql` to exist,
so run that one first.

The two SF0.5 probes need the benchmark cluster and the lock — see the design
doc's §6 for the exact `flock` invocation.

## A note on statistics

goopg's per-column statistics and relation sizes survive a restart
(M0125-0028/-0029), so the S-cold regime the census ran under is a
statistics-*present* regime. This was verified rather than assumed: ANALYZE in
one session, restart, then EXPLAIN in a fresh session with no ANALYZE returns
byte-identical row estimates. None of the mechanisms above is an
absent-statistics artefact.
