(idle — nothing in flight)

Last loop: **`M0125-0013` (Q47) CLOSED on a refuted premise.** Q47: 0 → **100
rows = SF0.5 oracle**. One arm in `internal/planner/bushy.go`
(`buildBindingsPosMap`'s `*MultiHashJoin` case matched only bare
`*SeqScan`/`*IndexScan`, so a `*Filter`-wrapped leaf was skipped silently —
no `scanEntry` AND no `off` advance). Fix = `collect(t)`. Design
`docs/design/0125-0013-mhj-posmap-filtered-leaf.md` (indexed); 3 ledger rows;
2 regression tests, the Filter one verified RED with the fix reverted and the
bare-leaf one green on both sides (pins strict generalisation).

Nightly triage: `ci/logs/action-items.md` unchanged (mtime Jul 25 03:20), all
26 `AI-` subjects already filed — no-op.

## NEXT (banner order, rewritten this loop)

1. **`M0125-0003` stage 1** — item 1 of the banner's ordered list, still
   unlanded (shape-neutral; land it and defer the four-arm TIMED study).
2. Then `M0125-0002 / -0004 / -0005`, and the `M0125-0013` **bookkeeping
   half** (Q47's 8.4x runtime verdict — needs a QUIET host).
Owed independently, now **three commits deep**: one full 99-query SF0.5 gate
run on a quiet host (own ledger row, 2026-07-30).

## Facts the next loop should NOT re-derive

- **A matching row count proves nothing about the projection.** Q47's 4-way
  join produced 332,240 rows — *exactly PG's* — while every projected column
  read a different relation. RC-1b declared the CTE body "exactly correct" on
  a row count and the wrong-column half survived. This is D6a's lesson at the
  sub-query level.
- **Do not trust the fix_plan's stated defect LOCATION.** -0013 said "start
  BELOW the CTE, at the `v1`→`v2` window layers". The window layers were
  never broken — `rank()` returned 1 for every row because it was
  partitioning on a permuted column. Verify the input to a suspect layer
  before debugging the layer.
- **The trigger shape:** ≥4 base tables AND a *multi-column* OR disjunction.
  A single-column OR (`d_year=2000 or d_year=1999`) stays a residual Filter
  and is FINE; only the multi-column form is pushed into the leaf by
  `pushSingleSourceFiltersIntoMHJTables`, which wraps `mh.Tables[i]` in a
  `*Filter`. 3 tables does not reproduce.
- Class, now seen twice (M0125-0008, -0013): a node kind introduced by one
  pass becomes an unhandled shape in every *other* pass that pattern-matches
  on node kinds. RC-2 hardened `collect`'s `default:` arm for exactly this and
  the MHJ loop nested inside it kept its own private unhardened switch.
- SF0.5 goopg cluster takes db **`postgres`**; PG oracle :65438 takes user
  **`ryo`**, db `tpcds05`. `bench/tpcds/server.sh {start|stop} {pg|sf05}`.
- The sweep supports `QUERIES="…" scripts/tpcds-sf05-regression.sh sweep`;
  it refuses to start while the nightly batch runs — `FORCE=1` overrides and
  is legitimate for row-count/value work ONLY, never a timing.
- `make plan-diff LABEL=tpcds-round2-head PLAN_DB=tpch PLAN_USER=tpch` needs
  the 65433 server up (`bench/tpch/setup_goopg.sh`); despite the label it
  holds **TPC-H** plans. 22/22 MATCH at this HEAD.

Gates run: planner + executor package suites PASS; units suite PASS;
`tpch-spotcheck.sh` PASS (Q12=2, Q13=35); `make plan-diff` 22/22 MATCH; SF0.5
**subset probe** (Q47/53/57/63/89 + Q16/94/95) PASS=7 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=1 (Q47, host-load artefact — measured 100 rows standalone);
`make ralph-state-guard`; pgbench smoke via the commit hook.

In-flight: none.
