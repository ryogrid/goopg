# Why goopg does not produce PostgreSQL 18.3's plans

Investigated 2026-09-08 on `fix-parallel-worker-bug` (after PR #114 merged).
Measured plan parity: **match=6 / shapediff=14** over 20 TPC-H queries.

> **This is rev 2.** Rev 1 blamed a **missing candidate set** — index paths not
> reaching the search — and an adversarial review **falsified it completely**.
> The cause of that error was reading `pathgen.go`, assuming it was production
> code, and **never checking its callers** (every caller is a test). The
> correction is recorded in §6 rather than quietly dropped, because the error
> type recurs.

## Conclusion, in three lines

1. **The statistics goopg collects are essentially PG's set.** Not the main gap.
2. **The candidate set is also there.** A `create_index_paths` equivalent
   exists and runs five producers, including a bitmap enumerator. Rev 1's
   central claim was wrong.
3. **The main cause is a cost-currency asymmetry**: inside one `addPath`
   comparison the sequential scan is charged for its quals and the index scan
   is not. **The source itself documents this defect.**

## 1. Statistics — a small gap

| statistic | goopg | consumed at (verified) |
|---|---|---|
| `NDistinct` / `NDistinctFrac` | yes | `cardinality.go` |
| `NullFrac`, `AvgWidth` | yes | same |
| `MCV`, `Histogram` | yes | `eqjoinsel` family |
| **`Correlation`** | yes (`internal/executor/operators_analyze.go:1193`) | `indexCorrelationFor` (`costindex.go:407-420`) → all five index-cost sites |
| extended (multivariate) | yes (`extstats.go:178`) | `cardinality.go:1424` |

**Nothing here is computed but never read.** Join selectivity also follows
`eqjoinsel` / `eqjoinsel_semi` (`cardinality.go:632`, `:728-745`).

**Two known quality defects, though**, and the first matters more than the
table suggests:

- **The planner frequently runs at `corr = 0`.** ANALYZE statistics are
  connection-scoped and correlation is lost across a restart. When the leading
  column has no correlation slot, `indexCorrelationFor` returns 0
  (`costindex.go:412-414`), which prices **every index scan at `max_IO_cost`**.
  Benchmark sessions land in this state easily.
- TPC-DS row estimates are wrong by 3–5 orders of magnitude in both directions
  (the `rowest` row in `.ralph/deferral_ledger.md`).

## 2. The candidate set — present (rev 1 was wrong here)

`addBaseRelIndexPaths` (`internal/optimizer/pathindexordered.go:40-49`) exists,
and its own header states it **"is the whole of `create_index_paths`
(indxpath.c:235)"**. Production calls it from `relfromjoinlist.go:689`.

Its five producers:

| producer | what it enumerates |
|---|---|
| `addParameterizedIndexPaths` | parameterised probes (nested-loop inner) |
| `addOrderedIndexPaths` | ordering-motivated scans |
| **`addBaseRelBitmapPaths`** | **every base rel × every index** (`pathbitmap.go:60`), **no pathkeys gate** |
| `addParameterizedBitmapPaths` | parameterised bitmap |
| `addIndexOnlyPaths` | index-only |

The bitmap producer's header says it *"competes against PathSeqScan and
PathIndexScan in add_path"* (`pathbitmap.go:5-6`). The enumeration rev 1 called
absent is exactly this.

**The sequential rival is in the comparison too.** The production base-rel seed
is `newPrebuiltPath` (`joinsearch.go:434-441`), which files the pre-search leaf
— a `*Filter` wrapping a `*SeqScan` — through `addPath`, with `Rows` taken
post-filter (`:495-499`) and `Cost = costSeqscan(..., scanQualOps)` charging the
qual conjuncts (`:485-487`).

### The narrow structural gap that does survive

`Kind: PathIndexScan` is constructed at only three non-test sites
(`pathparamindex.go:398`, `pathindexonly.go:120`, `pathindexordered.go:216`), so
there is **no unconditional plain-index-scan arm**, and `addOrderedIndexPaths`
is gated on `hasUsefulPathkeys` (`pathindexordered.go:114`).

That is real, but it is **one missing arm of an existing `create_index_paths`**,
not the absence of the enumeration.

## 3. The main cause — the two rivals are not priced in the same currency

`costindex.go:229-247` describes the defect in its own words:

> two rivals in one `addPath` comparison no longer use one currency for the
> qual: the index path pays `cpu_tuple_cost × tuples_fetched` where PG pays
> `(cpu_tuple_cost + qpqual) × tuples_fetched` (costsize.c:822-830).
> … **The asymmetry FAVOURS the index path.**

On 2026-09-07 the sequential rival began charging
`cpu_operator_cost × conjuncts` on every tuple **scanned** (the C-19 base-rel
scan repricing, ledger `c19-baserel-scan-priced-on-output-rows`). The index path
was **not** given the matching charge. Both candidates exist and are compared;
the index side simply looks cheaper by the size of the qual.

**This explains Q12.** goopg picks a merge join over a full index scan of
lineitem where PG picks a filtered sequential scan feeding 31,354 index probes.
The filtered sequential scan **is** a candidate — it loses on a mispriced
comparison, not on absence.

### A second-order hole that amplifies it

`baseSeqScanCostInputs` (`joinsearch.go:480`) type-switches on `*SeqScan`. When
the pre-search leaf is already an `*IndexScan`, it falls back to
`numQualOps = 0` and an estimated page count — so an index leaf is priced with
**no qual charge at all**, on post-restriction rows.

### Evidence that this is costing, not candidates

Turning on E-21 Cut 1b regressed **Q21 by ~2.3× in the direction of choosing a
sequential scan over an index scan**, and Q4 by ~10×
(`docs/design/not_ralph/minimize_datum/TODO_ALL.md:4357`), with values holding
(TPC-H 24/24, TPC-DS PASS=95).

Choosing the *wrong* side with the full candidate set available is direct
evidence for the costing thesis and **against** rev 1's candidate-set thesis.

## 4. Secondary cause — single-table statements bypass the search

`isSimpleSingle` (`planner.go:1235`) diverts single-table statements to
`planIndexScanFromWhere` (`:9871`), whose implementation
(`planIndexScanFromWhereShape`, `:9899`) is a **shape match on the WHERE
expression** with no cost comparison at all:

- `InExpr` → SAOP index scan
- `OpEq` → equality index scan
- otherwise → `tryRangeIndexScan`

The diversion is **flag-gated**: `isSimpleSingle && !oneRelSearchEnabled()`
(`GOOPG_ONEREL_SEARCH`, default OFF). It stays off for the reason in §3 — with
the costing asymmetry in place, removing the rule chooser makes plans worse
even though the candidates are all present.

## 5. Is this a fundamental design difference? No — and rev 1 undersold goopg

goopg has most of PG's Path model:

- **`EquivalenceClass`** — union-find at `equiv_class.go:38-65`, wired by
  `assignEquivClasses` (`joinrestrict.go:240`), with an `eclass_already_used`
  analogue (`pathparamindex.go:179-190`).
- **`PathTarget` / `attr_needed`** — `Path.Target`, `NeededCols`,
  `stampNeededColsOnRels` (`relfromjoinlist.go:684-686`).
- **Pathkeys** — `Path.Pathkeys` (`path.go:232`), `buildIndexPathkeys`, a
  `query_pathkeys` analogue (`querypathkeys.go`).
- **Parameterised paths** — `RequiredOuter`, ppi_rows semantics.
- Upper rels, partial paths, `cost_gather`, DP join-order search.

Known partial gap: `presearchPathkeys` runs at a seam with no
EquivalenceClasses (`querypathkeys.go:350`).

## 6. What rev 1 got wrong, and how

Rev 1 read `generateScanPaths` (`pathgen.go:22-60`) and concluded base rels
receive only a sequential-scan path.

**`generateScanPaths` is dead in production.** It has zero non-test callers, and
its own header says *"this test-facing entry"* (`pathgen.go:26-31`). The
production seed is `newPrebuiltPath`.

**Error type: read code, assumed it ran, never checked the callers.** This is
the same shape as an earlier error in this workstream where a conclusion was
drawn from a profile of the wrong build — a check skipped one level below the
claim.

## 7. Suggested priorities — estimates, not measurements

1. **Equalise the qual currency**: charge `qpqual` on the index path in
   `costindex.go` to match PG's `(cpu_tuple_cost + qpqual) × tuples_fetched`.
   The source already flags this as outstanding. Most likely to move Q12-class
   plans.
2. **Close the index-leaf repricing hole** at `joinsearch.go:480`.
3. **Drop the `hasUsefulPathkeys` gate** on `addOrderedIndexPaths` so a plain
   index path becomes a candidate (§2's narrow gap).
4. **Persist correlation** so the planner stops running at `corr = 0` (§1).

**1 and 2 do not add candidates; they correct the price of candidates that
already exist.** Rev 1's priority list targeted a dead function.

## 8. What this document does not claim

- The ~2× executor constant factor (48-byte `Datum`, interpreted expression
  evaluation, Go GC, per-row cloning) is a separate matter, recorded in
  `docs/design/not_ralph/optimize-row-decode/REPORT-EXHAUSTION.md` §3.
- §7's ordering is an **unmeasured estimate**. In this workstream a CPU share
  was read as a forecast three times and was wrong three times, and rev 1 of
  this document added a fourth verification failure. None of §7 is settled
  until implemented and measured.
