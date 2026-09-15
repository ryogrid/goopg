# M0141-S2a-fix1 — wire `agg.InputTarget` into `aggInputWidth`'s callers

Status: accepted (landed 2026-09-15)

## Task

`.ralph/fix_plan.md` M0141-S2a-fix1: "wire `agg.InputTarget` into
`aggInputWidth`'s three call sites (`groupingpaths.go:344`,
`partialaggpaths.go:338`, `partialaggupper.go:327`): when
`agg.InputTargetKnown`, compute `(ncols, avgVarBytes)` from the KEPT columns
(`agg.Child.Output()` indexed by `agg.InputTarget`) instead of the full
`child.Output()`; fall back to today's full-width behavior when unknown. Pin
with a test... Re-measure Q3/Q13/Q18 (TPC-H) and the TPC-DS 32-query
serial-only set from M0141-S1 after. No currency-formula change in this
slice." Follows directly from the scoping recon
(`m0141-s2a-fix-scoping-recon.md`), which found half (1) of M0141-S2a-fix is
already-cheap (a three-line change, no pipeline reordering) and derived the
B2 substitution from PG source.

## Absorption site (B2 — named and justified, per AGENT.md)

**Irreducible difference:** goopg computes `costAgg`'s spill-arm input width
from `aggNode.Child.Output()` — the aggregate's RAW input row, before any
narrowing pass has run — because goopg has no query-wide, up-front
"narrow every rel's target to what the query still needs" pass the way PG's
`build_joinrel_tlist`/`attr_needed` machinery does (`initsplan.c`). That
architecture gap is the K24/M0141-S2b item and out of scope here.

**PG-equivalent quantity substituted:** `agg.InputTarget`'s kept-column
indices into `agg.Child.Output()` — goopg's own per-node, NAME-derived
equivalent of PG's already-narrow `subpath->pathtarget`.

**Why this is the PG-equivalent, not a fitted constant (derive-before-measure):**
PG's `cost_agg` (`postgres/src/backend/optimizer/util/pathnode.c:3430-3434`)
is called with `input_width = subpath->pathtarget->width` — the width of the
**already-built**, already-narrow `PathTarget` of the path feeding the
aggregate. The executor-side memory-fit decision confirms the same currency:
`hash_agg_entry_size` (`postgres/src/backend/executor/nodeAgg.c:1701-1730`,
called at `:3701-3703`) uses `outerplan->plan_width` — the child PLAN node's
own already-narrow width. Both PG call sites key on **the width of whatever
the aggregate's input node actually, finally, emits**, computed once and
query-wide ahead of any path costing. `agg.InputTarget` (stamped by
`stampAggregateInputTarget`, `group_input_target.go:268`, called from
`buildAggregateStage` at `planner.go:8219` — strictly before
`createGroupingPaths`'s cost contest at `planner.go:1760`) answers the
identical question ("which columns does the query still need from this
point") for goopg's one node, computed later and per-node instead of once
and query-wide. This derivation was written down in the scoping recon
**before** any parity number from this task was taken (Findings 1-2 there);
this task did not search over candidate substitutions.

**Pinned by a test:** `agginputwidth_test.go`'s two tests assert the
cost-preview currency (`aggInputWidth`'s `InputTargetKnown` branch) and the
executor's actually-committed-or-declined narrowing
(`narrowAggregateInput`/`upper_narrow_apply.go`) are separate mechanisms —
the preview fires purely from the stamp, with no narrowing commit run at
all. If a future change silently re-merges the two currencies (e.g. by
routing `aggInputWidth` through the committed narrowing result instead of
the stamp), `TestAggInputWidthNarrowsWhenTargetKnown`'s discriminating
fixture (two kept, two dropped, all four variable-width) still passes as
long as the KEPT set is used — but a regression that dropped the
`InputTargetKnown` branch entirely falls back to the full-row width and
fails both new tests' assertions against the narrowed figures.

## Change

`aggInputWidth` (`internal/optimizer/groupingpaths.go`) gained a second
parameter, `agg *Aggregate`. When `agg != nil && agg.InputTargetKnown`, it
sums `nodeAvgVarBytes` over `child.Output()` restricted to
`agg.InputTarget`'s indices instead of the full row. All five call sites
were updated (three named by the task plus two the recon's Finding 3 did not
enumerate individually but covers by the same "all callers share the
helper" argument):

- `groupingpaths.go:383` (`addGroupingPaths`'s main `child` seed) — passes
  `aggNode`.
- `groupingpaths.go:461` (`addGroupingPaths`'s index-ordered variant,
  `idxChild`/`idxSpec`) — passes `idxSpec`, the builder's clone of `aggNode`
  remapped onto the index child; `indexOrderedAggInput` shallow-copies
  `aggNode` (`clone := *aggNode`), so `idxSpec.InputTarget` still indexes
  correctly into `idxChild.Output()` (same base-table columns, different
  access path).
- `partialaggpaths.go:338` — passes `agg`.
- `partialaggupper.go:327` — passes `aggNode`.
- `partialsortpaths.go:230` — passes `nil`. This call site prices a bare
  `*Sort` node, not an `Aggregate`; there is no aggregate keep-list in scope
  here, so it takes the unchanged, full-row fallback path — behavior-neutral
  by construction, not merely by omission.

No currency-formula change (`hashAggEntrySize` untouched) — that is
M0141-S2a-fix2, gated on this task's measured result per the task's own
text.

## Correctness note (carried forward from the scoping recon)

A HASHED strategy's real narrowing commit (`narrowAggregateInput`) declines
to apply for real (`upper_narrow_apply.go`'s `pastSort` condition — no Sort
to sink a Project below). This does **not** invalidate the preview:
`hash_agg_entry_size` charges entry size from the input width regardless of
row-storage retention, and B2's own worked example says a plan may
legitimately spill/run slower under goopg's real footprint while still
matching PG's plan **shape** — that is not a regression under this
milestone group's success criterion.

## Measurement

### Method

Built a private binary (`tmp/goopg-m0141-s2a-fix1-bin`) with this fix, never
touching the shared `:65433`/`:65437` clusters directly:

- **TPC-H**: `scripts/tpch-estimate-audit-arm.sh` (M0137-0007's private-clone
  lane), `PGSHAPED=1` (reproduces the live default —
  `GOOPG_PGSHAPED_DP=unset(on)` per the spotcheck banner — the arm script's
  own default is 0 and must be overridden), `PLAN_ONLY=1`, private port 5590,
  PG reference captured live from `:65432` via `-ref-port` (never restarted).
  Artefacts: `analysis/m0141/m0141-s2a-fix1-tpch.{txt,plans.txt,pg.plans.txt}`.
- **TPC-DS**: no private-clone-lane script exists yet for TPC-DS (the
  M0137-0007 gap is TPC-H-only), so this task drove
  `scripts/lib/tpch-private-clone.sh`'s (schema-agnostic) `BASE_BACKUP` clone
  helper by hand against the SF0.25 goopg cluster
  (`bench/tpcds/runtime_goopg/data-sf025`, source port 65437) onto a private
  clone/port (5591), started my fix binary there via
  `scripts/goopg-test-run.sh`, ran `scripts/capture-tpcds.sh` against it, then
  stopped the private server and removed its clone dir. The shared `:65437`
  cluster was never stopped/started/written; the shared `:65438` PG reference
  was not touched at all — the already-committed
  `analysis/m0141/m0141-s1-tpcds-pg.txt` was reused as the reference side (see
  "stats epoch" below for why this is valid here). Artefact:
  `analysis/m0141/m0141-s2a-fix1-tpcds-goopg.txt`.
- Diffed with `scripts/pg-plan-parity-diff.py` against both (a) the same-run
  PG reference and (b) the already-committed S1 pre-fix PG reference, to
  separate "the fix changed something" from "live PG drifted between
  captures" — both comparisons agree (see below), so there is no live-PG-drift
  confound.
- `shape-delta` computed with
  `docs/design/not_ralph/plan_parity_fix_take2/methodology/shape-delta.sh`
  (the tool AGENT.md's "what every task report must contain" §2 names,
  found via that citation rather than by browsing a round directory) between
  the pre-fix (`m0141-s1-*`) and post-fix (`m0141-s2a-fix1-*`) goopg captures.

### TPC-H result: match 6 -> 8, three queries' plan shape changed, all improving

`PLAN-PARITY: queries=22 match=8 shapediff=12 unparsed=0 missingnode=2
error=0 timeout=0` (vs S1/S2a's committed baseline `match=6`), confirmed
against **both** the live-captured and the S1-committed PG reference:

```
CATEGORIES:            join-order=12 join-method=10 scan-type=9 parameterisation=5 aggregation-strategy=8  sort-strategy=6 parallelism=0 qual-placement=4 rendering=2
CATEGORIES-EXCL-MATCH: join-order=12 join-method=10 scan-type=9 parameterisation=5 aggregation-strategy=8  sort-strategy=6 parallelism=0 qual-placement=4 rendering=1
```
vs the pre-fix baseline (same PG reference file, `m0141-s1-tpch.pg.plans.txt`):
```
CATEGORIES:            join-order=14 join-method=9  scan-type=8 parameterisation=5 aggregation-strategy=10 sort-strategy=9 parallelism=0 qual-placement=4 rendering=1
CATEGORIES-EXCL-MATCH: join-order=14 join-method=9  scan-type=8 parameterisation=5 aggregation-strategy=10 sort-strategy=9 parallelism=0 qual-placement=4 rendering=0
```

Every category count either fell or held; none rose. `shape-delta.sh`:
`queries=22 text-changed=22 shape-changed=3` — **exactly Q3, Q13, Q18**, the
three queries the task named for re-measurement:

- **Q3 and Q13 flip SHAPE-DIFF -> MATCH.** Q3's own Hashed-vs-Sorted contest
  (the scoping recon's live `EXPLAIN VERBOSE` witness: a Hash Join narrowing
  its `Output:` to 7 of ~25 raw columns, invisible to `costAgg` before this
  fix) now picks PG's shape. Q13 likewise.
- **Q18 stays SHAPE-DIFF** (join-order/join-method/scan-type/
  aggregation-strategy still differ) but its own tag set lost
  `sort-strategy` and gained a verdict-neutral `rendering` tag — a partial,
  not-yet-complete improvement, consistent with the recon's prediction that
  the currency correction (fix2) may still be needed for the harder cases.

`Q10`'s already-matching status (Finding 4 of the mechanism-A re-measure
doc) is unchanged — it was never in the tagged set and stays a MATCH.

### TPC-DS result: match floor held (2), one of the named 32 queries improved, zero regressions

`PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25
error=3 timeout=0` — identical totals to the pre-fix baseline (`Q9`/`Q41`
floor held, per AGENT.md's non-regression bar).
```
CATEGORIES:            join-order=90 join-method=69 scan-type=61 parameterisation=45 aggregation-strategy=69 sort-strategy=75 parallelism=84 qual-placement=21 rendering=23
CATEGORIES-EXCL-MATCH: (identical to CATEGORIES — no MATCH carries a tag in this corpus)
```
vs baseline `aggregation-strategy=70 sort-strategy=76 parallelism=85`
(everything else identical). `shape-delta.sh`: `queries=99 text-changed=5
shape-changed=2` (`Q31`, `Q78`):

- **Q31** — one of M0141-S1's named 32 serial-shaped queries — drops
  `aggregation-strategy`, `sort-strategy` **and** `parallelism` from its tag
  set (still `SHAPE-DIFF` overall on unrelated `join-order`/`join-method`/
  `scan-type`/`parameterisation`/`rendering` tags). This is the TPC-DS
  analogue of the TPC-H Q3/Q13 flip, inside the exact scope M0141-S1 named.
- **Q78** changed cost digits only (its `MISSING-NODE` verdict and full tag
  set are byte-identical before/after) — `shape-delta.sh` flags a textual
  change the category tool correctly treats as not shape-relevant (a
  `Materialize`-adjacent cost figure inside an already-`MISSING-NODE`
  section). Not a regression, not new information.
- `Q36`/`Q70`/`Q86` (the pre-existing `error=3` set, unrelated to
  aggregation/sort — a known EXPLAIN failure, not investigated by this or
  any prior M0141 task) show as `text-changed` but their verdict stays
  `ERROR [] estimates(goopg n/a | pg n/a)` identically both times — an
  artefact of the capture, not a plan change.

No category count rose anywhere in either corpus. `match` held at its floor
in both.

## What every M0137-M0143 task report must contain (per AGENT.md)

- **Category movement** — the two lines, both arms, both corpora: pasted
  verbatim above (TPC-H CATEGORIES/CATEGORIES-EXCL-MATCH pre- and post-fix;
  TPC-DS CATEGORIES pre- and post-fix). Net: TPC-H `match` 6 -> 8,
  `aggregation-strategy` 10 -> 8, `sort-strategy` 9 -> 6, `join-order`
  14 -> 12; TPC-DS `match` unchanged at 2, `aggregation-strategy` 70 -> 69,
  `sort-strategy` 76 -> 75, `parallelism` 85 -> 84. No category rose in
  either corpus.
- **shape-delta**: TPC-H `shape-changed=3` (Q3, Q13, Q18 — exactly the
  task's named set). TPC-DS `shape-changed=2` (Q31 — inside M0141-S1's named
  32-query serial-shaped set; Q78 — cost-digits-only, verdict unchanged).
- **Stats epoch**: TPC-DS — identical epoch both arms
  (`stats-epoch: 5d4dc56356f3d676`, since the SF0.25 cluster is persistent
  and this task only read it via a `BASE_BACKUP` clone, never re-`ANALYZE`d
  it) — a clean same-epoch comparison. TPC-H — epochs differ
  (`253df6b1d97f9f5b` pre-fix vs `f896fe01c92bc62e` post-fix): each
  `estimate-audit` run re-`ANALYZE`s its own fresh connection
  (`-warm-stats`, on by default) against a fresh `pg_basebackup` clone of
  the same live data, and PG's block sampler draws an independent random
  sample per run, so a differing fingerprint here reflects sampling noise
  across two independent `ANALYZE` runs on identical underlying rows, not a
  values sweep or a stats regression. This is disclosed rather than
  asserted away; the Q3/Q13 flip is a structural (deterministic) shape
  change — a Hashed-vs-Sorted strategy pick — not a borderline cost-number
  wobble that sampling noise could plausibly manufacture on its own, and the
  same flip reproduces identically against both the live and the
  S1-committed PG reference file.
- **Seam-decline census**: N/A — this task touches no seam-declining code
  path (`GOOPG_PGSHAPED_DP_TRACE`'s subject is join enumeration, not
  aggregate strategy costing).
- **Planning route**: unchanged from the scoping recon's finding —
  `agg.InputTarget` is read at `addGroupingPaths`'s (and its two siblings')
  existing call sites inside `createGroupingPaths`/`planStmtWithSettings`,
  strictly before `Plan()`'s tail-end `applyUpperNarrowing`. This task adds
  no new planning route; it makes an existing one's already-computed value
  reachable at an existing read site.

## Verification

- `go build ./...` clean.
- `go test ./internal/optimizer/...` — all pass, including the two new pinning
  tests (`agginputwidth_test.go`).
- `scripts/tpch-spotcheck.sh` — `RESULT=PASS` (Q12 rows=2, Q13 rows=34) on
  the shared `:65433` cluster, confirming this change does not regress the
  canonical silent-regression tripwire on the shared bench binary's own next
  rebuild.
- Live measurement above: TPC-H `match` 6 -> 8 (improvement, not a
  regression), TPC-DS `match` floor held at 2, zero category regressions in
  either corpus.
- Shared clusters (`:65432`, `:65433`, `:65437`, `:65438`) were read from but
  never stopped/started/rebuilt by this task; all private artefacts
  (binaries, clone data dirs, cgroup scopes) were removed after use.

## Resume points

- **M0141-S2a-fix2** (already filed) — the `hashAggEntrySize` fixed-overhead
  currency correction, now legitimately attemptable: this task supplies the
  live-at-cost-time preview R124 §7's prior attempt lacked. Q18's residual
  `aggregation-strategy` mismatch (TPC-H) and Q31's residual `join-order`/
  `join-method`/`scan-type`/`parameterisation` mismatches (TPC-DS, still
  `SHAPE-DIFF` overall) are candidates to re-check once fix2 lands, though
  neither is proven to be currency-shaped rather than a different mechanism
  entirely — re-diagnose, do not assume.
