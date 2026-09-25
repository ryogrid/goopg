# R122 Slice B result — the FIRST parity improvement of the A/B/C chain: TPC-H `join-method` and `scan-type` each −1 (Q3). TPC-DS restructures 3 plans for zero net category change.

Slice B carries narrowed widths past the first join. On TPC-H — where
the census shows the currency is essentially uniform (**0.07%** mixed
comparisons) — it closes **two categories on Q3** by making PG's join
method and scan type affordable, and the hash geometry now measured
(§6) shows exactly why.

On TPC-DS it is **live but parity-neutral**: 3 plans change structure,
1 changes cost, no category moves and no query regresses. The census
(**34.2%** of join costings mixed-currency) means that null does **not**
settle what Slice B would do at a lower decline rate — but it is a
result, not a void, and §8 answers the SCOPE's worth-finishing question
rather than deferring it again.

Flag stays default-off pending the promotion decision (§7).

## 1. What was implemented

`narrowJoinWidths` in `narrowcostinputs.go`, called from **`addPath` and
`addPartialPath`** (`path.go`) — the single funnel every join path
passes through, rather than seven constructors. A join publishes
`NCols`/`AvgVarBytes`/`OutputWidth` = **outer + inner**, outer-only for
`JoinSemi`/`JoinAnti`.

Timing looks wrong and is not: the constructor computes the join's own
cost from its *children's* widths before calling `addPath`, so stamping
there cannot affect that cost — correct, because the triple is consumed
only when this path becomes a child one level up.

Decline rules, each preventing a currency mix: (1) either child
un-narrowed; (2) all three fields or none; (3) idempotent — never
overwrite; (4) **either child index-only** — `NCols > 0` is not a proxy
for "this round narrowed it", since `pathindexonly.go` writes the whole
triple unconditionally; (5) nil-safe, as `addPath` has no nil guard.
Rule 4 is R121's wrapper leak one level up, caught in review before
implementation.

The `Kind` test is an explicit **whitelist** (`PathHashJoin`,
`PathMergeJoin`, `PathNestLoop`), never "has two children" or "has a
`Jointype`": `parser.JoinInner` is the **zero value** and `PathSetOp`
has exactly two children, so either shortcut would read a set-op as an
inner join and sum two widths that were never joined.

**24 pins** in `narrowcostinputs_test.go`.

## 2. Results

| # | bar | result | verdict |
|---|---|---|---|
| P0 | OFF bit-identical to pre-round HEAD, **both corpora** | TPC-H byte-identical; TPC-DS OFF identical to R121's OFF except 3 lines of psql temp-file path (see §5 on that artefact) | **PASS** |
| P1 | sum-of-children; outer-only SEMI/ANTI; all-three-or-none; decline on un-narrowed **or index-only** child; non-join Kinds refused (`PathSetOp` negative pin); `OutputWidth>0 ⟺ NCols>0`; **no `RelOptInfo` field written**; idempotent; nil-safe; wiring pin | all pinned; the wiring pin is **mutation-tested** (removing the `addPath` call fails it on both call sites) | **PASS** |
| P2 | values unchanged | SF0.25 sweep stamped `GOOPG_NARROW_COST_INPUTS=1`: **PASS=96 (60 ck-verified, 36 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3**. TPC-H Q3 — the one query whose plan changed — result digest **byte-identical** OFF vs ON (`6fb25a69…`) | **PASS** |
| P3 | no regression; join-order/join-method/sort-strategy non-increasing | **TPC-H IMPROVED: `join-method` 10→9, `scan-type` 9→8**, all others equal, match holds at 6, nothing regressed. TPC-DS: every category identical and match=2 — but **not inert**: 3 plans change structure and 1 changes cost (§5) | **PASS, with a first improvement** |
| P4 | a non-top join's `pathNCols` < `relNCols(joinrel)` on a named witness | multi-level sums observed (`13+2`, `11+2`, `1+18`). **Now a PERMANENT guard** rather than a deleted dump: `TestNarrowJoinWidthsNarrowsANonTopJoinInALiveSearch` runs the real search protocol over 3 relations and asserts a level-2 join publishes fewer columns than its joinrel's full width — mutation-verified | **PASS** |
| P5 | the §2 census reported | delivered in full, §4 | **PASS** |

Suites green, `go vet` clean, spotcheck PASS.

## 3. Q3 — what actually improved, and why it is the thesis confirming itself

Q3 went `[join-order, join-method, scan-type, aggregation-strategy,
sort-strategy]` → `[join-order, aggregation-strategy, sort-strategy]`.
It is the only query whose verdict line moved.

```
OFF   ->  Nested Loop
            ->  Hash Join (orders.o_custkey = customer.c_custkey)
            ->  <index probe on lineitem>

ON    ->  Hash Join (lineitem.l_orderkey = orders.o_orderkey)
            ->  Seq Scan on lineitem
            ->  Hash Join (orders.o_custkey = customer.c_custkey)

PG    ->  Hash Join (lineitem.l_orderkey = orders.o_orderkey)
            ->  Seq Scan on lineitem
            ->  Hash (...)
```

goopg now picks **PG's join method and PG's scan type**. This is the
width thesis validating itself end to end: with `lineitem` priced at 4
needed columns instead of 16, the hash build becomes affordable and the
search stops preferring a nested loop over an index probe. R120
diagnosed the currency, R121 built the narrowing, and Slice B is the
first point at which a real query's shape moves toward PG because of it.

Node-kind deltas across the arms: Sort ×4, Hash Join ×3, Seq Scan,
Nested Loop, Index Scan — all inside Q3.

**The residual `join-order` is a positional cascade, not a real
divergence** — worth stating because it makes the result stronger than
the category count suggests. `--verbose` shows every join-order entry is
an offset artefact of goopg's extra `Sort` under `GroupAggregate`
(`Hash Join Inner vs Seq Scan on lineitem`, plus three `c-extra-*`
entries that are the same subtree compared one level out of step).
**goopg's Q3 join subtree is now shape-identical to PG's.** The only
true remaining divergence is `GroupAggregate` vs `HashAggregate` and the
Sort it drags along — i.e. Q3 is now a pure aggregation-strategy case,
which is Slice C / K12 territory.

**Merge-input-sort attribution (SCOPE gate 8).** The SCOPE required
separating merge-sort movement from hash-geometry movement, because
`sortPathFor*` prices a merge input sort from the subpath triple and
with Slice B that subpath can be a join — a join-*method* lever. On this
corpus no merge join moved: Q3's change is hash-geometry
(`Nested Loop → Hash Join`), and no `Merge Join` appears in the diff —
on **either** corpus (`grep -ci "merge join"` over the TPC-DS arm diff
is 0 too). The hazard is therefore **not exercised**, not disproved.

## 4. The census (P5) — and it changes the TPC-DS reading

Measured with temporary scaffolding, since removed.

| | TPC-H | TPC-DS SF0.25 |
|---|---|---|
| rels narrowed | 65 | 590 |
| rels declined | 4 | 176 |
| — (a) collector declined | 4 | **144** |
| — (c) nil `ColVarBytes` | 0 | 32 |
| — (b) empty keep-set / (d) short map / (e) non-table | 0 | 0 |
| joins narrowed | 4257 | 73,349 |
| joins declined, both un-narrowed | 98 | 8,588 |
| joins declined, index-only child (rule 4) | 6 | 0 |
| **joins declined, MIXED PAIR** | **3** | **42,679** |
| **mixed share of all join costings** | **0.07%** | **34.2%** |

**TPC-H's currency is effectively uniform**, so its category movement is
real signal — which is what makes the Q3 result trustworthy.

**TPC-DS's is not**, and the SCOPE pre-registered what to do about it:
*"If that last count is large, P3's category reading is confounded and
the REPORT must say so rather than crediting or blaming narrowing."* It
is large — a third of join costings compare a narrowed input against a
declined one, and the bias is systematic (a decline over-charges, so it
favours whichever side narrowed).

**Stated precisely, because "uninterpretable" would be an overreach.**
What is confounded is the *attribution of a hypothetical signal*: had a
category moved, this census would forbid crediting it to narrowing. What
was actually measured is interpretable and is stated in §5 — Slice B
restructures 3 TPC-DS plans, moves no category, loses no match, regresses
nothing. The correct reading is: **Slice B yields no parity gain on
TPC-DS at the current decline rate, and the census says a third of
comparisons are mixed, so this does not settle what it would do at a
lower one.**

**On WHY rels decline, the census is weaker evidence than it looks.**
The SCOPE blamed `WITH`/subquery leaves via nil `ColVarBytes`; the
counts put 144 of 176 under (a) the collector declining. But the arms
are **order-dependent**: `relNarrowedWidths` tests `scanPathTarget`
(arm a) *before* `ColVarBytes` (arm c), and `neededColumnNames` declines
**per statement, not per relation**, so a query whose `WITH` shape trips
the collector books *every* rel in it under (a) — including exactly the
CTE leaves the SCOPE predicted would book under (c). So the honest
finding is: **the collector arm MASKS the stats arm, and which shapes
trip the collector is unmeasured.** This round cannot say the SCOPE's
causal story was wrong, only that it fires one arm earlier than guessed.
§7's Slice D proposal rests on this, and should re-measure per shape
before committing.

## 5. TPC-DS is NOT inert — what actually moved (review finding B2)

The category table is flat; the plans are not. Section-wise comparison
of this round's own captures:

| queries | change |
|---|---|
| **Q6, Q64, Q75** | **STRUCTURAL** — real shape moves |
| Q95 | cost-only (`Hash Join cost=199292 → 53780`, same shape) |
| Q36, Q70, Q86 | **not a change**: these are the 3 unplannable-on-both queries, and their sections differ ONLY in the psql temp-file path inside the ERROR text |

Q64 reorders the join spine (`income_band ib1` and its `Join Filter`
migrate up a level); Q75 re-costs a `HashSetOp Union` chain; Q6's whole
plan changes. So Slice B is demonstrably **live** on TPC-DS — it simply
converts one non-PG shape into another. A reader of §4 alone would have
concluded the corpus was inert, which is why this section exists.

The Q36/Q70/Q86 artefact is the documented **K18 trap** resurfacing:
`capture-tpcds.sh` names its temp file with `$$`, so two runs embed
different PIDs in the ERROR text of the unplannable queries and diff
spuriously. (A review of this round initially counted those three as
structural changes for exactly that reason.) Worth fixing in the script.

## 6. The hash geometry, MEASURED (SCOPE gate 7)

R121 REPORT §4 asked Slice B to convert its inferred explanation into a
fact, and the first draft of this REPORT silently dropped that gate.
Captured on Q3 with temporary scaffolding (since removed; retained as
`r122-geometry.txt`), budget 134,217,728 B in every line:

| inner build (rows) | OFF | ON |
|---|---|---|
| **141,795** (orders ⋈ customer) | 17 cols, entry **1052 B**, **nbatch=2 → SPILLS** | 6 cols, entry **321 B**, **nbatch=1 → FITS** |
| 3,254,531 (lineitem) | 16 cols, 838 B, nbatch=32 | 4 cols, 216 B, nbatch=8 |
| 1,598,741 | 25 cols, 1344 B, nbatch=32 | 8 cols, 408 B, nbatch=8 |
| 736,853 (orders) | 9 cols, 530 B, nbatch=4 | 4 cols, 216 B, nbatch=2 |

The 141,795-row build **crosses the batch boundary**: entry 1052 → 321 B
takes it from spilling to fitting in memory. That is the mechanism
behind Q3's flip, no longer an inference from a cost delta.

**It also refutes R121's story.** R121 REPORT §4 explained TPC-H's zero
movement as "nothing on this corpus appears to sit near a batch/spill
boundary" and flagged it as inferred. It is wrong: things do sit near
one. R121 saw no movement because its narrowing never reached a join —
a join path published its full un-narrowed width — not because the
corpus was insensitive.

## 7. What follows

The chain is no longer speculative: **A+B yields real parity movement
where the currency is uniform.** The blocker for TPC-DS is now measured
and specific, and it is *not* the one this round expected.

- **Slice D (new, and now the highest-value next step): reduce collector
  declines.** 144 of 176 TPC-DS rel declines are
  `NeededColsKnown == false`. `neededColumnNames`
  (`pathindexonlyneed.go:14-27`) abandons the ENTIRE statement on any
  unenumerable shape. Making it decline *per relation* instead of
  wholesale — or enumerating a few more shapes — would convert a large
  share of those 42,679 mixed comparisons into uniform ones, after
  which TPC-DS's categories become readable for the first time.
- **Slice C (aggregate coordinate)** is unchanged and still holds
  R120's promote-or-delete decision: the aggregate's width comes from
  the built node's schema, unreachable from a Path.
- The merge-input-sort under-charge (goopg's Sort does not project)
  remains **deferred, not discharged** — Slice B widened the population
  it could affect, and no merge join happened to move.

## 8. R121's gate is discharged

R121 shipped under an explicit condition: *"if Slice B does not land in
the next round-cluster, R121 is dead code and gets deleted."* Slice B
landed in the next round and R121 is now load-bearing — Slice B has
nothing to sum without it. The dead-code clock is **cleared**.

## 9. Disposition

**Keep default-off for now**, but this is the first arm with a genuine
parity case for promotion, and the promotion question is now concrete
rather than hypothetical: TPC-H gains two closed categories with zero
regression and byte-identical values, while TPC-DS is unreadable until
the collector declines are reduced. Promoting before Slice D would ship
a change whose effect on 34% of one corpus's comparisons is unmeasured.

**Recommended sequencing:** Slice D (collector declines) → re-take the
TPC-DS census → if the mixed share drops below a few percent, re-read
TPC-DS categories and decide promotion on both corpora at once.

Do not tune constants to chase further movement.

### Is the A+B+C chain worth finishing? (SCOPE §3 required this answer)

The SCOPE pre-registered that a second flat result must trigger this
question explicitly rather than a third deferral. Answering it:

**Yes for TPC-H, on evidence rather than hope.** The chain has now
produced a measured mechanism (§6: a build crossing the batch boundary),
a PG-faithful shape change (§3: Q3's join subtree becomes identical to
PG's), two closed categories, zero regressions and byte-identical
values. That is no longer a speculative programme.

**Not proven for TPC-DS, and the honest ceiling is visible.** Even at a
zero decline rate, narrowing addresses `join-method`/`scan-type`-shaped
divergences. TPC-DS's dominant categories are `join-order` (90) and
`parallelism` (86), neither of which this chain claims to touch. So the
realistic upside there is a subset of `join-method` (62) and `scan-type`
(57), *after* Slice D. Anyone continuing should size the work against
that ceiling, not against the 97 non-matching queries.

**The chain does not reach a single MATCH on its own.** Q3 — the query
it moved furthest — still needs `aggregation-strategy` (Slice C / K12).
That is the arithmetic the roadmap has always predicted: categories fall
one at a time and matches appear only when a query's last one closes.

## 10. Artefacts

`/tmp/pp2-r120/`: `r122off.plans.txt` / `r122on.plans.txt` (TPC-H A/B,
25 diff lines, all Q3), `ds-r122off.plans.txt` / `ds-r122on.plans.txt`
(TPC-DS A/B), `r120.pg.plans.txt` / `pg-tpcds.plans.txt` (PG refs).
Sweep with the arm stamped: `sweep-20260914-031138.txt`.
Hash geometry (§6): `/tmp/pp2-r120/r122-geometry.txt`, copied to the
session scratchpad `r122-evidence/`.

`make plan-gate` not run — **reasoned omission**: default-off flag plus
P0 bit-identity means its structural pins cannot move.

**Capture stamping gap** (carried from R121, still open, and worse than
R121 stated): only the sweep artefact is machine-stamped with its arm.
The TPC-DS captures carry a **hand-typed** header (`# R122 OFF` /
`# R122 ON`); the TPC-H captures carry **no header at all** — they begin
at `=== Q1`, so the arm attribution for this round's headline rests
entirely on filename convention. It cross-checks out (`r122off` is
byte-identical to R121's three OFF captures, and only Q3 differs in
`r122on`), but the gap should be closed: `capture-tpc*.sh` and
`estimate-audit` should both emit the sweep's machine-generated
`planner-flags:` line. Also fix `capture-tpcds.sh`'s `$$` temp filename
(§5, the K18 trap).

**Census and geometry reproducibility** (review note): the census
scaffolding was removed per the SCOPE and its raw output was NOT
retained, so §4's figures exist only as prose. The geometry (§6) IS
retained as `r122-geometry.txt`. A successor re-measuring the census
must rebuild the scaffolding; next round should keep the dump.
