# M0142-0005 — recon: the real blocker is Gather-eligibility refusing a Memoize-wrapped partial inner, not "no Memoize on the NL probe path"

Status: accepted (recon closed 2026-09-16, no code change)

## Task

`.ralph/fix_plan.md`'s M0142-0005 line ("break the Memoize / probe-multiplier
interlock", B6+B8) claimed goopg's executor has **no Memoize on the NL probe
path at all**, so pricing an NL+IndexScan probe PG-faithfully sends other
queries (Q72) into 320s timeouts, and `indexProbeCostMultiplier=2.0`
compensates by keeping the DP away from NL-index plans generally. M0142-0010
(2026-09-15) restated this as the diagnosed cause of TPC-DS Q34/Q73 not
matching PG's `Nested Loop`+`Memoize`+`Index Scan using date_dim_pkey` shape.

Before spending a loop implementing an executor-side Memoize fix on that
premise, this recon re-verified it — per the standing instinct
`planner_verify_both_candidates_generated` ("instrument `addPath`, not the
cost functions") and the sibling memory
`goopg_parallelism_is_gather_stamping_not_partial_paths`, both of which warn
that a missing plan shape can be a generation gap, a domination loss, or (as
turned out here) neither.

## Finding 1 — Memoize already exists, both in the executor and as a costed DP-search path

Grep and `git log` show a full Memoize implementation has been live since
2026-07-21 (`894485ba7`, "csq(R2-7/S7): Memoize"):

- `internal/executor/operators_memoize.go` (`newMemoizeOp`) — a real executor
  operator, wired at `executor.go:266`.
- `internal/optimizer/joinpathsmemoize.go` (`getMemoizePath`,
  `memoizePathInfo`) — a genuine **cost-based path**, offered alongside the
  bare inner to `addPath` inside `addNLIPaths` (`joinpathsnli.go:313`) so
  `add_path`-style dominance decides whether the cache pays, exactly PG's own
  `try_nestloop_path` shape (joinpath.c:1965-1986).

So "no Memoize on the NL probe path" as a blanket claim is **stale** — it
predates neither B6 (R59, `plan_parity_fix_take2`) nor M0142-0010 correctly;
the mechanism simply landed since and the ledger row was never re-checked
against the current binary.

## Finding 2 — the NLI+Memoize candidate for Q34 IS generated and correctly costed

Cloned `bench/tpcds/runtime_goopg/data-sf025` to a private port (5533,
`tmp/m0142-0005/`, per `goopg_shared_bench_cluster_collisions`) and ran Q34
with `GOOPG_PGSHAPED_DP_TRACE=1` (an **existing**, no-code-change trace
channel — `internal/optimizer/joinsearchtrace.go`/`pathtrace.go` — DPTRACE for
pairs/costs/vetoes, DPPATH for every individual path `addPath`/`addPartialPath`
sees, `verdict=accepted|dominated`).

Q34's 4-relation join (`store_sales, date_dim, store, household_demographics`)
lets the DP reach relset `{date_dim+household_demographics+store_sales}` via
two different pairings: `{date_dim+store_sales}⋈{household_demographics}`
(cheap: `date_dim`'s own `d_dom`/`d_year` filter is very selective, 235/73049
rows) and `{household_demographics+store_sales}⋈{date_dim}` — the PG-matching
order (hdemo's own filter, ~500/12000 rows, joined to the full `store_sales`
first, THEN NLI+Memoize-probing the now-much-smaller intermediate into
`date_dim`'s PK).

The DPPATH trace shows the second pairing's **partial** NLI+Memoize candidate
(`producer=join.nestloop.partial`, `outer={store_sales+household_demographics}`
partial hash, `inner={date_dim}` Memoize-wrapped parameterised probe) filed at
**total=16463.24** (per-worker) and `verdict=accepted` — dominating every
other partial candidate for this joinrel down to a `PartialPathlist` of one
entry by the time `generateUsefulGatherPaths` runs
(`DPTRACE cpgather rel={date_dim+household_demographics+store_sales}
partials=1 verdict=admitted`). This is dramatically cheaper than the eventual
winning **serial/gathered** hash join (`total=19760.88`) — confirmed against
an isolated 2-relation `EXPLAIN` of `store_sales⋈household_demographics`
alone, whose `Parallel Hash Join (cost=182.10..16215.33 rows=15054)` is
near-identical to PG's own real plan fragment
(`Hash Join (cost=257.11..16125.21 rows=15076)`, `bench/tpcds/plans-pg/Q34.txt`)
— goopg's join-order/cardinality math for this join is **already PG-faithful**;
this is not a cardinality or `indexProbeCostMultiplier` bug.

## Finding 3 — the candidate never gets a chance to compete, because it can never be Gathered

Despite `cpgather verdict=admitted` (not blocked by parallel-mode/`ConsiderParallel`
gates) and a one-entry `PartialPathlist` holding exactly the winning candidate,
**no `producer=gather` DPPATH line exists at all for this joinrel** — not even
a `dominated` one. Every sibling relset at the same levels (`{0,1}`, `{0,2}`,
`{0,3}`, `{0,1,2}`, `{0,2,3}`) gets one; only `{0,1,3}` (the one whose sole
partial survivor is Memoize-wrapped) does not.

Root cause, `internal/optimizer/gatherpaths.go`:

- `generateUsefulGatherPaths` calls `makeGatherPath(rel, rel.PartialPathlist[0], cp)`.
- `makeGatherPath` calls `gatherSubpathIsRunnable(sub)` →
  `partialPathShapeIsGatherable(sub)` → `partialPathDrivingKind(sub)`.
- `partialPathDrivingKind`'s `PathNestLoop` case (`:456-490`) explicitly
  refuses a Memoize-wrapped inner in **both** its branches:
  - the whole-inner (`in.RequiredOuter == 0`) branch: `if in.Kind == PathMemoize
    { return PathPrebuilt }` — own comment: "must not be a Memoize cache
    (per-probe semantics no worker can supply)";
  - the lateral-probe (`in.RequiredOuter != 0`, our shape) branch:
    `if in.Kind != PathIndexScan || len(in.IndexClauses) == 0 { return
    PathPrebuilt }` — own comment: "a parameterized `PathIndexScan` from R60's
    producer (its Memoize loop output is `PathMemoize`, refused above)".
- `PathPrebuilt` makes `makeGatherPath` return `nil` before constructing
  anything, so no path is even built — matching the observed silence (no
  DPPATH line of any verdict).

This is a **deliberate, documented, already-reasoned restriction** — not an
oversight — because the *executor* side has no per-worker Memoize: `memoizeOp`
(`internal/executor/operators_memoize.go`) is a single cache instance wired to
one `indexScanOp`, with no claim-set partitioning story for splitting a
Memoize'd probe stream across parallel workers (`parallelClaimSet`/`attachAll`,
`gatherpaths.go:322-328`, only models `PathSeqScan`/`PathIndexScan`/
`PathBitmapHeapScan`/`PathHashJoin`/`PathMergeJoin`/plain `PathNestLoop`). The
planner's refusal is the *correct* conservative choice given that gap — the
alternative would cost a plan the executor cannot honour, the exact class of
bug rule #2 (`createplanindex.go`) exists to prevent elsewhere.

## Verdict — M0142-0005 is real, but mis-scoped; corrected

**B6's actual mechanism (2026-09-16 restatement):** goopg's DP search already
generates and correctly costs the PG-matching `Nested Loop`+`Memoize`+
`Index Scan` shape as a **partial** path — the join-order/cardinality layer is
not at fault — but `partialPathDrivingKind` unconditionally excludes any
Memoize-wrapped inner from ever becoming a **Gathered total** path, because
the executor has no mechanism to run a per-worker-partitioned Memoize cache.
Every TPC-DS query whose PG-parity shape needs a parallel Memoize-driven probe
(Q34, Q73, and likely others sharing the "wide dimension table PK, narrow FK
domain, filtered on the small side first" shape this recon's own M0142-0010
predecessor already flagged as common) is structurally blocked from reaching
it, regardless of any cost-model tuning.

**B8 (`indexProbeCostMultiplier=2.0`) is a separate, narrower question this
recon did not re-settle**: the calibration commit (`c61781d6`, 2026-09-05)
post-dates Memoize's own landing (`894485ba7`, 2026-07-21), so it was
calibrated against a binary that already had serial NLI+Memoize available —
its continued need is plausible (a *serial* NLI probe still materializes its
whole TID list eagerly per the multiplier's own comment) but wasn't
re-measured here; that remains open.

**This recon changes what the correct next step is.** The original framing
would have sent a loop hunting for "why doesn't the executor support Memoize"
(it does). The corrected framing is an executor+planner slice: either (a)
give the executor a per-worker Memoize so `partialPathDrivingKind`'s refusal
can be relaxed for the lateral-probe branch, or (b) determine PG's own
`parallel_worker_number`-keyed cache model (`nodeMemoize.c`) is not directly
portable and find a narrower relaxation. Both are executor-shaped work, sized
like a dedicated slice — not attempted this loop, per the recon-first
discipline M0142-0004a/0009/0010/0012a already established.

## Floor measurements (mandatory for M0137–M0143 recon tasks)

No production code changed — a temporary debug print was added to
`internal/optimizer/pathparamindex.go` during the investigation and fully
reverted (`git checkout --`) before this doc was written; `git status
--porcelain -- internal/` is clean. `GOOPG_PGSHAPED_DP_TRACE=1` is an existing,
already-shipped trace channel, not new instrumentation. TPC-H/TPC-DS
plan-parity floors and `make ea-ratchet`'s baseline are therefore unchanged by
construction (same precedent as M0142-0010/0013). Pre-commit pgbench smoke
gate run per policy. Throwaway server/clone (port 5533, `tmp/m0142-0005/`)
stopped and removed before commit.

## Follow-up

Filed **M0142-0005** itself, rewritten in `.ralph/fix_plan.md` with this
corrected mechanism and resume point (`gatherpaths.go:456-490`
`partialPathDrivingKind`'s `PathNestLoop` case; `internal/executor/
operators_memoize.go`'s `memoizeOp` for the executor-side per-worker cache
gap). No new task ID needed — M0142-0005 keeps its slot, only its diagnosis
and resume point change. Deferral ledger row added for the B8 multiplier
re-measurement, which stays open and unscoped.
