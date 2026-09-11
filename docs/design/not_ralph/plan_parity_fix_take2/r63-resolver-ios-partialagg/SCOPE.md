# R63 SCOPE — resolver arms: IndexOnlyScan + partial-agg group-key identity (2026-09-11)

Follows R62 LANDED (`r62-yao-parameterized-probe/REPORT.md`, commit
`163fecd3c`): Q11 search-exact 32000 / display 10666, pp 5/15/0/2, DS
PASS=94 + Q72 TIMEOUT.

## 0. Re-triage: why M1 is the flagship

Queue at R62-close: (a) Materialize producer, NEW #6 (large-group
strategy/stats), R61 #4 (CTE-body tagging), (b) Q4, R62-#1 (no live case),
M1-display. Live-evidence triage (no new code, tree clean):

- **(a) is insufficient-alone, not next.** goopg has NO Materialize plan
  node by design (`joinpathsmergeouter.go:68`: introducing one "would be
  wrong: it would buffer the inner twice"; the NL plan builder notes the
  executor already does materialize-once-and-replay, `createplannl.go:357`). PG's Q5/Q8
  Materialize sites are NL-inner rescans, but goopg's Q5 shape differs
  underneath (Hash Join nation×region vs PG's NL chain; HashAgg vs
  GroupAgg+Sort — both cost-driven). A perfect Materialize cut leaves Q5
  SHAPE-DIFF. Necessary-but-insufficient shape item; stays queued behind a
  round that closes a query.
- **#6 / (b) are cost-model/stats scope**, deferred twice by precedent
  (R61, R62). No new measurement promotes them.
- **R61 #4 is fail-closed-correct** (R61 REPORT §2.3: no shape shift, CTE
  cases A/B identical) — a why-question, not a defect. Not a flagship.
- **M1 is a live estimator defect with a chained mechanism** (this round):
  single-table `GROUP BY ps_partkey` displays `rows=200`
  (`defaultNumDistinct`) through a Parallel Index Only Scan leaf where the
  search sizes 201356 and PG says 203361. One family (`resolveBaseColumn`),
  two missing arms, display-only numbers. Flagship.

## 1. Probe verdicts (live instrumented run, SF1 clone :5533, seed 20260905)

Temporary `GOOPG_R63DBG` prints on `examineGroupVar` (outcome branch +
child type; FULLY reverted, tree clean). Bin
`/tmp/pp2/bin/goopg-r63probe` (instrumented, superseded); server log
`/tmp/pp2/r63/start-probe.log` (tmp-only). Query
`/tmp/pp2/r63/m1probe.sql`: `EXPLAIN SELECT ps_partkey, count(*) FROM
partsupp GROUP BY ps_partkey` → goopg `Finalize GroupAggregate rows=200`
(via `Gather` → `Partial GroupAggregate rows=200` → `Parallel Index Only
Scan`); PG oracle (`:65432`, `m1-pg.plan`) `GroupAggregate rows=203361`
via plain `Index Only Scan`.

### (i) The leaf-direct calls die on the missing IOS arm (×9)

```
R63DBG fallback200 idx=0 child=*optimizer.IndexOnlyScan   (×9)
```

`estimateAggregate` on the Partial node passes the IOS leaf as `child`
directly. `resolveBaseColumn` has `*SeqScan`/`*IndexScan` arms but NO
`*IndexOnlyScan` arm (`joinkeyproof.go:137-243` switch — confirmed by
reading, not by guessing); `groupUniqueNDistinct` on a bare scan misses;
`defaultNumDistinct = 200.0` (`joinselectivity.go:62`) is the displayed
200. Exactly the ledgered M1 shape ("narrowed-IndexOnlyScan + missing arm").

### (ii) The finalize display dies one level up (×1)

```
R63DBG examine idx=0 child=*optimizer.Gather → fallback200   (×1)
```

`estimateAggregate` on the Finalize node passes `Gather→Partial→IOS`.
`resolveBaseColumn` walks the `*Gather` arm, then stops: NO `*Aggregate`
arm at all. `groupUniqueNDistinct` walks the `*Gather` arm and then
REFUSES the partial (`joinkeyproof.go:352`: `Mode != AggModePartial` —
principled, per-worker rows are not the group count). So the IOS arm
alone fixes the Partial display but NOT the Finalize display — the
round needs both arms (§2).

### (iii) The search resolves fine through SeqScan (×2, no fallback)

```
R63DBG examine idx=0 child=*optimizer.SeqScan   (×2, zero fallback200)
```

The search episode sizes the grouping rel through a SeqScan-shaped child
(nd=201356) — DP confirms (`start-m1.log`: `upper.partialgroupagg.partial
rows=201356 … verdict=accepted`). The defect is display-recompute-only
(the Amendment-A1 consumer on the built tree, whose leaf is the promoted
IOS). PG parity target for display is therefore goopg's own nd **201356**,
not PG's 203361 (~1% column-stats gap — #6(ii) family, own scope).

### (iv) R62 interplay: none

M1's IOS carries no `OuterColumnRef` (single table, no join) — the R62
decline arm cannot fire here. The Yao term then sees
`filtered=tuples=800000` (n==rel generic hit on the IOS) → no discount →
reldistinct stays at the resolved nd. R62 and R63 compose additively.

## 2. The cut

TWO arms in `resolveBaseColumn` (`joinkeyproof.go`), both identity-only
remaps mirroring the `*Project` arm (M0125-0038), both fail-closed:

1. **`*IndexOnlyScan`**: output schema is the narrowed `Covered`
   projection (`plan.go:978` struct decl, `Covered []catalog.Column` at
   :1010 — "the output
   schema contains ONLY the projected covered columns"). Map output
   `idx` → `Covered[idx].Name` → position in `tbl.Columns` →
   `baseColumnOfTable(x, x.Table, nil, tableIdx)` (IOS carries no
   UniqueKeys; empty/mismatched `Covered` misses exactly as today).
2. **`*Aggregate`, partial-mode ONLY** (`x.Mode == AggModePartial`):
   output layout is `[group exprs…, agg calls…, passthrough…]`
   (`plan.go:1284`; `AggModePartial` const at :1415), so
   `idx < len(GroupExprs)` with `GroupExprs[idx]` a bare `*ColumnRef`
   recurses into `x.Child` with the remapped index (the *Project rule,
   one level down). Anything else (expressions, agg-call columns,
   grouping-sets exoticism beyond bare refs) misses exactly as today.

Partial-only is load-bearing, not incidental: `groupUniqueNDistinct`
answers whole aggs EXACTLY (PG's isunique branch) and `resolveBaseColumn`
is tried FIRST — a general *Aggregate arm would SHADOW it, replacing
agg-rows answers with base-nd overestimates. Restricting to partial mode
converts ONLY today's misses (groupUnique refuses partials), whole aggs
keep today's behavior bit-identically.

Twin-walker test (`TestResolverFamilyArmListsAgree`, bidirectional,
`resolverArmExemptions` currently empty):

- `*IndexOnlyScan` needs NO edit in `relFilteredRowsWalk`: IOS is a leaf
  (no `Child` field — Table/Alias/Index/Key/Keys/LowKey/HighKey/Covered/
  Cond/… only), reached via the existing `n == rel` identity check
  exactly like the `*SeqScan`/`*IndexScan` leaves; leaf arms are
  structurally excluded from `descendingSwitchArms`. No exemption needed
  either. (Review correction: the draft's `passthrough(x.Child)` would
  not compile. Soundness note: `EstimateRows(*IndexOnlyScan)` prices
  Key/Keys/LowKey/HighKey but NOT `Cond`, so a `Cond`-bearing IOS
  overstates filtered rows — conservative direction, same as unknown;
  M1's IOS has no `Cond`. Fail-closed, just not `Cond`-exact.)
- `*Aggregate` gets an EXEMPTION with reason: an aggregate's output is
  groups, not base-rel rows — descending would misattribute base
  restriction evidence (the grouped-subquery case belongs to
  `groupUniqueNDistinct`, not the Yao walk). Exempting rather than
  adding is the direction the test message anticipates ("add the arm, or
  add an exemption with its reason").

Deliberate non-mirrors (not drift): no whole-agg arm (shadowing, above);
no LowKey/HighKey/SAOPKeys handling (probe keys are restriction evidence,
orthogonal — R62-ledgered family); no Yao/selectivity/display/executor
change (NUMBERS move only); no new node types (the plan already HAS the
IOS — M1 moves numbers on the identical shape); no GUC.

Known corner R63-#1 (ledgered, gates adjudicate): the partial-agg arm
answers base nd for per-worker groups — exact when every worker sees
every group (the high-nd parallel case), over-count when skewed workers
miss groups. Bounded by base nd; no live case known (corpus partial-aggs
are high-nd). Any gate movement consistent with over-count triggers
re-audit, not celebration.

## 3. Falsifiable predictions

| # | Claim | Mechanism (§1) | Verdict on miss |
|---|---|---|---|
| P0 | fallback200 lines vanish for M1 (9× IOS-direct + 1× Gather-child now resolve) | §1.i+ii arms | any surviving fallback200 on M1 → STOP, re-audit |
| P1 | M1 display Partial/Finalize/Gather rows 200→**201356** (search-exact) | resolved nd=201356, Yao no-discount (§1.iv), clamp min(201356, 201356) | ≠201356 → re-audit (nd moved or clamp path differs); =203361 → bonus, record stats path |
| P2 | values md5 MATCH all TPC-H queries (8+q11); **Q11 pinned 32000/10666, Q5 pinned 25** | planner-only identity arms; Q11/Q5 resolver paths contain no IOS leaf / partial-agg-above-groupcol — sharp R62-vs-R63 discriminators | ANY values mismatch or Q11/Q5 move → STOP |
| P3 | pp stays 5/15/0/2 modulo number-moves toward-oracle; DS sweep PASS=94 + Q72 TIMEOUT alone; plan-set moves adjudicated per-row (expect SMALL: only IOS-leaf / partial-split resolver paths fire) | previously-miss→now-resolve only; whole-agg/SeqScan paths bit-identical | new MISMATCH/CKMISMATCH/TIMEOUT, or any unadjudicated flip → explain-or-stop |
| P4 | DPTRACE M1 A/B vs R62 binary: only group-count lines move (200→201356 family); joins bit-identical | display-recompute-only defect (§1.iii) | join movement → re-audit |

WATCH (direction-checked, not gated): TPC-H Q1/Q6 (single-table group-bys
— move only if their leaves promote to IOS AND previously missed);
DS queries with partial-split aggs over IOS leaves.

Re-audit rule (R59–R62): any P-miss triggers DPTRACE A/B against the
pre-R63 binary (non-display lines bit-identical), not celebration.

## 4. Sibling audit + executor inventory (why the cut is display-led)

- `resolveBaseColumn` is THE shared family resolver (`columnNDistinctForChild`,
  `columnStatsForChild`, `columnRawRowsForChild` all delegate): the arms
  also repair selectivity/join-sizing lookups through IOS leaves and
  partial aggs. Known blast radius — gates 2–5 adjudicate every movement;
  fail-closed default keeps all other shapes bit-identical.
- `groupUniqueNDistinct` untouched (still refuses partials, still exact on
  whole aggs — the shadowing analysis in §2 is why).
- `relFilteredRows` sole caller is the Yao site (R62 SCOPE §1.iv) — the new
  IOS passthrough inherits that containment.
- Executor OUT: no new nodes, estimates never reach execution; P2 proves it.
- No GUC, no knob, no display-code change.

## 5. Gates (implementation round)

1. `go test ./internal/optimizer/` green (no `-count=1`, arms-agreement
   test included); `go vet` clean.
2. M1: display Partial/Finalize/Gather rows EXACTLY 201356; values 8+q11
   md5 MATCH vs R62; Q11 pinned 32000/10666; Q5 pinned 25.
3. DPTRACE A/B vs R62 binary on M1 (+Q11/Q5 smoke): only group-count lines
   move; joins bit-identical.
4. `scripts/pg-plan-parity-diff.py`: 5/15/0/2; moves recorded.
5. DS SF0.5 sweep foreground, fresh build, private lane: PASS=94 (same set)
   + Q72 TIMEOUT alone; plan set re-adjudicated per P3.
6. REPORT.md with the A/B numbers, then review, then commit + push.

## 6. Ledgered follow-ups (not this round)

- R63-#1 (partial-skew over-count), R63-#2 (review-found: `soleBaseScan`
  (`joinkeyproof.go:438-460`) lacks `*IndexOnlyScan` (and `*CTEScan`) — an
  IOS sole-scan join side loses key-implied `rowsBound`; conservative,
  M1-unaffected, unpinned by the arms test; same disposition for
  `IsSmallDimensionSide` (`cardinality.go:522`) and `outerScanRowCount`
  (`nl_index_join_selectivity.go:89`), both SeqScan/IndexScan-only),
  (a) Materialize (insufficient-alone —
  needs a join-shape round first), #6 (cost-model/stats), R61 #4
  (fail-closed-correct why-question), (b) Q4 (cost-model framing, 3×
  deferred), M1 rounding n/a (201356 vs 203361 is stats, not rounding),
  NLI staleness comment, R61 #5 (executor watch), R62-#1 (no live case).

Evidence tmp-only `/tmp/pp2/r63/` (`m1probe.sql`, `m1-goopg.plan`,
`m1-pg.plan`, `start-probe.log` R63DBG verdict lines, superseded
`bin/goopg-r63probe`); clone-tpch :5533; PG reference :65432.
Tree holds NO probe code (reverted, `git status` clean for `internal/`).
