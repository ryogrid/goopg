# 0124-0004 — Q35: resolve the row count or classify it

Status: **partially executed** — D1 landed, D4 half-discharged, D5's third
disposition reached but on a **contaminated host**, so the run must be repeated.
See "Execution record (2026-07-29)" at the end.
Date: 2026-07-28 (plan), 2026-07-29 (first execution)
Milestone: M0124-0004 (`docs/design/tpcds-round2-fixes/README.md` §13.5 action 7)

## Problem

Q35 is the only query in this programme whose **row count** has never been recorded on
goopg. The measurement history is contradictory in a way worth stating precisely:

| sweep | scale | budget | goopg |
|---|---|---|---|
| 2026-07-26 (`analysis/tpcds-sf1-goopg-20260726.md`) | SF=1 | 600 s | **OK, 525 s, row count `?`** — lost to the **PATH-loss** harness defect |
| set A, 2026-07-27 | SF=1 | 600 s | TIMEOUT 651 s |
| SF0.5 gate, 12:09 | SF0.5 | **300 s** | **TIMEOUT 319 s** |
| SF0.5 gate, 21:46 | SF0.5 | 180 s | TIMEOUT 201 s |

**The lost count is a PATH-loss casualty, not a `tail -1` one.** `query35.sql` holds a single
statement, and the multi-statement set is Q14/Q23/Q24/Q39 (`0124-0001` §D6). The parent report
rules `tail -1` out for exactly this reason and attributes the `?` entries to `psql` falling
off `PATH` partway through the sweep. Do not repeat the `tail -1` attribution.

Three further corrections to the framing the audit invites:

- **The SF0.5 "201 s" is a kill line, not a runtime.** Every TIMEOUT row in that sweep
  carries the same ~20 s harness overhead above its budget. Q35 also timed out at the
  **300 s** budget, so its SF0.5 runtime is unknown and above ~300 s.
- **PG's answer is already git-tracked.** `oracle.txt` holds `35|OK|100|0`, and set A records
  PG at 2 s / 100 rows for SF=1. This is a goopg-only question; the "times out on both" branch
  is falsified in advance.

There is also a genuine anomaly to record rather than explain away: **Q35 completed once at
SF=1 (525 s) but has never completed at SF0.5, on half the data, above 300 s.** Halving the
dataset should not make a query slower. That points at plan instability or GC-state
contamination rather than at volume, and it is itself a finding.

Why this matters beyond bookkeeping: §13.3 documents a query that made exactly this
transition and turned out to be a *correctness* defect — "**Q51 left the timeout class and
entered the wrong-answer class** — it completes at 597 s under the 600 s budget and returns
0 vs 100. A wrong answer that had been hiding behind a timeout." Until Q35 completes *with a
count*, its class is unknown, and "M0125-0003 fixed Q35" would be unfalsifiable.

## Design

### D1. Cheap path — SF0.5, solo, with real headroom. **A small script change is in scope.**

The SF0.5 stack needs no PostgreSQL instance and the oracle already carries the answer. But
neither existing script can run this as written, so the task includes the fix:

| obstacle | evidence | what this task does |
|---|---|---|
| `scripts/tpcds-sf05-regression.sh` has **no per-query mode** — `cmd_sweep` is `for q in $(seq 1 99)`, and the dispatcher accepts only `build-data\|load-pg\|load-goopg\|oracle\|sweep\|all\|status` | script body | add an optional query-list argument to `sweep`, mirroring `tpcds-bench-compare.sh`'s `parse_qlist` |
| `TPCDS_RESULTS_DIR` is **not** env-overridable — `bench/tpcds/env_tpcds.sh` sets it with no `:-` default, so a "SF0.5" run of `tpcds-bench-compare.sh` still writes into the SF=1 results dir and would clobber `0124-0001`'s `goopg_q35_*.txt` | `env_tpcds.sh` | give it a `:-` default so the SF0.5 run writes to its own directory |
| `restart_goopg` in `tpcds-bench-compare.sh` hardcodes `server.sh stop/start **sf1**` | `tpcds-bench-compare.sh` | parameterise the scale, or use the SF0.5 script's own path |
| `guard_sf1_sweep` in the SF0.5 script **refuses to run while the SF=1 sweep harness is active** (override `FORCE=1`) | script header | do not override it — see D3a |

Then:

1. `bench/tpcds/server.sh start sf05` — that cluster only (port **65437**).
2. Run Q35 **solo** at `TIMEOUT_SEC=900`: 3× the largest observed overrun, so a GC-degraded
   run still completes.
3. Compare to `100`.

Cost: one query plus a small shell change. This is the whole task if it succeeds.

### D2. SF=1 confirmation, only if needed

Run only if SF0.5 mismatches or does not complete at 900 s:
`TIMEOUT_SEC=1800 ENGINES="goopg pg" scripts/tpcds-bench-compare.sh 35`, both servers freshly
started. The PG arm supplies the SF=1 oracle.

### D3a. Sequencing against M0124-0001

Two hard constraints the milestone's "0004 runs solo" line understates:

- `guard_sf1_sweep` physically refuses to start while the SF=1 sweep is active, and that guard
  should be respected rather than `FORCE=1`-ed — the two would contend for the host.
- D6's deliverable is a row **in M0124-0001's report**, so 0004 either follows 0001 or 0001's
  report is reopened to receive it. Prefer following it.

### D3. Solo-run hygiene — why this cannot be read off M0124-0001's sweep

A goopg server that has just run a timeout query sits at `GOMEMLIMIT` with `GOGC=off` and
thrashes GC. Q35's SF=1 overrun was 8.5 %, so a run taken after another timeout can be a
**false** timeout — the most likely explanation of the 525 s → 651 s flip between the two SF=1
sweeps, and a candidate explanation for the SF0.5 anomaly. Q35 must be measured from a freshly
started server with **no prior query in the process**.

### D4. Fold in the RC-8 measurement while the plan is in hand

Q35's shape is `exists(…) and (exists(…) or exists(…))` — an exact instance of §7.3's RC-8
pattern, the one named for Q10/Q69. Capture one `EXPLAIN ANALYZE` with the per-SubPlan
`Calls/Rebuilds/Rescans/CacheHits/CacheMisses` counters. If `CacheMisses ≈ Calls`, RC-8's
"measure first" reopen criterion is discharged for **three** queries in a single run, and the
indicated fix is hashed-SubPlan caching rather than decorrelation.

Q35 also calls `stddev_samp` three times, so it was a live candidate for the
`exactIntVariance` `Quo(0,0)` panic fixed by `927472e0` — and its checksum, once M0124-0005
lands, needs that task's float normalisation.

### D5. The decision this task produces

| outcome | classification | consequence |
|---|---|---|
| goopg = 100 rows | performance-only | Q35 belongs entirely to M0125-0003's timeout class |
| goopg ≠ 100 rows | wrong answer hiding behind a timeout — the Q51 shape | a **new** correctness defect: ledger row + M0125 blocker. The fallback must not be credited with "fixing" Q35 |
| does not complete at 900 s / 1800 s | **unresolved — a third disposition, not a failure of this task** | re-run once from a cold server; if it still does not complete, classify Q35 as *timeout-class, row count still unknown*, record that explicitly, and hand it to M0125-0003 with the caveat that "M0125-0003 fixed Q35" remains unfalsifiable until a count exists. Apply §8 step 5 first (solo run, RSS monitoring, cgroup `memory.events` `oom_kill` check; "connection lost with no `backend goroutine panic`" means SIGKILL, not a Go panic) |

Record the SF=1-vs-SF0.5 anomaly in the report regardless of outcome. Never write a goopg
number into `oracle.txt`, which is PG ground truth.

### D6. Deliverable

A Q35 row in M0124-0001's report carrying the count and the budget it was measured at (flagged
budget-incomparable with the 600 s column), plus an UPDATE on the existing
`tpcds-round2 timeouts` ledger row recording the classification, and — if D4 succeeded — the
SubPlan counter readings on the `tpcds-round2 exists-under-or` row.

## Non-goals

Making Q35 faster (M0125-0003). Re-running the whole sweep at an extended budget — the
budget-invariance rule means a longer budget produces a table comparable to nothing; Q35 gets
its own budget and its own asterisk.

## Gate

The shell changes from D1 → `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
plus the pre-commit hook. No engine change.

---

## Execution record (2026-07-29)

Artefacts: `analysis/tpcds-q35-m0124-0004/`.

### D1 — landed, and one obstacle above was mis-stated

All four obstacles were real. Two corrections to D1's table:

- The per-query mode is **`QUERIES=` env, not a positional argument**. `cmd_sweep`
  and `cmd_oracle` share the loop, and a positional list would have collided with
  the subcommand dispatcher (`sweep`/`oracle`/`all` are already `$1`). A new
  `query_list` helper serves both. A subset sweep stamps its report `SUBSET
  PROBE … NOT a gate result`, so a one-query summary cannot be misread as a
  passing gate.
- D1 named `TPCDS_RESULTS_DIR`, but the SF0.5 harness reads **`SF05_RESULTS_DIR`**;
  both are now `${VAR:-default}`. A third hazard surfaced that D1 did not
  anticipate: `ORACLE` was derived from `${OUTDIR}`, so redirecting a probe's
  output would have orphaned the git-tracked fixture. The oracle path is now
  `SF05_ORACLE`, defaulting to the canonical location independently of
  `SF05_RESULTS_DIR` — it is a fixture, not a run artefact. And because
  `cmd_oracle` *truncates* that file, `QUERIES=` with `oracle` now hard-fails
  unless `SF05_ORACLE` also names a scratch path: a partial re-capture would
  otherwise have deleted the other 98 rows, silently reducing the gate to one
  query.

`restart_goopg` now bounces `${SF_LANE:-sf1}`. Every default is unchanged, so
existing callers are byte-for-byte no-ops.

### D4 — half discharged: the shape is confirmed, the counters are not

`goopg-sf05-explain.txt` and `goopg-sf1-explain.txt` are **identical in shape**.
Q35 is the predicted RC-8 instance:

```
->  Multi-Way Hash Join (3 tables)
      Filter: (EXISTS(SubPlan 1) AND (EXISTS(SubPlan 2) OR EXISTS(SubPlan 3)))
      SubPlan 1
        ->  Nested Loop (INNER)
              Filter: ((($0 = ss_customer_sk) AND (d_year = 1999)) AND (d_qoy < 4))
          ->  Seq Scan on public.store_sales
          ->  Index Scan using public.date_dim_pkey on public.date_dim
```

The load-bearing detail is **where the correlation lands**: `$0 =
ss_customer_sk` is a *Filter on the nested loop*, not an index condition and not
a hash key. Each of the three `EXISTS` therefore re-scans an entire fact table
per outer row. The `Calls/CacheMisses` counters that decide *hashed-SubPlan
caching vs decorrelation* still need a completing `EXPLAIN ANALYZE`, which no
budget has reached.

**The SF=1-vs-SF0.5 anomaly is not a plan flip** — the plans match, and
`customer` holds 100,000 rows at *both* scale factors, because the SF0.5 sampler
halves facts by key parity and copies dimensions whole. The outer cardinality —
the multiplier on all three SubPlans — is therefore identical at both scales, so
D3's "plan instability" hypothesis is ruled out for this pair. A mechanism that
survives: halving the facts does not halve the work, because a *failing* `EXISTS`
must scan its whole table while a succeeding one exits early, so removing half
the facts converts matched customers into unmatched ones and trades early exits
for full scans.

### D3 — the solo-run hygiene rule was necessary but not sufficient

Both probes were taken from freshly started servers with no prior query in the
process, and both timed out:

| probe | scale | budget | result |
|---|---|---|---|
| solo sweep | SF0.5 | 900 s | `TIMEOUT` 921 s |
| `EXPLAIN (ANALYZE, TIMING OFF)` | SF=1 | 1800 s | `TIMEOUT` 1846 s |

**Both numbers are void.** Before concluding that a query which once finished in
525 s now exceeds 1800 s, the host was checked: the **nightly CI batch was
running throughout**. It fired at `2026-07-29T00:23:44` and its TPC-H stage had a
capped goopg server on :65434 alive at **112% CPU and 7.5 GiB RSS**, 5 h in, on a
16-core / 31 GiB host.

D3 scoped contamination to *the server's own history*. The larger hole was the
*host*: the nightly shares no port and no data directory with the TPC-DS
clusters, `tpcds-bench-compare.sh` had no host-quiet guard at all, and
`guard_sf1_sweep` checked only for its sibling SF=1 harness. So "solo, fresh
server" was satisfied while a multi-hour benchmark had the other cores. The two
most recent SF0.5 gate sweeps (00:47 and 03:38) were taken the same way and
inherit the same doubt.

**M0124-0001's SF=1 re-sweep is clean** — chunks span 17:06–23:49 on 2026-07-28,
finishing 34 min before the fire — but by luck, not by design.

Both harnesses now refuse to start while `ci/batch/run-nightly.sh` or any
`ci/batch/stages/` script is alive (`FORCE=1` overrides), detecting the nightly
by driver script rather than by port so a lane change cannot re-open the hole.
Making that guard trustworthy required fixing a self-match first: `ps | grep
<script-name>` matches the invoking shell's own argv — the `pkill -f goopg` trap
of CLAUDE.md rule 3 — and it fired for real here, the SF0.5 harness refusing to
start against a `bash -c` wrapper string. `bench_foreign_procs`
(`bench/tpcds/env_tpcds.sh`) walks the `$$` ancestor chain and excludes it; both
guards were then re-verified to fire on the real nightly and not on themselves.

### D5 — outcome: unresolved, and the run is owed a repeat

Q35 reached D5's third disposition (does not complete), but on a host that
invalidates the reading, so the "re-run once from a cold server" instruction is
**not** yet satisfied. Q35 stays *timeout-class, row count unknown*, and
"M0125-0003 fixed Q35" remains unfalsifiable.

Resume, on a quiet host (the guard now enforces it):

```bash
QUERIES=35 TIMEOUT_SEC=1800 RESTART_AFTER_TIMEOUT=0 \
SF05_RESULTS_DIR="$PWD/analysis/tpcds-q35-m0124-0004" \
  bash scripts/tpcds-sf05-regression.sh sweep
```

On a second `TIMEOUT`, escalate to SF=1 with a **plain** run — not `EXPLAIN
ANALYZE`. Per-tuple instrumentation inside three per-row SubPlans is itself a
large multiplier, and using it as the escalation confounds the comparison
against the 525 s / 628 s history. D2's `tpcds-bench-compare.sh 35` form is now
usable for this (its `SF_LANE` no longer hardcodes the wrong cluster).

### D6 — not yet deliverable

No count exists, so no Q35 row can be added to M0124-0001's report. The ledger
rows for the un-recovered count and for the contaminated sweeps are dated
2026-07-29 in `.ralph/deferral_ledger.md`.
