# P4-A — narrowing what a relation carries (`PathTarget`, slice 1)

Implementation design for TODO **P4-01**. Parent design:
[08 §7](../08-target-design.md).

Promoted out of Phase 4 order by measurement —
[FINDING-workmem-advantage.md](FINDING-workmem-advantage.md) §2b.

Revision 2 — corrected after agent review. The **diagnosis reproduced
independently**; the **mechanism was substantially wrong**. Revision 1 proposed
building a collector that already exists, and recommended the one option that
cannot work. Corrections marked **[R2]**.

---

## 1. The measured problem

TPC-H Q14 references two columns of `part`: `p_partkey` and `p_type`. goopg's
build side carries all nine.

**[R2] The width model in revision 1 was wrong, though the conclusion holds.**
The hash geometry does **not** use the 548-byte tuple width. `hashsize.Choose`
(`internal/executor/hashsize/hashsize.go:156`) takes a **column count**, and

```
EntryBytes = 48·ncols + 24 + avgVarBytes        (hashsize.go:121-128)
```

so the driver is **48 bytes per column**, not the byte width. `200000 × 548 B`
was a coincidence of magnitude. Measured directly against `hashsize.Choose` at
`work_mem = 64MB`, 200 000 rows:

| ncols | avgVarBytes | entry | inner | NBatch |
|---|---|---|---|---|
| 9 (full `part`) | 0 | 456 | 87.0 MB | **2** |
| 9 | 110 | 566 | 108.0 MB | **2** |
| 2 (narrowed) | 0 | 120 | 22.9 MB | **1** |
| 2 | 25 | 145 | 27.7 MB | **1** |
| 9 @ 512 MB | 0 | — | — | 1 |

So the fix is **"drop seven columns"**, not "shrink 548 bytes to 6", and
`avgVarBytes` is a second-order term. The causal chain from the `DPPATH` trace
is unchanged and the 512 MB arm not batching is reproduced:

**9 columns → 456 B/row → 87 MB hash → batching at 64 MB → `join.hash` costed
1 811 944 against `mergejoin` 754 717 → merge join over a full 6 M-row index
scan wins → 13.9 s where PG takes 1.08 s.**

## 2. Where the width comes from today

- `internal/optimizer/joinsearch.go:349-364` — `nodeTupleWidth(leaf)`,
  `rel.NCols = len(leaf.Output())`, and `AvgVarBytes` summing `cs.AvgWidth` over
  **every** column.
- The executor does **not** read a width from the plan. It measures the schema
  at runtime: `operators_join_agg.go:543-546`
  (`o.lazyLW = len(o.left.Schema())`) and `parallel_hash_build.go:209-211`
  (`buildWidth := len(j.right.Schema())`).

That second fact is load-bearing and revision 1 missed it — see §5.

## 3. **[R2]** What already exists

Revision 1 proposed building an `attr_needed` collector. **M0134-0187 landed
it.**

| revision 1 proposed to build | exists at |
|---|---|
| per-FROM-item needed-column set | `neededColumnNames()` — `pathindexonlyneed.go:34`; per-rel filter `neededColumnsOf()` — `pathindexonly.go:76` |
| threading it to the search | `searchCtx.neededCols/neededColsKnown` — `joinsearch.go:171-172`, populated `planner.go:1252`, carried through `relfromjoinlist.go:117`, `geqo.go:333`, `joinsearchseam.go:351` |
| safety for shapes the collector cannot model | already by construction: unmodelled shapes return `ok=false` and **nothing narrows** (`pathindexonlyneed.go:53-58`, `default:` at `:250`) |
| a leaf emitting fewer columns than the table | `IndexOnlyScan` — built at `createplanindex.go:333-370` |
| coordinate recovery for a narrowed leaf | `baseRelLayout`'s by-NAME re-basing — `createplanjoin.go:141-168` |
| boundary handling for dropped coordinates | `boundaryMap`'s `fill` licence — `createplanroot.go:101-107, 181-232`; typed-NULL padding at `:433` |

Revision 1's parenthetical "the IOS promotion is schema-preserving" was a
**stale note**. `pathindexonly.go:1-30` records that the old schema-preserving
peephole "fired zero times across all 22 TPC-H queries", which is why the
non-schema-preserving path was built.

## 4. **[R2]** Mechanism — the third option

Revision 1 offered two options and recommended the one that cannot work.

**Rejected (was recommended): narrow the leaf scan's own output schema.**
`joinsearchseam.go:244-263` asserts that concatenated leaf widths equal the
analyzer's `rangeBinding.offset` progression (`planner.go:424-427`) — the
coordinate space **every `ColumnRef.Index` in the statement was resolved into**.
Narrowing a leaf shifts every later binding's offset, the seam declines with
`offset-disagreement`, and goopg **silently falls back to the legacy plan**.
Q14 would be unchanged. `translateToLayout` (`createplanjoin.go:205`) is
`set_join_references` — it re-bases clauses into a node's output positions and
cannot renumber the binding space.

**Rejected: insert a `Project` below the build side.** True that no `Project`
sits above a scan in the join tree (the only one `createPlan` inserts is the
search-boundary `projectToBindingOrder`, `createplanroot.go:150`) — but a new
node class must be taught to ~10 leaf-recognition switches.

**Chosen: narrow the COST INPUTS and emit a PROJECTED SCAN; leave the binding
space wide.**

1. At `joinsearch.go:350-364`, compute `Width`/`NCols`/`AvgVarBytes` over
   `neededColumnsOf(tbl)` instead of `leaf.Output()`, gated on
   `neededColsKnown`.
2. At `createPlan`, emit the scan with a narrowed schema — exactly as
   `createplanindex.go:333-370` already does for `IndexOnlyScan`, as a schema
   field rather than a new node type.

**§5's "make the executor agree" problem disappears**: the executor reads
`len(schema)` at runtime, so a narrower emitted node narrows its `ncols`
automatically. Revision 1 called this "the substance of the design"; it is
nearly free.

## 5. **[CLOSED — P4-01a landed]**

> `pathindexonly.go:129-130` sets `NCols: len(covered)` and
> `AvgVarBytes: coveredAvgVarBytes(...)`, and `relNCols` (`path.go`) prefers
> `r.NCols`. The planner/executor width desync this section described is fixed
> and survived the P4-01b revert. Section retained for the reasoning only.

## 5 (historical). **[R2]** A live desync this closes

The index-only paths **already** narrow the emitted node without narrowing
`rel.NCols`: the cost model prices the build at full `relNCols`
(`pathgen.go:82`) while the executor measures the narrowed `IndexOnlyScan`.
That is precisely the planner/executor divergence this design declares
unacceptable, **live at HEAD**. Step 1 of §4 closes it. Recorded as a fix, not
as a hypothetical hazard.

## 6. Why not narrow the costing only

It would make the planner price a geometry the executor would not build. Under
§4 the question does not arise, because the emitted node narrows too.

## 7. **[R2]** Real remaining work

Not the collector, and not the coordinate machinery. It is the
**leaf-recognition switches** that must accept a projected scan:
`joinlayout.go:777`, `parallel.go:194,390,447,578`, `cardinality.go:51`,
`subplan_cost.go:38`, `subplan_lower_walk.go:63`, `plan.go:884`,
`view_privilege.go:30`, `planner.go:9379`.

**`parallel.go` matters most.** Q14's target plan is a parallel hash join; a node
the parallel-eligibility walker does not recognise loses parallelism, which would
eat the win. Emitting a projected `SeqScan`/`IndexScan` (a schema field) rather
than a new node class minimises this.

## 8. Verification

1. **Value-level, not row counts.** `cmd/tpch-runner -digest` (real:
   `cmd/tpch-runner/digest.go`) on both arms, then `-diff`. A projection bug
   drops or misplaces a COLUMN, which row counts cannot see (risk R5).
2. **[R2] Corrected thresholds.** Revision 1 asked for `width=6`. goopg's
   `typeWidth` gives, for HammerDB's schema (integer keys declared NUMERIC):
   `p_partkey` → 32, `p_type` varchar(25) → 68, so the narrowed width is
   **≈100**, not 6. PG's 6 comes from a different declared schema and width
   model. The criteria are: **`NBatch` 2→1**, and `join.hash`'s `DPPATH` total
   below `mergejoin`'s 754 717.
3. Q14 and Q3 timings at `work_mem = 64MB` against the 403.27 s control.
4. Full TPC-H 24-item A/B and the TPC-DS SF0.5 gate (0 MISMATCH) — a projection
   defect is a wrong-answer class.
5. `RALPH_PRECOMMIT_SCOPE=units` and pg_regress: `limit` and `numerology` are
   the only two live failures, so a third is attributable.

## 9. Risks

| risk | mitigation |
|---|---|
| A parallel-eligibility switch does not recognise the projected scan and Q14 loses parallelism. | §7's list; assert `Workers Planned` is unchanged on Q14. The largest real risk. |
| **[R2]** The needed-set is a NAME-based over-approximation (`pathindexonlyneed.go:15-18`), not RTE-attributed — a column name shared with another relation silently widens it. | Widening is safe (it only forgoes the optimisation); narrowing wrongly is not, and the collector cannot narrow wrongly by construction. Recorded as a known imprecision. |
| A column needed by an unmodelled shape is dropped. | Cannot happen: `neededColsKnown=false` disables narrowing entirely. |
| Slice 1 helps Q14 and not Q3. | Expected; Q3's build side is `orders`, whose needed set is larger. A slice boundary, not a failure. |
| ~~Narrowing makes an index-only scan newly eligible~~ | **[R2] Void** — IOS eligibility is already driven by the same `neededCols` set (`pathindexonly.go:30,46`). Revision 1's mitigation would have wasted effort. |
| ~~`boundaryMap` panics on a pruned-column hole~~ | **[R2] Overstated** — `fill` licenses pruned coordinates (`createplanroot.go:181-232`). It panics only on *unlicensed* holes, which is the intended tripwire. |

## 10. Scope note

`RelOptInfo.NCols`/`AvgVarBytes` are **per-REL** (`path.go:218-240`); PG's
`PathTarget` is per-PATH. For slice 1 the needed set is a statement property, so
per-rel is adequate — but this constrains slice 2, since `build_joinrel_tlist`
is inherently per-joinrel and `calcJoinrelSize`'s rows-once discipline
(`joinrelsize.go:105-117`) will resist it. That gap is already ledgered there in
the same terms.

---

## 11. Revision 3 — **SUPERSEDED, DO NOT CITE** (see §12 and §13)

> Kept for history only. Its table is the UNEQUAL-cardinality comparison, and
> the 24,005,020-row plan it measures is now known to be the dropped-equi-clause
> merge join that RETURNED WRONG ANSWERS
> (`FINDING-CRITICAL-mergejoin-wrong-answers.md`, fixed in `13d53603f` /
> `a96c65978`). These numbers were taken on a plan computing the wrong result.
> The "8x" below is also wrong: PostgreSQL's `work_mem` default is 4 MB, so
> 512 MB is **128x**, not 8x — the 8x figure is goopg-vs-the-bench-conf's 64 MB
> and does not belong in a sentence about PostgreSQL's default.

Revision 2 justified this work on Q14 and Q3. Measurement since has shown the
scope is far wider: **the missing projection is what makes goopg's memory
budget load-bearing**, and it blocks P2-02b and the remaining P2-02
propagation slices. Evidence in
`FINDING-planner-settings-not-propagated.md`; the numbers, taken on the same
cluster at the same `work_mem = 64MB x hash_mem_multiplier 2`:

| | PostgreSQL 18.3 | goopg |
|---|---|---|
| tuple widths through Q9's join tree | 23 / 32 / 54 / 81 B | 1542 / 2616 / 3164 B |
| peak hash memory | 38 MB, `Batches: 1` throughout | 97 MB, `Batches: 8` |
| rows through the middle join | ~319 k | 24,005,020 |
| Q9 | 6.2 s | 187 s |

**≈39x wider tuples at the same point in the same plan.** A hash table over them
needs ~39x the memory for the same rows, so goopg batches at a budget where
PostgreSQL does not, and its multi-batch path costs two orders of magnitude.

### What this changes about the plan

- goopg's headline TPC-H numbers depend on a `work_mem` default of 512 MB — 8x
  PostgreSQL's — which is roughly the headroom a 39x-wide tuple needs to stay
  single-batch on this corpus. P2-03's 37 % win was bought the same way, by
  doubling the budget rather than by fixing a mis-costing.
- Correcting `work_mem` to PostgreSQL's 4 MB (P2-02b) removes that headroom, so
  P2-02b is blocked **here**, not on the settings plumbing.
- Propagating real session settings into derived tables is correct and was
  hand-verified to build and pass the unit suites, but it costs Q9
  15.4 s -> 187 s for the same reason and must land *after* this item.

### Mechanism note for the next attempt

The reverted P4-01b attempt narrowed the **leaf** schema, which moved the join
search's seam offsets and returned wrong answers (Q2 418 -> 0 rows, Q5 5 -> 0,
Q18 different tuples) while being 3.6 % *faster* and matching 21 of 24 queries.
Two lessons carry forward:

1. Narrowing must not change the coordinate space the seam maps through. A real
   `PathTarget` — projection expressed as a property of the path, with
   `setrefs`-style fixup at `create_plan` time — is the mechanism; rewriting the
   leaf's `Output()` is not.
2. **Gate on values, not row counts.** Row counts alone would have passed Q18.
   `cmd/tpch-runner -digest` + `-diff` is the required gate, on all 24 items.

### Sequencing

1. This item (`PathTarget` + projection).
2. The derived-table / set-op / scalar-subquery propagation slices — a two-line
   change each once the width is fixed (`planSelectWithParent` takes the
   settings and calls `planSelectWithSettings`; `planSubqueryRangeVar` forwards
   them).
3. P2-02b, which should be close to free once 1 and 2 are in.


---

## 12. Revision 4 (2026-09-03) — the width claim, restated on a fair comparison

Revision 3 claimed a ~39x width gap made goopg batch where PostgreSQL does not,
and `FINDING-planner-settings-not-propagated.md` withdrew that as a CAUSAL
story: at the time, goopg's fast and slow arms had identical batching and
widths, so neither explained the difference between them. The real cause was
two bugs, both now fixed — a merge join costed on post-filter rows
(`c281b0830`) and a merge join that dropped an equi-clause entirely
(`13d53603f`).

With both fixed the comparison is finally fair, and the width claim can be made
properly — on EQUAL CARDINALITY, which is what was missing before.

TPC-H Q9, SF=1, `work_mem` = PostgreSQL's default on both engines:

| | PostgreSQL 18.3 | goopg |
|---|---|---|
| rows through the join tree | ~319 k (63 749.60 x 5 workers) | 321 056 |
| tuple widths | 23 / 32 / 54 / 81 B | 1098 / 1642 / 2090 / 3164 B |
| peak hash memory | 38 MB | 97 MB |
| batches | 1 throughout | **8** |
| parallelism | Parallel Hash Join, 4 workers | none |
| Q9 | 6.2 s | 63.8 s |

The row counts now AGREE. The plans compute the same thing, and goopg's tuples
are 14-39x wider at every level because there is no `PathTarget` and so no
projection. That is what drives 97 MB against 38 MB and 8 batches against 1.

This is the whole of P2-02b's remaining cost. Correcting `work_mem` to
PostgreSQL's default is now VALUE-CORRECT (24 MATCH) and costs
239.7 s -> 295.9 s, of which Q9 is +47.1 s and Q7 +9.7 s; every other query is
neutral. Both are the same shape: at the smaller budget the plan moves onto
index-scan-driven joins, which batch more AND are not eligible for goopg's
parallel post-pass (it drives off sequential scans), so they lose the Gather too.

### Consequences for sequencing

1. **P4-01 (this item) is the remaining blocker for P2-02b**, on evidence that
   now survives the fair comparison. It was not the blocker for the earlier
   failures, which were the two merge-join bugs.
2. The lost `Gather` is a SECOND, independent cause of Q9's +47 s and belongs to
   Phase 5 (parallelism in the Path model): an index-scan-driven plan cannot be
   parallelised by a post-pass that only recognises sequential scans. Narrowing
   the width alone will not recover it.
3. The gate remains value-level. Both bugs fixed this session returned the
   CORRECT ROW COUNT while computing the wrong answer or the wrong plan.


---

## 13. Revision 5 (2026-09-03) — agent review, and the two things it changed

Rev 4 was reviewed. Two findings are structural enough to change the plan; one
prediction the review made was tested and did not hold. Recorded here rather
than silently folded, so the next reader can see which claims were checked.

### 13.1 The collector declined the query this item is justified by — FIXED

`collectExprColumnNames` had no `*parser.ExtractExpr` arm, so `extract(field
from src)` fell to the `default:` arm and returned false — and
`neededColumnNames` returns false for the WHOLE statement on any decline. A
single `extract` set `neededColsKnown = false`, disabling index-only scans
(`pathindexonly.go:34`) and every narrowing mechanism in §4.

TPC-H **Q7, Q8 and Q9** all use `extract(year from ...)`, and Q9's sits inside
the derived table that owns its six-way join tree. **P4-01 as designed could not
have touched its own motivating query.**

Fixed in `915ce7882`, and verified rather than assumed: `neededColsKnown` is
`false` before and `true` after, for a bare `extract` and for Q9's
derived-table shape. Gated at Q12=2 / Q13=34 and 24 MATCH.

### 13.2 The mechanism in §4/§7 is the one that returned wrong answers

This is the correction that matters, and rev 4 got it wrong.

The seam's coordinate space is **safe**. `extractSearchLeaves` computes
`widths[i]` from the PRE-search chain leaves, and `baseRelLayout` +
`boundaryMap`'s licensed `fill` already handle a narrowed leaf's coordinates.
§3's rows about those are accurate; the revert message's "they are NOT
sufficient" was right for the wrong reason.

The invariant that actually broke is **"a node's `Output()` equals the row its
operator emits"**:

- `newSeqScanOp` (`operators_storage.go:1338-1350`) takes `schema: p.Output()`
  but `cols: p.Table.Columns`; `scanRow` is sized `len(o.cols)` (`:2081`) and
  `decodeRowRangeInfo` decodes every table column in table order (`:1383`).
- `indexScanOp` is identical (`operators_index.go:608-611`).

So narrowing a leaf's `schema` re-bases the planner onto narrowed positions
while the executor still emits table-order rows: ColumnRef *i* reads table
column *i*. That is exactly Q2/Q5 → 0 rows and Q18 → same count, wrong tuples,
and it is why every plan-time tripwire passed — the *layout* was self-consistent;
the row shape was not.

`IndexOnlyScan` works only because it is a different node class with a genuinely
projecting operator. §3 cites it as precedent FOR a schema field; it is evidence
against.

**Therefore:** a `PathTarget` with `setrefs`-style fixup is necessary but NOT
sufficient. Either the scan operators must project (changing `newSeqScanOp`,
`decodeScanRow`, `decodeScanRowRange`, the `Next` emit block, the parallel
sibling in `parallel_hash_build.go`, and `operators_index.go`), or a real
`Project` node goes below the build side. §4 dismissed the Project option; §7's
argument that "parallel.go matters most" is largely wrong, since
`stampParallelScan`, `drivingScan` and `extractSeqScanFromPlan` all already have
`*Project` arms. **Re-cost the Project option honestly before choosing.**

### 13.3 A review prediction that did NOT hold

The review argued width and the lost Gather share a gate: that fixing
`neededColsKnown` would let an index-only-driven plan become Gather-eligible
(`drivingScan` does accept `*IndexOnlyScan`), making §12's two-causes framing
overstated.

Tested directly, which is cheaper than arguing. With the `ExtractExpr` fix in
and `neededColsKnown` true, Q9 at PG's 4 MB is **56.94 s against 55.42 s
before** — unchanged, with no Gather and no index-only scan in the plan. The
gate was necessary and not sufficient. §12's split stands, and
`MEASUREMENT-p202b-width-vs-gather.md` quantifies it: width ~87 %, Gather ~13 %.

### 13.4 Accepted corrections not yet actioned

- **§12's absolute numbers are stale.** They predate nine planner commits; the
  current figures are 215.62 → 265.44 s and Q9 +40.1 s (`7d03fc0cf`). **The §12
  EXPLAIN table has not been re-taken since** and must be before it is used as
  the evidentiary base.
- **§12 argues in the wrong currency.** §1's own [R2] established that goopg's
  hash geometry is driven by COLUMN COUNT —
  `EntryBytes = 48*ncols + 24 + avgVarBytes` (`hashsize.go:120-128`) — and
  `seqScanOp` allocates a 48-byte Datum slot per TABLE column. §12 reverts to
  byte-width language. Same conclusion, wrong units.
- **The equal-cardinality table needs a row per measured node**, not one
  cardinality line for four width levels.
- **"97 MB vs 38 MB" is not like-for-like:** goopg is at `Batches: 8`, so its
  figure is one batch's residency, and PG's is a Parallel Hash shared total.

### 13.5 The gate, corrected

§11/§12 narrowed §8's gate to `tpch-runner -digest` alone. Restore §8 in full,
and add three things the review is right about:

1. **Diff against the PG oracle, not a goopg baseline.** Both merge-join bugs
   were invisible goopg-vs-goopg.
2. **Run the gate at BOTH budgets.** P4-01 exists to change plan shapes at the
   SMALL `work_mem`; gating only at 512 MB validates it under the plans it does
   not affect. "The default is not a safeguard, it is camouflage."
3. **Add a planner-vs-executor row-shape assertion** — under a debug flag,
   assert `len(row) == len(op.Schema())` on the first row of every operator.
   Every existing tripwire is plan-time and self-consistent, which is why
   P4-01b passed them all. This turns the whole failure class into a unit-test
   failure.

### 13.6 Status

**Not ready to implement against.** §13.1 is fixed; §13.2 changes the mechanism
and must be re-decided; §13.4's re-measurement is outstanding. The review's
recommended pre-implementation order — now that §13.1 is done and §13.3 is
answered — is: re-take the §12 EXPLAIN table on current HEAD, then run
`hashsize.Choose` at the NARROWED ncols for each of Q9's four join levels to see
whether `NBatch` still exceeds 1. If it does, width is not the dominant cause
and this item is mis-scoped.


---

## 14. Revision 6 (2026-09-03) — §12's table re-taken on current HEAD

§13.4 recorded that §12's evidence predated nine planner commits and had three
methodological defects. Re-taken at `f65e7c154`, with those corrected.

### The measurement

TPC-H Q9, both engines at their own defaults, `EXPLAIN (ANALYZE, TIMING off)`.

**Join-tree tuple widths, per node:**

| level | goopg | PostgreSQL 18.3 |
|---|---:|---:|
| top join | 3164 B | 81 B |
| 2 | 2716 B | 54 B |
| 3 | 2168 B | 32 B |
| 4 | 1094 B | 23 B |
| (supplier ⋈ nation) | 1074 B | — |

**Cardinality at the top:** goopg 321,056; PostgreSQL 63,749.60 × 5 loops =
318,748. **0.7 % apart** — the two clusters are separately loaded by HammerDB,
so exact equality is not expected and this is as close as the comparison gets.
The equal-cardinality premise holds.

**Batching and memory:** goopg `Batches: 8, Memory Usage: 97482kB`;
PostgreSQL `Batches: 1, Memory Usage: 38176kB`.

**Execution:** goopg 15,946 ms; PostgreSQL 6,776 ms.

### Three corrections §13.4 asked for

1. **A row per node, not one cardinality line.** Done above. Note the caveat it
   exposes: goopg does not emit per-node `actual rows` for join nodes UNDER a
   `Gather`, so per-LEVEL cardinality equality cannot be shown from this plan —
   only the top-level figure is measured on both sides. The width rows are
   plan-time on both engines and directly comparable.
2. **"97 MB vs 38 MB" was not like-for-like, and still is not.** goopg is at
   `Batches: 8`, so 97482kB is ONE batch's residency — bounded by the budget
   almost by construction. The comparable total is ≈ 8 × 97 MB ≈ 780 MB against
   PostgreSQL's single 38 MB. That makes the gap larger, not smaller, but the
   pair "97 vs 38" should not be quoted as if it were one.
3. **Do not quote a single width ratio.** Level-for-level the ratios are 39×,
   50×, 68× and 48×, but the two join trees are not the same shape, so those
   pairings are not guaranteed to be like-for-like. The defensible statement is
   the RANGE: goopg carries 1094–3164 B where PostgreSQL carries 23–81 B.

### What did not change

The direction and the order of magnitude are unchanged from rev 4, now on
current HEAD and with the arithmetic stated honestly. §12's *numbers* are
superseded by this section; its *conclusion* stands.

What changed materially since rev 4 is elsewhere: `FINDING-p401-alone-is-not-enough.md`
shows that narrowing to Q9's needed columns takes the batching build from 128
batches to 64 at PostgreSQL's `work_mem` — not to 1 — because
`EntryBytes = ncols × 48 + 24` makes the per-column footprint co-dominant with
the column count. **P4-01 remains justified on its own terms and no longer
carries the claim that it unblocks P2-02b.**


---

## 15. Revision 7 (2026-09-03) — the mechanism decision §13.2 forced

§13.2 established that a `PathTarget` with `setrefs` fixup is necessary but not
sufficient, and left two options. Decided here, on the code rather than on
preference.

### The deciding property

`projectOp` sizes its output from the SAME list its schema comes from:

- `o.out = acquireRow(len(o.targets))` (`operators.go:341`)
- `schema: plan.Output()`, which is the projected target list
  (`newProjectOp`, `operators.go:334-335`)

So a `Project` narrows the row and the schema **together, by construction**. It
cannot produce the P4-01b failure, because there is no second place holding the
old width.

The scan operators are the opposite: `newSeqScanOp` takes `schema: p.Output()`
but `cols: p.Table.Columns`, and sizes `scanRow` from `cols`. The width lives in
two places, and P4-01b moved one of them.

### Decision: **insert a real `Project`, do not make the scans project**

| | Project below the build side | scans project |
|---|---|---|
| invariant | satisfied **by construction** | must be maintained by hand across every decode path |
| sites to change | planner inserts a node | `newSeqScanOp`, `decodeScanRow`, `decodeScanRowRange`, the `Next` emit block, `parallel_hash_build.go`'s scan extraction, and `operators_index.go`'s equivalents — in lockstep |
| failure mode if wrong | a missing column is a *plan* error, caught at build | a width mismatch is a *silent wrong answer* |
| parallel machinery | already descends it — `drivingScan` has `case *Project` (`parallel.go:457`), `extractSeqScanFromPlan` likewise (`parallel_hash_build.go:320`) | scan-shaped, but every extraction site must agree |

§7 dismissed the `Project` option on the grounds that the parallel
leaf-recognition switches would need updating and that "parallel.go matters
most". That is **wrong**: those switches already have `*Project` arms, added for
other reasons. §7's objection should be struck.

### The cost of the chosen option, stated

An extra operator over the build side: one `evalExprSlot` per target per row.
For the `*ColumnRef` targets a projection is made of, that is a slot read, not an
expression evaluation — but it is not free, and it is paid on the BUILD side
only, which is where the memory is saved. That trade is the thing to measure
first: a narrowed build that costs one slot read per column per row against a
hash table that no longer batches.

### What still gates implementation

`FINDING-p401-alone-is-not-enough.md`: narrowing takes the Q9 build from 128
batches to **64** at PostgreSQL's `work_mem`, not to 1, because
`EntryBytes = ncols × 48 + 24` makes per-column footprint co-dominant. So this
mechanism is now decided, and the item is still not sufficient on its own for
P2-02b. It should be implemented for its own 2–4× batch reduction, with the
`MinimalTuple`-shaped work (07 §6) tracked as its partner rather than as a
downstream residual.

Run it under `GOOPG_ASSERT_ROW_SHAPE=1` (both scan paths are instrumented) and
gate on `tpch-runner -digest` against the PG oracle at BOTH `work_mem` budgets,
per §13.5.


---

## 16. Revision 8 (2026-09-03) — where the `Project` goes, and why not at the join

Rev 7 decided the mechanism. Attempting the insertion surfaced a placement
constraint the design did not state; recorded here because it is the difference
between a working slice and a plan-time panic.

### The obvious place does not work

Wrapping the build child at the hash-join arm — after `joinInputsFor` has run —
is wrong. `joinInputsFor` derives the join's schema from its children:

    outerSchema, innerSchema := outerNode.Output(), innerNode.Output()
    merged := <outerSchema ++ innerSchema>        // createplanjoin.go:288-295

and immediately above that it PANICS when "child layouts disagree with child
schemas" (`:290`). A `Project` inserted after that point changes
`innerNode.Output()` without changing the layout the child returned, so the
check fires.

### Where it must go

**The child's own `createPlan` arm must emit the `Project` and return an
`outputLayout` consistent with the narrowed schema.** Every arm already returns
that pair; the narrowing has to be produced by the same code that produces the
layout, not bolted onto its result.

That is the same shape as the rev 7 decision one level up: the width and the
thing that describes it must come from a single place. At the operator level
that is `projectOp`'s `targets`; at the plan level it is the arm's
`(Node, outputLayout)` return.

### The reassuring part

`createplanjoin.go:290` means the layout-vs-schema disagreement class is caught
**at plan time, by a panic**. It is not the class that produced P4-01b's wrong
answers — that one is planner-vs-executor row shape, which no plan-time check
can see, and which `GOOPG_ASSERT_ROW_SHAPE` now covers on both scan paths.

So the two failure modes of this change now have distinct, existing guards:

| failure | guard | when |
|---|---|---|
| child layout disagrees with child schema | `createplanjoin.go:290` panic | plan time, already present |
| emitted row disagrees with advertised schema | `GOOPG_ASSERT_ROW_SHAPE` | run time, added this session |

### Remaining unknown, stated plainly

Whether narrowing a leaf's `Output()` at `createPlan` time disturbs the seam's
recorded widths. `extractSearchLeaves` computes `widths[i] = len(n.Output())`
from the PRE-search chain leaves, and the review's reading is that nothing in
the search or in `createPlan` can move that. That reading should be CONFIRMED
against the offset check (`joinsearchseam.go:244-263`) before the first slice
lands — it is the one place where rev 5 §13.2's "the seam is genuinely safe"
has not been tested by executing a narrowed plan.
