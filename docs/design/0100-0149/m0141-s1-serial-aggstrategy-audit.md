# M0141-S1 — serial Hashed-vs-Sorted audit (recon, no production change)

Status: accepted (landed 2026-09-15)

## Task

`.ralph/fix_plan.md` M0141-S1: audit why goopg's existing cost-based
Hashed/Sorted `PathAgg` candidate contest (`groupingpaths.go`) doesn't
produce PG's shape for TPC-H's `aggregation-strategy`(10)/`sort-strategy`(9)
mismatches, none of which can involve Gather/GatherMerge under the canonical
`-serial=true` protocol. Two actions, both recon: (1) resolve the apparent
contradiction between `plan.go:1346-1348`'s comment and
`createplansimple.go:173,219`'s wiring; (2) a live capture +
`pg-plan-parity-diff.py` pass to split TPC-DS's aggregation-strategy/
sort-strategy counts into serial-shaped (in S1/S2 scope) vs already
parallel-blocked (M0140's floor, out of scope here). Per the plan-parity
harness, this is measurement, the fix is S2. **No production code is
touched by this task.**

## Action 1 — the `Strategy` wiring contradiction is resolved: the comment is stale

`plan.go:1343-1349`'s `Aggregate.Strategy` doc comment reads:

> The planner does not set it yet (M0134-0001 S8 lands the executor
> capability first); sorted mode is reachable only via direct node
> construction until the pathkey slice wires the choice in.

This is **false at HEAD**. Reading the chain end to end:

1. **`groupingpaths.go:addGroupingPaths`** already runs a genuine cost-based
   contest, not a placeholder. For a grouped aggregate it calls `addPath`
   with **both** a `PathAgg{AggStrategy: AggStrategyHashed, ...}` candidate
   (whenever `groupingHashable` allows it) **and**, independently, a
   `PathAgg{AggStrategy: AggStrategySorted, ...}` candidate (whenever a
   presorted-keys or synthesised-Sort input is valid) — each carrying its own
   `costAgg(cp, <strategy>, ...)` price. `addPath` keeps whichever is
   cheaper (or both, if neither dominates on cost+pathkeys). This is the
   PG-shaped `PathAgg` candidate contest S0 already named as existing
   machinery, not something S1 discovered — but S1 is the first task to
   trace it into the create-plan step below.
2. **`createplansimple.go:createAggPlan` (line 172) and
   `createFinalizeAggPlan` (line 219)** both copy the winning path's
   `AggStrategy` onto the executor node: `out.Strategy = p.AggStrategy` /
   `simple.Strategy = p.AggStrategy`. This is the exact mechanism the stale
   comment says doesn't exist yet.
3. **`operators_join_agg.go:2222`** (`openSorted`'s dispatch guard) reads:
   `o.plan.Strategy == optimizer.AggStrategySorted && o.plan.GroupingSets ==
   nil && len(o.plan.GroupExprs) > 0 && o.plan.Mode ==
   optimizer.AggModeSimple`. Under the canonical `-serial=true` protocol
   every aggregate plans and executes with `Mode == AggModeSimple` (no
   Partial/Finalize split), so this guard's `Mode` clause is unconditionally
   satisfied for every TPC-H query in scope here — the **only** gate that
   matters for the serial case is `Strategy == AggStrategySorted`, and step
   2 shows the planner already sets exactly that field from the cost
   contest's winner.

**Conclusion: the serial Hashed-vs-Sorted machinery is wired end to end —
path generation, cost, plan creation, executor dispatch — with no missing
link.** The comment is leftover from a state that predates R47's
per-candidate spec clone (`groupingpaths.go`'s own comments cite "R47 slice
1" on the very candidates this task traced) and should be corrected in the
same commit that lands S2's fix (deferred to S2 rather than done here, to
keep this task a pure recon with no production diff — a comment edit is a
one-line production change and the harness reserves those for the task that
actually resolves the underlying question).

**What remains genuinely open** (S2's job, not this task's): the contest
exists and is wired, but it evidently does not pick PG's shape on TPC-H's
10/9 tagged queries — meaning either `costAgg`'s Sorted/Hashed price
comparison disagrees with PG's `cost_agg` at some term, or the Sorted
candidate is never generated for the queries in question (e.g.
`presortedAggKeysOrAbsent` finds no usable key, or `groupingHasSpecialAgg`
declines). This audit did not narrow between those; S2 opens with a
per-query trace of which of the two happened.

## Action 2 — live capture, and a materially sharper split than S0's estimate

### Method

Fresh captures against the live bench clusters (both up at task start:
`:65432`/`:65433` TPC-H, `:65437`/`:65438` TPC-DS SF0.25), per
`docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md`:

```
PGPASSWORD=tpch /tmp/estimate-audit-s1 -plan-only -label m0141-s1-tpch \
  -out analysis/m0141 -port 65433 -db tpch -user tpch -password tpch \
  -ref-port 65432 -ref-db tpch -ref-user postgres -ref-password postgres
scripts/capture-tpcds.sh 65437 postgres postgres analysis/m0141/m0141-s1-tpcds-goopg.txt \
  "m0141-s1 goopg SF0.25" bench/tpcds/runtime_goopg/data-sf025
scripts/capture-tpcds.sh 65438 tpcds025 ryo analysis/m0141/m0141-s1-tpcds-pg.txt \
  "m0141-s1 PG18.3 SF0.25 reference"
python3 scripts/pg-plan-parity-diff.py analysis/m0141/m0141-s1-tpch.plans.txt analysis/m0141/m0141-s1-tpch.pg.plans.txt
python3 scripts/pg-plan-parity-diff.py analysis/m0141/m0141-s1-tpcds-goopg.txt analysis/m0141/m0141-s1-tpcds-pg.txt
```

Artefacts committed under `analysis/m0141/` (`m0141-s1-tpch*`,
`m0141-s1-tpcds*`).

### TPC-H result: confirms S0's claim by direct measurement (no longer an inference)

`PLAN-PARITY: queries=22 match=6 shapediff=14 unparsed=0 missingnode=2
error=0 timeout=0`. `CATEGORIES: ... aggregation-strategy=10 sort-strategy=9
parallelism=0 ...`. Since `estimate-audit -plan-only` defaults `-serial=true`
(`max_parallel_workers_per_gather=0` on **both** engines), `parallelism=0` is
not a coincidence — the whole corpus is measured with Gather/GatherMerge
switched off on both sides, so **all 10 aggregation-strategy and all 9
sort-strategy tagged queries are, by construction, serial-shaped**. S0's
"decisive scoping fact" was reasoned from the `-serial` flag's documented
effect; this is the same conclusion from a fresh capture, not a re-assertion
of the same inference. Non-regression floor held: `match=6`.

Tagged queries: aggregation-strategy = {Q2, Q3, Q4, Q5, Q8, Q12, Q13, Q18,
Q21, Q22} (10); sort-strategy = same set minus Q2 (9).

### TPC-DS result: a materially sharper split than S0's placeholder estimate

`capture-tpcds.sh` pins `max_parallel_workers_per_gather=4` (unlike the
TPC-H tool, it does **not** force serial) — so, unlike TPC-H, some
TPC-DS mismatches genuinely need the missing Gather-Merge machinery and some
do not, and the two must be told apart by looking at what PG's *own* chosen
plan does, not by which category label the diff tool attaches.

`PLAN-PARITY: queries=99 match=2 shapediff=69 unparsed=0 missingnode=25
error=3 timeout=0`. `CATEGORIES: ... aggregation-strategy=70 sort-strategy=76
parallelism=85 ...`. (`match=2` holds the non-regression floor
`AGENT.md` names — Q9, Q41.) These raw counts differ slightly from the
milestone-filing snapshot's "69/76" (this run: 70/76) — a one-query
drift on `aggregation-strategy`, not a regression signal on its own; no
baseline pin exists yet for this pair to diff against, so this task
reports it and moves on rather than chasing it.

**The classification test**: for every query tagged `aggregation-strategy` or
`sort-strategy`, does PG's own captured plan (not goopg's, not the diff
tool's category label) contain `Partial (Group)?Aggregate`,
`Finalize (Group)?Aggregate`, or `Gather Merge`? If yes, PG chose an
AGGSPLIT-shaped plan for that query and the mismatch is at least partly a
missing-machinery question (S3-S6 territory, additionally gated on M0140's
own parallelism floor per S0). If no, PG's own reference plan is fully
serial for that query — the mismatch **cannot** be about Gather-Merge
machinery goopg lacks, because PG isn't using it either; it is a same-shape
serial Hashed-vs-Sorted (or narrower) cost/path-selection question, in S1/S2
scope.

| category | tagged | PG plan AGGSPLIT-shaped | PG plan fully serial (S1/S2 scope) |
|---|---|---|---|
| aggregation-strategy | 70 | 41 | **29** |
| sort-strategy | 76 | 49 | **27** |
| union (either tag) | 83 | 51 | **32** |

Serial-shaped (S1/S2 scope) query list (union, 32 of 83):
Q1, Q2, Q8, Q10, Q18, Q22, Q23, Q24, Q26, Q30, Q31, Q32, Q42, Q45, Q49, Q52,
Q53, Q54, Q55, Q56, Q59, Q60, Q63, Q69, Q71, Q80, Q81, Q83, Q91, Q92, Q93,
Q94.

This is a materially sharper number than S0's "69/76 is an unseparated mix"
placeholder: roughly **two in five** of the union (32/83) is serial-shaped
and addressable by S2 without any new executor machinery; the remaining
**three in five** (51/83) is at least touched by AGGSPLIT and stays gated
behind S1/S2's own measurement (this table) plus M0140's parallelism floor,
per S0's entry gate for S3-S6.

**Caveat on precision.** The classification is a plan-text marker search, not
a structural proof that the marker's node is the query's *aggregate* node
specifically (a `Gather Merge` elsewhere in the tree, e.g. feeding an
unrelated parallel index scan, would also match). This is adequate for a
recon-level split — the false-positive direction only shrinks the
"S1/S2-addressable" bucket, never inflates it — but S2 should re-verify the
marker's placement per query it actually works, not trust this table's
per-query membership blindly.

## What every M0137-M0143 task report must contain (per AGENT.md)

- **Category movement**: none — no production change, so
  `aggregation-strategy`/`sort-strategy`/`parallelism` counts at HEAD are
  unchanged by this task; the numbers above are a fresh capture, not a
  before/after.
- **shape-delta**: 0 (no plan changed; this task touches no planning code).
- **Stats epoch**: TPC-H — `estimate-audit`'s own `# stats-epoch:` line in
  `analysis/m0141/m0141-s1-tpch.txt`; not compared against a prior arm (no
  values sweep in this task). TPC-DS — `# stats-epoch: 5d4dc56356f3d676`
  (goopg side, `m0141-s1-tpcds-goopg.txt` header); same caveat.
- **Seam-decline census**: not applicable — no seam-declining code path is
  exercised or changed by a recon task.
- **Planning route**: not distinguished per-query in this task; S2 opens
  with the per-query trace this leaves for it (see Action 1's "what remains
  open").

## Verification

- Both live captures completed without ERROR/TIMEOUT beyond `error=3` on
  the TPC-DS side (pre-existing, unrelated to aggregation/sort — not
  investigated here, out of this task's scope).
- `python3 scripts/pg-plan-parity-diff.py` exit 0 on both corpora (report-only
  tool, always exits 0 per its own docstring).
- No `go build`/`go test` run — no `.go` file is touched by this task.
- `go build ./...` re-confirmed clean anyway (state check before commit,
  matching S0's practice): clean.

## Next step (S2)

`.ralph/fix_plan.md`'s M0141-S2 opens with: (a) land the one-line
`plan.go:1343-1349` comment correction this task diagnosed as stale
(cite this doc); (b) a per-query trace of TPC-H's 10 aggregation-strategy /
9 sort-strategy queries (all serial per this task) to determine, for each,
whether the Sorted candidate was priced-and-lost or never generated; (c) the
same trace for the 32-query TPC-DS serial-shaped subset this task's table
names. S3-S6 (the AGGSPLIT executor programme) stay gated on S2's own
finding plus M0140's parallelism floor, per S0's entry gate — this task's
51/83 AGGSPLIT-touched count is evidence for sizing that gate, not a
green light to start it.
