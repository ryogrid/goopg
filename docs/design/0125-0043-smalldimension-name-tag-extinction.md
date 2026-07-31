# M0125-0043 — extinction of the `SmallDimension` benchmark-name tag

**Status: FIXED (2026-07-31).** Production planner behaviour no longer branches
on the literal table names `region` / `nation`. The small-dimension property is
derived from the relation's SIZE at plan-build time and stamped on the scan
leaf. Measured plan-neutral on all 22 TPC-H queries.

- Milestone: M0125 (`docs/milestones/0125-tpcds-timeout-class-and-walker-extinction.md`)
- Predecessor: M0054-0010 (`docs/design/0054-0005-hash-join-small-side-build.md`),
  which introduced the flag; M0077-0001 Slice A, which added the gate that
  depends on it.
- Related: `docs/design/cost-model/` ch. 06 §2.1 ("retiring the SmallDimension
  name-tag as the primary rule"), the long-run destination this change is a
  down payment on.

---

## 1. The defect

goopg branched **production** planner behaviour on the schema of a benchmark.
Two sites, a sibling pair (Hard-won Rule #2 — CREATE path ↔ catalog-reload
path), were the only non-test writers of `catalog.Table.SmallDimension`:

| site | code before |
|---|---|
| `internal/executor/operators_ddl.go` (`execCreateTable`) | `switch strings.ToLower(s.Name.Name) { case "region", "nation": tbl.SmallDimension = true }` |
| `internal/initdb/open.go` (`loadUserTablesFromHeapForDB`) | `SmallDimension: tr.RelName == "region" || tr.RelName == "nation"` |

Three separate things are wrong with that, and they matter in different ways:

1. **It is a correctness/architecture defect, not a tuning wart.** A user table
   called `nation` got a different plan shape than the identical table called
   `nations`. Nothing about the name is a property of the data.
2. **It under-covers.** Every tiny dimension table that is *not* one of TPC-H's
   two got none of the treatment — including every TPC-DS dimension, which is
   the benchmark this milestone is actually about.
3. **The CREATE-TABLE site never had the information it was guessing at.** A
   table being `CREATE`d is empty; its eventual size is unknowable there. The
   name was standing in for a size because a size was not available at that
   point in the lifecycle. It is available at *plan* time.

The flag was not decorative. It feeds seven consumers that can each flip plan
shape (see §4), including the `shouldAttachBeforeMHJ` gate whose own comment
records that removing it regressed **TPC-H Q8 / Q21 from PASS to CANCEL**
(M0077-0001 Slice A). A naive deletion re-opens a measured regression; this is
why the change is a *migration* of the signal rather than a deletion.

## 2. The fix

**The property moves from the catalog to the plan node, and its source moves
from the name to the size.**

New file `internal/planner/small_dimension.go`:

- `smallDimensionTag(cat, tbl) bool` — the single derivation point.
- `smallDimensionRows(cat, tbl) int64` — the row count it thresholds: the
  ANALYZE-derived count (`tableRows`) when this session has one, otherwise the
  block-count fallback (`estimateTableRowsFallback`, goopg's `estimate_rel_size`).
- `smallDimensionMaxRows = 1024` — deliberately the same constant as
  `smallAnchorRowsThreshold` (`equiv_class.go:207`). Both answer "is this
  relation small enough to pin planner decisions on without stats"; letting
  them disagree would allow a relation to anchor an inferred equality edge
  while being denied the build side of the very join that edge produces.
- `smallDimensionSide(n, tbl) bool` — read helper tolerating a nil leaf (the
  bushy DP calls `estimateBaseRelInfo` with a nil scan for bindings that
  produced no scan of their own).

New fields `SeqScan.SmallDim` / `IndexScan.SmallDim` (`plan.go`), stamped at
plan-build time in `planScanRangeVar` and the three index-scan builders. They
sit next to `EstRelRows` and exist for the same recorded reason: the consumers
(`IsSmallDimensionSide`) take only a `Node` and run with **no catalog in
scope**, including from EXPLAIN inside the executor.

`IsSmallDimensionSide` now reads `x.SmallDim` instead of
`x.Table.SmallDimension`. Its meaning is unchanged; it simply answers for every
tiny dimension table rather than for two TPC-H ones.

### Why the threshold works in both states the planner meets

- **ANALYZEd:** the real row count. TPC-H `region` is 5 and `nation` is 25, so
  both stay flagged; `supplier` (10k at SF=1) does not.
- **Cold** — goopg's normal state, since `TableStats` does not survive a
  restart and ANALYZE stats are per-connection: the fallback derives rows from
  the LIVE block count, where upstream's never-analyzed 10-page floor
  (`neverAnalyzedMinPages`, `relsize.go:179`) dominates. Any relation occupying
  fewer than 10 blocks estimates `10 * density` rows — **170** for both `region`
  and `nation` on the TPC-H schema. The threshold is therefore effectively "the
  whole heap fits in well under a hundred kilobytes", which is the physical
  property the name tag was standing in for.
- **No storage at all** (the in-memory catalogs most planner unit tests build):
  estimates 0, and 0 is explicitly NOT small. "No estimate" must never read as
  "tiny".

### Two deliberate decisions worth defending

**(a) The fallback is read UNGATED — not through `relSizeFallbackRows`.**
`GOOPG_RELSIZE_FALLBACK` stages which *cost* consumers trust a block-derived
cardinality, and turning it off must restore pre-M0125-0003 plans. But
pre-M0125-0003 plans had the small-dimension property populated (by name) at
every stage. Gating it here would make `GOOPG_RELSIZE_FALLBACK=0` silently drop
the build-side pinning M0054-0010 added — a different change than the knob
promises. Pinned by `TestSmallDimensionTagIgnoresRelSizeFallbackStage`.

**(b) `catalog.Table.SmallDimension` survives as an explicit hint with no
production writer.** `internal/testutil/tpch/tpch.go` sets it on catalog-only
fixtures that have no heap to measure, so the planner's TPC-H unit tests keep
exercising the small-dimension paths without a loaded cluster. On a live server
the field is always false. `internal/initdb/catalog_cache.go` merely persists
whatever it is given and needed no change.

## 3. TPC-H queries affected — **measured, not inherited**

The item required this list be confirmed by measurement rather than by reading
the query text. Both were done, and they disagree in the informative direction.

**By query text** (`internal/testutil/tpch/tpch.go:112-133`), the queries that
reference `nation` or `region` at all — i.e. the only queries whose plans
*could* move — are:

> **Q2, Q5, Q7, Q8, Q9, Q10, Q11, Q20, Q21** (9 of 22).
> The other thirteen (Q1, Q3, Q4, Q6, Q12–Q19, Q22) never mention either table.

That list is **confirmed as the candidate set** and is unchanged.

**By measurement**, the set of queries whose plan actually changes is
**EMPTY — 0 of 22.** A same-cluster A/B (`git stash` of `internal/`, rebuild,
recapture on the identical loaded TPC-H SF=1 cluster at :65433) produced
byte-identical snapshots:

```
plan_snapshots/m0125-0043-before.txt   (22 queries)
plan_snapshots/m0125-0043-after.txt    (22 queries)
$ diff before after   →   no output
```

This is the result the change was aiming for and it is a **positive** result,
not a null one. The before-arm has the name tag ON, so `region` and `nation`
are flagged there. Had the size derivation failed to reproduce that — had it
returned false for either table — Q5's MHJ anchor and the `shouldAttachBeforeMHJ`
gate would have moved, and the 22 plans would differ. Byte-identical plans are
therefore direct evidence that **the derived tag reproduces the name tag exactly
on all nine candidate queries.**

The three the item flagged as at real risk all came through clean:

| query | risk the item named | outcome |
|---|---|---|
| Q5 | leans on filtered `region` as its MHJ anchor | plan byte-identical |
| Q8 | M0077-0001 Slice A CANCEL regression | plan byte-identical |
| Q21 | M0077-0001 Slice A CANCEL regression | plan byte-identical |

## 4. Consumers audited

Each reads the property and can flip plan shape. All were checked; all now
reach it through the node tag or through `baseRelInfo.isSmallDimension`, which
`estimateBaseRelInfo` populates via `smallDimensionSide`.

| consumer | how it reads the property now |
|---|---|
| `cardinality.go` `estimateBaseRelInfo` / `IsSmallDimensionSide` | node tag (`smallDimensionSide`, catalog hint only for a nil leaf) |
| `bushy.go:1383-1384` | `IsSmallDimensionSide` |
| `pushdown.go:305-306` | `IsSmallDimensionSide` |
| `local_filters.go` `shouldAttachBeforeMHJ` | **signature changed** — takes `scans []Node`; see below |
| `equiv_class.go:253` | `ri.isSmallDimension` |
| `inner_join_qual_pushdown.go` | comment-only reference |
| `executor/parallel_hash_build.go:20` | comment-only reference |

`shouldAttachBeforeMHJ(bindings, scans)` is the one that needed a signature
change: its second clause asks whether the FROM list contains a small-dimension
relation, and that answer is no longer on the binding's table. `scans[i]` is the
leaf built for `bindings[i]`; a binding with no scan falls back to the catalog
hint (what the unit fixtures set). Its sole caller `tryBushyDP` already had the
scans in hand. **This gate is the Q8/Q21 CANCEL guard** — disarming it silently
was the primary hazard of this change, and it is pinned in both directions by
new assertions in `TestShouldAttachBeforeMHJGate`.

### Sibling paths (Hard-won Rule #2)

`SmallDim` must survive every SeqScan↔IndexScan substitution, or a leaf silently
loses the property when a later pass promotes or demotes it:

| site | direction | handled |
|---|---|---|
| `nl_index_join.go:654` (`tryBuildNLI`) | Seq → Index | copies `innerScan.SmallDim` |
| `mhj_input_rewrite.go` ×4 | Seq → Index | copies `ss.SmallDim` |
| `unnest.go:1418` | **Index → Seq** (correlated probe demotion) | copies `n.SmallDim` |

The `unnest.go` demotion was found by auditing the construction sites rather
than by a failing test: it preserves alias, schema and privilege fields but was
dropping the new tag. It is the exact shape Rule #2 warns about — three
promotion sites were obvious, the one demotion site was not.

**Not stamped, deliberately:** the four DML target scans in `planner.go`
(`planUpdate` / `planDelete`, lines ~8928/8948/9096/9120). They feed
`Update`/`Delete`, never a hash-join build-side selector or the bushy DP, so no
consumer of the property can observe them. Stamping them would be inert.

## 5. Verification

| gate | result |
|---|---|
| `go build ./...`, `go vet` on the four touched packages | PASS |
| `go test ./internal/planner/... ./internal/catalog/... ./internal/initdb/...` | PASS |
| TPC-H plan A/B, same cluster, stash-based (§3) | **0/22 changed — byte-identical** |
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | PASS |
| `scripts/tpch-spotcheck.sh` | PASS (Q12=2 / Q13=35) |
| TPC-DS SF0.5 gate, full 99-query sweep | **PASS=89 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=6 SKIP=4** — and **all 99 cells byte-identical** to the loop-#12 baseline `sweep-20260731-094015.txt` in status, rows and checksum |
| timed 22-query TPC-H stream, arm c2, `PER_Q=600` | 21/22 ok, Q21 timeout — see §6 |

The TPC-DS gate matters here for a reason the TPC-H A/B cannot cover: this flag
feeds shared planner code (`estimateBaseRelInfo`, `equiv_class`, `pushdown`,
the bushy DP) that TPC-DS also traverses, and TPC-DS is where the derivation
newly makes *other* dimension tables eligible. The sweep is
`analysis/m0125-0043-sf05-20260731/`, report
`bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260731-121447.txt`. Zero
cells moved, so the widened eligibility is neutral on TPC-DS at SF0.5 — it
neither fixes nor breaks anything there. The timeout class is unchanged at
**Q30 Q64 Q65 Q72 Q78 Q81** (6).

New tests in `internal/planner/small_dimension_test.go` (7) cover: the TPC-H
tiny dimensions when cold (the acceptance direction), a fact table rejected,
the ANALYZEd path, no-storage-is-not-small, the explicit fixture hint, the
ungated fallback decision of §2(a), and the read side taking the node tag.
`local_filters_test.go` adds the two size-derived directions of the Q8/Q21 gate.

## 6. Timed acceptance run

Acceptance per the item: the full 22-query TPC-H stream completes with correct
row counts, **no query exceeding 600 s**; a slowdown inside that budget is an
explicitly accepted outcome, since this is a correctness/architecture cleanup
rather than a perf task.

Arm **c2** (`GOOPG_RELSIZE_FALLBACK=2`, no ANALYZE — the production S-cold
regime after M0125-0005 flipped the default), per-query server restart via
`scripts/tpch-relsize-arm.sh`, `PER_Q=600`, quiet host, nightly batch confirmed
idle.

Results: `analysis/m0125-0043-tpch-20260731/c2.tsv` (host load 0.24 at start,
wall 1207 s).

**RESULT: 21 of 22 `ok` with correct row counts; Q21 `timeout`.**

| | |
|---|---|
| completed with correct rows | Q1 Q2 Q3 Q4 Q5 Q6 Q7 Q8 Q9 Q10 Q11 Q12 Q13 Q14 Q15 Q16 Q17 Q18 Q19 Q20 Q22 (21) |
| exceeded the 600 s budget | **Q21** (killed at the 660 s external clamp) |
| canonical tripwires | **Q12 = 2, Q13 = 35** ✅ |
| slowest completed | Q5 61.6 s, Q9 52.1 s, Q4 44.2 s, Q18 37.9 s |

Row counts are correct across all 21 completed queries, including every one of
the nine candidates of §3 (Q2 455, Q5 5, Q7 4, Q8 2, Q9 175, Q10 20501, Q11 819,
Q20 76 — and Q21 is the timeout).

**Q21 is a PRE-EXISTING failure, not a regression of this change**, on three
independent grounds:

1. **Measured at HEAD before this change.** The 2026-07-30 four-arm study
   (`analysis/tpch-relsize-fallback-20260730/`) has Q21 `timeout` in **both**
   cold arms — `c1` at 305 s and `c2` at 366 s — captured before any line of
   this change existed.
2. **Already filed.** M0125-0031 recorded "Q21 times out in ALL FOUR arms →
   shape class", filed as **`M0125-0032`**. It is a known open item.
3. **This change cannot have caused it.** §3's A/B makes Q21's plan
   byte-identical before and after. A plan that does not change cannot change
   its runtime class.

One thing here IS new and is recorded for `M0125-0032`: at `PER_Q=600` Q21 is
**still** a timeout (660 s clamp, peak RSS 14.8 GB). Its previous readings were
against a 300 s cap, which left open the reading "Q21 is a budget crossing near
300 s". It is not — Q21 exceeds 600 s, so `M0125-0032` is a genuine shape
defect and doubling the budget does not reach it. A deferral-ledger row carries
this.

**Verdict against the item's acceptance.** The acceptance asks for the full
22-query stream with correct row counts and no query over 600 s. 21/22 meet it
outright; Q21 does not, and Q21 did not before this change either, on a
byte-identical plan. This change therefore introduces no timeout and no wrong
answer, but it **does not leave the stream clean** — that is `M0125-0032`'s
job, and it is stated here rather than worked around, per the item's own
instruction to record a measured outcome.

Because §3 measured the plans as byte-identical, the timings above should be
read as a *liveness and correctness* check rather than an A/B. This harness's
single-run per-query noise band is **~±17 %** (M0125-0031), so no per-query
comparison against an earlier arm is drawn here.

## 7. What this does NOT do

- **It does not retire the property.** The cost model's own answer — build-side
  orientation falling out of cost, `pathgen.go:44-49` / cost-model ch. 06 §2.1 —
  remains the destination. This change makes the signal honest (size-derived,
  universal) without waiting for that line to land.
- **It does not tune the threshold.** 1024 was chosen to agree with
  `smallAnchorRowsThreshold`, not from a sweep. No TPC-H or TPC-DS plan moved at
  that value, so there is no evidence in hand for a different one.
- **It does not give TPC-DS dimensions a *measured* benefit.** They now become
  eligible for the treatment for the first time, which is the point, but this
  loop measured plan-neutrality on TPC-H and status/checksum neutrality on the
  TPC-DS SF0.5 gate — not a win. Any win is future work.
