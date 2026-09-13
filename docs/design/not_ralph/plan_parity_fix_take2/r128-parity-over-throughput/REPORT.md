# R128 result — `GOOPG_NARROW_COST_INPUTS` promoted to default ON

The promotion decision R124 handed forward by name, taken. **TPC-H
`join-method` 10 → 9 and `scan-type` 9 → 8.** Match stays **6/22** and no
query flips, exactly as predicted — this is progress along the goal's
metric, not a win.

And the headline surprise: **the throughput cost is +0.2%, not the ~10%
rev 1 assumed.** That number was inherited from a different
implementation; measured for this flag it is nil.

## 1. The cut

| file | change |
|---|---|
| `narrowcostinputs.go:51-53` | parser inverted `v == "1"` → `v != "0"`; default ON, opt-OUT, matching the `GOOPG_NARROW_*` family |
| `narrowcostinputs_test.go` | `TestNarrowCostInputsFlagIsStrictAndDefaultOff` → `…IsOptOutAndDefaultOn`, **re-pinned in both directions**, not deleted |
| `flaglabels.go:104-108` | the "this one is opt-IN" comment rewritten (it would otherwise contradict the code), keeping the still-true distinction: it is the only flag in the family gating a planner COST input rather than executor SHAPE |
| `scripts/planner-flags.env` | regenerated — now `GOOPG_NARROW_COST_INPUTS='unset(on)'` |

No planner logic changed. Only which arm runs by default.

## 2. Results

| # | bar | result | verdict |
|---|---|---|---|
| P1 | TPC-H reproduces the ON measurement as the default | `match=6 join-order=14 **join-method=9 scan-type=8** parameterisation=5 agg=10 sort=9 **parallelism=0** qual=4 rendering=1`. Plan **shapes byte-identical** to the env-ON capture (the residual text diff is pure cost/row drift from an intervening ANALYZE) | **PASS** |
| P2 | TPC-DS plan-TEXT diff, every change enumerated | §3 | **PASS** |
| P3 | values unchanged, incl. an SF=1 execution pass | TPC-H spotcheck **PASS** (Q12=2, Q13=34); **SF=1 values 24/24 MATCH** — "every label matched on values, not merely on row count", no OOM; TPC-DS SF0.25 **PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0** | **PASS** |
| P4 | throughput measured and reported | **SF=1 total 74.17s → 74.33s = +0.2%** over 24 labels. Movers: Q19 −25.7% (2.18→1.62s), Q3 +29.1% (1.03→1.33s) | **PASS** |
| P5 | `make plan-gate` run | **20/22 diverged — unchanged.** Identical to the R125 and R126 binaries measured on the same data dir earlier today. It diffs goopg against live PG, so it cannot pass until the goal is met; not an R128 signal | **pre-existing FAIL, unmoved** |

Suites: `internal/optimizer`, `internal/executor` green; `go vet` clean;
`TestFlagProvenanceEnvIsGenerated` green after regeneration. No pinned
flag expectation exists in `ci/batch/`, the capture scripts, the
spotcheck or the sweep — checked, clean, as review predicted.

## 3. P2 — the TPC-DS changes, enumerated and adjudicated

Raw diff: 126 lines across **6** queries. Three are spurious:

**Q36, Q70, Q86 are the K18 trap, not changes.** They are the three
queries neither engine can plan, and `methodology/capture-tpcds.sh`
embeds `$$` in its temp filename, so the *error text* carries the psql
PID: `parity-capture-587413.sql` vs `…586757.sql`. Nothing else differs
on those three. Anyone re-running this must strip the PID before reading
a TPC-DS diff.

The **three real** changes:

| query | change | adjudication |
|---|---|---|
| Q64 | 76 lines, `Nested Loop` subtree re-priced and restructured | shape change, parity-neutral |
| Q75 | 12 lines, `HashAggregate` / `HashSetOp Union` / `Hash Left Join` / `Nested Loop Left Join` | shape change, parity-neutral |
| Q95 | 4 lines, **`Hash Join` → `Merge Join`**, and the width collapses **564 → 16** | the narrowing doing exactly what it is for; parity-neutral |

**Parity is unmoved by all three.** Same-epoch OFF vs ON, both captures
committed here:

```
OFF  match=1 shapediff=68 unparsed=0 missingnode=27 error=3
ON   match=1 shapediff=68 unparsed=0 missingnode=27 error=3
CATEGORIES  identical in all nine, both arms
```

So the TPC-DS cost R124 flagged is real but **inert against the parity
metric**: three shapes move, none of them toward or away from PG.

*(The `match=1` here is this capture methodology's number, not the 2/99
quoted elsewhere — different script and GUCs. Only the same-epoch OFF vs
ON comparison is load-bearing, and it is exact.)*

## 4. What this cost, stated as a trade

The scope required this be recorded as a trade rather than a free win:

- **Gain:** TPC-H +2 category-instances toward PG. No match flip.
- **Cost:** every plan pin re-baselined (`r121/REPORT.md:143-146`).
- **Cost:** 3 TPC-DS shapes change for no TPC-DS gain (§3).
- **Cost:** +0.2% SF=1 throughput — measured, and far below the ~10% the
  round was scoped to accept.
- **Cost, standing:** R124's accepted planner-publishes-below-executor
  divergence (`TestR124AcceptedAvgVarDivergenceOnNonTableChild`) is now
  **shipped default behaviour** rather than an opt-in arm. P3's SF=1
  execution pass is evidence it does not bite at SF=1 today; the debt is
  assumed, not discharged.

## 5. Corrections this round makes to earlier documents

- **`FRONTIER.md:52-62, :81-84`** said the cost-input narrowing chain was
  "parity-neutral, R124". **R124's *increment* was neutral; R122's two
  categories are real**, as this round's OFF/ON pair now shows directly.
  Corrected in place, keeping the true half (it flips no query to MATCH,
  so Q4 and Q9 stay blocked on widths).
- **The rev-1 scope's central premise was wrong** and is preserved in
  §0 of the SCOPE rather than deleted: it claimed the D-05 changes were
  reverted "only because they cost throughput". They were reverted
  because they **lost Gathers** — a *parity* cost this goal does not
  disclaim. My own memory recorded that failure mode and I did not apply
  it. §2 of the scope then *tested* the hazard instead of arguing past
  it: `parallelism` stays 0 in both arms.
- **The `≈ +10%` figure belonged to `tmp/d05p3-costside-narrow.patch`**,
  a different implementation. Measured here: +0.2%.

## 6. Deliberately NOT in this round

**`MapSlotBytes` 48 → 96**, documented "KNOWN 2x LOW … do not read 48 as
validated". Its "no longer flips Q14" coupling was measured with the D-05
patch underneath, not this flag, so it needs its own A/B. Two further
reasons make the cut decisive rather than tidy: bundling would destroy
attribution (both re-price corpus-wide), and the bundled failure mode is
**losing a MATCH** — 48→96 flipped **Q14**, one of the current six.

That round now starts from a clean baseline *because* of this cut: flag
default-ON as the floor, `tmp/d05p2-bucket-charge.patch` on top, one
pre-registered prediction that Q14 holds MATCH.

## 7. Artefacts

`tpch-narrow-{OFF,ON}.plans.txt` and `parity-{OFF,ON}.txt` (the env A/B
that motivated the round); `tpch-default-ON.plans.txt` (P1, the shipped
default); `tpcds-{opted-OUT,default-ON}.plans.txt` (P2, same-epoch, PID
noise notwithstanding); `sf1-values-{OFF,ON}.txt` (P3/P4).
