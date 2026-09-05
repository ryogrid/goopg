# C-20d — index-probe cost calibration: 1.0 → 2.0

Date: 2026-09-05. Item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md`
C-20d. **This is the session's largest measured performance result.**

```
regime: fresh capped server per arm, GOGC=100/GOMEMLIMIT=12GiB, S-cold serial,
  work_mem 64MB, statistics PINNED (GOOPG_ANALYZE_SEED=20260905, autovacuum off),
  TPC-H SF=1 port 65433 tpch@tpch
values: tpch-runner -digest + -diff on every arm
```

## What was wrong

`indexProbeCostMultiplier` (`internal/optimizer/cost_funcs.go`) exists
because, in its own comment's words, *"PG's constants (multiplier 1)
under-cost goopg's NL-index probe — goopg materialises the whole TID list
eagerly per probe, so an NL-probe of a large relation runs far slower than
PG's random_page_cost model predicts, and the cost-driven DP would pick
ruinous PG-shaped NL plans (measured: Q5/Q9 20-200x)."*

**It shipped at 1.0** — exactly the value the comment identifies as wrong.
The trailing sentence explains why: *"the calibrated default is set once a
value is validated on SF1"*, and that validation was never run. The knob
was created, documented, and left at the value it was created to replace.

C-20d was filed as "retire this flag". Retiring it at 1.0 would have made
the mis-costing permanent.

## Measurement

Probed on the NL-sensitive subset first (Q2, Q5, Q7, Q9, Q17, Q20, Q21),
then confirmed on the full 22:

| Q | mult=1 | mult=2 | mult=4 |
|---|---|---|---|
| Q5 | 21.60 s | **4.07 s** | 6.84 s |
| Q7 | 15.72 s | **5.86 s** | 6.24 s |
| Q9 | 13.17 s | **7.06 s** | 7.18 s |
| Q3 | 6.25 s | **2.67 s** | — |

Full suite, 21 timed labels: **138.58 s → 100.79 s (−27%)**.

No query regressed outside the noise band. Q18 30.93 vs 31.93, Q21 12.56 vs
12.80, Q12 12.78 vs 12.71, Q1 7.48 vs 7.15.

**2 was chosen over 4** because they select the same plans on the probed
queries while 4 is marginally worse on Q7 — 2 is the smaller departure from
PG's constants that still buys the whole win, and raising it further has no
evidence behind it.

## Correctness

- TPC-H values **24/24 MATCH** against the multiplier-1 baseline
  (`tpch-runner -diff`, digests not row counts).
- TPC-DS SF0.5 sweep: **PASS=95, MISMATCH=0, CKMISMATCH=0, ERROR=0,
  TIMEOUT=0, SKIP=4.**
- Q12=2 / Q13=34 canonical.

## Plans moved, deliberately — with the roll-up

This is a cost-model change, so ground rule 3 applies: the moved plans are
reported rather than suppressed. 95 shape lines changed across the suite.
The pattern is consistent — NL-index probes over large relations become
hash joins:

| Q | before | after |
|---|---|---|
| Q3 | Nested Loop | Hash Join (lineitem × orders) |
| Q5 | Merge Join over `orders_pk` Index Scan | Hash Join |
| Q9 | Merge Join over `orders_pk` Index Scan | Hash Join |
| Q7 | two-key hash + merge spine | single-key hash + Join Filter |

**Plan parity against PG improved**: `match=5 shapediff=15 missingnode=2`
→ **`match=6 shapediff=14 missingnode=2`**. One query moved INTO shape
agreement with PostgreSQL, and the shapediff category decremented — the
monotone direction take3 09 §5 requires. So the calibration moved goopg's
plans toward PG's, not merely toward faster.

Baseline re-pinned as `plan_snapshots/probe-mult2-20260905.txt`;
`make plan-gate` passes 22/22 in structural mode and in `MODE=costs`.

## What this does NOT resolve

- **C-20d's own goal — retiring the flag — is not done and is now
  explicitly blocked.** The multiplier is load-bearing at 2.0; deleting the
  knob would hard-code a calibration that the comment itself expects to be
  revisited once goopg's NL-probe execution stops materialising the whole
  TID list eagerly. The knob stays, with a validated default instead of an
  unvalidated one.
- The underlying execution defect is untouched. The calibration compensates
  for it in the cost model; it does not make an NL probe cheaper.
- Four tests pinned the old default and were updated to express the
  arithmetic *through* the constant rather than through its value, so the
  next recalibration does not require editing arithmetic literals. One
  (`TestNLIArmAdmission`) was decoupled from the live constant entirely: it
  tests `try_nestloop_path`'s admission verdict, and reading the
  calibration made a calibration change silently retarget an admission
  test.
