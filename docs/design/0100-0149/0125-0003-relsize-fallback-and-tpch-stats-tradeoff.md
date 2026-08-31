# 0125-0003 — `GOOPG_RELSIZE_FALLBACK`: block-count relation sizes, and the TPC-H statistics trade-off it re-enters

Status: **stages 1–2 landed (flag-off by default); the C-arms and §D8's TPC-DS arm are
MEASURED (§I11–I19) — stage 3, the W arms and the default flip (M0125-0005) remain owed**
Date: 2026-07-28 (stage-1 record 2026-07-29; stage-2, TPC-H C-arm and TPC-DS §D8 arm 2026-07-30)
Milestone: M0125-0003 (§13.5 action 2, phase 6.1; design body §7.1)
Depends on: M0124-0002 (plan baseline). **Independent of M0125-0002** — see the milestone's
"A coupling that was investigated and found NOT to exist".

## Problem

`loadStatisticsFromHeap` (`internal/initdb/open.go:3454` — §7.1's `:3433` is stale) restores
per-column statistics from `pg_statistic` and ends by setting `TableStats{Columns: colStats}`,
leaving `RowCount`, `Pages` and `AvgWidth` zero. `tableRows`
(`internal/planner/cardinality.go:89`) is unconditional:

```go
func tableRows(tbl *catalog.Table) int64 {
    if tbl == nil || tbl.Stats == nil { return 0 }
    return tbl.Stats.RowCount
}
```

Three planner consumers degrade simultaneously after every restart: `EstimateRows` is 0 for
every scan so the MHJ probe side is arbitrary; the bushy DP seeds `rowCounts[i] = 1` for every
relation so join order degenerates toward FROM order; and `estimateBaseRelInfo.baseRows` is 0,
so filtered cardinalities are meaningless.

Confirmed live in round 2: the bench server reported `pg_stats` = 0 rows and `reltuples` = 0
for every table. **That the 16 timeouts are *caused* by this remains the primary hypothesis** —
the ledger row `tpcds-round2 timeouts` labels it exactly that. What is confirmed is the
observation plus the code-derivable consequences; the causal link is what this task tests.

> **One consequence §7.1 lists that this task cannot deliver.** `pg_class.reltuples` rendering
> 0 is real, but it reads `t.Stats.RowCount` directly (`internal/catalog/catalog.go:6946`), not
> `tableRows`. A planner-side fallback cannot change it and must not promise to.

## Design

### D1. The fallback, modelled on the code PG actually runs for tables

`estimate_rel_size` (`postgres/src/backend/optimizer/util/plancat.c`) dispatches
`RELKIND_HAS_TABLE_AM` to `table_relation_estimate_size` → `heapam_estimate_rel_size`
(`postgres/src/backend/access/heap/heapam_handler.c`) → **`table_block_relation_estimate_size`**
(`postgres/src/backend/access/table/tableam.c`). The formula written inline in `plancat.c` is
the `RELKIND_INDEX` branch and must not be copied.

The table path, in order:

1. `if (curpages < 10 && reltuples < 0 && !relhassubclass) curpages = 10;` — the sentinel is
   **`reltuples < 0`, "never vacuumed/analyzed"**, not "zero rows". The `!relhassubclass`
   exclusion carries PG's comment *"Totally empty parent tables are quite common, so we should
   be willing to believe that they are empty."*
2. `if (curpages == 0) { *tuples = 0; return; }`
3. with real stats, `density = reltuples / relpages`;
4. **without** them:
   ```c
   tuple_width  = get_rel_data_width(rel, attr_widths);
   tuple_width += overhead_bytes_per_tuple;          /* do not drop this */
   density = (usable_bytes_per_page * fillfactor / 100) / tuple_width;
   density = clamp_row_est(density);
   ```
   the per-tuple overhead, the fillfactor scaling and the clamp are all load-bearing;
5. `*tuples = rint(density * curpages)`.

goopg implements branch 4 plus rules 1–2 and 5.

**A design gap this exposes, and the trigger it rules out.** goopg has no `-1` "never
analyzed" sentinel — `TableStats` is either absent or carries a non-negative `RowCount`. So
the naive trigger `Stats.RowCount <= 0` also fires on a genuinely **empty, analyzed** table,
where PG would not.

Gating on `Stats == nil || Stats.Columns == nil` is **not** the fix, and the reason is in this
document's own Problem section: `loadStatisticsFromHeap` ends with
`stats := &catalog.TableStats{Columns: colStats}` where `colStats` is a `make(...)` slice —
**non-nil**, with `RowCount` zero. That is exactly the analyze-then-restart state the fallback
exists for, so a `Columns == nil` gate would suppress it there and fire only in the
never-analyzed case. Introduce the explicit sentinel instead, and say so in the code comment.

### D2. Reuse the existing block-count accessor

`planner.ParallelSettings.BlocksForTable` (`internal/planner/parallel.go:74-77`), populated from
`smgr.NBlocks` at `internal/server/dispatch.go:1219` and wired for the extended protocol too.
Its comment already states the rationale: a live O(1) counter read, the same input PG's
`compute_parallel_worker()` gets from `RelationGetNumberOfBlocks()`, which is what lets it work
on a freshly started server.

Thread that same accessor into the cardinality context via a field on the planner context that
`planSelect` populates from `ParallelSettings`. **No package-level variable** — a global makes
the unit tests order-dependent and leaks between the dispatch and extended-protocol wirings,
which are already two siblings that must agree. Do not invent a new block proxy;
`parallelRelationBlocks`'s own comment records what happened last time one was invented.

### D3. The flag, and the invariant that makes the A/B honest

`GOOPG_RELSIZE_FALLBACK`, default **off**, mirroring `costDrivenJoinOrder` (`bushy.go:563`).

> **When `Stats.RowCount > 0` the fallback must not fire.** Flag-on and flag-off must produce
> byte-identical plans in any ANALYZEd state.

`TestTableRowsFallbackDoesNotFireWhenAnalyzed` asserts it. It is also the property that makes
§7.1's original mitigation unexecutable — D4.

### D4. Staged by consumer

A single flag that switches all three consumers at once produces one number and no
attribution. Stage it:

| stage | consumers enabled | expected TPC-H exposure |
|---|---|---|
| **1** | `EstimateRows` / MHJ probe-side choice only | shape-neutral for TPC-H; the cheap, safe first cut |
| **2** | + the bushy DP seed (`rowCounts[i]`) | where round-4's regressions live |
| **3** | + `estimateBaseRelInfo.baseRows` | routes real sizes into filtered-cardinality estimation |

Add `GOOPG_RELSIZE_DEBUG=1` dumping per-relation estimate, DP seed and probe-side choice, so a
TPC-H move is attributable to one consumer rather than to "statistics".

**Stage 3 has no dependency on M0125-0002.** An earlier draft claimed it did, on the grounds
that `estimateBaseRelInfo` calls the unconverted `localizeExprToLeaf` and is dormant only
because `baseRows` is 0. It is not: the function returns on `local == nil`
(`cardinality.go:142`) *before* `baseRows` matters, and `local` is non-nil only when
`shouldAttachBeforeMHJ` opened (`bushy.go:158-169`) — in which case
`attachRelationLocalFilters` already calls that walker on the same predicates. Stage 3 changes
where the walker's output *goes*, not whether it runs.

### D5. The trade-off, and why §7.1's stated mitigation cannot detect it

§7.1's mitigation: "ship behind `GOOPG_RELSIZE_FALLBACK=1` defaulting off; run the full TPC-H
power test **in both flag states** with a per-query table; flip the default only in a separate
commit."

**As specified, that test proves nothing.** Every TPC-H power run in current practice ANALYZEs
all eight tables immediately before the sweep — round 4 §8, round 5 §8 and the round-5 fix
report §1 all record it. In that state `RowCount > 0`, so by D3's invariant the fallback
**cannot fire** and both arms produce identical numbers. A "no difference" result reads as
"safe" when it means "never exercised".

(Not a universal: round-4 §1.2 records that **R1–R3 were deliberately stats-less**, and R3 is
the 1162 s stream in D5.2's own table. So a no-ANALYZE power run has precedent — which is
what makes the C1 arm below a reconstruction rather than an invention.)

#### D5.1 Corrected measurement matrix, per stage

| arm | ANALYZE | flag | what it measures |
|---|---|---|---|
| **C1** | no | off | today's S-cold: `RowCount = 0`, DP seed 1, join order ≈ FROM order |
| **C2** | no | **on** | the change — the only interesting cell |
| **W1** | yes | off | control; the regime every published goopg TPC-H number is in |
| **W2** | yes | on | must equal **W1** exactly — D3's invariant, measured not assumed |

#### D5.2 The prior: goopg has made this transition once, and it was mixed

C1 → C2 is "give the planner real relation sizes". The nearest measured analogue is the
stats-less serial baseline versus the stats-on serial baseline
(`analysis/tpch-evolution-round4-parallel-query-20260722.md` §2/§5):

| Q | stats-less | stats-on | effect |
|---|---:|---:|---|
| Q5 | 415.2 s | 18.2 s | **22.8× faster** |
| Q4 | 3.4 s | ~269 s | **79× slower** |
| Q22 | 0.8 s | ~103 s | **128× slower** |
| Q8 | 3.8 s | ~200 s | **53× slower** |
| Q2 | 2.6 s | ~67 s | **26× slower** |
| Q12 | 27 s | ~121 s | **4.4× slower** |

with the serial stream **1162 → 1307 s**, 12 % slower overall.

**The absolute seconds are stale** — the round-5 fix bundle cut the stream 1086 → 325 s by
removing `runtime.Stack` from the spill path. But that bundle changed no plans and no rows, so
the *sign and order of magnitude* are properties of join orders it did not touch.

**Three honest qualifications.**

1. Round 4's regressions came from a **full ANALYZE** — MCV and histogram *selectivity* as well
   as row counts — while this flag supplies only relation-level row counts against
   `pg_stats` = 0. That is a **third regime nobody has measured**.
2. The R3 → w0 comparison is **not a clean statistics A/B**. Round-4 §1.3 states it "also
   carries a **code-version** and a **warmth** confound" — the arms are 20 commits apart and w0
   ran last and warmest — and certifies only the large moves (Q5, and Q4/Q8/Q22 at 50–100×).
   Q12's 4.4× and, to a lesser degree, Q2's 26× sit closer to that line.
3. The **blast radius for the gated walkers is {Q2, Q5, Q7, Q8, Q9}**, not the round-4
   regression list — see `0125-0002`. Q2 and Q9 appear in both, which is why they are the
   highest-priority cells to watch.

So the sign is well-supported, the magnitude is not entailed, and no quantitative prediction is
made here. Pre-register instead: round-4's five regressed queries **plus Q9** are the named
watch list and Q5 the named expected win, committed to **before** the run.

### D5.3 Risk statement, and why the flip is a separate task

> `GOOPG_RELSIZE_FALLBACK=1` at S-cold plausibly imports round-4's statistics regressions into
> every un-ANALYZEd goopg server — one large TPC-H win against four to five large losses — in
> exchange for relief of a TPC-DS timeout class that is hypothesised but not yet measured.

That is not an argument against building it: §7.1's diagnosis is correct and the timeout class
is 15–16 of 21 defects. It is an argument against flipping the default as a side effect of the
implementation commit. Commit A (this task) is inert; the flip is **M0125-0005**.

Round-5 §6 is the precedent for this discipline: the cost-driven planner produced real 18.8×
and 4.1× wins and still ships off by default, because two of its regressions do not complete.

### D6. Second hazard: the TPC-H correctness gate itself runs S-cold

`scripts/tpch-spotcheck.sh` starts a fresh capped server and runs Q12/Q13 **without** ANALYZE —
verified: the script issues none, and per `CLAUDE.md` it could not, since `ANALYZE <table>` in
database `tpch` errors (ledger `bench-reorg ANALYZE-scope`); the note says "the gate runs
S-cold regardless".

So the gate every planner/executor commit must pass runs in exactly the state this flag
changes — and **Q12 is one of the five regressed cells (27 → 121 s, 4.4×)**. `CLAUDE.md` further
records that heavy TPC-H queries at S-cold need GC headroom: Q21 drew a host-level OOM at
`GOMEMLIMIT=18GiB` and completes at `GOGC=100` + 12 GiB. M0125-0005 must re-measure the
spotcheck's wall clock **and peak RSS** in both flag states.

### D7. Measurement harness — not a plain sweep

Stages 2–3 must use round-5 §6's per-query-isolated harness (one query per process, external
hard cap, server restart between queries, RSS and `oom_kill` monitoring). Round-5 §6 measured a
mis-ordered star query **not honouring cancellation**, with the server pinned at ~10 GB RSS, so
a plain sweep can wedge the host instead of reporting a regression.

**One deliberate divergence from that harness:** round-5 §8 specifies "full server restart
**+ re-ANALYZE** between queries". Drop the re-ANALYZE for the C1/C2 arms — it is exactly what
would push `RowCount > 0` and, by D3's invariant, null the experiment. Keep it for W1/W2.
State this in the report, or a reader following the cited protocol will re-ANALYZE and measure
nothing.

Never measure this flag together with `costDrivenJoinOrder` — round-5 §6 would make the two
indistinguishable.

### D8. TPC-DS instrumentation

The cheap instrument is the SF0.5 gate (~1 h, goopg-only, pinned oracle) at both flag states at
a **single** budget, with M0124-0005's checksums. Expected signals: the TIMEOUT count falls;
**Q72** resolves — §13.3 records it went wrong → slow once RC-1b made its join carry real
volume, and §13.5 action 2 predicts the fallback is what lets the planner order that volume;
**Q35** completes, having been *classified first* by M0124-0004, or "Q35 now passes" is
unfalsifiable.

## Deliverables

1. `internal/planner/cardinality.go` fallback + context plumbing + the flag, staged per D4.
2. Unit tests: D3's invariant; the `table_block_relation_estimate_size` arithmetic including
   fillfactor, `clamp_row_est`, the `curpages < 10 && reltuples < 0 && !relhassubclass` rule and
   the `curpages == 0 ⇒ tuples = 0` rule; the empty-analyzed-table boundary from D1; the
   flag-off no-op.
3. `analysis/tpch-relsize-fallback-<date>.md` — the four-arm matrix **per stage**, the C1→C2
   delta against the pre-registered watch list, and an explicit recommendation on the default.
4. SF0.5 gate results at both flag states, single budget, checksum-verified.

## Out of scope

- **Phase 6.2**, the greedy join-order fallback for `n > 12` (`bushy.go:93`). Not in §13.5;
  review B3 showed it does not fix Q64 alone. Ledger row; reopen after M0125-0005.
- **Persisting `reltuples`/`relpages`** — `pq-P10` option (a). Per that row, one
  analyze-plus-restart round-trips but a second does not and the cause is unestablished. If this
  fallback works, option (a) is less attractive; if not, it is the fallback's fallback.
- `pg_class.reltuples` rendering (D1's note).
- Rendering real `EXPLAIN` costs; needs a trustworthy cost model first, which both round 4 and
  round 5 name as the top follow-up.

## Gate

Units; `make plan-diff LABEL=tpcds-round2-head` — must show **no** diff with the flag off, since
the commit is inert by construction and a diff means the plumbing leaked;
`scripts/tpch-spotcheck.sh`; the SF0.5 gate. The four-arm matrix is the deliverable, not the
gate, but C1/C2 for stage 1 must exist before the milestone is accepted.

---

## Implementation record — stage 1 (2026-07-29)

Stage 1 is landed and **inert**: `GOOPG_RELSIZE_FALLBACK` defaults off, and with it off no
catalog read happens, nothing is stamped, and `EstimateRows` reduces to exactly the
pre-M0125-0003 `tableRows`. Stages 2 and 3, and every arm of §D5.1's matrix, are still owed.

### I1. What landed

| piece | where |
|---|---|
| `TableStats.Analyzed`, the `reltuples < 0` stand-in §D1 asks for | `internal/catalog/catalog.go` |
| `InMemory.SetRelationSizer` / `RelationBlocks` — the live `RelationGetNumberOfBlocks` | `internal/catalog/catalog.go` |
| sizer installed from the buffer pool at startup | `internal/initdb/open.go` |
| `estimateRelSize` — `table_block_relation_estimate_size`, rule for rule | `internal/planner/relsize.go` |
| `clampRowEst` — `clamp_row_est` | `internal/planner/relsize.go` |
| `typeWidth` corrected to `get_typavgwidth` (see I3) | `internal/planner/relsize.go` |
| the staged flag + `SetRelSizeFallbackStage` | `internal/planner/relsize.go` |
| `SeqScan.EstRelRows`, stamped at the FROM-clause scan sites | `internal/planner/plan.go`, `planner.go` |
| the stage-1 consumer, `seqScanRows` | `internal/planner/cardinality.go` |

The flag is a **stage number**, not a boolean: `1`/`2`/`3` (and `on`/`true` → 1) enable every
consumer up to and including that stage, so stages 2 and 3 need no re-spelling of the knob.
Unparseable values are off — an unrecognised value must not silently enable a plan-shape change.

### I2. §D2's plumbing is not implementable as written, and what replaced it

D2 says to thread the accessor "via a field on the planner context that `planSelect` populates
from `ParallelSettings`". **There is no planner context**: `planSelect` is a free function
`(*parser.SelectStmt, catalog.Catalog)`, and `ParallelSettings` reaches the planner only through
`MaybeAddGather`, a post-pass that runs *after* planning from `internal/server/dispatch.go`.
`EstimateRows` is worse — it takes only a `Node`, and the executor's `EXPLAIN` calls it too, so
there is no catalog in scope at the point of use.

D2's prohibition on a package-level variable is nonetheless correct, and for a sharper reason
than it gives: goopg takes **no lock around planning**, so sessions plan concurrently. A global
holding per-plan state is a data race, not merely an order-dependent test. (`planParent`
already is one; that is pre-existing and out of scope here — ledger row appended.)

What landed instead follows PostgreSQL: **resolve the size once and stamp it into the plan**,
the way `get_relation_info` fills `RelOptInfo.pages`/`.tuples` rather than re-reading the smgr
per cost call. `SeqScan.EstRelRows` is that stamp; the catalog owns the block accessor, so the
value is per-relation rather than per-session and nothing is shared mutably.

One consequence to know: a **cached plan carries the block count that was live when it was
planned**. PostgreSQL has the same exposure and answers it with plan invalidation, which goopg
does not have. Ledger row appended; it cannot bite while the flag is off.

### I3. Discovery — `typeWidth` was not `get_typavgwidth`, and the error is multiplicative

The estimate divides a page by the tuple width, so the width **is** the estimate. Checking it
against PG 18.3 (temp tables on the TPC-DS reference instance, so autoanalyze cannot interfere;
all reported `reltuples = -1`) found the pre-existing C2 helper wrong for every bounded varlena:

| declared type | goopg (before) | PostgreSQL 18.3 | why |
|---|---:|---:|---|
| `varchar(20)` | 24 | **58** | typmod counts CHARACTERS: max = 20·4+4 = 84, then 32+(84−32)/2 |
| `varchar(100)` | 104 | **218** | max = 404, then 32+(404−32)/2 |
| `char(10)` | 14 | **44** | max = 10·4+4; for BPCHAR the max **is** the width (blank-padded) |
| `numeric(15,2)` | 16 | **18** | `numeric_maximum_size`: 8 + ⌈(15+6)/4⌉·2 |

Two independent bugs: the encoding factor (`pg_encoding_max_length`, 4 under UTF8 — goopg's only
`server_encoding`) was missing, and so was `get_typavgwidth`'s sliding scale (`≤32` full width,
`<1000` half of max, `≥1000` a fixed 516). On the char/varchar-heavy schemas this fallback exists
to serve — TPC-H and TPC-DS — a 2.4× width error is a 2.4× row-estimate error, i.e. it would have
poisoned the very measurement the milestone is built to take.

Safe to correct now: `typeWidth`'s only live consumer is `nodeTupleWidth` at `bushy.go:685`,
which feeds `dpEntry.pgCost`, read only under `costDrivenJoinOrder` — default off.

With the width fixed, goopg reproduces PG's `EXPLAIN` row estimate **exactly** on all four
measured relations, and `TestEstimateRelSize_MatchesPostgresOracle` pins them:

| relation | blocks | PG `rows=` | goopg | exercises |
|---|---:|---:|---:|---|
| `(int4, bigint, varchar(20))` | 148 | 12284 | 12284 | width branch, integer density 8168/98 = 83 |
| `(int4, varchar(100))` | 207 | 6624 | 6624 | the half-of-max rule at a larger bound |
| same, `WITH (fillfactor=50)` | 299 | 12259 | 12259 | fillfactor scaling; 4084/98 truncates 41.67 → 41 |
| empty, never analyzed | 0 | 830 | 830 | the 10-page floor **before** the `curpages == 0` exit |

The last row is the §D1 boundary made concrete: PG estimates 830 rows for a never-analyzed
empty relation and 1 for the same relation after `ANALYZE`.

### I4. One deliberate divergence from PostgreSQL

PG runs `estimate_rel_size` unconditionally and **scales stored statistics by the live block
count**, so a relation that grew since its last `ANALYZE` gets a proportionally larger estimate.
goopg short-circuits on `RowCount > 0` and does not.

This is §D3's invariant, and it is load-bearing for the milestone rather than an oversight:
flag-on and flag-off must be byte-identical in any ANALYZEd state, or the W1/W2 control arms stop
being a control. Adopting upstream's scaling is a separate decision with its own measurement —
ledger row appended, and it is a natural rider on M0125-0005.

### I5. What stage 1 does NOT do

- No plan-shape movement for TPC-H by construction: the only shape stage 1 reaches is the
  MultiHashJoin probe-side choice. The DP seed (stage 2) and `baseRows` (stage 3) are unwired.
- **No measurement.** §D5.1's four arms, §D7's per-query-isolated harness and §D8's SF0.5
  instrumentation are all still owed, and until C1/C2 exist for stage 1 the milestone is not
  acceptable. Nothing here justifies flipping the default; that remains M0125-0005.
- Plain (non-partition) inheritance parents are not detected as `relhassubclass`, so the 10-page
  floor may be applied to one. Ledger row appended.

## Implementation record — stage 2 (2026-07-30)

Stage 2 wires the second of §D4's three consumers: the **bushy DP seed**. It is the first
consumer that moves the JOIN ORDER rather than a single node's local choice, which is why §D4
predicted "where round-4's regressions live" — and, as measured below, it is not a subtle effect.

### I6. What landed

- `bushySeedRowCounts` (`internal/planner/bushy.go`), extracted from the body of
  `enumerateBushyPlans` so the seed's now-four tiers are readable and unit-testable without
  standing up the whole DP. The tiers, in order: post-filter `relInfos[i].filteredRows`
  (M0077-0002) → ANALYZE'd `Stats.RowCount` → **the stage-2 fallback** → the historical 1-row
  floor.
- `relSizeFallbackRows(stage, cat, tbl)` (`relsize.go`), one gated entry point that every staged
  consumer now goes through. `stage1RelSizeRows` delegates to it unchanged, and stage 3 will need
  no further knob work — §D4's "cumulative by construction" promise, made concrete.
- Three tests in `relsize_fallback_test.go`: stage gating at the seed (stages 0 and 1 must still
  produce the 1-row floor — stage 1's probe-side consumer must not leak into the join order),
  the tier ORDER (post-filter still outranks the fallback at stage 2), and the failure direction
  (no sizer / nil catalog ⇒ the floor, never 0, which would divide into the join-cost model).

### I7. Stage 2 is live, and its effect at S-cold is total — measured

The landing is inert with the flag off and demonstrably not inert with it on. Both arms are
plan-SHAPE comparisons (`make plan-diff LABEL=tpcds-round2-head`, structural mode, TPC-H SF=1 on
65433), so they are valid under the CPU contention that ruled out a timed run this loop:

| arm | result |
|---|---|
| flag off (default) | **22 / 22 MATCH** — byte-identical to the committed baseline |
| `GOOPG_RELSIZE_FALLBACK=2` | **22 / 22 DIFFER** (`analysis/m0125-0003-stage2/plan-diff-stage2-on.txt`) |

Every TPC-H query changes. The reason is visible in the diff: before stage 2, an S-cold server
seeded **`rows=1` for every relation**, so the DP ranked join orders on no cardinality signal at
all. After, it seeds real block-derived sizes. Against SF=1 truth:

| relation | stage-2 estimate | actual SF=1 | ratio |
|---|---|---|---|
| `lineitem` | 2,196,757 | 6,001,215 | 0.37× |
| `orders` | 767,286 | 1,500,000 | 0.51× |
| `partsupp` | 809,690 | 800,000 | 1.01× |
| `part` | 101,100 | 200,000 | 0.51× |
| `supplier` | 7,136 | 10,000 | 0.71× |
| `nation` | 520 | 25 | 20.8× — the 10-page floor, upstream's behavior exactly (§D1 rule 1) |

Coarse, and *right in the ways that matter for ordering*: the estimates preserve the relative
sizes that a join order is chosen on, which a flat 1 destroys entirely. They under-estimate on the
wide tables because `get_typavgwidth`'s 32-byte "wild guess" for unbounded text over-states the
average width, and width divides into density — the same direction PG errs in, for the same
reason. `nation` shows the floor doing its job: refusing to believe a small relation is tiny.

Q9 is the clearest single case — with real sizes the planner reaches `Gather` / `Workers
Planned: 4` over the 4-table MHJ, which it could not justify when every input claimed one row.
Whether that is a *win* is precisely the open question, and it is **not** answered here.

### I8. Ordering consequence: stage 3 will shadow stage 2 at this site

Stage 3 feeds the fallback into `estimateBaseRelInfo.baseRows`, which makes `filteredRows`
positive on a cold server — so tier 1 becomes live and the stage-2 tier stops being reached at
this site. That is the correct outcome (stage 3's number has passed through the local filter and
is strictly better here), but it has a scheduling consequence worth stating plainly: **stage 2's
arm of the four-arm measurement must be read before stage 3 lands, not after.** Recorded in the
seed's own doc comment so it cannot be lost.

### I9. Discovery — a fourth, unstaged consumer that stays blind cold

`reorderCommaFromByCardinality` (`internal/planner/joinorder.go:89-93`) is the greedy comma-FROM
reorder, and it is a *sibling* of the DP seed: same question, different code path (hard-won rule
#2). It bails out entirely — "without a row count for every relation, the reorder has no signal"
— when any table lacks `Stats.RowCount`, so on a cold-started server it never runs at all.

Stage 2 therefore does not remove cold-start blindness from the planner; it removes it from the
bushy DP only. This is left deliberately out of scope — it is a fourth consumer, §D4 staged
three, and adding it silently would make the measurement un-attributable, which is the exact
failure §D4 exists to prevent. Ledger row appended.

### I10. What stage 2 does NOT do

- **Still no measurement.** §D5.1's four arms and §D7's per-query-isolated harness remain owed;
  the nightly CI batch held the host all loop, and a plan-shape diff is the strongest evidence
  available under contention. Nothing here justifies flipping the default — that is M0125-0005.
- The 22/22 divergence is a statement about plan *shape*, not about plan *quality*. Round-4's
  five regressed queries are the pre-registered watch list and none of them has been timed.

## Measurement record — the C-arms (2026-07-30)

Full report: [`analysis/tpch-relsize-fallback-20260730.md`](../../../analysis/tpch-relsize-fallback-20260730.md).
Raw per-query TSVs with D4a provenance: `analysis/tpch-relsize-fallback-20260730/`.

### I11. §D7's harness exists now — `scripts/tpch-relsize-arm.sh`

§D7 specified a per-query-isolated harness and no such thing existed: the only TPC-H
driver in the repo (`bench/tpch/run_power_test_goopg.sh`) drives HammerDB, which runs all
22 queries in ONE session against ONE long-lived server. That shape cannot express this
experiment for a reason beyond the memory-thrash argument §D7 gives:
**`GOOPG_RELSIZE_FALLBACK` is read once, in the planner package's `init()`, from the
SERVER's environment** (`relsize.go:40`). There is no GUC, so the arm can only be
selected at server start — per-query restart is what makes "arm" a well-defined thing,
not merely a hygiene measure.

The script takes `c1|c2|w1|w2|probe-analyze`, and it keeps §D7's one deliberate
divergence (no re-ANALYZE for the C arms) as the default rather than as a comment.

### I12. The C1 → C2 result, and the pre-registration it refutes

21 comparable queries: **693.8 s → 494.0 s (−28.8 %, 1.40×). Four wins — Q9 3.29×,
Q12 3.43×, Q10 2.58×, Q7 1.32× — and ZERO regressions**, largest adverse move Q14 at
1.08× (0.43 s) inside the harness's own 1.02–1.04× noise band. Row counts identical in
both arms on every completing query.

**§D5.2's pre-registration was wrong in both directions.** None of round-4's five
regressed queries regressed; **Q12, round-4's 4.4× loss, is the second-largest win**;
and **Q5, the named expected win, did not move** (0.99×) because M0077 had already
fixed it — this cluster runs Q5 at 66.7 s cold, not round 4's 415.2 s. Q9 was the one
correct call.

§D5.2's qualification 1 is therefore **confirmed as the operative fact**: round 4
supplied full ANALYZE (selectivity *and* sizes) while this flag supplies sizes only, and
the measured shape of that third regime is *sizes alone are monotonically helpful here;
selectivity is what wrecked those five queries*. §D5.3's risk statement is **refuted for
stage 2 on this workload** — which is a statement about TPC-H SF=1 at S-cold on this
code, and is NOT transferable to stage 3 (see §I8: stage 3 shadows this tier).

### I13. Q21 fails in BOTH arms, and it reproduces round-5 §6's non-cancelling defect

Q21 TIMEOUTs at a 300 s cap and again at a 600 s cap in **both** arms (re-run at 600 s
in each so the table is symmetric), at 14.2–14.8 GB VmHWM. It is not caused by the flag.
Two findings worth more than the cell:

- **It does not honour cancellation** — 672 s of wall clock against a 300 s runner
  budget, ended by the external clamp, then needing SIGKILL. Round-5 §6 measured this on
  the *cost-driven* planner; here it is the **default integer DP at S-cold**, so the
  defect class is broader than the experimental planner. Ledger row.
- It sits ~0.2–0.8 GB under the harness's `GOOPG_MEM_MAX=15G`, consistent with
  `CLAUDE.md`'s record of Q21 drawing a host-level OOM at `GOMEMLIMIT=18GiB`.

### I14. §D5.1's W arms are unconstructible today — measured, not assumed

`probe-analyze` measured both blockers on this cluster: `ANALYZE lineitem` in database
`tpch` errors *relation does not exist* (ledger `bench-reorg ANALYZE-scope`), so
`RowCount` cannot be raised above 0 at all; and goopg's stats are per-connection, so
even after that fix the ANALYZE must be issued in the query's own session, which
`cmd/tpch-runner` cannot do. **§D3's W1 = W2 invariant therefore remains unmeasured** and
rests on `relSizeFallbackRows`'s early return plus its unit test. The harness refuses the
w-arms with that text rather than emitting a duplicate C-arm under a W label. Ledger row
carries the resume point (`cmd/tpch-runner -analyze`).

### I15. What is still owed after this arm

- **Stage 3** (`estimateBaseRelInfo.baseRows`) — unimplemented, and its arms must be run
  fresh; §I8's shadowing means this arm says nothing about it.
- ~~**§D8's TPC-DS instrument**~~ — **RUN 2026-07-30; see §I16.**
- **W1/W2**, per §I14.
- The **absolute seconds** carry this harness's memory configuration
  (`GOMEMLIMIT=12GiB`, `MEM_HIGH=13G`, `MEM_MAX=15G`); nine queries exceeded `MEM_HIGH`,
  so the heaviest cells include kernel throttle-band time. The A/B is unaffected — one
  binary, one configuration, same-age server per query — but cross-report seconds
  comparisons are not licensed. Ledger row.

---

## Measurement record — §D8's TPC-DS arm (2026-07-30)

Full report: [`analysis/m0125-0003-sf05-relsize-20260730/README.md`](../../../analysis/m0125-0003-sf05-relsize-20260730/README.md).
Merged 99-query board: `…/sweep-COMPLETE-20260730-155432.txt`; four chunk files and
their drivers alongside it; the Q72 second-budget probe in `…/q72-probe/`.

### I16. The acceptance measurement: the timeout class shrinks 16 → 13, and no answer changes

```
flag ON   PASS=82 (50 ck) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=13 SKIP=4
flag OFF  PASS=79 (49 ck) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=16 SKIP=4
```

Five of 99 statuses move: **Q10 `TIMEOUT`→`PASS 40s`, Q69 →`PASS 17s`, Q67 →`PASS 157s`,
Q47 →`PASS 277s`**, and **Q72 `PASS 276s`→`TIMEOUT 307s`**. All 78 queries that pass in
both arms agree on row count **and** value checksum, so a change that re-orders joins
across the whole suite produced zero answer changes — the statement M0124-0005's checksum
column exists to make. Common-PASS wall time `2273 s → 1845 s` (**−18.8 %**); 27 of the 28
queries that move by ≥5 s or ≥1.25× are faster (Q43 11×, Q52 8×, Q40 6.3×, Q88 2.11×), one
is slower (Q21 1.74×).

The off arm was **reused, not re-run**: `git diff e29faca9..HEAD -- '*.go'` is empty and
both reports carry the same D4a `engine-id` with the empty-diff digest. That is the only
form in which an A/B may skip an arm, and it is checkable from the artefacts alone.

### I17. §D8's two predictions are both refuted — and one is refuted backwards

§D8 pre-registered "the TIMEOUT count falls; **Q72** resolves; **Q35** completes". Only the
first happened.

- **Q72 was already passing** at 276 s, so §13.3's "wrong → slow" premise the prediction
  rested on was stale; the flag made it **1.13× slower** and pushed it over the cap.
- **Q35 still times out at 300 s with the flag on.** M0124-0004 had classified it first
  (performance-only, RC-8 shape), so the claim was falsifiable — and it is false. Q35 is
  M0125-0003's acceptance query and **the relation-size fallback is not what it was waiting
  for**; its nine-day arithmetic floor is a per-`EXISTS` re-scan cost that a better relation
  size does not touch.

Taken with §I12 — where round 4's five predicted regressions did not regress and the
predicted win did not move — two independent studies now say this planner's per-query
effects are **not** predictable from prior rounds' tables. Pre-registration remains worth
doing (it is what makes these refutations sayable), but a prediction from an earlier round
is not evidence about a later one.

### I18. `TIMEOUT` is a budget statement, and Q72 shows why it must be read as one

Q72's `PASS → TIMEOUT` looks like a cliff. Re-run standalone at a **900 s** budget, both
arms, same binary and a fresh S-cold server: off `PASS 270 s`, on `PASS 305 s`, 100 rows
each. The flag costs ≈35 s (1.13×) on a query that sits ~10 % under the cap without it, so
the status change is a **budget crossing of a marginal query**, not a new unbounded plan.
This is design 0124-0001 §D6's budget-marginal class, and the general rule it implies is
that the gate's TIMEOUT column may never be read as "unbounded" without a second budget.

The 1.13× is real and is charged to M0125-0005's flip rather than waived. It also makes Q72
the most informative single query for `M0125-0026`: the only member of the suite that shows
the fallback's *downside* on TPC-DS, so its two plans are worth capturing side by side.

### I19. Consequences for the tasks downstream

- **M0125-0005 (the default flip) is now evidence-backed and recommended.** Two independent
  benchmark families agree in sign and shape. Its commit must name Q72's 1.13× as a known
  cost, and must not fold in stage 3 — §I8's shadowing would make this arm unattributable.
- **`M0125-0026`'s capture list is the 13-query flag-on set** — Q5 Q8 Q14 Q30 Q31 Q35 Q54
  Q64 Q65 Q71 Q72 Q78 Q81 — not the 16 written against the off arm. Q10/Q47/Q67/Q69 are
  answered and need no root-cause class.
- **The gate artefact now states its arm.** `scripts/tpcds-sf05-regression.sh` prints
  `# planner-flags: …` on every sweep, every flag printed even when unset, so "off" is a
  positive statement in the file. Before this, two arms of the same commit were
  indistinguishable on their face and the comparison rested on the operator's memory of
  what was exported — the same class of hole D4a closed for the *engine* identity.
- Still owed: **stage 3**, the **W arms** (§I14), and an SF=1 reading for nothing in
  particular — Q35, the one query SF=1 would be run for, is unmeasurable there
  (M0124-0004: ≈9.1-day floor).

## Closing record — the fourth consumer, and what is left owed (2026-08-06)

This closes M0125-0003. §I19's three outstanding items are discharged, one by measurement,
one by supersession, and one by the change recorded here.

### I20. The fourth consumer (§I9) is landed — as a tier at stage 2, not as a stage of its own

`reorderCommaFromByCardinality` (`internal/planner/joinorder.go`) no longer bails on
`Stats.RowCount <= 0`. That guard is now a ladder — stored count, then
`relSizeFallbackRows(2, cat, tbl)`, then decline — which is the same estimate, through the same
single gated entry point, that `bushySeedRowCounts` reads at the DP seed.

**Why it does not get a fourth stage, contradicting §I9's own reasoning.** §D4 stages *by
consumer* so that a regression is attributable to one consumer; §I9 concluded that adding this
one silently would break that. Both were right at the time and both are now moot, because
M0127-P5.6 retired stage 3 (`applyRelSizeFallback`, `relsize.go` — the search seam reads a base
relation's cardinality exactly once, so the second consumer stage 3 existed to sequence no
longer exists) and the flag's `3` became a documented alias for stage-2 behaviour. What is left
is a two-valued flag whose stage 2 is *defined* as "the consumers that move the JOIN ORDER".
This consumer moves the join order and nothing else, so it belongs to that stage by the
staging's own definition; giving it a `4` would mean shipping it default-off behind a value no
script sets, which is not attribution, only concealment.

The tier order matters and is pinned: an ANALYZEd relation is still ordered by its stored
count in both flag states. That is §D3's invariant, and it has to hold at every consumer, not
only at the one the W arms measured.

### I21. The measured effect is ZERO plan movement — which is the safety evidence, not a null result

TPC-DS SF0.5 plan-shape channel, 99 queries, against the previous capture: **`queries=99
same=99 changed=0`** (`plans-20260806-191105.txt`). The gate's goopg cluster is restarted for
the capture, so every relation is in exactly the S-cold state this change is about — the pass
now *runs* on those lists where it previously declined, and the final plans are byte-identical
anyway.

The mechanism is M0127-P5.9-r's boundary, read in the other direction: `extractScans` descends
`JoinTypeCross` only, so a **comma-FROM** list is precisely the shape that *does* reach
`tryPGShapedJoinSearch`. On everything the new search accepts, the search re-derives the join
order from the whole relset and the parser-level permutation cannot survive into the plan.
So the live consumers of this pass are now the cases where the search declines: an
explicit-`JOIN` FROM clause (which never reaches it), a relset over the search's limit, and any
leaf shape the seam's whitelist rejects. After M0127-P6.3 demotes `joinorder.go` to the
over-limit sequencer, that residue is the *only* join-order chooser those queries get, and it
would have been the blind one.

Two consequences worth stating plainly: the TPC-DS sweep was not re-run, because identical plan
text for all 99 queries means identical execution and the plan channel exists exactly to license
that; and no performance claim is made for this change on either benchmark family.

### I22. §I19's other two items

- **The W arms are MEASURED, not owed** — resolved 2026-07-30 by M0125-0031's first motion
  (`analysis/m0125-0031-warm-tpch-20260730.md`): warm stream 413.3 s (w1, flag off) vs 420.1 s
  (w2, stage 2), with `plan-diff` 22/22 MATCH in both `structural` and `strict-text` mode. §D3's
  "flag-on == flag-off when ANALYZEd" invariant is confirmed byte-for-byte rather than resting on
  `relSizeFallbackRows`' early return. Both blockers §I14 measured were removed as that row
  predicted: M0125-0028 made `ANALYZE lineitem` resolve in database `tpch`, and M0125-0029 made
  its output durable and cross-connection, so the harness's one-time warm-up pass replaced the
  planned `cmd/tpch-runner -analyze` flag (retired unimplemented — nothing needs it).
- **Stage 3 is superseded, not deferred** — M0127-P5.6's roll-up (`leftdeep-joins` 04 §2.1)
  re-derived its placement as `applyRelSizeFallback` at the search seam, where the block-derived
  count is `estimate_rel_size`'s pre-filter `tuples` and `set_baserel_size_estimates`'
  `clauselist_selectivity` scales it. §I8's shadowing hazard died with the second consumer.

### I23. What this still does NOT do

`estimateRelSize` remains blind to plain (non-partition) inheritance parents — `hasSubclass` is
`len(tbl.PartitionKey) > 0`, so an inheritance parent with children can take upstream's 10-page
floor when it should not (I4's divergence, ledger). And nothing here touches
`pg_class.reltuples`, which reads `Stats.RowCount` directly and still reports 0 after a restart:
the fallback is a planner-internal estimate, exactly as `estimate_rel_size` is upstream.
