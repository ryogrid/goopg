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

## 4a. CORRECTION — §4's conclusion was too strong

**§4 said "PG emits the SubPlan, so slice 1 moved Q30 toward PG and the
timeout is the rule's sanctioned case". That is wrong, and I committed
it.** The SubPlan matches. The JOIN STRUCTURE does not, and that is
where the 70x lives:

```
goopg R25:  Nested Loop  Filter: (ca_address_sk = c_current_addr_sk)
              Nested Loop
                CTE Scan ctr1
                Seq Scan on customer_address_1      <-- CROSS PRODUCT
              Index Scan using customer_pkey

PG:         Nested Loop
              Nested Loop
                CTE Scan ctr1
                Index Scan using customer_pkey      <-- INDEX
              Index Scan using customer_address_pkey <-- INDEX
```

goopg cross-products `ctr1 x customer_address` and applies the join
predicate as a `Filter` above; PG index-looks-up both inner sides. PG
runs the query in **4.1 s** (Q81: 14.7 s). So the shape is NOT PG's and
the slowness is NOT inherent to it.

**How I got it wrong:** I compared the one node the diff drew my eye to
(`SubPlan`), found PG had it too, and generalised to "the plan matches
PG" without comparing the rest of the structure. That is exactly K22 —
a headline outrunning evidence that was correct as far as it went.

**Two falsified repair hypotheses, both measured** (recorded so neither
is retried):

1. *The lateral driver re-materialises the CTE per outer tuple.*
   `lateralJoinStream` does empty `innerCTE` per outer row, so this was
   plausible. Seeding it from the enclosing cache instead changed
   nothing — still >300 s.
2. *The CTE is being rebuilt at all.* Instrumented the materialise
   path: **exactly 1 materialisation** for Q30. The cache works.

The cost is the cross-product intermediate, on which the SubPlan is
then evaluated per row.

**Revised status of the timeouts:** they are a **plan divergence**
(join-method / scan-type on Q30's inner sides), not a sanctioned
slowdown. They belong to the parity work, not to slice 2's driver, and
the sweep's `TIMEOUT=2` is a real loss of values coverage on two
queries — not an accepted cost.

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
**It should not — but not for the reason §4 gave.** §4a shows the cause
is a cross-product join structure, which is a PLANNER divergence, not
an executor-driver one. Slice 2 cannot fix it and must not try.
Slice 2's job stays what `DESIGN.md` §§2–5 says — the executor driver —
and its §6 open questions (multi-key/SAOP keys, BitmapHeapScan inners,
deform bounds) are the load-bearing part.

If the sweep is re-run, Q30/Q81 report TIMEOUT with MISMATCH=0
(measured: `PASS=93 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=2 SKIP=4`).
Per §4a that is a divergence to CLOSE, not a state to accept: the
target is PG's index-scan inners on Q30's two joins. Restoring the old
decorrelation is still the wrong fix — it would trade one non-PG shape
for another.
