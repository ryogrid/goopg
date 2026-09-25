# R25 slice-1 follow-up — the seam was declining whole queries because a CTE body held an OuterColumnRef

*2026-09-09. Closes the Q30/Q81 divergence that REPORT-slice1 §4a
identified but mis-attributed to costing.*

## 1. Result

| | slice 1 | + this fix | PG |
|---|---|---|---|
| Q30 | >300 s (TIMEOUT) | **3.8 s** | 4.1 s |
| Q81 | >300 s (TIMEOUT) | **5.1 s** | 14.7 s |
| TPC-DS sweep | PASS=93 **TIMEOUT=2** | **PASS=95 TIMEOUT=0** | — |

`MISMATCH=0 CKMISMATCH=0 ERROR=0` throughout. **Values coverage on the
two queries is recovered** — that was the real cost of the timeouts.

## 2. The cause, found by tracing rather than reasoning

`GOOPG_PGSHAPED_DP_TRACE=1` on Q30:

| | seam declines | DP problems searched |
|---|---|---|
| base | none | `{ctr1, customer_address, customer}` **and** the CTE body |
| slice 1 | `reason=lateral nrels=3` | the CTE body only |

Slice 1 made the seam **decline Q30's entire outer 3-way join**, so it
fell to the legacy path — which is what produced the cross product
(`Nested Loop` + `Filter: ca_address_sk = c_current_addr_sk`) where PG
index-scans both inner sides.

The chain: slice 1 gives a decomposed NLI probe `OuterColumnRef` keys →
the CTE body (planned first, in its own statement) now contains one →
`nodeReferencesOuter` → `planHasEscapingOuterRef` walks the CTEScan
LEAF's body, finds `Level >= 1`, returns true → `chainCarriesLateral`
true → seam declines.

Those refs are **bound inside the CTE body** by its own lateral join.
They do not escape. `walkPlanExprs` flattens the subtree, so depth
increments only for subquery-bearing *expressions* and never for a
lateral join's right side, which is why a bound ref read as an escaping
one.

## 3. The fix

`planHasEscapingOuterRef` returns false at a `*CTEScan`. A plain `WITH`
body is planned in its own scope and SQL gives it no way to reference
the enclosing query (goopg has no LATERAL CTE), so nothing inside it
can escape through the scan. That is the one boundary that provably
closes the scope, which is why it is the one this fix stops at rather
than attempting general depth-tracking through join nodes.

## 4. Two hypotheses this replaces, both falsified by measurement

Recorded so neither is retried (REPORT-slice1 §4a):

1. *The lateral driver re-materialises the CTE per outer tuple.* Seeding
   `innerCTE` from the enclosing cache changed nothing.
2. *The CTE is rebuilt at all.* Instrumented: exactly **1**
   materialisation for Q30.

Both were plausible and both were wrong. The trace channel that already
existed (`traceSeamDecline`, added by a previous round for exactly this
class of question) answered it in one run.

## 5. Gates

- optimizer + executor suites (`-count=1`) green.
- TPC-H values 22/22 byte-identical to slice 1; TPC-H parity unchanged
  (match=2, every category identical).
- TPC-DS sweep **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0
  SKIP=4** — the all-zero bar, restored.
- TPC-DS parity: match 0→0, `join-method` 73→74, everything else
  unchanged. A one-category move against restoring two queries' worth of
  values coverage and the DP search on a query that had lost it.

## 6. Note for the roadmap

This did not move the match count, as `ROADMAP-to-all-match.md`
predicts for any single round. What it did move is more basic: **a query
that had stopped entering the PG-shaped search at all now enters it
again.** Any query the seam declines cannot converge on PG's plan by
any amount of costing work, so `seam-decline` counts are worth watching
as a parity signal in their own right.
