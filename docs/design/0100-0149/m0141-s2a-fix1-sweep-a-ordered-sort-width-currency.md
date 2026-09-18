# M0141-S2a-fix1-sweep-a — narrow the ORDERED upper rel's Sort cost inputs

Status: **landed, measured no-movement** (2026-09-18). `Parent:
M0141-S2a-fix1-sweep`. `Kind: impl`.

## Problem (recon's own framing, `m0141-s2a-fix1-sweep.md` "Site A")

PG's `create_sort_path`
(`postgres/src/backend/optimizer/util/pathnode.c:3221-3250`) prices a Sort
from `subpath->pathtarget->width` — already narrow by the time
`create_ordered_paths` runs, because PG's upper-planner projection machinery
(`apply_projection_to_path`, staged through `grouping_planner`) narrows the
pathtarget at each step before this point.

goopg has no equivalent staged narrowing above the search seam:
`sizeUpperRelFromNode` (`internal/optimizer/upperrel.go:178-188`) sizes the
`ORDERED` upper rel's `NCols`/`AvgVarBytes`/`Width` from the FULL
`child.Output()` of the finished input Node, and `createOrderedPaths`
(`internal/optimizer/upperordered.go`) → `addOrderedPaths` →
`sortPathForBounded` (`internal/optimizer/joinpathsmerge.go:480-514`) prices
the Sort from that same full-width rel via `pathNCols`/`pathAvgVarBytes`. A
ready-made keep-set mechanism already existed for this exact Sort node
(`sort.InputTarget`/`InputTargetKnown`, `internal/optimizer/
sort_input_target.go`) but is stamped at `planner.go:1972/2027/2441` — all
downstream of `createOrderedPaths`, because the `*Sort` node itself does not
exist until `createPlanNode(best)` runs at the end of `createOrderedPaths`,
after the Sort has already been costed. There is nothing to stamp at
cost-time.

## Implementation

New file `internal/optimizer/ordered_input_narrow.go`:

- `finalSelectOutputNames(s, ctx, agg, win, starPS, srfPending)` resolves the
  statement's own final SELECT-list output columns EARLY — using the exact
  same `resolveTargets`/`resolveTargetsAfterAggregate`/
  `resolveTargetsAfterWindow` entry points `buildSelectPlan`'s own later,
  authoritative target resolution reads a little further down in the same
  function, over the SAME `ctx`/`agg`/`win` objects (nothing between the two
  call points mutates them for the covered arms — verified by reading the
  function, not assumed). This is a second, throwaway resolution done purely
  to name columns; any error is swallowed (declined), never surfaced — the
  real error still raises at the later, authoritative call. Declines
  (`ok=false`) whenever a ProjectSet of either kind (composite-star
  `(expr).*`, or a SELECT-list SRF like `generate_series` in the target
  list) is in play: its expanded schema is not this function's to name
  early, and declining is always safe.
- `deriveOrderedSortInputKeep(keys, aboveNames, aboveKnown, input)` unions
  the sort-key columns (`sortKeyColumnNames`, the SAME helper
  `sort_input_target.go`'s later above-aware re-stamp of this same Sort
  already uses) with `aboveNames`, and maps the union to ascending positions
  in `input.Output()`. Declines on any unenumerable key, an unknown
  `aboveNames`, or an empty result (matching `relNarrowedWidths`'s own
  reason for declining an empty keep: `Path.NCols`/rel `NCols` must be `> 0`
  for `AvgVarBytes` to mean anything).
- `narrowOrderedRelWidths(rel, child, keep)` refines the `NCols`/
  `AvgVarBytes` `sizeUpperRelFromNode` just published on `rel` to the
  `keep`-named columns of `child.Output()`. `Rows`/`Width` are left exactly
  as `sizeUpperRelFromNode` set them — Site A's task explicitly scopes to
  NCols/AvgVarBytes currency, and `rel.Width` has exactly one consumer,
  `pgSortRelationBytesCostEnabled()` (default OFF, `GOOPG_PG_SORT_RELATION_
  BYTES_COST`), so narrowing it would be inert under the shipped default
  anyway — narrowing it too was considered and dropped as unnecessary
  complexity for a currently-unreachable code path, not overlooked.

Wiring: `createOrderedPaths` and `electOrderedGrouping` both gained a
trailing `narrowKeep []int` parameter (nil = "no keep known", i.e. today's
full-width sizing, unchanged). `planner.go`'s main SELECT builder computes
the keep-set ONCE, right after the ORDER BY keys are resolved, guarded by
`selectSrfPending == nil` (the plain top-level ORDER BY arm only), and
passes the SAME slice to both call sites:

- `createOrderedPaths` (normal arm, `planner.go:~1970`);
- `electOrderedGrouping` (`planner.go:~1978`) — the GROUP_AGG-adjudication
  loop (M0141-S2b) that a GROUP BY + ORDER BY query (e.g. TPC-H Q18) goes
  through INSTEAD of `createOrderedPaths` when it elects. This has its OWN
  `sizeUpperRelFromNode(ordered, agg.node)` call
  (`upperorderedgrouping.go:219`) sizing the identical rel from the
  identical node (`agg.node == node`, enforced by the function's own entry
  gate) — a sibling site this task's initial pass missed until the
  measurement below surfaced it (TPC-H Q18 IS a GROUP BY query, so it never
  reaches `createOrderedPaths` at all when the loop elects). Fixed in the
  same loop per `pattern_sibling_paths_must_agree`.

The other two `createOrderedPaths` call sites (`wrapSetOpSortLimit`'s set-op
`ORDER BY`, and the min/max-rewrite `ORDER BY` re-attach, `planner.go:803`/
`:11042`) pass a `nil` keep unconditionally: in both, the Sort's input
`Node.Output()` already IS the minimal final row (a set-op's own declared
column list, or the min/max rewrite's single-column output) — there is
nothing wider above them to narrow away, so narrowing would be a no-op by
construction. Confirmed by reading both call sites, not assumed.

## Why the SRF arms decline

- Pre-sort (`selectSrfPending != nil`, sort before ProjectSet expansion):
  the ProjectSet's expanded schema is not yet known in final form at
  Sort-cost time for this arm.
- Post-sort (`selectSrfPending != nil`, sort after ProjectSet expansion,
  `planner.go:~2020`): the Sort already runs directly over the
  ProjectSet's own output (`ps.Output()` — `node` at that point already
  equals it) — nothing wider sits between them, so there is nothing to
  narrow even in principle.

Both decline to `nil`, i.e. unchanged full-width behavior — the always-safe
answer this codebase's narrowing sites (`relNarrowedWidths`,
`sort_input_target.go`) already use throughout.

## Measurement

Same private-clone/private-port procedure as fix1/fix2/fix2r — never
touching the shared `:65432`/`:65433`/`:65437`/`:65438` clusters beyond
read-only `pg_basebackup`/`SELECT`/`EXPLAIN`. Baseline binary: detached
`git worktree add --detach /tmp/wt-sweepa-baseline HEAD` at `6f7acae46`
(before this task's diff, i.e. fix2r already landed). After binary: working
tree with this task's diff (both the `ordered_input_narrow.go` site AND the
`electOrderedGrouping` sibling fix).

- **TPC-H values** (`scripts/tpch-acceptance-arm.sh`, full 22-query digest,
  `PGSHAPED=1`, private port 5583, `GOOPG_ANALYZE_SEED=20260905`,
  `NO_BUILD=1` with the two pinned binaries): `tpch-acceptance-runner -diff`
  reported **VERDICT: PASS — 24/24 labels MATCH on values**, every row count
  unchanged. Confirms this is a pure costing change.
- **TPC-H shape/category** (`scripts/tpch-estimate-audit-arm.sh
  PLAN_ONLY=1 PGSHAPED=1`, same two binaries, private port 5582,
  `--ref-port 65432`, `REFERENCE=` to skip the stale committed-file default
  in favor of the live `--ref-port` capture): artefacts
  `analysis/leftdeep-joins/m0141-s2a-fix1-sweep-a-{before,after}.{txt,
  plans.txt,pg.plans.txt}`. `shape-delta.sh` between the two goopg-only
  captures:

  ```
  SHAPE-DELTA: queries=22 text-changed=0 shape-changed=0
  ```

  **Zero text change at all, before OR after the `electOrderedGrouping`
  sibling fix was added** — not merely zero shape change, zero of anything,
  including estimate digits. This is a genuine measured **no-movement**
  result, not a declined/inert implementation: `shape-delta.sh` diffs raw
  `EXPLAIN` text, so a narrowed `NCols`/`AvgVarBytes` that changed the
  `costSortRun` byte volume even slightly, without flipping a plan choice,
  would still show up as a text-changed (cost-digit) line. None did.
- **TPC-DS SF0.25** (`scripts/tpcds-sf025-regression.sh sweep`, run against
  the after-binary tree — this script runs the shared regression gate, not
  an A/B arm): `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`,
  `PLAN-SHAPE: queries=99 same=99 changed=0`.

### Why zero movement, not a bug

Traced directly rather than left as a mystery: TPC-H's own ORDER BY queries
are overwhelmingly `GROUP BY ... ORDER BY <agg-derived column>` shapes (Q18
included) — and a GROUP BY aggregate's own published row IS already the
minimal SELECT-list width by construction (goopg's `Aggregate` node emits
exactly the GROUP BY columns plus the aggregate results, the same as PG's
own `Agg` node). There is no "hidden extra width" for `aboveNames` to trim
away in that shape: `aboveNames` (the final SELECT list) and the sort-key
names are already a subset of what the Aggregate emits, one-to-one. The
recon's own named witness (TPC-H Q18's 1.5M-row Sort) turned out, on direct
measurement, to be exactly this shape — the recon's speculation that it
would move was untested at recon time (`C1`, no production diff permitted)
and the implementation now shows it does not. TPC-DS SF0.25's 99-query
corpus shows the same zero-movement result, so the "aggregate output is
already minimal" explanation is not merely a TPC-H artefact.

A genuine currency gap for THIS keep-set (sort keys ∪ final SELECT list)
would need a plain (non-aggregate) `ORDER BY` over a join/scan tree wider
than the statement's own SELECT list — e.g. `SELECT a FROM t1 JOIN t2 ...
ORDER BY t2.b` where `b` is not selected. Neither corpus's `ORDER BY` set
happens to hit this shape at a scale where the `costSortRun` byte volume
crosses `sortByteBranch`'s `memory`/`bounded`/`disk` thresholds, or even
moves the printed cost digits (both are floating-point sums; a real but
tiny width delta would still show as a changed decimal in the `EXPLAIN`
line, and none did — meaning the `keep` slice this run computed was very
likely full-width-equivalent for every query in both corpora, not merely
sub-threshold).

## Disposition

**No `Parent: M0141-S2a-fix1-sweep-a` follow-up filed.** Per this
programme's own rule ("file every query whose categories worsen"), there is
nothing to file — no query worsened, and no query improved either. This is
recorded as a clean, PG-faithful currency correction with a confirmed
zero-regression, zero-movement measurement, kept because it is provably
correct and matches PG's real cost model (the R121/`relNarrowedWidths`
precedent: correctness of the cost INPUT is the goal, independent of
whether today's corpus happens to exercise the difference) — the same
standard fix2r's re-apply used, just landing at a smaller Movement.

`Movement: none.` Both gates (`go test ./internal/optimizer/...`,
`scripts/tpch-spotcheck.sh` Q12=2/Q13=34) pass; `make ea-ratchet` is N/A —
a costing-only change touching no row-estimate/selectivity path, same
disposition as fix2r.
