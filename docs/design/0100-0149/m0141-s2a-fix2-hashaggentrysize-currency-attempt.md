# M0141-S2a-fix2 — `hashAggEntrySize` fixed-overhead currency correction (attempted, declined)

Status: accepted (attempted and measured 2026-09-15; **decision: HOLD — not
adopted**, code reverted after measurement)

## Task

`.ralph/fix_plan.md` M0141-S2a-fix2: "`hashAggEntrySize` fixed-overhead
currency correction (gated on M0141-S2a-fix1 landing and being measured): add
the missing `MAXALIGN(SizeofMinimalTupleHeader) + tupleWidth` fixed-overhead
term PG's `hash_agg_entry_size` (`nodeAgg.c:1701-1730`) charges alongside the
variable payload, re-derived per B2 (not a verbatim reinstatement of the
deleted `GOOPG_HASHAGG_WIDTH_CURRENCY`, per M0141-S2a-fix's own text). Attempt
only after fix1's isolated result is known." Fix1 landed and measured
(`m0141-s2a-fix1-agg-input-width-preview.md`, TPC-H match 6 -> 8) the same
loop before this one, satisfying the gate.

## Absorption site (B2 — named and justified, per AGENT.md)

**Irreducible difference:** `costAgg`'s SPILL ARM (`cost_funcs.go`, R3) calls
`hashAggEntrySize(nAggs, inAvgVarBytes)`, handing it the variable-payload
byte estimate ALONE as the `tupleWidth` argument. PG's `hash_agg_entry_size`
(`postgres/src/backend/executor/nodeAgg.c:1701-1730`) is called with
`tupleWidth` = the aggregate input's FULL per-row width (fixed-width columns
included), sourced from `outerplan->plan_width` at the call site
(`nodeAgg.c:3701-3703`) — the same `input_width` `cost_agg`
(`postgres/src/backend/optimizer/util/pathnode.c:3430-3434` /
`postgres/src/backend/optimizer/path/costsize.c:2801-2802,2824`) hands the
`pages`/`relation_byte_size` term and the same currency the SORTED rival is
priced in via `cost_tuplesort` (`costsize.c:1903`). Feeding
`hashAggEntrySize` the payload alone means goopg's hashed-vs-sorted spill
contest is NOT priced in one currency the way PG's is — the exact divergence
M0137-0009's comment names as "KNOWN, currently-accepted."

**PG-equivalent quantity substituted:** `hashsize.EntryBytes(inNcols,
inAvgVarBytes)` — goopg's already-defined PG-equivalent full-row currency
(`48*ncols + 24 + avgVar`), the SAME function `costSortRunWithWidth` already
uses to price the sorted rival. `inNcols`, after M0141-S2a-fix1, is no longer
dead: it reaches `costAgg` from `aggInputWidth`'s live-at-cost-time
`agg.InputTarget` preview, so (unlike at R120/M0137-0009's time) a
trustworthy per-node column count is available when the spill arm evaluates.

**Why this is the PG-equivalent, not a fitted constant (derive-before-measure):**
Both PG call sites (`pathnode.c:3430`, `nodeAgg.c:3701-3703`) key on the same
full per-row width, computed once from the path's own `pathtarget`/
`plan_width` — not the variable-length portion alone. `hashsize.EntryBytes`
is goopg's own existing named substitute for that quantity (already the
substitute used on the SORTED side of this exact contest), so using it here
merely applies the SAME already-derived substitution to the arm that had not
yet received it. This derivation restates M0139-0007's finding (the "R120"
row of its inventory table) and R124 §7's own; it was not re-derived by
searching for an expression that would move the metric — see "Decision"
below for why that distinction matters here.

**Pinned by a test:** yes — the attempt (before it was reverted) added
`TestCostAggHashedFixedWidthChargesSpill` (renaming
`TestCostAggHashedNeverChargesSpill`, whose premise the change inverts) and
`TestCostAggHashedUnknownWidthNeverChargesSpill` (pinning the one surviving
`(0,0)` opt-out `addDistinctPaths` relies on), plus two `cost_funcs_test.go`
edits changing "blind" sentinels from `(ncols, 0)` to `(0, 0)` so they stay
true unpriced baselines under the new guard. All are described here for the
record but are **not in the tree** — see "Decision".

## Change (attempted, then reverted)

`costAgg`'s SPILL ARM guard changed from `inAvgVarBytes > 0` to `inNcols > 0
|| inAvgVarBytes > 0`, and its `tupleWidth` argument to both
`hashAggEntrySize` and the `pages` computation changed from bare
`inAvgVarBytes` to `hashsize.EntryBytes(inNcols, inAvgVarBytes)`. This
restores "Arm C" from the R120/R124 history: a fixed-width (avgVarBytes == 0)
aggregate input now has a real `48*ncols+24` footprint and prices it, where
the payload-only currency declined unconditionally on such inputs. The
`(0, 0)` opt-out `addDistinctPaths` relies on (DISTINCT has no per-column
width estimate wired to that call) was preserved by keying the guard on
`inNcols` too rather than dropping the guard outright.

Full diff (for the record, not applied): `internal/optimizer/cost_funcs.go`
(`costAgg`'s spill-arm body and its comment), `cost_funcs_test.go` (two
"blind" sentinels), `groupingpaths_test.go`
(`TestCostAggHashedNeverChargesSpill` -> `TestCostAggHashedFixedWidthChargesSpill`
+ new `TestCostAggHashedUnknownWidthNeverChargesSpill`),
`partialaggpaths_test.go` (`TestPartialAggVerdictIsScaleFree`'s row range and
doc comment, narrowed from `10_000_000` to `3_000_000` — the corrected,
larger entry currency lowers the memory threshold for an all-`int4` fixture
from "never crossed in this range" to ~4.6M groups, which the old top row
crossed).

## Measurement

### Method

Built a private binary (`tmp/goopg-m0141-s2a-fix2-bin`, removed after use)
with the change above applied, never touching the shared `:65433`/`:65437`
clusters directly — identical procedure to fix1's:

- **TPC-H**: `scripts/tpch-estimate-audit-arm.sh`, `PGSHAPED=1`, `PLAN_ONLY=1`,
  private port 5590, PG reference captured live from `:65432` via
  `-ref-port`. Artefacts:
  `analysis/m0141/m0141-s2a-fix2-tpch.{txt,plans.txt,pg.plans.txt}`.
- **TPC-DS**: `scripts/lib/tpch-private-clone.sh`'s `BASE_BACKUP` helper
  against the SF0.25 goopg cluster (`bench/tpcds/runtime_goopg/data-sf025`,
  source port 65437) onto a private clone/port (5591), fix binary started via
  `scripts/goopg-test-run.sh`, `scripts/capture-tpcds.sh` run against it,
  server stopped and clone dir removed. The shared `:65437` cluster was never
  stopped/started/written; the shared `:65438` PG reference was not touched —
  the already-committed `analysis/m0141/m0141-s1-tpcds-pg.txt` was reused as
  the reference side (valid: identical `stats-epoch: 5d4dc56356f3d676` on
  both the fix1 and fix2 goopg captures, confirmed below). Artefact:
  `analysis/m0141/m0141-s2a-fix2-tpcds-goopg.txt`.
- **Isolated the goopg-side effect from PG-side sampling noise.** Each
  `estimate-audit` run re-captures its own live PG reference, and PG's block
  sampler draws an independent sample per run — comparing fix2's goopg
  capture against fix2's OWN freshly-captured PG reference therefore
  conflates "what fix2 changed in goopg" with "how PG's sample differed this
  run" (it visibly did: several TPC-H categories moved that `shape-delta.sh`
  proves did not change shape). The reported numbers below instead diff
  fix2's goopg capture against **fix1's own committed PG reference**
  (`m0141-s2a-fix1-tpch.pg.plans.txt`) — a same-PG-capture control that
  isolates the goopg-only delta. (TPC-DS needs no such control: the SF0.25
  cluster is persistent and both fix1 and fix2 read it via a `BASE_BACKUP`
  clone without ever re-`ANALYZE`-ing it, so the epoch match already proves
  a clean comparison.)
- `shape-delta` computed with
  `docs/design/not_ralph/plan_parity_fix_take2/methodology/shape-delta.sh`
  between the fix1 (pre-fix2) and fix2 goopg captures.

### TPC-H result: match unchanged (8), exactly one query (Q18) changes shape, LATERALLY

`PLAN-PARITY: queries=22 match=8 shapediff=12 unparsed=0 missingnode=2
error=0 timeout=0` — identical totals to fix1's own baseline.

Same-PG-reference control (`m0141-s2a-fix2-tpch.plans.txt` vs
`m0141-s2a-fix1-tpch.pg.plans.txt`):
```
CATEGORIES:            join-order=12 join-method=10 scan-type=9 parameterisation=5 aggregation-strategy=8 sort-strategy=7 parallelism=0 qual-placement=4 rendering=1
CATEGORIES-EXCL-MATCH: join-order=12 join-method=10 scan-type=9 parameterisation=5 aggregation-strategy=8 sort-strategy=7 parallelism=0 qual-placement=4 rendering=0
```
vs fix1's own baseline (same PG reference):
```
CATEGORIES:            join-order=12 join-method=10 scan-type=9 parameterisation=5 aggregation-strategy=8  sort-strategy=6 parallelism=0 qual-placement=4 rendering=2
CATEGORIES-EXCL-MATCH: join-order=12 join-method=10 scan-type=9 parameterisation=5 aggregation-strategy=8  sort-strategy=6 parallelism=0 qual-placement=4 rendering=1
```

Only two categories move, by exactly 1 each, in OPPOSITE directions:
`sort-strategy` 6 -> 7 (regression), `rendering` 2 -> 1 (improvement).
`shape-delta.sh`: `queries=22 text-changed=22 shape-changed=1` — **Q18 only**:

- **Q18** trades its `rendering` tag for a `sort-strategy` tag
  (`{join-order,join-method,scan-type,aggregation-strategy,rendering}` ->
  `{join-order,join-method,scan-type,aggregation-strategy,sort-strategy}`).
  It stays `SHAPE-DIFF` either way — the currency correction does not
  converge it, it relabels which of its five mismatch categories carries the
  tag. `rendering` is the category fix1's own doc already called
  "verdict-neutral"; `sort-strategy` is not. **Net: lateral, not an
  improvement.**
- Every other TPC-H query is byte-identical in shape to fix1 (confirmed by
  `shape-delta.sh`, not merely by the category counts agreeing).

### TPC-DS result: match floor held (2), exactly one query (Q31) changes shape, REGRESSING three categories back to the pre-fix1 baseline

`PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25
error=3 timeout=0` — identical totals to fix1's own baseline.
```
CATEGORIES:            join-order=90 join-method=69 scan-type=61 parameterisation=45 aggregation-strategy=70 sort-strategy=76 parallelism=85 qual-placement=21 rendering=23
CATEGORIES-EXCL-MATCH: (identical to CATEGORIES — no MATCH carries a tag in this corpus)
```
vs fix1's own baseline:
```
CATEGORIES:            join-order=90 join-method=69 scan-type=61 parameterisation=45 aggregation-strategy=69 sort-strategy=75 parallelism=84 qual-placement=21 rendering=23
```
`shape-delta.sh`: `queries=99 text-changed=4 shape-changed=1` — **Q31 only**
(the other three text-changed entries, Q36/Q70/Q86, are the pre-existing
`error=3` set, unchanged verdict, same artefact fix1's own doc already
disclosed).

- **Q31** — the ONE query M0141-S2a-fix1 improved on TPC-DS — reverts
  exactly: fix1's tag set
  `{join-order,join-method,scan-type,parameterisation,rendering}` regains
  `aggregation-strategy`, `sort-strategy` and `parallelism`, becoming
  `{join-order,join-method,scan-type,parameterisation,aggregation-strategy,
  sort-strategy,parallelism,rendering}` — **byte-identical to Q31's tag set
  before fix1 ever ran** (M0141-S1's baseline). fix2 does not merely fail to
  help; layered on fix1 it exactly cancels fix1's one TPC-DS gain, taking
  the corpus back to the pre-fix1 category counts
  (`aggregation-strategy=70 sort-strategy=76 parallelism=85`, the same
  figures fix1's own doc cited as its starting baseline).

## Decision: HOLD — not adopted, code reverted

Both corpora were measured cleanly (same-PG-reference control for TPC-H,
matching stats-epoch for TPC-DS) and neither shows a net improvement:

| corpus | match | shape-changed | net category movement |
|---|---|---|---|
| TPC-H | 8 -> 8 (no change) | 1 (Q18) | lateral: +1 `sort-strategy`, -1 `rendering` |
| TPC-DS | 2 -> 2 (no change) | 1 (Q31) | **regression: +1 each on `aggregation-strategy`, `sort-strategy`, `parallelism`**, exactly cancelling fix1's own gain |

No `MATCH` was gained or lost anywhere, so the group's hard non-regression
floor (TPC-H >= 6, TPC-DS >= 2) is not implicated either way — this is not a
"floor" decision. But AGENT.md's success criterion frames the milestone
group's actual currency as **category movement**, and by that currency this
specific correction is net-negative (TPC-DS) to neutral (TPC-H), with no
compensating gain anywhere to weigh against the TPC-DS regression the way
B3's `GOOPG_GATHER_PATHS` decision had a genuine bug fix to weigh against its
category rise. There is no such counter-argument here: the change is exactly
what it claims to be (a straight currency port) and it still does not move
the goal metric forward.

This is not a new finding in isolation — it is the SAME outcome R124 §7
already measured for the identical currency correction
(`r124-nontable-leaf-widths/REPORT.md §7`: "measured it identical to R120's
arm alone"), now reproduced a second time on a foundation R124 did not have
(fix1's live-at-cost-time `inNcols` preview, which the M0141-S2a-fix task
text explicitly hoped would change the outcome). It did not. Two independent
measurements — one without the preview, one with it — now agree that
`hashAggEntrySize`'s payload-only-vs-full-width currency gap is not where
the remaining TPC-H/TPC-DS mismatch lives, at least not for the
plan-shape-visible categories this milestone group scores. M0137-0009's
"DELETE rather than carry another round" resolution for the flag this
attempt re-derived is hereby reproduced, not overturned; no new
`GOOPG_*` flag was introduced to avoid recreating that same debt.

**Per B2's own rule**, deriving the substitution correctly and then
accepting whatever the measurement says (rather than searching for a
different substitution that would move the number) is what makes this
faithfulness work rather than tuning — declining to adopt a correctly-derived
but non-helping change is the same discipline applied to the adopt/hold
decision, not a departure from it.

The code change (`cost_funcs.go`'s `costAgg` and its four accompanying test
files) was **reverted** (`git checkout --`) after measurement; the tree
carries no `costAgg` diff from this task. The measurement artefacts
(`analysis/m0141/m0141-s2a-fix2-*`, 4 files) are committed as the record.

## What every M0137-M0143 task report must contain (per AGENT.md)

- **Category movement** — the four lines (TPC-H/TPC-DS, both arms) pasted
  verbatim above. Net: TPC-H lateral (+1/-1), TPC-DS regresses 3 categories
  by 1 each, cancelling fix1's one gain.
- **shape-delta**: TPC-H `shape-changed=1` (Q18 — lateral tag swap). TPC-DS
  `shape-changed=1` (Q31 — reverts to the pre-fix1 tag set exactly).
- **Stats epoch**: TPC-DS — identical epoch both arms
  (`stats-epoch: 5d4dc56356f3d676`), a clean same-epoch comparison. TPC-H —
  epochs differ between fix1's and fix2's own live captures
  (`f896fe01c92bc62e` vs `1a8979d159906a5d`), the same independent-`ANALYZE`-
  sampling-noise situation fix1's doc disclosed; the reported TPC-H numbers
  above use the same-PG-reference control specifically to avoid this noise
  contaminating the category counts.
- **Seam-decline census**: N/A — this task touches no seam-declining code
  path (aggregate strategy costing, not join enumeration).
- **Planning route**: unchanged — `costAgg`'s spill arm is reached from the
  same existing `createGroupingPaths`/`createPartialGroupingPaths` call
  sites fix1 already documented; this task added no new planning route.

## Verification

- `go build ./...` clean (both with the attempted change and after revert).
- `go test ./internal/optimizer/...` — all pass with the attempted change
  (including the two new/renamed pinning tests) and all pass after revert
  (back to the pre-task baseline).
- Live measurement above: TPC-H `match` unchanged at 8, TPC-DS `match` floor
  held at 2 — the non-regression floor was never at risk either way.
- Shared clusters (`:65432`, `:65433`, `:65437`, `:65438`) were read from but
  never stopped/started/rebuilt; all private artefacts (binary, clone data
  dir, server log, cgroup scope) were removed after use.

## Resume points

- No new lever identified inside this attempt's scope. A future task
  revisiting the aggregate-strategy category mismatch should treat
  `hashAggEntrySize`'s currency as **closed** (two independent measurements,
  R124 §7 and this task, agree it does not move the metric) and look
  elsewhere — Q18's residual `join-order`/`join-method`/`scan-type`/
  `aggregation-strategy` mismatch and Q31's residual `join-order`/
  `join-method`/`scan-type`/`parameterisation` mismatch are still open but
  are not currency-shaped by this evidence.
- **M0141-S2b** (GROUP_AGG rel publishes Pathlist, not Node, to the ORDER BY
  step) remains the next filed slice in this milestone's sequence and does
  not depend on this task's outcome either way.
