# M0140-0005 — Q14's third category (K92) and the non-planner floor (K14/K15/K41): ledger filing

**Status:** accepted
**Milestone:** M0140 (TPC-DS parallelism), plan-parity group
**Harness:** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Task:** `.ralph/fix_plan.md` M0140-0005
**Outcome:** filing only, no production change. Two ledger rows added to
`.ralph/deferral_ledger.md`: `m0140-0005-q14-parallel-hash-execution-model`
and `m0140-0005-nonplanner-heap-density-floor`. M0140 is now fully closed
(all five tasks `[x]`).

## The task as filed

> M0140-0005 — file the two out-of-reach items as ledger rows — Q14's third
> category (K92: needs PG's real partial-inner execution model, "NOT cheap")
> and the non-planner floor (K14/K15 heap density; K41's unexplained
> dimension-table `relpages` divergence, `customer` 1,979 vs 2,872 and `item`
> 716 vs 1,284). Each row names the mechanism and what would unblock it;
> neither is silently dropped.

Both items were already fully investigated and written up in the prior
phase's record (`docs/design/not_ralph/plan_parity_fix_take2/`); this task is
filing, not investigation. The two sub-items below summarize what that record
already established, cite it, and record the ledger row.

## Item 1 — K92: Q14's third category needs PG's real Parallel Hash execution model

**Where it's carried:** `TODO.md:2509-2540` (K92, "R43's TRUTHFUL verdict
NARROWED"), `METHODOLOGY3/01-what-we-learned.md:399` and
`METHODOLOGY3/02-open-problems.md:171-172`.

**Finding (already established, re-confirmed here from the same sources):**
after R44, TPC-H Q14 differs from PG on the `parallelism` category alone —
the cheapest-looking remaining mismatch on the TPC-H corpus. But closing it
is not a labelling fix. `Parallel Hash` in PG's `EXPLAIN` output asserts a
specific execution model: **the build (inner) relation is scanned as a
partial path, consumed by the Gather's own worker set, into a shared DSM hash
table, behind a barrier** (`postgres/src/backend/executor/nodeHash.c`'s
parallel build path). goopg's `joinOp` does something different by design —
`parallel_scan.go`'s own comment states it plainly:

> "P8. Only PROBE side partial: build side drained once by leader before
> fan-out. Attaching allocator build side instead would give each worker
> PARTITION build input, every worker's hash table missing most rows, join
> would silently drop matches."

So the comparison is:

| | who scans the build relation | when |
|---|---|---|
| PG `Parallel Hash` | the Gather's workers, into shared DSM | during the join, behind a barrier |
| goopg | the leader's producer goroutines | in `gatherOp.Open`, before fan-out |

Both parallelize the build scan (K16's `parallel_hash_build.go` cooperative
build), so goopg is not missing the *capability* — but stamping `Parallel
Seq Scan on part` inside Q14's `Gather` subtree today would claim the
Gather's workers each read a partition of `part`, the exact arrangement
`joinpathsparallel.go`'s executor-side assertion calls out as silently
dropping matches. Doing that would be a misdescription — the arbitrary
plan-forcing the plan-parity goal explicitly forbids (`AGENT.md`
§"Plan-parity harness", the goal is "the same plan as PG… never by forcing
shapes").

**What would unblock it:** implementing PG's actual execution model —
workers building a shared hash table from a partial inner path, not
relabelling the leader-prebuild model. This is new executor machinery (a
DSM-equivalent shared build target workers can safely write into
concurrently, plus a barrier so probing waits for the build to finish), not a
planner change. `TODO.md:2540` states the conclusion this row carries
forward verbatim: *"R43 §6 step 2 must not be implemented as a labelling
change; DESIGN rev 4 §4a records this."*

**Ledger row:** `m0140-0005-q14-parallel-hash-execution-model`.

## Item 2 — K14/K15/K41: the non-planner heap-density floor

**Where it's carried:** `TODO.md:149-190` (K14, K15), `TODO.md:1405-1408`
(K41), `METHODOLOGY3/02-open-problems.md:174-183` (the "non-planner floor"
framing this task's title quotes), `r4-heap-density/FINDINGS.md` (K14
detail).

**Finding (already established):** `relpages` is a planner **input** — it
feeds every page-priced cost term, the Mackert-Lohman estimate, and PG's
`compute_parallel_worker` band thresholds — and goopg's on-disk heap does not
reproduce PG's page count for identical row counts. K14's original
measurement (`store_sales`: identical `reltuples` 1,439,608, `relpages`
29,761 goopg vs 25,928 PG, a ~15% over-count) was root-caused by R5 into two
separate, opposite-direction effects:

- **`character(N)` blank-padding** (confirmed divergent — PG pads fixed-width
  `bpchar` storage to the declared width, goopg does not) shrinks goopg's
  page count on `character(N)`-heavy tables: `item` 0.573x, `customer`
  0.712x of PG's page count.
- **Heap page fill on bulk load** (`numeric` storage was checked and
  falsified as a cause — it matches PG exactly at one and five columns, with
  and without fractional digits, and with NULLs) inflates goopg's page count
  on fact tables by ~15%, later quantified as ~21.9 bytes/row of free space
  PG does not leave (`TODO.md:915-918`, filed there as `R22`).

K39 (`TODO.md:1395-1397`) already **closed** the fact-table direction:
`store_sales` 29,761 → 25,866 pages vs PG's 25,928 (0.4% off), and the
4-worker-plan divergence it caused went 19→0, matching PG (which plans none).
But fixing it **did not move plan parity** (K39a) — every category held
except `qual-placement` 13→12 — which the record reads as: the remaining
divergence is in cost computation and planning logic, not primarily in the
statistics fed to them, for the queries K39 touched.

**K41 is the open half this row exists to carry forward**: dimension tables
diverge the *other* direction and remain **unexplained** — `customer` 1,979
pages (goopg) vs 2,872 (PG), `item` 716 vs 1,284. This is the opposite sign
from the fact-table fill gap K39 closed, so it is not the same mechanism;
`TODO.md:1405-1408` records it as newly surfaced (previously masked while the
fact-table over-count dominated) and does not name a root cause. The
character(N) blank-padding fix moves in the *same* direction as this
divergence would need — goopg presently under-pads relative to PG, which
should make goopg's character(N)-bearing tables (both `item` and `customer`
carry wide `varchar`/`character` columns) *smaller* than PG, which is exactly
what's observed — but R5 already treated blank-padding as accounted for
separately (it explained the pre-K39 15% *fact*-table gap alongside the fill
effect, not this dimension-table gap on its own), so whether blank-padding
alone or a still-different mechanism explains K41's magnitude is unmeasured.

Two concrete open on-disk items sit under this floor, already filed but easy
to lose (`METHODOLOGY3/02-open-problems.md:180-183`):

- **R22 — heap page fill on bulk load** (`TODO.md:915-918`, the fact-table
  remainder): goopg leaves ~21.9 bytes/row of free space PG does not
  (~15% on `store_sales`, pre-K39-fix baseline; K39 already closed the
  aggregate number, but the per-page fill mechanism itself — comparing free
  space per page directly on both engines rather than inferring from
  totals — was never done and is the resume point if the fill gap recurs on
  a table K39's fix didn't touch).
- **R23 — `character(N)` blank-padding** (`TODO.md:919-921`, R5 §2.1): an
  on-disk PG-compat defect in its own right (independent of plan parity —
  `SELECT` output for a blank-padded `bpchar` column can differ from PG's),
  which shifts `relpages` on every `bpchar` table and is the leading
  candidate mechanism for K41.

**What would unblock it:** direct per-page free-space comparison between
goopg's and PG's heap files for `customer`/`item` (not inferred from
page-count totals, per R22's own stated method) to confirm or refute
blank-padding as K41's mechanism, then either implement `character(N)`
blank-padding (R23, a real on-disk PG-compat fix, not planner work) or find
the actual cause if padding doesn't account for the full 1,979-vs-2,872 gap.
Either way this is on-disk representation work, not planner/cost-model work
— no planner change can close a gap in the statistics fed to it.

**Ledger row:** `m0140-0005-nonplanner-heap-density-floor`.

## Disposition

Both items are filed as `.ralph/deferral_ledger.md` rows per the milestone
DoD's "or its absence is a filed ledger row with a resume point" clause —
the same disposition M0140-0004 and M0137-0012 used. `.ralph/fix_plan.md`
M0140-0005 is checked off on that basis; no production code changed, no test
pins moved. **M0140 is now fully closed** (M0140-0001 through -0005 all
`[x]`) — per the banner, the next selection is M0141-S0 (scoping recon) or
M0143 (gated on nothing), whichever the banner's ordering favors at the next
loop.
