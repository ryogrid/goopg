# 0125-0003 — `GOOPG_RELSIZE_FALLBACK`: block-count relation sizes, and the TPC-H statistics trade-off it re-enters

Status: draft
Date: 2026-07-28
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
