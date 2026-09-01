# GEQO implementation reviews — M0134-0190

This directory records the two agent reviews the M0134-0190 GEQO
(genetic query optimizer) implementation received, per the task
"実装に先立ち...エージェントレビューを受けてください" and
"実装後も修正差分についてロジックバグがないかレビューを受けてください".

Both reviews were performed by read-only subagent reviews cross-checking the
Go implementation and design doc against the PostgreSQL 18.3 oracle in
`postgres/src/backend/optimizer/geqo/` and `postgres/src/include/optimizer/geqo*.h`.

## 1. Design Doc review (pre-implementation)

File reviewed: `docs/design/0100-0149/m0134-0190-geqo-genetic-query-optimizer.md`
Reviewed: 2026-09-01 (before implementation).

Findings (all fixed in the doc):

1. **Internal contradiction on >16 dispatch** — the doc's integration-point
   section said the seam routes to GEQO "when `geqo` GUC is ON and
   `nrels >= geqo_threshold` (default 12), **or when `nrels > maxSearchRels`**",
   but the RelSet-width section said v0 limits GEQO to the same 16-relation
   maximum. PG's dispatch is only `enable_geqo && levels_needed >= geqo_threshold`
   (allpaths.c:3420); there is no `> maxSearchRels` arm. Fixed: removed the
   `> maxSearchRels` arm and stated the 16-rel representation limit explicitly
   with widening as a separate follow-up.
2. **Pool sizing formula omitted `ceil()` and the explicit-value bypass** —
   `gimme_pool_size` returns `(int) ceil(size)` and is bypassed when the user
   sets `Geqo_pool_size >= 2`. Fixed: doc now spells both.
3. **Minor line-reference drift** — `RelSet uint16` declaration is at path.go:30
   (comment path.go:26-29); `gimme_tree` is at geqo_eval.c:163. Fixed.
4. **Initial `sort_pool` not stated** — `random_init_pool` skips DBL_MAX tours
   and then `sort_pool` establishes the sorted invariant `spread_chromo`
   relies on. Fixed: step 1 now mentions the one-time sort.

## 2. Implementation review (post-implementation, logic-bug check)

Files reviewed: `internal/optimizer/geqo.go` (implementation),
`internal/optimizer/relfromjoinlist.go` (dispatch),
`cmd/goopg/main.go` (GUC bridge), `internal/optimizer/geqo_test.go` (tests).
Reviewed: 2026-09-01 (after implementation, before commit).

Findings (all fixed):

1. **CRITICAL — `mergeClump` never called `setCheapest` on intermediate
   joinrels** (geqo.go). PG's `merge_clump` calls `set_cheapest(joinrel)`
   immediately after `make_join_rel` (geqo_eval.c:280). Without it, a clump
   grown past one relation had `CheapestTotal == nil`, so the next merge using
   it as input failed at the `joinpaths.go` "outer rel has no cheapest path"
   guard, and `gimmeTree` returned nil for every tour with ≥3 relations —
   GEQO could never produce a plan at the default threshold. Fixed: `setCheapest`
   after the successful `makeJoinRel` in `mergeClump`.
2. **`TestGeqoEvalPanicsOnNilCtx` asserted nothing** — the deferred recover only
   logged; it never failed. Fixed: `t.Fatal` when no panic.
3. **`gimmeGene` / `edgeFailure` silently returned an invalid gene (0)** — PG
   raises `elog(ERROR)` in the same unreachable positions. These paths are
   believed unreachable in correct operation; left as defensive returns (the
   callers handle the failure through tour rejection), matching PG's structure.
4. **Dead `builder` parameter in `gimmeTree` / `mergeClump`** — the actual
   construction goes through `s.makeJoinRel` using the context's own builder.
   Fixed: removed the parameter.
5. **`geqoEval`'s fresh-context strategy shares base-rel pointers** across tour
   evaluations — safe because `makeJoinRel` adds paths only to newly created
   joinrels and base rels are not mutated by `setCheapest`; documented as a
   latent hazard (future path that mutates a base rel's pathlist would leak
   across evaluations).

Verified after fixes: `go build ./...` clean, `go test ./internal/optimizer/ -count=1`
full suite PASS.
