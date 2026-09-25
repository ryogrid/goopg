# M0144-0008 — TPC-DS SF1 cadence capture + per-milestone-boundary convention

Status: recon landed 2026-09-20 (loop #40); second SF1 capture taken,
convention below. No production change.

Source: `METHODOLOGY4/03-forward-plan.md` §5 — "the corpus's goal is SF1 and
exactly one SF1 capture exists (P0-E7, `match=1/99`)… Add a
per-milestone-boundary SF1 capture, or at minimum one before the next
stocktake." Task entry: `.ralph/fix_plan.md` M0144-0008.

## Convention (pinned by this task)

**An SF1 goopg-vs-PG plan-parity capture is taken at every milestone
boundary** — i.e., when a milestone's last task closes, and at minimum once
before any stocktake/census re-baseline. Procedure (G3):

1. Build the goopg binary from HEAD; record `sha256` and `git rev-parse
   HEAD`. The tree's `internal/`/`cmd/`/`go.mod`/`go.sum` must be clean
   (untracked test files do not affect the binary).
2. Private lane only: `cp -r bench/tpcds/runtime_goopg/data
   tmp/<task>-data-sf1` **while the source server is down** (verify no
   `postmaster.pid`; a live copy is corrupt), drop the clone's
   `postmaster.pid`, start on a `55xx` port through
   `scripts/goopg-test-run.sh` (`GOOPG_CG_UNIT=<task>-sf1`). Crash
   recovery on first start is expected (copied mid-checkpoint WAL) — wait
   for `goopg listener bound` in the log.
3. goopg arm: `CAPTURE_ENGINE=goopg GOOPG_EXPECT_BIN_SHA256=<sha>
   scripts/capture-tpcds.sh <port> postgres postgres
   analysis/<ms>/<task>-tpcds-sf1-goopg.plans.txt "<hdr>"
   tmp/<task>-data-sf1`.
4. PG arm (read-only): `CAPTURE_ENGINE=pg scripts/capture-tpcds.sh 65438
   tpcds ryo analysis/<ms>/<task>-tpcds-sf1-pg.plans.txt "<hdr>"` — db
   **`tpcds`** (SF1), not `tpcds025`.
5. `python3 scripts/pg-plan-parity-diff.py <goopg> <pg> >
   analysis/<ms>/<task>-tpcds-sf1-diff.txt`; record `PLAN-PARITY`,
   `CATEGORIES`, `CATEGORIES-EXCL-MATCH` verbatim plus both plan-file
   sha256s and both stats-epoch stamps.
6. Stop the lane (`<bin> stop -D <datadir>`), keep or delete the clone
   freely — it is regenerable in ~30 s of `cp`.

Cost is trivial (~7 s of EXPLAINs); the corpus is the goal's real SF1, so
there is no reason to skip a boundary. SF0.25 stays the regression *gate*;
SF1 is the convergence *measurement*.

## Second capture — result (2026-09-20, HEAD `5fa3c98c9`)

```
PLAN-PARITY: queries=99 match=1 shapediff=73 unparsed=0 missingnode=22 error=3 timeout=0
CATEGORIES: join-order=89 join-method=67 scan-type=58 parameterisation=52 aggregation-strategy=45 sort-strategy=68 parallelism=88 qual-placement=18 rendering=25
CATEGORIES-EXCL-MATCH: join-order=89 join-method=67 scan-type=58 parameterisation=52 aggregation-strategy=45 sort-strategy=68 parallelism=88 qual-placement=18 rendering=25
```

- `match=1/99` (Q41), headline counts identical to P0-E7 — but
  `aggregation-strategy` dropped 72→45 (−27, far beyond the ±3 noise
  band), `sort-strategy` −5, `join-method`/`qual-placement` −4 each;
  `parameterisation` rose +6 as a newly-exposed axis. 55/99 queries
  changed category sets. Attribution and per-query deltas:
  `analysis/m0144/m0144-0008-sf1-cadence-capture.md`.
- Evidence the cadence matters: the corpus moved −27 in its headline
  category over a 2-day, 38-production-commit window. The SF0.25 gate
  alone would not have shown this.

## D2 report fields

1. `CATEGORIES:`/`CATEGORIES-EXCL-MATCH:` + MATCH — verbatim above.
2. shape-delta counts — analysis record (lost/gained table; Q12 narrowed
   6→2 categories, Q91→1, Q93/Q98→2).
3. stats epoch: goopg `e036d311a5e0d84e`, pg `4cdaa6d2f9c2034f` (capture
   stamps; no values sweep intervened).
4. seam-decline census: N/A — capture-only recon, no production change.
5. planning route: unchanged (PG-shaped DP default; no code touched).
6. `Movement: none` — recon/measurement, no plan movement claimed.
   `Parent: none`.
7. Wall times: N/A — `capture-tpcds.sh` is `EXPLAIN`-only; no query was
   executed.

## Artifacts

- `analysis/m0144/m0144-0008-tpcds-sf1-goopg.plans.txt` (sha256 `fe094574…`)
- `analysis/m0144/m0144-0008-tpcds-sf1-pg.plans.txt` (sha256 `d8418309…`)
- `analysis/m0144/m0144-0008-tpcds-sf1-diff.txt` (full diff output)
- `analysis/m0144/m0144-0008-sf1-cadence-capture.md` (comparison record)

Private lane `tmp/m0144-0008-data-sf1` on `:5593` (cgroup
`m0144-0008-sf1`), stopped after capture; binary `tmp/m0144-0008-bin/goopg`
(sha256 `6a76d290…`, HEAD `5fa3c98c9`).
