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

## 5. **[R2]** A live desync this closes

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

## 11. Revision 3 (2026-09-03) — this item is the corpus blocker, and it is now quantified

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
