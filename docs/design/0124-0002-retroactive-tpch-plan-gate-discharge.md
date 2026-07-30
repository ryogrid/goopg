# 0124-0002 — Retroactive TPC-H + plan-snapshot discharge for the round-2 planner commits

Status: accepted (executed 2026-07-29 — see "Execution record" at the end)
Date: 2026-07-28
Milestone: M0124-0002 (`docs/design/tpcds-round2-fixes/README.md` §13.5 action 5)

## Problem

§13.4 item 4: "**No planner regression gate covered phases 1.2 and 1.3.** They landed while
`scripts/tpch-spotcheck.sh` reported SKIPPED (the TPC-H data dir had been overwritten by
TPC-DS) and `make plan-gate` was not run. … A retroactive TPC-H + plan-gate run against
`9740fce9`'s changes has never happened — this is an open item, not a closed one."

`9740fce9` is not small. It touches exactly `internal/planner/bushy.go` and
`internal/executor/expr.go`. It took `remapByPosMap` (`bushy.go:2154`) from 11 enumerated
`Expr` kinds to 18 by adding seven case arms, and gave **`buildBindingsPosMap`** — the
bindings *position* map, phase 1.3 — eight opaque-leaf arms, five pass-through descends and a
decline-on-unknown `default:`. (It does **not** touch `conjunctIsLocalEligible`, which is one
of the seven still-unstarted phase-2.2 walkers.)
`remapByPosMap` now *visits* kinds it previously skipped, so conjuncts previously left alone
are rewritten. Unmeasured on TPC-H.

Separately, §13.1 phase 0.1 records a skipped rider: the plan baseline was never re-captured.
`plan_snapshots/` holds nothing newer than `r5-default.txt` / `r5-costdriven.txt` /
`costmodel-c4.txt`, so **no round-2 or M0125 planner commit has a baseline to diff against**.

## Design

### D1. Arms — an A/B on today's cluster, not a checkout of an old commit

A literal run *at* `9740fce9` is the obvious approach and it is wrong: that commit predates
`ef4a65a5`'s cluster rebuild and anchor re-pin, and the naive span `b3493a6e`..HEAD covers
**four** `internal/` commits — `9740fce9`, `927472e0` (executor), `5db0a067`, and `095e3ab5`
(arrived via the master merge; adds `--no-sync` init and a new fsync GUC). An fsync-GUC change
inside a *timed* A/B is a confound.

Instead, both arms are built from **HEAD**, so everything except the commits under test is
held constant:

| arm | build | meaning |
|---|---|---|
| **A** | HEAD with `9740fce9`'s `bushy.go` hunks locally reverted | the pre-phase-1.2/1.3 planner |
| **B** | HEAD unmodified | today |

`9740fce9`'s `internal/executor/expr.go` bounds check **stays in arm A** — reverting it
returns the Q8 crash and confounds the arm.

Run **A/B/A/B alternating**, not A-then-B: it is the cheapest defence against drift in
machine state, and this programme has already been burned once by an A/B whose arms differed
in server age rather than in code.

Build each arm in its own `git worktree` off a clean checkout. Never `cd` between trees
mid-loop — a stray `cd` silently routes later builds to the wrong tree.

### D2. Run configuration

- Fresh capped server per arm (`GOOPG_CG_UNIT=goopg-tpch-retro scripts/goopg-test-run.sh`).
- `GOGC=100`, `GOMEMLIMIT=12GiB`. **Not 18 GiB** — `CLAUDE.md` records Q21 drawing a
  host-level OOM at 18 GiB and completing at `GOGC=100` + 12 GiB.
- 22 queries, 600 s per-query timeout, server default parallel degree.
- **State: S-cold**, by necessity rather than preference: `ANALYZE <table>` inside database
  `tpch` errors "relation does not exist" (ledger `bench-reorg ANALYZE-scope`), so S-warm is
  unreachable on this cluster. Say so in the report. It also means this run measures the same
  state `tpch-spotcheck.sh` measures — the state M0125-0003's flag changes.
- **Same cluster for both arms.** Round-5's absolute times were taken on a different load and
  are not comparable to anything measured now.

### D3. Baseline capture — the exact commands, and two tool facts

The Makefile defaults are correct after the rebuild and must **not** be overridden:
`PLAN_DB ?= tpch`, `PLAN_USER ?= tpch`, `PLAN_PORT ?= 65433`. Since the per-DB catalog work
the TPC-H tables live in a durable `tpch` database and `tpch@tpch` survives restarts. Pointing
this at `postgres@postgres` would capture a database with no TPC-H tables — every query an
error string, the "baseline" garbage. (The older `PLAN_DB=postgres PLAN_USER=postgres` advice
is stale folklore from before the rebuild.)

```
# arm A server up:
make plan-snapshot-capture LABEL=tpcds-round2-base
# arm B server up:
make plan-diff LABEL=tpcds-round2-base MODE=structural
make plan-snapshot-capture LABEL=tpcds-round2-head
```

Two tool facts this recipe respects, both previously got wrong:

- **`plan-diff` requires `LABEL=`** (`Makefile`, `plan-diff:` exits 2 without it) and compares
  a **live capture against one stored label**. It cannot diff two stored snapshots — hence
  capture on A, then diff live-B against it.
- **`plan-gate` has no label parameter.** It selects
  `ls -t "$(REPO_ROOT)/plan_snapshots"/*.txt | head -1` — newest by **mtime**. So
  capture-then-gate on the same arm is green *by construction*, and `tpcds-round2-head` must
  be captured **last**. Any later capture silently retargets the gate for every concurrent
  line, M0123 included. Where a *specific* baseline matters, always use
  `make plan-diff LABEL=…`, never `plan-gate`.

`plan_snapshots/tpcds-round2-head.txt` is **committed**. It is the durable artifact of this
task and the thing whose absence let phases 1.2/1.3 land ungated.

### D4. Acceptance

1. Row counts match `bench/tpch/spotcheck_expected.env` and `ci/batch/tpch-row-anchors.csv`
   (both re-pinned by `ef4a65a5`), identically on both arms.
2. The arm-to-arm plan diff shows no diff, or every hunk is attributed to a named
   `9740fce9` behaviour change and justified in the report.
3. **Noise band, advisory not blocking.** Round-5 §3 measured the no-join floor at
   **−1.6 % (Q1) … +7.1 % (Q6)** and explicitly calls Q11 +8.4 %, Q17 +7.5 %, Q19 +7.1 %
   noise, concluding that a small query's **2–8 %** move cannot be attributed. The cited source calls **Q11 +8.4 %** noise, so an
   "investigate above 8 %" rule would flag its own example. Use: a move **> 10 %** is
   investigated and explained; **> 25 %** blocks.
4. Nothing that completed in arm A fails to complete in arm B.

### D5. If a regression is found

It is a round-2 defect, not new scope. File a ledger row and an M0125 blocker; do **not** fix
it here — the value of this run is a clean attribution, and folding in a fix destroys it.

## Why the spotcheck is not sufficient

`scripts/tpch-spotcheck.sh` checks Q12/Q13 **row counts**. In round-5 §6 every *completing*
query returned identical rows while running up to 1.9× slower, and three others did not
complete at all — Q12/Q13 among neither group, so the spotcheck would have reported green. `9740fce9`'s risk is both correctness (index remapping) and
shape (which bindings a conjunct is judged to span), so this task runs both instruments.

## Deliverable

`analysis/tpch-tpcds-round2-retro-<YYYYMMDD>.md`: provenance (both arms, the rebuild commit
`ef4a65a5`, GC settings, the S-cold constraint and its reason); the 22-query A/B table with a
ratio column and the noise band marked; the plan-diff verdict hunk by hunk; the row-anchor
check; and a closing statement of which snapshot label is now the live baseline.

**Also retro-files §8 step 7's outstanding artifact for phase 2.1.** §13.1 records that
RC-1b's TPC-H before/after "exists only in the commit message and ledger row; no `analysis/`
artifact was produced" — a protocol violation §13.5 does not list. One section here closes it.

## Gate

Docs plus one committed snapshot. `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
plus the pre-commit hook. No engine change.

## Execution record (2026-07-29)

Executed as designed; report at `analysis/tpch-tpcds-round2-retro-20260729.md`,
raw output and the two harness scripts under `analysis/m0124-0002/`.

**Verdict: discharged — no regression attributable to `9740fce9`.** Arms built
from HEAD `40ad746a` per D1 (arm A = the `bushy.go` hunks reverse-applied in
worktree `tmp/wt-armA`, −95/+1; `internal/executor/expr.go` verified byte-identical
between arms, so the bounds check stayed and the Q8 crash could not confound the
arm). All four D4 acceptance criteria met:

- 22/22 queries completed on both arms with identical row counts, and all 12
  `spotcheck_expected.env` / `tpch-row-anchors.csv` anchors exact on both.
- `make plan-diff LABEL=tpcds-round2-base MODE=structural` returned **22/22
  MATCH** — `9740fce9` changes which conjuncts are remapped, not which plan is
  chosen, on every TPC-H query. That also makes the timing table a like-for-like
  comparison: identical plans, so no timing delta could be a shape effect.
- Stream totals 912 s (A) vs 885 s (B). Two queries crossed D4.3's 10 % band —
  Q9 −13.6 % and Q22 +14.3 % — and round 2 re-read both. In each case the
  **intra-arm** spread exceeded the inter-arm gap (Q9 arm A alone: 202.5 → 166.3 s,
  22 %; Q22 first-vs-later read inside one server: 22 %), so both are stream
  position / page-cache artifacts, not code. Nothing approached the 25 % blocking
  band, and D5 was never triggered.

`plan_snapshots/tpcds-round2-head.txt` is captured **last** and committed, with
`tpcds-round2-base.txt` alongside it as the arm-A reference. D3's two tool facts
held exactly as written: the Makefile's `tpch@tpch` defaults resolved to the real
data (the `postgres@postgres` fallback never fired) and the capture order is what
makes `plan-gate` meaningful for the next line that runs it.

**Deviation from D1, filed as a deferral row** (`.ralph/deferral_ledger.md`,
2026-07-29): round 2 was narrowed from the full 22-query stream to the two
queries under question. It adjudicates exactly what the protocol wanted round 2
for, but it is a weaker drift defence than A/B/A/B — a *uniform* drift would be
invisible to it, cross-checked only by the single-reading stream totals.
