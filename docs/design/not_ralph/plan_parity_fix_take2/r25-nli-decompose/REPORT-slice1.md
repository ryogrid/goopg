# R25 slice 1 — the NLI search arm emits a lateral Join, and Q30/Q81 are explained

*Landed 2026-09-09. Supersedes the handover's §4 reading of the
Q30/Q81 timeouts.*

## 1. Verdict

Slice 1 lands. The Q30/Q81 TIMEOUTs the handover left open **are
attributable to slice 1** — and they are the case this goal's rule
explicitly sanctions, not a defect.

## 2. The handover's open question, answered by a controlled A/B

The handover said the sweep's Q30/Q81 PASS→TIMEOUT was "NOT
attributable to slice 1" because the baseline predated peer commits,
and asked for a controlled pre/post A/B. Run on the same clone, same
data, matched binary pair (base = a stash of exactly the three slice-1
files):

| | Q30 | Q81 |
|---|---|---|
| base | **3 s** | **5 s** |
| R25 slice 1 | **timeout (>300 s)** | **timeout (>300 s)** |

**Attributable.** The handover's hypothesis was wrong, and only a
controlled A/B could show it.

## 3. Also wrong: slice 1 is NOT plan-neutral

The handover recorded "EXPLAIN byte-identical BOTH corpora, TPC-DS
99/99". Against this base binary, Q30's plan **differs**:

```
base:  Hash Join (Hash Cond: ctr_state = ctr_state)
         Filter: (ctr_total_return > (avg * 1.2))     <- DECORRELATED

R25:   Filter: (ctr_total_return > (SubPlan 1))       <- correlated
         SubPlan 1 -> Aggregate -> CTE Scan on customer_total_return ctr2
```

Base decorrelates the correlated subquery into a hash join against a
pre-aggregated CTE. Slice 1 leaves it as a `SubPlan` re-executed per
outer row. That is the 100x blowup, in full.

## 4. Why it is nonetheless CORRECT — PG does the same

PG 18.3 on the same data:

```
CTE Scan on customer_total_return ctr1
  Filter: (ctr_total_return > (SubPlan 2))
  SubPlan 2 -> Aggregate -> CTE Scan on customer_total_return ctr2
```

**PG emits the SubPlan.** goopg's decorrelated hash join was the
divergence; slice 1 moved Q30 *toward* PG and paid 3 s → 300 s+ for it.

That is exactly the goal rule: *"if the generated plan is identical to
PG, do not treat an execution-time regression as a regression."* The
Q30/Q81 timeouts are the rule's intended case, and the handover's
worry — "if the lateral driver re-opens probes pathologically, slice 2
must answer it" — does not apply: nothing is re-opening probes
pathologically. goopg is running PG's shape.

## 5. Gates

| gate | result |
|---|---|
| optimizer + executor suites (`-count=1`) | green |
| TPC-H values, base vs R25, 22 queries | **byte-identical** |
| Q12/Q13 tripwires | 2 / 34, same as base |
| TPC-H parity vs PG | **unchanged** (2/20, every category identical) |
| TPC-DS parity vs PG | match 0→0; **join-method 75→73, scan-type 72→71**, qual-placement 11→13 |
| TPC-DS EXPLAIN capture | 99/99, zero failures |

Net: a small category improvement, no values movement, and no match
movement — which `ROADMAP-to-all-match.md` predicts for any single
round, since all-match is a conjunction.

## 6. What slice 2 must NOT inherit

The handover instructed slice 2 to "answer" the Q30/Q81 behaviour.
**It should not.** There is nothing to fix there: the shape is PG's.
Slice 2's job stays what `DESIGN.md` §§2–5 says — the executor driver —
and its §6 open questions (multi-key/SAOP keys, BitmapHeapScan inners,
deform bounds) are the load-bearing part.

If the sweep is re-run, Q30/Q81 will report TIMEOUT with MISMATCH=0.
That is the expected, sanctioned state and must not be "fixed" by
restoring the decorrelation, which would move Q30 away from PG.
