# M0144-0008 — second TPC-DS SF1 cadence capture

Second-ever full-SF1 goopg-vs-PG plan-parity capture, taken per the
per-milestone-boundary convention filed in
`docs/design/0100-0149/m0144-0008-tpcds-sf1-cadence.md`. First capture: P0-E7
(`analysis/m0142/p0e7-tpcds-sf1-*.txt`, HEAD `a0e741a68`, 2026-09-18).

## Method (G3)

- **goopg**: binary built from HEAD `5fa3c98c9` (sha256
  `6a76d29056e11e3e85d91c67b53141d55bca1d7b641b0a6fc2dab6717326a2d3`),
  serving a private `cp -r` clone of `bench/tpcds/runtime_goopg/data` (SF1,
  3.3 G, source server down / no `postmaster.pid` at copy time) on `:5593`
  under cgroup unit `m0144-0008-sf1`. Captured with
  `CAPTURE_ENGINE=goopg GOOPG_EXPECT_BIN_SHA256=<above>
  scripts/capture-tpcds.sh 5593 postgres postgres … tmp/m0144-0008-data-sf1`.
- **PG**: `CAPTURE_ENGINE=pg scripts/capture-tpcds.sh 65438 tpcds ryo …` —
  read-only, `EXPLAIN`-only, no datadir arg.
- Plan file sha256: goopg `fe094574…`, pg `d8418309…` (differs from P0-E7's
  `e0bc37d2…`/`991955cb…` — not a re-measure of the same artifact).
- Stats epochs (capture-stamp): goopg `e036d311a5e0d84e`, pg `4cdaa6d2f9c2034f`.
- The diff tool changed only in a comment between the two captures
  (`44b17d459`, M0137-M0143 → M0137-M0144 wording) — category shifts below
  are real plan differences, not tool drift.

## Result vs P0-E7

```
# this capture (HEAD 5fa3c98c9):
PLAN-PARITY: queries=99 match=1 shapediff=73 unparsed=0 missingnode=22 error=3 timeout=0
CATEGORIES: join-order=89 join-method=67 scan-type=58 parameterisation=52 aggregation-strategy=45 sort-strategy=68 parallelism=88 qual-placement=18 rendering=25
CATEGORIES-EXCL-MATCH: join-order=89 join-method=67 scan-type=58 parameterisation=52 aggregation-strategy=45 sort-strategy=68 parallelism=88 qual-placement=18 rendering=25

# P0-E7 (HEAD a0e741a68):
PLAN-PARITY: queries=99 match=1 shapediff=73 unparsed=0 missingnode=22 error=3 timeout=0
CATEGORIES: join-order=91 join-method=71 scan-type=60 parameterisation=46 aggregation-strategy=72 sort-strategy=73 parallelism=88 qual-placement=22 rendering=23
```

- `match=1/99` unchanged — still **Q41** only. Q9 remains `SHAPE-DIFF` at
  SF1 (consistent with P0-H12's "pre-dates `27d4ae001`" finding).
- Headline counts identical (shapediff 73, missingnode 22, error 3 — the
  Q36/Q70/Q86 dsqgen skips).
- Per-category movement beyond the ±3 noise band in both directions:

| category | P0-E7 | M0144-0008 | Δ |
|---|---|---|---|
| aggregation-strategy | 72 | 45 | −27 |
| sort-strategy | 73 | 68 | −5 |
| join-method | 71 | 67 | −4 |
| qual-placement | 22 | 18 | −4 |
| join-order | 91 | 89 | −2 |
| scan-type | 60 | 58 | −2 |
| parallelism | 88 | 88 | 0 |
| parameterisation | 46 | 52 | +6 |
| rendering | 23 | 25 | +2 |

## Per-query shape-delta

55 of 99 queries changed their divergence-category set between the two
captures. Net per-category flow (queries where a category stopped
diverging vs started):

| category | lost | gained | net |
|---|---|---|---|
| aggregation-strategy | 28 | 1 | −27 |
| join-method | 14 | 10 | −4 |
| qual-placement | 7 | 3 | −4 |
| sort-strategy | 7 | 2 | −5 |
| parameterisation | 6 | 12 | +6 |
| parallelism | 5 | 5 | 0 |
| scan-type | 4 | 2 | −2 |
| join-order | 3 | 1 | −2 |
| rendering | 0 | 2 | +2 |

Largest single-query narrowing: Q12 dropped from 6 divergent categories to
2 (`join-order,join-method,aggregation-strategy,sort-strategy,parallelism,rendering`
→ `parallelism,rendering`); Q91 to 1; Q93 and Q98 to 2.

## Attribution

38 production commits landed in `a0e741a68..5fa3c98c9` under `internal/` +
`cmd/`. The `aggregation-strategy` −27 movement is consistent with the
grouping-path ordering work in that range — notably `1e48aca50`
(M0141-S2b-11: emit SORTED grouping candidates before HASHED, PG order)
and `a628b5381` (M0141-S2b-13: restore PG `COSTS_EQUAL` in the serial
pathlist comparator). The `parameterisation` +6 is a *new-axis* effect —
queries whose agg/join divergence resolved now expose parameterisation as
the remaining axis — not necessarily a regression; per-query write-up left
to the census (M0144-0002's SF1 table already carries these records).

## Cadence evidence

This second capture is the proof the task existed to produce: the SF1
corpus **does** move with landed planner work (−27 in the headline
category in 2 days), so SF0.25 alone under-reports convergence. The
per-milestone-boundary convention is pinned in the design doc.

Artifacts: `m0144-0008-tpcds-sf1-{goopg,pg}.plans.txt`,
`m0144-0008-tpcds-sf1-diff.txt` (this directory).
