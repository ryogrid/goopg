# TPC-DS SF0.5 gate at `GOOPG_RELSIZE_FALLBACK=2` — design 0125-0003 §D8

**Verdict: the timeout class shrinks 16 → 13 and nothing gets a wrong answer.
Stage 2 earns TPC-DS acceptance, but §D8's two named predictions are both
refuted, and the flip (`M0125-0005`) inherits one measured slowdown.**

```
flag ON   PASS=82 (50 ck-verified, 32 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=13 SKIP=4
flag OFF  PASS=79 (49 ck-verified, 30 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=16 SKIP=4
```

Report: [`sweep-COMPLETE-20260730-155432.txt`](sweep-COMPLETE-20260730-155432.txt)
(merged from the four chunk files in this directory; drivers `driver-c1..c4.txt`).
99/99 covered, 13:53 → 15:54 on a verified quiet host (no `ci/batch`, no SF=1
harness, load ≤0.9 and falling, only PG:65438 idle — **no `FORCE`, so the
per-query seconds in this arm are valid**, which matters because the verdict
asked for is a TIMEOUT count).

This is the measurement `docs/design/0125-0003-…md` §I15 named as "the single
next measurement": TPC-DS timeout relief — not the TPC-H 1.40× of
`analysis/tpch-relsize-fallback-20260730.md` — is M0125's acceptance criterion.

## The off arm was reused, not re-run, and here is the licence

Off arm: [`../m0125-sf05-fullgate-20260730/sweep-COMPLETE-20260730-102025.txt`](../m0125-sf05-fullgate-20260730/sweep-COMPLETE-20260730-102025.txt)
(HEAD `e29faca9`, 300 s cap, quiet host). `git diff e29faca9..HEAD -- '*.go'` is
empty, and both reports carry the **same** D4a source digest:

```
# engine-id: 5a45a42d33ef58ff9de0bcf5f82aa6ccfb66ddd5 c47d4ed683a0ac63d56c7f755e70892a635f3a42  diff=e3b0c44298fc
```

`diff=e3b0c44298fc` is the empty-diff digest in both, so the uncommitted tree was
shell/docs-only on both days. The two runs' **binary** shas differ
(`ca653634810c2821` vs `766595e8fd3fb675`) because the build stamps commit
metadata; the *source* the digest covers is identical. All four chunks of this
arm ran on one binary sha and one engine-id, each chunk starting a fresh S-cold
server, and no restart bounce tripped `*** SWEEP VOID ***`.

The arm itself is recorded in the report header, not just in this prose —
`scripts/tpcds-sf05-regression.sh` now prints

```
# planner-flags: GOOPG_RELSIZE_FALLBACK=2 GOOPG_COST_DRIVEN_JOINORDER=unset(off) GOOPG_MEMOIZE=unset(on) GOOPG_PARALLEL=unset(on)
```

on every sweep. Without it, two arms of the same commit are indistinguishable on
their face and the A/B rests on the operator's memory of what was exported. Every
flag prints even when unset, so "off" is a positive statement in the artefact.

## The five status changes, of 99

| query | off → on | reading |
|---|---|---|
| Q10 | `TIMEOUT 300s` → **`PASS 40s`** | ≥7.5× — the largest rescue |
| Q69 | `TIMEOUT 300s` → **`PASS 17s`** | ≥17.6× |
| Q67 | `TIMEOUT 300s` → **`PASS 157s`** | ≥1.9× |
| Q47 | `TIMEOUT 300s` → **`PASS 277s`** | closes the *runtime* half of M0125-0013 at SF0.5 — its row defect was fixed on 2026-07-30, and this is the first arm in which the query both answers correctly and fits the budget |
| Q72 | `PASS 276s` → **`TIMEOUT 307s`** | the one change in the wrong direction — see below |

**Every one of the 78 queries that PASS in both arms agrees on row count *and*
value checksum.** A planner change that alters join order on 99 queries produced
zero answer changes; that is the strongest correctness statement available from
this instrument, and it is what M0124-0005's checksum column was added to make
sayable.

## Q72 crosses the cap; it does not hang — measured, not inferred

The gate says `PASS 276s → TIMEOUT 307s`, which on its face reads like a query
falling off a cliff. Re-run standalone at a 900 s budget
([`q72-probe/`](q72-probe/)), both arms, same binary, fresh S-cold server:

| arm | Q72 |
|---|---|
| off | `PASS 270s`, 100 rows |
| on | `PASS 305s`, 100 rows |

So the flag costs Q72 **≈35 s (1.13×)** and Q72 sits ~10 % under the 300 s cap
without it. The status change is a **budget crossing of a marginal query**, not a
new unbounded plan, and the answer stays correct. This is exactly design
0124-0001 §D6's budget-marginal class, and it is why the gate's TIMEOUT column
must never be read as "unbounded" without a second budget.

It is still a real regression of 1.13× and it is charged to the flip
(`M0125-0005`), not waived.

## Both of §D8's named predictions are refuted

§D8 pre-registered two expected signals. Neither happened, and one went backwards:

- **"Q72 resolves"** — Q72 was *already passing* at 276 s in the off arm, so the
  prediction's premise (§13.3's "wrong → slow") was already stale; the flag made
  it 1.13× slower and pushed it over the cap. The prediction is refuted in the
  strongest available sense: the flag moved that query the other way.
- **"Q35 completes, having been classified first by M0124-0004"** — Q35 is still
  `TIMEOUT` at 300 s with the flag on. M0124-0004 did classify it first
  (performance-only, RC-8 shape), so the claim was falsifiable, and it is false.
  **The relation-size fallback is not what Q35 was waiting for.**

The general shape matches the TPC-H arm's lesson: the pre-registration was wrong
in both directions there too (round 4's five predicted regressions did not
regress; the predicted win, Q5, did not move). Two independent studies now say
this planner's per-query effects are not predictable from prior rounds' tables.

## Aggregate timing — the 78 common PASSes

`off = 2273 s → on = 1845 s`, **−428 s (−18.8 %)**, one binary, one budget, same
host state. Twenty-eight of the 78 move by ≥5 s or ≥1.25×; **twenty-seven are
faster and one is slower** (Q21, 19 s → 33 s, 1.74×). Largest wins: Q43 11×,
Q52 8×, Q40 6.3×, Q93 5×, Q3/Q42/Q55 4×, Q92 3×, Q88 2.11× (236 s → 112 s),
Q80 1.98×, Q49 1.82×.

The −18.8 % here and the −28.8 % of the TPC-H arm are **not** comparable numbers
(different query sets, budgets, memory configuration); what is comparable is the
sign and the shape: a large majority of queries improve, a small minority
regresses by well under 2×, and no answer changes.

## What still times out — the class this milestone is named after

13 queries: **Q5 Q8 Q14 Q30 Q31 Q35 Q54 Q64 Q65 Q71 Q72 Q78 Q81**.

So the relation-size fallback removes 4 of the 16 (25 %) and adds back one
marginal member. `M0125-0026`'s plain-EXPLAIN capture list should be **this**
13-query set (its earlier list of 16 was written against the off arm): Q10, Q47,
Q67 and Q69 are now answered and no longer need a root-cause class, and Q72 joins
as a budget-marginal member whose plan diff is worth capturing precisely
*because* the flag made it slower — it is the only query in the suite that shows
the fallback's downside on TPC-DS.

## Recommendation on the default (`M0125-0005`)

**Flip it.** The evidence for stage 2 is now two independent benchmark families:
TPC-H 1.40× / zero regressions / identical rows, and TPC-DS −18.8 % / 4 timeout
rescues / zero answer changes / one 1.13 × slowdown. The one cost is bounded,
measured, and attached to a single query.

Two conditions the flip should carry, both cheap:

1. Land Q72's 1.13 × as a known cost in the flip's own commit message and a
   ledger row — do not let the flip be described as "no regressions".
2. Do **not** fold stage 3 into the flip. §I8 says stage 3 shadows stage 2 at the
   same site, so a combined commit would make this arm unattributable.

## What this arm does not say

- Nothing about **stage 3** (`estimateBaseRelInfo.baseRows`), which is
  unimplemented; §I8's shadowing means these numbers do not transfer.
- Nothing about **SF=1**. Q35's SF=1 row count is unrecoverable by any budget
  (M0124-0004: warm floor ≈9.1 days), so SF0.5 is the only instrument that exists
  for it, and at SF0.5 the flag does not rescue it.
- Nothing about the **W arms** (`RowCount > 0`), which stay unconstructible for
  the reason §I14 measured on the TPC-H cluster.
