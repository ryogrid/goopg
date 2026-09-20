# M0144-0011 — first vertical-slice campaign: TPC-DS SF0.25 Q8

Status: ESCALATED 2026-09-20 (loop #49) under AGENT.md S4 — five
consecutive completed descendants reported `Movement: none`, so the root is
`[!]` and only the owner reopens it. The campaign did NOT reach "Q8
matches"; it reached the second of 03-forward-plan §4's two endings — the
residue is a named, measured, unfunded capability: `Materialize`, worth
`missingnode` 25 → 14 (−11), sized in four slices in
`m0144-0011c-materialize-sizing.md` §4.

Children, and what each proved: **0011a** `Movement: yes` (SF0.25
`sort-strategy` 67 → 60, the lineage's one movement — a redundant Sort over
an already-ordered `GroupAggregate` removed corpus-wide); **0011a-3** the
walk crosses a positional-identity `*Project`, Q21's depth-1 node kind now
matches PG; **0011a-2** a 99-query ORDERED-seam census, candidate minimum
2 → 1 to match `planner.c:5337`; **0011b** refuted its own premise (goopg's
Q8 NL is CHEAPER than PG's, not dearer) and found the real defect a layer
down; **0011b-1** fixed it — a sub-plan leaf is priced from its own subtree,
Q8's 650x cost-monotonicity violation is gone and its top join now elects
PG's Nested Loop; **0011c** sized `Materialize`. The full escalation block,
with the blocker and the expected movement, is in `.ralph/fix_plan.md`
under this task.

## Pick

Top first-divergence cluster from the m0144-0002 census:
`Limit → {GroupAggregate | Incremental Sort | Sort+CTE}` under `Limit`
vs `goopg Sort` — ~25 records SF0.25, ~20 SF1. Representative **Q8**
(SF0.25): `dominated-noncost`, candm −46.8% (m0144-0007) — the PG-family
candidate is generated AND cheaper; the missing layer is an ordering
claim, the most actionable subclass. Sibling subclasses the same seam
should move: election-0% records (Q17/Q25/Q29) and the
`nonemptykeys=0` IS-arm records.

## Findings (trace: `analysis/m0144/m0144-0011-q8-slice-trace.md`)

| layer | verdict |
|---|---|
| admission | OK — `planner.go:1979/1984` reaches electOrderedGrouping + createOrderedPaths |
| candidate generation | **GAP (a)+(b)** — ordering claims don't propagate to the ordered-step seed |
| cost inputs | not the layer-1 blocker; implicated at the join one level down |
| election | join rel {0,1,2,3}: NL generated (19852) but dominated by HJ (19455); inner economics diverge from PG (Materialize+Index-Scan rescan) |
| executor existence | **GAP** — no `Materialize` plan node |

### Gap (a): `inputNodePathkeys` ordered-emission blind spot

`internal/optimizer/upperorderedinput.go:176` — derives the seed's
ordering claim only from `*Sort`, `searchedTreePathkeys`, and
`*Filter`/`*Limit` descent; `default: return nil` for `*Aggregate` (and
every other ordered-emitting top: MergeJoin, GatherMerge,
IncrementalSort). Q8's winning `upper.groupagg.sort` PathAgg carried
`pathkeys=1` — the claim exists on the path, unreachable from the Node.

PG: `create_agg_path` sets `AGG_SORTED` pathkeys =
`list_copy_head(subpath->pathkeys, root->num_groupby_pathkeys)`
(`postgres/src/backend/optimizer/util/pathnode.c:3412-3416` — "preserves
order"); `create_ordered_paths` accepts any input path whose pathkeys
contain `root->sort_pathkeys` (`postgres/src/backend/optimizer/plan/planner.c:5344-5348`).

Existing machinery to reuse: `groupingEmissionPathkeys`
(`upperorderedgrouping.go:78`) already translates a PathAgg candidate's
emission ordering into output coordinates.

### Gap (b): `electOrderedGrouping` lone-candidate gate

`cands<2(1)` decline — the GROUP_AGG rel legitimately holds one PathAgg
(hashed dominated by sorted under fuzzy-equal cost + pathkey superset,
matching PG `add_path`). PG's `create_ordered_paths` iterates the whole
input pathlist with no minimum (`planner.c:5337`) — a lone AggPath must
still be offered. May become moot once (a) lands; the child decides.

### Deeper layers

- Join election at `{dd⋈ss}⋈{store,ca-view}`: goopg HJ 19455 vs NL 19852
  (dominated); PG NL 28502. goopg's NL inner is priced on a different
  shape (no Materialize; Seq Scan store vs PG Index Scan pkey +
  Materialize(NL)). Cost-input/election layer — 0011b.
- `Materialize`: PG-only node kind (`MISSING-NODE`). PG:
  `create_material_path` (`pathnode.c:1637`). Executor-existence
  layer — 0011c.

## Children (S5 — each names expected movement + measurement)

| child | scope | expected movement |
|---|---|---|
| M0144-0011a | ordering-claim propagation at ORDERED-step boundary | SF0.25 `sort-strategy` on the `Limit→{GroupAgg|Finalize}` family (≤18 records; named: Q8/Q21/Q26/Q45/Q50/Q62/Q99 dominated-noncost + Q17/Q25/Q29 election); census depth-1 advance on a fresh capture |
| M0144-0011b | join election under Q8's aggregate input | `join-method`/`scan-type`/`join-order` categories on Q8; DPPATH candidate margins at rel {0,1,2,3} |
| M0144-0011c | `Materialize` path+node existence | `missingnode` count on Q8-family records where PG materializes NL inners |

## D2 report fields

1. `CATEGORIES:`/`MATCH`: N/A this loop — recon trace only, no parity
   capture taken for the slice yet (existing census/capture cited).
2. shape-delta: N/A. 3. stats epoch: a574afdef103f449 (cited capture).
4. seam-decline census: N/A — no production change.
5. planning route: `electOrderedGrouping` → `createOrderedPaths`
   (`planner.go:1979→1984`) — confirmed reached, both arms traced.
6. `Movement: none` — recon; children carry the movement claims.
7. wall times: N/A.
