# M0147-0016 — retire the dead `GOOPG_RELSIZE_FALLBACK` read and stale lever text

Status: landed 2026-10-26.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 1. Lever count 25 → 24
(`scripts/lever-count.sh`).

## What was retired

M0125-0003 added the relation-size fallback, a port of `table_block_relation_estimate_size`
(`postgres/src/backend/access/table/tableam.c`) that sizes a never-ANALYZEd relation from its block count. A staged
knob, `GOOPG_RELSIZE_FALLBACK`, enabled its consumers one at a time. M0125-0005 flipped the default to stage 2.
take2 P1-05 then made the fallback unconditional and dropped every read of the stage, but left the
`init()` env read, the parser and the test setter behind. From then on the variable was stored and never loaded:
setting it changed nothing. `flaglabels.go` has listed it as `retired(take2-P1-05)` since then.

This task deletes those leftovers and the other stale lever text the M0147 census found.

## Removed / changed

- `internal/optimizer/relsize.go`: `relsizeFallbackStage`, `defaultRelSizeFallbackStage`, the `init()` env read,
  `parseRelSizeFallbackStage`, `SetRelSizeFallbackStage`, the orphaned "single gated entry point" comment, the
  stage-3 "keeps answering with stage-2 behaviour" sentence, and four imports. The P1-05 history paragraph on
  `relSizeFallbackRows` now records this deletion as well.
- `internal/optimizer/cost_funcs.go`: `envFloatDefault`. It has had no caller since M0146-0159 retired
  `GOOPG_INDEX_PROBE_MULT`. Its `os` / `strconv` imports went with it.
- Comments describing the knob as live are rewritten: `small_dimension.go` (`smallDimensionRows`), `plan.go`
  (`SeqScan.EstRelRows`) and `cardinality.go` (`seqScanRows`).
- `flaglabels.go`: the `GOOPG_PG_SORT_RELATION_BYTES_COST` and `GOOPG_INCREMENTAL_SORT` comments said "default
  off". Both are default ON (`planner-flags.env`: `unset(on)`). The two `envFloatDefault` mentions (here and in
  `flaglabels_test.go`) are now in the past tense.
- `scripts/tpch-relsize-arm.sh`: the four arms crossed ANALYZE with the flag, and the flag had been inert since
  P1-05, so c1 measured the same thing as c2 and w1 the same as w2. The script now runs `cold` and `warm`.
  `c2` / `w2` are accepted as aliases. `c1` / `w1` are refused with a pointer here, because the stage-0 planner they
  selected no longer exists. The script no longer exports the variable.
- `.ralph/fix_plan.md` M0140 section: the K80 text called `GOOPG_GATHER_PATHS` default-OFF. A dated note now says
  it is `all` today. The original text is kept as history.

`deadcode ./cmd/...` was diffed before and after. Only the two deleted functions left its list, and nothing new
appeared. `scripts/planner-flags.env` regenerates byte-identically, because the row was already retired.

## Tests

- `TestParseRelSizeFallbackStage` is deleted: the parser is gone.
- `TestSmallDimensionTagIgnoresRelSizeFallbackStage` is deleted. The asymmetry it pinned (the small-dimension tag
  ignores the knob) has no knob left. Its positive property is still covered by `TestSmallDimensionTag*`.
- `TestRelSizeFallbackDoesNotFireWhenAnalyzed`: the loop over stages 0-3 collapses to one assertion, since the
  stage was never read.
- `relsize_baserel_placement_test.go`: the three `SetRelSizeFallbackStage` save/restore pairs are dropped. The
  assertions are unchanged.

## Inert proof (AGENT.md C6)

- At HEAD `relsizeFallbackStage` had only `Store` (in `init`) and `Swap` (in the test setter) and no `Load`, so no
  planner path could observe the variable.
- `envFloatDefault` had no caller (`deadcode` listed it as unreachable).
- The only setter of the variable was `scripts/tpch-relsize-arm.sh`, whose arms were already equivalent. No gate,
  CI config or env file sets it.

## Gates

See the M0147-0016 entry in `.ralph/fix_plan.md`.
