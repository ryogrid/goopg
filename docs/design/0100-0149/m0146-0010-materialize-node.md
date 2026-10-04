# M0146-0010 — the `Materialize` node

Status: landed 2026-09-28 (slices 1–3 of the M0144-0011c sizing). Slice 4
landed 2026-10-05: re-timed by M0146-0010b, and the work_mem bound became
the default in M0146-0010c. The strict executor arm is still deferred
(M0146-0010a, held). See §7.
Spec: [m0144-0011c-materialize-sizing.md](m0144-0011c-materialize-sizing.md) §4.

## 1. What PG does

`match_unsorted_outer` (joinpath.c:1890-1901) files a nested loop over the
inner's cheapest total path AND, when `enable_material` is on, the inner is
unparameterised and `ExecMaterializesOutput` (execAmi.c:640) does not already
cover its pathtype, a second nested loop over `create_material_path` of that
inner. The two candidates are priced separately: the bare inner's rescan is
`cost_rescan`'s default arm (full re-execution), the MaterialPath's is the
T_Material arm (`cpu_operator_cost` per tuple, plus a page re-read on spill),
and the MaterialPath itself carries `cost_material`'s build charge
(`2 * cpu_operator_cost` per tuple). `add_path` picks. The executor follows
the plan: a Material node caches; a bare inner is `ExecReScan`-ed.

## 2. What goopg did before

The executor wrapped EVERY streaming NL inner in `materializeOp`
unconditionally, and the planner fused that assumption into every inner's
rescan price (`nestLoopInnerRescanCost`, take2 P2-06). There was no
PathMaterial and no plan node, so PG's `Materialize` could never appear
(`missingnode`), and the two-candidate adjudication that decides both the
Material election and the NL orientation (Q91) was collapsed into one price.

## 3. What landed

- **Plan node + path kind.** `*optimizer.Materialize` (plan.go) and
  `PathMaterial` (path.go), threaded through every node/path switch as a
  transparent single-child wrapper (cardinality, createplanroot, parallel,
  unnest clone/walk, narrowing, searched-tree, EXPLAIN). `createMaterialPlan`
  is `make_material`.
- **Cost functions** (`internal/optimizer/materialize.go`): `costMaterial`
  (cost\_material), `materialRescanCost` (T\_Material/T\_Sort rescan arm),
  `relationByteSize`. `pathRescanCost` is now `cost_rescan` per candidate:
  Memoize, Material/Sort, **T\_CteScan/T\_WorkTableScan** (`cpu_tuple_cost`
  per tuple + spill re-read), **T\_FunctionScan** (startup dropped), default.
  The fused helpers are retired.
- **Admission** (`materialInnerPathFor`, pathgen.go / joinpathsnli.go and the
  partial-NL twin, joinpath.c:2129-2141): `enable_material` generation gate,
  unparameterised inner, and `execMaterializesOutput`'s exclusion set
  (Material, Sort, Memoize, CTE/WorkTable/function scans). `pathLeafNode`
  resolves a leaf path's node class — PathPrebuilt directly, the leaf scan
  kinds through `Rel.baseLeaf` — and answers nil for interior paths over a
  base rel (a Unique over a CTE rel is T\_Unique, not T\_CteScan).
- **Executor.** `buildNode` builds `materializeOp` for the plan node;
  `openNestedLoop` drives a plan-elected Materialize directly through
  `rescannable` (forwarded through `instrumentedOp` and `opNodeOperator`, so
  EXPLAIN ANALYZE stays live) instead of stacking a second cache.
- **Diff tool.** `scripts/pg-plan-parity-diff.py` no longer lists
  `Materialize` as unemittable; it is categorised with Memoize under
  `parameterisation`.

## 4. The two traps found while landing it

1. **CTE leaves hide in `Rel.baseLeaf`.** A CTE reaches the NL as a
   PathSeqScan, so checking only PathPrebuilt nodes filed a matpath over a
   CTE scan (goopg-only Materialize on TPC-DS Q31).
2. **The fused price hid a missing `cost_rescan` arm.** With the fused
   helper gone, a bare CTE inner was charged a full re-execution per outer
   row, and Q31's join order moved off PG's. PG prices it as a tuplestore
   re-read; adding the T\_CteScan/T\_FunctionScan arms restored Q31 to MATCH.

## 5. Deferred

- **Strict executor arm.** PG re-executes a bare inner; goopg keeps the
  legacy attach-time wrap for bare inners by default, strict re-execution
  (`rescanByReexec`) only under `GOOPG_NL_BARE_REEXEC=1`. Reason: TPC-DS Q14
  statement 2 elects a bare NL over an 827-row (est. 1) outer whose inner is
  a costly hash-semi subtree (cost 18825) where PG has a cheap index-probe
  pipeline (2730) driven by `unique(cross_items)`. goopg cannot lead with a
  unique-ified CTE semi-inner (`joinIsLegal`'s unique\_ified arm,
  M0142-0008c-2, unwired), so strict re-exec takes Q14 from 11 s to >300 s.
  Retire the wrap once that ordering exists (M0146-0010a).
- **Slice 4** — re-time the Q54-class `nlInnerWorkMemEnabled` cliff
  (M0146-0010b).
- **`enable_material` session wire** — the GUC is registered but no bridge
  carries it into `PlannerSettings.EnableMaterial` (P2-02 remainder).
- **`cost_rescan` T\_HashJoin single-batch arm** — goopg's hash-join Path
  does not record its batch count.

## 6. Movement

Same diff tool on both arms (the tool change alone moves `missingnode`
24 → 14 on HEAD too, so it is not engine movement):

| lane | match | join-order | parameterisation | PG-only Materialize queries |
|---|---|---|---|---|
| TPC-DS SF0.25 | 15 → 16 (Q91 gained; Q31 held) | 68 → 66 | 35 → 35 | 7 → 6 (Q8 cleared) |
| TPC-DS SF1 | 14 → 14 | 68 → 65 | 44 → 41 | — |
| TPC-H | plans identical modulo cost | | | |

Gates: units, tpch-spotcheck (Q12=2/Q13=33), TPC-DS SF0.25 sweep, TPC-H
acceptance arm (24/24 values), fire-set (19 fires, no introduced timeouts).

## 7. Re-timing both deferred arms (2026-10-05, M0146-0010a / M0146-0010b)

Evidence: `analysis/m0146/m0146-0010ab/`.

### The work_mem bound (slice 4, M0146-0010b)

`GOOPG_NL_MATERIALIZE_WORK_MEM=1` bounds the nested loop's inner
Materialize cache by work_mem, as PG's tuplestore is. It was left off because
TPC-DS Q54 at SF0.5 nested-looped over a 1.44M-row `store_sales` scan, and the
spilled cache replayed per outer row (144 s → >400 s).

- **SF0.25 sweep with the bound.** 96/96, no timeout.
  - Its slow readings were Q5 1.3 → 4.6 s, Q74 32 → 73 s and Q72 176 → 282
    s. Re-timed alternately in subset mode, they are cold-server noise:
    - Q5: 1552 / 1549 ms (default / bound);
    - Q74: 35292 / 35061 ms;
    - Q72: 175471 / 175283 ms.
  - The first, cold run of each pair was the slow one in either mode.
- **Q54.** 1.1 s in both modes. At SF1 its plan no longer has the cliff
  shape: the only Materialize sits over the 12-row `store` scan, because
  `cost_material` and `cost_rescan` now price the big inner out, as §3
  expected.
- **Verdict.** The unbounded exception is no longer needed. Flipping the
  default is an executor change, so it is filed as M0146-0010c rather than
  made under the recon.

### The strict bare-inner arm (M0146-0010a)

`GOOPG_NL_BARE_REEXEC=1` re-executes a bare inner on rescan, as PG's
ExecReScan does. Its named prerequisite, M0142-0008c-2, has landed. On the
SF0.25 sweep, though:

| query | default | strict |
|---|---|---|
| Q4 | 15 s | timeout (300 s) |
| Q11 | 11 s | timeout |
| Q14 | 39 s | timeout |
| Q74 | 32 s | 157 s |
| Q5, Q7, Q31 | — | 2–3× slower |

Q4's plan nests five Nested Loops whose inners are filtered `CTE Scan on
year_total`s, estimated at 9–29 rows. The compat cache materializes each
filtered inner once. Re-executing them reads the CTE's 1.19M rows per outer
row. PG plans those joins as hash joins from its own estimates. So the
remaining blocker is the outer-row estimate and join method of those CTE
joins, not an executor gap. M0146-0010a stays held on it.

### The flip (M0146-0010c)

`nlInnerWorkMemEnabled` (`internal/executor/join_nl_stream.go`) is on unless
`GOOPG_NL_MATERIALIZE_WORK_MEM=0`. That applies both to a plan-elected
Materialize and to the compat cache over a bare inner. So the nested loop's
inner cache spills past work_mem as PG's tuplestore does.

- **Test.** `TestNestedLoopInnerCacheSpillsByDefault` fails on HEAD. It runs
  a 20 × 80 inner join at work_mem = 512 bytes with no opt-in. It requires
  the cache to spill and the rows to equal the in-memory run.
- **Gates.** Units and tpch-spotcheck passed, and the TPC-H arm matched
  24/24. The sweep passed 96/96 with every plan unchanged (99/99) and the
  total runtime down 10.1% against the previous sweep (Q74's earlier 73 s
  was the cold reading).
