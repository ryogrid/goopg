# Handover — planner_refactor_take2

For the agent picking this up. Written to be read in full; everything else is
referenced, not restated. Branch `planner-refac-bigbang`. P4-01 steps 1-2 are `aab14de3e` and `8dc298e92`.

**Read in this order, and stop when you have what you need:**

1. this file
2. `TODO.md` — the item list, with per-item status and measurements
3. `impl/P4-A-pathtarget.md` **§13–§18** (revs 5–10) — the active work
4. the `impl/FINDING-*.md` you are pointed at, only when pointed at

---

## 1. Start here: the immediate next action

**P4-01 rev 10 step 3.** The plan is `impl/P4-A-pathtarget.md` §18, written at
the level of files and functions. Steps 1 and 2 are committed (`aab14de3e`,
`8dc298e92`); step 3 was drafted, reverted unapplied, and is yours.

Step 3 in one paragraph: in `joinInputsFor` (`internal/optimizer/createplanjoin.go`),
immediately after `innerNode, innerLay := createPlanNode(innerPath)` and BEFORE
the layout/schema panic four lines below, narrow the pair for `kind ==
"PathHashJoin"` using `narrowPlanOutput` + `neededKeepSet`
(`internal/optimizer/narrowoutput.go`, both already committed and unit-tested),
reading the set from `innerPath.Rel.NeededCols` / `.NeededColsKnown`. Put it
behind `GOOPG_NARROW_BUILD=1`, **default off**. Then §18 steps 4–5: measure,
and flip the default only if clean.

Why the pair and not the node, why a `Project` and not a narrowed scan, and why
`PathHashJoin` only: §15 (rev 7) and §18 (rev 10). **Read §16 (rev 8) for its
CONSTRAINT only** — its prescription ("the child's own `createPlan` arm") is
superseded by §18 and is now banner-marked as such; taking the insertion point
from §16 hands you the reverted design.

`joinInputsFor` is shared with `createMergeJoinPlan`, so the `kind ==
"PathHashJoin"` guard is not optional — without it you narrow merge-join inputs
too, for no memory saving and a real projection cost.

---

## 2. Non-negotiables

**Gate on VALUES, never on row counts.** Three of the five correctness bugs
found this session returned the *correct row count* while computing the wrong
answer. TPC-H Q9 returned its correct 175 rows while summing 4.02× the right
value (`impl/FINDING-CRITICAL-mergejoin-wrong-answers.md`).
`cmd/tpch-runner -digest` + `-diff` is the gate. `bench/tpch/spotcheck_expected.env`
and `ci/batch/tpch-row-anchors.csv` compare row counts and are **structurally
blind** to this class.

**Run the gate at BOTH `work_mem` budgets.** P4-01 exists to change plan shapes
at the *small* budget; gating only at the shipped 512 MB default validates it
under the plans it does not affect. The default is not a safeguard, it is
camouflage — that is how the merge-join bug survived.

**Diff against the PG oracle, not a goopg baseline.** Both merge-join bugs were
invisible goopg-vs-goopg.

**Three guards exist — use them.** The value digest; the plan-time layout panic
at `createplanjoin.go:289` (pre-existing); and `GOOPG_ASSERT_ROW_SHAPE=1`
(`internal/executor/rowshape_assert.go`). P4-01b had none of them; that is the
difference between this attempt and the one that returned wrong answers while
looking 3.6 % faster.

> **`GOOPG_ASSERT_ROW_SHAPE` and `GOOPG_NARROW_BUILD` are read once at process
> init, inside the SERVER.** Putting either on the `tpch-runner` command line
> sets it on the client, where nothing reads it: the assertion is silently off
> and the run reports clean. Export them in the environment of
> `scripts/goopg-test-run.sh … start`, and RESTART the server to change either.
> A failure surfaces as a **panic in a server backend** — look in the cluster
> log, not in the runner's output.
>
> Coverage is `seqScanOp` and `indexScanOp`. **`bitmapHeapScanOp` is NOT
> instrumented** (`internal/executor/operators_bitmap.go`, the
> `MaterializedSlot{schema: o.plan.Output(), row: row}` sites), and bitmap paths
> do win on this corpus — goopg picks 9 against PG's 6. Add the same
> `assertRowShapeInline` call there before trusting a clean run.

Repo-wide rules that bit this session: `CLAUDE.md` (never `pkill -f goopg`;
memory-capped server via `scripts/goopg-test-run.sh`; `--listen` not `-p`;
never `gofmt -w`, the repo baseline is go1.25 and a newer local gofmt rewrites
unrelated lines; stage by explicit pathspec, a concurrent Ralph loop's WIP is
usually present).

---

## 3. Measurement traps, all paid for once

- **The TPC-DS gate could not see a broad shallow regression.** Its runtime arm
  is per-query at 2×; a change moving 60 plans and slowing ten queries 3–5 s
  each reports `runtime-moves=0`. A `TOTAL` aggregate arm was added to
  `scripts/tpcds-sweep-diff.py` and validated against three real sweep pairs.
- **A per-query runtime move is only attributable if that query's PLAN changed.**
  The gate names queries whose plans are byte-identical. For P3-12 the split was
  −41 s on the 64 changed plans against +7 s on the 35 unchanged. Check the plan
  capture beside the report before believing any per-query figure.
- **The TPC-H variance band widened to ±3 %.** P3-10 looked like +2.8 % until an
  A/A control showed the *unchanged* baseline moving 213.84 → 221.01 s. Re-run
  the baseline before believing a TPC-H delta of that size.
- **The sweep diffs against the most recent report**, which may be a run whose
  code you reverted. That flattered one change to −4.3 % when the honest figure
  was −1.2 %. Check which report the delta used.

**One thing nobody has argued, and you should before step 3.** The keep-rule is
safe *for the parse tree*: `neededColumnNames` walks the whole statement and
declines wholesale on the shapes it cannot handle. But three passes run on the
plan AFTER that set was collected — `rewriteScanInputsWithSingleTablePredicates`
and `rewriteJoinsToNLI`, which §4 below says are load-bearing and still running,
and `reconcileNLILayout`'s unconditional tripwire
(`searchedtree.go:200`, fired from `createplanroot.go:137`). Whether a build side
those passes later touch is in scope for narrowing is an open question, not a
settled one. Decide it explicitly; do not discover it.

---

## 4. Do NOT do these

P2-08, P2-10, P6-03, P6-04 and P6-05 are marked `[-]` in `TODO.md` with their
measurements; the two P2-09 rows live under a `[~]` item. They look like
cleanups and are not:

| item | why not |
|---|---|
| P6-03 delete `rewriteScanInputsWithSingleTablePredicates` | load-bearing — Q20 6.5×; a correlated `Index Cond` becomes `Filter: (true)` |
| P6-04 delete `rewriteJoinsToNLI` | load-bearing — Q4 12.5×; it supplies the NLI shapes the search does not win (P3-11) |
| P6-05 delete "dead" `reconcileNLILayout` | **not dead** — it is the oracle for a live wrong-column tripwire that runs on every searched plan |
| P2-08 `cost_subplan`, P2-10 semi/anti factors, P2-09 `num_sa_scans` | no consumer exists; blocked on Phase 3/4 |
| P2-09 per-tuple index qual cost | faithful, symmetric, not double-charged — and still **+3.3 % SLOWER** on TPC-DS. Land with the rest of `btcostestimate`, not alone |

The pattern: **Phase 6's deletions assume the search has superseded the legacy
passes, and it has not.** Phase 6 is gated on Phases 3–5 far more tightly than
the bundle's ordering suggests.

---

## 5. Scope corrections you must not re-inherit

- **P4-01 does NOT unblock P2-02b.** Narrowing takes Q9's build from 128 batches
  to 64 at PG's `work_mem`, not to 1, because `EntryBytes = ncols × 48 + 24`
  makes per-column footprint co-dominant with column count. The 48-byte Datum
  (07 §6, currently "out of scope") is P4-01's partner, not a downstream
  residual. `impl/FINDING-p401-alone-is-not-enough.md`.
  Read that finding's **Correction** paragraph before acting on it: the fix is
  PG's slot duality — retained rows packed, only the row in flight deformed into
  a per-slot scratch array — and NOT a hash-table patch. `[]Row` is retained by
  sort, materialize and the CTE caches too, not only the hash join. goopg has
  both halves already: the packed encoding (`appendRowPayload` /
  `spillReader.ReadRowInto`, used on the spill path) and the seam
  (`TupleSlot.Row()`'s "for a future `VirtualSlot` this materializes lazily").
  Doing the hash join alone is a MEASUREMENT of the encode/decode cost, not the
  design; stopping there leaves two retention formats and the sibling-path
  hazard that implies.
- **P2-02b's remaining +23.1 % is 87 % width / 13 % Gather**, measured.
  `impl/MEASUREMENT-p202b-width-vs-gather.md`.
- **P3-01's hazard:** `min = syn` is the SAFE direction. An *under*estimated
  `min_righthand` permits a reordering PG forbids — a wrong-answer class. A
  partial resolver must fall back to `syn` on ANY uncertainty. The blocker is
  that `deconstructFromItem` has no catalog/resolver and `parser.ColumnRef`
  carries names, not relation indices.

---

## 6. State of the tree

`TODO.md` is authoritative — trust it over this summary, which is deliberately
coarse. Phase 2: P2-02b, P2-05, P2-09 and P2-11 are open or partial (`[~]` means
part landed; the entry says which part). Phase 3: the four bounded items are done
(P3-09..P3-12), P3-01..P3-08 open. Phases 4–5 are the structural remainder.
P7-02 is an interim verdict at
`analysis/planner-refactor-take2/acceptance-20260903/README.md`, to be rewritten
when Phases 3–5 land — but do not delete it, its "what got worse" and
methodology sections do not reproduce themselves.

The eleven items that were *investigated and stopped* have resume points in
`.ralph/deferral_ledger.md` (rows tagged `take2-*`, 2026-09-03): P2-02b, P2-08,
P2-10, both P2-09 halves, P6-03..P6-06, P3-01, and the executor width residual.
Items merely untouched (P3-02..P3-08, P4-02..P4-09, all of P5, P6-01/02/07) have
no row — `TODO.md` is their only record.

TPC-H moved 245.71 s → ~215–224 s this session, every step verified on values;
TPC-DS SF0.5 held PASS=95 MISMATCH=0 throughout. The variance band is wide
enough that the endpoint is a range, not a number — see §3.

---

## 7. Commands

Binaries (the `tpch-runner` used all session was built here, not committed):

    go build -o tmp/take2-bin/tpch-runner ./cmd/tpch-runner
    go build -o tmp/take2-bin/goopg-X     ./cmd/goopg

goopg TPC-H server — **always memory-capped**, and this is where the env flags
belong (`CLAUDE.md`: never `pkill -f goopg`; `--listen`, not `-p`):

    ./bin/goopg stop -D bench/tpch/runtime_goopg/data
    cp tmp/take2-bin/goopg-X bench/tpch/runtime_goopg/goopg-bin
    GOOPG_ASSERT_ROW_SHAPE=1 GOOPG_NARROW_BUILD=1 \
    GOGC=100 GOMEMLIMIT=12GiB GOOPG_CG_UNIT=<unique> \
      scripts/goopg-test-run.sh bench/tpch/runtime_goopg/goopg-bin \
      start -D bench/tpch/runtime_goopg/data --listen 127.0.0.1:65433

TPC-H value gate (goopg-vs-goopg):

    tmp/take2-bin/tpch-runner -port 65433 -digest -per-query-timeout 15m > /tmp/run.log
    tmp/take2-bin/tpch-runner -diff bench/tpch/baseline-digests.txt /tmp/run.log

`bench/tpch/baseline-digests.txt` is the committed reference (24 digests at
`8dc298e92`); its header states the two limits — it is LOAD-DEPENDENT, and it is
not a PG oracle.

TPC-H vs the **PG oracle** — the stronger gate, and the only one that catches a
pre-existing wrong answer. PG runs on :65432; capture its digests the same way
and `-diff` the two logs:

    tmp/take2-bin/tpch-runner -port 65432 -db tpch -user tpch -digest > /tmp/pg.log

TPC-DS SF0.5 (~1 h; the oracle is git-tracked, no PG instance needed):

    GOOPG_BIN=$(pwd)/tmp/take2-bin/goopg-X bash scripts/tpcds-sf05-regression.sh sweep

Reports land in `bench/tpcds/runtime_goopg/tpcds-results-sf05/` as
`sweep-<ts>.txt` and `plans-<ts>.txt`. The sweep diffs against the MOST RECENT
report — check which one, per §3. To compare two chosen reports:

    python3 scripts/tpcds-sweep-diff.py <old>.txt <new>.txt

Unit gate: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`. Never
pass `-count=1`.

**`psql` trap:** the local `psql` is 19beta1 and negotiates protocol 3.9999,
which goopg rejects. Append `?max_protocol_version=3.0` to the URI:

    psql "postgresql://tpch:tpch@127.0.0.1:65433/tpch?max_protocol_version=3.0" -Atc "..."

**"Both `work_mem` budgets" means:** the shipped default (512 MB) and
PostgreSQL's own (4 MB). There is no runner flag — the small budget is the
P2-02b edit itself: `work_mem`'s `BootVal` in `internal/utils/misc/defaults.go`
plus `hashsize.DefaultMemLimitBytes`, which must move together (`TODO.md`
P2-02b). The bench conf's 64 MB is a third, separate thing — the value both
engines are configured with, not a budget to gate at.

**Reading `NBatch`** (§18 step 4's expected 2–4× fall): `EXPLAIN (ANALYZE)` prints
`Batches:` per hash join. For a prediction without running anything, feed
`hashsize.Choose` the row count and column count directly in a throwaway test —
that is how `impl/FINDING-p401-alone-is-not-enough.md` was produced, and it costs
minutes.

---

## 8. Two habits worth keeping

**Check the item against the tree before implementing it.** Six items this
session specified something that should not be built as written, and two were
already done. A `grep` and an `EXPLAIN` cost minutes; the implementations would
have cost weeks and landed short.

**Run the arithmetic before the implementation.** Feeding `hashsize.Choose` the
narrowed column counts took one throwaway test and corrected P4-01's entire
scope — it is why this item no longer claims to unblock P2-02b.
