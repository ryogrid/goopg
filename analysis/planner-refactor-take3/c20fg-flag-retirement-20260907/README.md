# C-20f / C-20g — flag retirement adjudicated against the byte-identical gate

Date: 2026-09-07. Items: `docs/design/not_ralph/minimize_datum/TODO_ALL.md`
C-20f (`GOOPG_NLI_COSTGATE`) and C-20g (`GOOPG_PGSHAPED_DP`).
**Verdict for both: BLOCKED on their own gate. Nothing was deleted.**

```
regime: PRIVATE clone of the TPC-H SF=1 data dir at /tmp/c20-data, port 5541,
  fresh capped server per arm (GOOPG_CG_UNIT per arm, GOGC=100/GOMEMLIMIT=12GiB,
  MemoryMax=16G), autovacuum OFF, GOOPG_ANALYZE_SEED=20260905,
  work_mem 64MB, shared_buffers 2048MB (inherited from the bench conf).
  The canonical clusters (65433 / 65437) were NOT touched — peers own them.
binary: one build for every arm, `go build -o /tmp/c20-goopg ./cmd/goopg`
  off the working tree at e11b5a0a5.
capture: `estimate-audit -plan-only -port 5541 -label <arm>`, i.e. serial
  (`-serial` defaults true → `max_parallel_workers_per_gather = 0`) with
  warm per-session ANALYZE. Serial-only: see ledger
  `take3-plan-capture-is-serial-only` — these captures are blind to a
  parallelism-only plan change.
A/A control: `c20f-costgate-default.plans.txt` and `c20g-dp-on.plans.txt` are
  two captures of the same configuration on two different server lifetimes and
  are **byte-identical**, so the pin is clean and any diff below is signal.
```

## C-20f — `GOOPG_NLI_COSTGATE`: the flip moves TPC-H Q4

The flag is an escape hatch with one live value: `legacy` restores the
pre-D6.3a stats-blind semi/anti heuristic (`nl_index_join.go:62`,
`nliCostGateAccepts`). Retiring it deletes that branch.

`c20f-plans.diff` — one query moves, Q4:

| arm | Q4 join node | top-level cost | Q4 runtime (serial, warm stats) |
|---|---|---|---|
| default (cost gate) | `Nested Loop Semi Join` over `idx_lineitem_orderkey_fkidx` | 8 672.13 | **1.60 s** |
| `=legacy` | `Hash Semi Join` (`o_orderkey = l_orderkey`), Seq Scan on lineitem | 105 656.84 | **18.30 s** |

**11.4x**, in the direction the D6.3a comment predicts (this is the Q4
semi-join class recorded at 12.5x in 07 §3.8). The flip is therefore not
plan-neutral and the item's gate — "byte-identical plans for the flip" —
fails. Precedent `take3-C-20c-blocked` and `c06-collapse-flip-moves-q13`:
a flag whose flip moves a plan is not retired.

Nuance worth recording, because it differs from C-06 and C-20c: here the
**losing arm is the flag's own OFF path**, not the default. Retiring the
hatch would not change what production plans — it would delete the only
reachable spelling of the slower plan. That is a defensible deletion but it
is a deliberate *exception* to the stated gate (take3 08 §9's "or every
difference explained and timed"), not a pass of it, and deletion is
irreversible, so it is left to an explicit decision rather than taken by
default.

## C-20g — `GOOPG_PGSHAPED_DP`: 17 of 22 queries move

The flag selects the ENUMERATOR (M0127-P5.9): ON = the PG-shaped join
search, `=0` = no search, syntactic order, legacy rule rewrites. Re-verified
on the current tree (post-C-04c, which changed jointree admission), not
inherited:

`c20g-plans.diff` — 587 diff lines, **17 of 22 queries move**
(changed lines/query: Q2 67, Q8 59, Q11 42, Q5 41, Q7 36, Q9 35, Q21 35,
Q10 24, Q3 20, Q18 20, Q20 17, Q13 14, Q12 12, Q16 12, Q17 9, Q14 6, Q19 4).
Every one of those 17 also changes its top-level cost (e.g. Q8
184 794.76 → 187.96, Q2 33 460.37 → 287 869.59, Q12 1 608 955.75 →
76 169.21; the two arms cost different plan spaces, so the OFF numbers are
not a quality claim about the OFF arm).

C-20g's own text is confirmed against the current tree: the off path is a
whole second planner, not dead weight, and P6-03/P6-04's must-not-delete
status (6.5x correlated-SubPlan, 12.5x semi-join — recorded, not re-measured
here) is unchanged by C-04c. `scripts/planner-flags.env` is untouched: both
flags keep their generated labels.

## What was NOT done

- No `plan_snapshots/` re-pin (a peer owns the pin this session), and no
  `make plan-gate` run against the shared pin — both verdicts come from the
  paired captures in this directory.
- No TPC-H digest / TPC-DS sweep: no engine code changed.
