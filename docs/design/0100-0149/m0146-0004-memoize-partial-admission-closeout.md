# M0146-0004 — per-worker Memoize + Gather-over-Memoize: scoping pass and closeout

Status: COMPLETE 2026-09-27 — the adopted work was already landed by
M0142-0005a (2026-09-19); this task's residual content was the
scoping/floor-measurement pass M0142-0005's note requires, run here
against the post-cutover corpus. Verdict: **zero remaining divergence
attributable to Gather-over-Memoize or per-worker Memoize.**
Kind: impl (carried as M0142-0005 resume option (a); content delivered
this loop is verification + identity pin, no production change)
Parent: M0142-0005
PG oracle: `postgres/src/backend/optimizer/path/joinpath.c:674-820`
(`get_memoize_path`), `:2194-2199` (the mpath call pair inside
`try_partial_nestloop_path`); `postgres/src/backend/executor/nodeMemoize.c:1190-1260`
(per-worker MemoizeState; the DSM shuttles only instrumentation counters).

## 1. What the task was re-adopted to do, and what was already true

The 2026-09-23 owner disposition adopted the M0142-0005 resume — "(a) give
the executor a per-worker-partitioned Memoize cache … and then relax
`partialPathDrivingKind`'s lateral-probe branch to admit `PathMemoize`" —
as M0146-0004. Both halves predate the disposition:

- **Executor model**: correct by construction. Every worker builds its own
  operator tree including a private `memoizeOp`/`kvcache.Cache`
  (`internal/executor/executor.go:354-371`), matching `nodeMemoize.c`'s
  per-worker MemoizeState exactly — the DSM only carries instrumentation.
  Re-scoped 2026-09-18 (M0142-0005's "RE-SCOPED AGAIN" note).
- **Planner admission**: landed by **M0142-0005a** (2026-09-19).
  `partialPathDrivingKind`'s `PathNestLoop` arm unwraps one `PathMemoize`
  layer (`in.Children[0]`, always the wrapped probe per
  `getMemoizePath` `joinpathsmemoize.go:318`) and re-runs the bare-probe
  check; the SetOp-branch mirror matches. All claim/collect/stamp walks
  (`drivingScan`, `stampParallelScan`, `attachParallelScan`,
  `collectShareableJoins`, `collectBitmapScans`, `HasShareableHashJoin`,
  `drivingScanCrossesSort`) route the fused `*NestedLoopIndexJoin` through
  the shared `NestedLoopIndexJoinIsPartialCapable` predicate.

The memoized inner never exists as a free-standing node: `createPlan`
panics on `PathMemoize` outside a nested-loop inner
(`createplan.go:115-124`); the cache is `NestedLoopIndexJoin.InnerMemo`
(a field, not a child), unwrapped into `nestedLoopIndexJoinOp.inner` at
`executor.go:279-281`.

## 2. Scoping pass (2026-09-27, post-cutover HEAD)

Re-verified the chain end to end on current code and the canonical corpus:

- **Producer** (`getMemoizePath`, `joinpathsmemoize.go:206`): wraps only a
  bare keyed index probe (`IndexClauses` non-empty, cache keys = bare
  outer `*ColumnRef`s), PG's gate sequence preserved including
  `outer_path->parent->rows < 2` and the `calls`-vs-rel correction.
- **Path admission** (`gatherpaths.go:686-702`): the R95 lateral-probe
  branch admits `PathMemoize` via the single unwrap for jointypes
  `{INNER, SEMI, ANTI}` (`partialProbeNestLoopJointype`); LEFT is refused
  — no worker-local-verified consumer, matching the decomposed-probe
  gates' standing ledger row.
- **Node admission** (`parallel.go:1346`): the fused gate admits
  `{I, L, S, A}` — InnerMemo's presence is irrelevant to the verdict
  because the cache is per-worker by construction.
- **Lowering/EXPLAIN**: the fused node renders PG's `Memoize` +
  `Cache Key:` lines between the join and the index scan
  (`operators_explain.go:1519`).

## 3. Floor measurement (canonical captures, `analysis/m0146/m0146-0003d/`)

| corpus | PG Memoize nodes | goopg Memoize nodes | memoize-attributable divergence |
|---|---|---|---|
| TPC-H (parallel) | 0 | 0 | none (corpus has no consumer) |
| TPC-DS SF0.25 | 41 | 50 | none at a first-divergence position |
| TPC-DS SF1 | 37 | 38 | none at a first-divergence position |

- Witnesses hold post-cutover: TPC-DS Q34 and Q73 render
  `Gather Merge → Sort → … → Nested Loop → Memoize → Index Scan` — the
  exact admission this task was filed to enable (0005a's original
  pre-flip evidence was Q34's accepted `producer=gather` lines and the
  SF0.25 flips of Q34/Q73 to PG's shapes).
- Every PG Memoize child in the corpus is `Index Scan` (34) or
  `Index Only Scan` (3, SF1 only: Q53/Q63/Q77 on `store_pkey`). goopg
  cannot emit a parameterised index-only probe at all
  (`addParameterizedIndexPaths` emits the full-index-scan shape;
  `TestGetMemoizePathDeclinesIndexOnlyInner` pins the refusal) — but all
  three occurrences sit **downstream of an unrelated first divergence**
  (Q53 `Subquery Scan vs WindowAgg`; Q63 Incremental Sort; Q77 CTE), so
  the gap is not corpus-actionable here; it folds into the standing
  M0127-P5.5-c deferral rows for parameterised index-only paths.
- goopg emits *more* Memoize nodes than PG (50 vs 41 at SF0.25) — an
  election/costing artefact of different join contexts, not an admission
  gap; over-election lives in M0146-0005/0009 territory.
- First-divergence census (M0146-0001's ranked list, re-read against the
  0003d capture): **no record** attributes a residual to Gather-over-
  Memoize or per-worker Memoize.

## 4. Identity pin added this loop

`TestParallelNLIMemoizeIdentity` (`parallel_nli_memoize_test.go`): the
memoized fused NLI under a hand-wrapped `Gather` returns the serial
rowset as a multiset at workers 1/2/4, under `-race`. The fixture's
4-rows-per-key stream splits repeats across workers, so each worker's
first sight of a key is a genuine miss — the per-worker cache is
exercised, not bypassed. Complements the claim-topology pins
(`TestParallelNLIWalkerAdmission/Refusals`) and the corpus-level gate
(SF0.25/SF1 sweeps execute Q34/Q73 through this shape at oracle-verified
counts — PASS in the 0003d evidence).

## 5. Remaining refusals (standing, ledgered)

- LEFT parameterized probes (path-level `{I,S,A}`; the fused family is
  wider `{I,L,S,A}`) — no measured consumer; ledgered under 0002i/0002j.
- Memoize over non-index inners (parameterized bitmap scans, nested-loop
  inners) — PG's `get_memoize_path` admits them; goopg's producer only
  wraps keyed index probes. No corpus consumer observed.
- Memoize over a parameterised Index Only Scan — blocked upstream by the
  parameterized-index-only gap (M0127-P5.5-c ledger rows), not by this
  task's gates.

## 6. Gates run this loop

- `go test -run 'TestParallelNLIMemoizeIdentity' ./internal/executor/` PASS
- `go test -race -run 'TestParallelNLI|TestCollectShareableJoinsDescendsNLI|TestCollectBitmapScansDescendsNLI|TestMemoize' ./internal/executor/` PASS
- Corpus floor measured on the committed `analysis/m0146/m0146-0003d/`
  captures (no re-run needed — their candidate binary carries HEAD's
  planner code; the intervening commits are docs/evidence only).
