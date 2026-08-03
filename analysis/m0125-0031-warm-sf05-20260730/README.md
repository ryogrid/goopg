# The WARM TPC-DS SF0.5 gate — M0125-0031 goal (a), measured

**Verdict: warm statistics do not dissolve the timeout class. 12 of the 13
baseline members survive unchanged, the 13th (Q72) merely straddles the 300 s
cap, and no query anywhere changes its answer. Goal (a)'s target (0 goopg-only
timeouts) is NOT met, and the reason is now measured rather than predicted: the
class is a *shape* class almost in its entirety — neither cardinality regime
touches it.**

```
S-cold @ relsize=2  PASS=82 (49 ck-verified, 33 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=13 SKIP=4
WARM   @ relsize=2  PASS=83 (50 ck-verified, 33 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=12 SKIP=4
```

Report: [`sweep-COMPLETE-20260730-220423.txt`](sweep-COMPLETE-20260730-220423.txt),
merged from the five chunk files in this directory (Q1-25, Q26-50, Q51-63,
Q64-75, Q76-99). 99/99 covered, 22:04 → 23:58 on 2026-07-30.

Baseline arm (not re-run): [`../m0125-0003-sf05-relsize-20260730/sweep-COMPLETE-20260730-155432.txt`](../m0125-0003-sf05-relsize-20260730/sweep-COMPLETE-20260730-155432.txt)
— the 13-member class the M0125-0031 task body names, i.e. the **flipped
default** (`GOOPG_RELSIZE_FALLBACK` unset ⇒ 2), not the superseded 16 of the
flag-off arm.

## What "warm" means here, and how it was verified before the run

The SF0.5 cluster carries **persisted ANALYZE statistics**: M0125-0028 made
`ANALYZE <table>` resolve in the connection's database, M0125-0029 made the
statistics survive restart for every database and every connection, and
M0125-0030 warmed the three standing bench clusters. Because -0029 landed, the
gate's own "fresh S-cold server" contract no longer implies *no statistics* —
the server restarts, the statistics stay. That is precisely why this run is the
warm arm without a single change to the gate script.

Verified on the live cluster immediately before the sweep, not assumed:

| probe | result |
|---|---|
| `pg_class.reltuples > 0` over the 25 user tables | 24/25 (`dbgen_version`, a 1-row table, is the cosmetic exception recorded in §-0030a) |
| distinct tables present in `pg_stats` | 25 |
| spot values | `store_sales` 1 439 608, `inventory` 4.71e6, `catalog_sales` 720 657, `customer` 100 000, `date_dim` 73 049 |

Then the server was stopped so the sweep owned its own lifecycle.

## Run hygiene

- **One binary, one engine-id, all five chunks.** Every chunk header carries
  `engine-id: b1640d67…` and `engine-binary: running=fdd0c6e199182fbb
  on-disk=fdd0c6e199182fbb`, built to the private path
  `tmp/goopg-sf05-warm-bin` so the shared `tmp/goopg-bench-bin` (owned by the
  nightly lane) was never touched.
- **One arm, printed in every artefact**: `planner-flags:
  GOOPG_RELSIZE_FALLBACK=unset(2) GOOPG_COST_DRIVEN_JOINORDER=unset(off)
  GOOPG_MEMOIZE=unset(on) GOOPG_PARALLEL=unset(on)` — the same arm as the
  baseline, so statistics are the only intended variable.
- **Chunked for the same reason, and with the same soundness, as the baseline's
  own four chunks** (`../m0125-0003-…/run-chunks.sh`): each chunk starts a fresh
  server, which is the gate's determinism contract, and ~2 h exceeds one
  foreground call. Each chunk report is stamped `SUBSET PROBE`; the merged file
  is the gate-shaped artefact.
- **Quiet host**: no `ci/batch`, no SF=1 harness, no `FORCE`, load 1.2 and
  falling at the start — so the per-query seconds are valid, which matters
  because the verdict asked for is a TIMEOUT count.
- `RESTART_AFTER_TIMEOUT=1` throughout: goopg was bounced after each timeout, so
  no query inherited a heap left at `GOMEMLIMIT` (sweep-tail collapse).

## The single status change, of 99

| query | S-cold → warm | reading |
|---|---|---|
| Q72 | `TIMEOUT 307s` → `PASS 308s` | **not a rescue.** Both numbers sit on the 300 s cap, and the baseline's own standalone probe at a 900 s budget measured Q72 at `PASS 305s` in this arm. Q72 straddles the cap; which side it lands on is harness jitter, not a planning outcome. |

Nothing else moved status. In particular the four queries the relation-size
fallback rescued stay rescued and stay at the same cost (Q10 40→39 s, Q67
157→155 s, Q47 277→276 s), so warm statistics neither add to nor subtract from
that earlier win.

**The 12 hard members are identical to the baseline's**: Q5, Q8, Q14, Q30, Q31,
Q35, Q54, Q64, Q65, Q71, Q78, Q81. Every one of them exceeded 300 s under
*full* statistics — MCVs and histograms included — after already having
exceeded it under relation-size-only cardinality and under no cardinality at
all. Three cardinality regimes, one class, no movement.

## Correctness: warm statistics change no answers

All **82** queries that PASS in both arms agree on row count **and** value
checksum — 50 of them checksum-verified, the rest row-count-only because a
saturated `LIMIT` window has no stable row set. Zero MISMATCH, zero CKMISMATCH,
zero ERROR in either arm. A statistics regime that re-plans 99 queries produced
no answer change; that is the strongest correctness statement this instrument
can make, and it is what M0124-0005's checksum column exists to say.

This also discharges the -0030 prediction that row-count gates must not move.
They did not.

## Runtime: flat overall, one real regression

Common-PASS wall time: **2336 s S-cold → 2398 s warm** (warm 2.7 % slower).
That aggregate is one query:

| query | S-cold → warm | note |
|---|---|---|
| **Q18** | `117s` → **`251s`** (2.1× slower) | the only query outside the noise band moving the wrong way. Its history: `156s` flag-off → `117s` relsize=2 → `251s` warm — so warm statistics cost Q18 more than the relation-size fallback ever won it. Filed as **`M0125-0033`**. |

Excluding Q18 the two arms are 2219 s vs 2147 s, i.e. **warm is 3.2 % faster** —
inside the harness's single-run noise. The largest individual wins are small in
absolute terms (Q24 34→20 s, Q6 28→18 s, Q13 31→23 s); the largest losses
besides Q18 are 1–2 s moves on sub-5 s queries. Read the per-query column as
directional only: M0125-0031's TPC-H motion measured this harness's single-run
band at ≈±17 %, and nothing here contradicts that.

## What this means for M0125-0031 and what comes next

1. **Goal (a) is measured, not met.** The target is 0; the result is 12 (13 with
   Q72's marginal member). The gap is not a tuning gap — it is the whole class.
2. **The predicted split happened, and it split all the way to one side.** The
   task body expected the class to divide into size-starved members (rescued by
   warm stats) and shape-class members (not). Zero members landed on the
   size-starved side. Every remaining timeout is a plan-shape defect:
   RC-8 rescan-per-outer-row, CTE×N re-execution, missing TopN pushdown —
   M0125-0026's suspects a/d/e.
3. **Therefore the fix path runs entirely through `M0125-0026`** (capture and
   classify both engines' plans for the class) and the per-class tasks it files
   from `M0125-0032`+. No further cardinality work can move goal (a); that
   avenue is now closed by measurement rather than by argument.
4. TPC-H agrees. `M0125-0032` recorded Q21 as TPC-H's only timeout, surviving
   both cardinality regimes at 14.4 GB RSS. The two benchmarks now report the
   same shape: one taxonomy, not two.
