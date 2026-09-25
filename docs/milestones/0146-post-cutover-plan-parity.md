# M0146 — Post-cutover plan parity

**Filed:** 2026-09-23 (owner decision, delegated scope M0137–M0145).
**Status:** planned — **gated on the M0145-0008 flip landing**; no task in
this milestone is selectable before it.
**Basis:** `/home/ryo/work/tmp/ef5e65e27094282ab28769901e05621c/01-progress-assessment-260923.md`
(the assessment that M0145's structural work landed and the residual
divergence lives downstream of the flow), the M0144-0007 first-divergence
census, and the M0145-0008 executor-capability inventory
(`docs/design/0100-0149/m0145-0008-executor-capability-inventory.md`).

## Why this milestone exists

M0145 unified the planning ROUTE with PG 18.3 — jointree-level IR, pull-up,
one DP pass, one lowering. Measured result: the knob arm is value-identical
on both corpora, timing-neutral (1.01x), and at the parity floor, with the
seam-decline classes (`pinned-spine` 285→0, `outer-over-derived` 3→0,
pull-up `(pulled)` 9→27) burned down. What remains is what the flow work was
always expected to expose rather than fix — the assessment's conclusion,
anticipated by `tmp/planner-rewrite-possibility260920.md`'s honest caveats:

- **Executor substrate** — shapes the planner could print but the executor
  cannot run PG's way: `Parallel Hash` over a genuinely partial inner,
  row-emitting PartialAgg (the `Finalize→GatherMerge→Sort→PartialAgg`
  stack), per-worker Memoize under Gather, `Materialize`.
- **Plan election / costing** — the dominant residual category
  (join-order ≈ 90 SF0.25 records): candidate pools, join-method and
  join-order elections, Incremental Sort's input candidates.
- **Statistics / cardinality** — the ea-ratchet baseline's 53 findings and
  the B-06 CTE-output residual.
- **Upper-rel / missing nodes** — `Materialize` (`missingnode` 25→14
  claim), remaining PG-only plan nodes.

M0144's vertical-slice campaign and the M0137/M0140/M0141/M0142 parked
escalations all terminate in this list — every one is re-homed below with
its measured size. This milestone is the burn-down.

## Strategy

- **Same discipline as M0137–M0145**: recon before impl, measured demand
  only, `Movement:` recorded per task, no production change without the
  gate suite.
- **Executor capability is verified BEFORE admission** (the M0145-0010
  scope-(d) rule): every newly generatable shape gets a
  parallel-vs-serial / per-shape identity pin before the planner admits it.
- **Ordering follows divergence size, not convenience**: the census
  re-take (0001) ranks residuals; the join-order/candidate-pool class
  (0005) precedes Incremental Sort (0006) because input-candidate
  divergence decides 6 of 7 of S7's reachable witnesses.
- **Nothing here reverts the M0145 flow.** A divergence that only exists
  because the new pipeline produces a different-but-PG-faithful shape is
  recorded, not "fixed" back.

## Tasks

| id | kind | summary |
|---|---|---|
| M0146-0001 | recon | Post-cutover parity re-baseline: re-run the first-divergence census + category counts + TPC-H parity capture on the NEW default arm; produce the ranked residual list that re-sequences this milestone. Complements M0145-0025 (pre-flip TPC-H baseline) |
| M0146-0002 | impl | `Parallel Hash` over a genuinely partial inner (M0140-0007 re-scope): build inside the Gather, shared publication, build-completion barrier, error propagation; label + model together; per-shape parallel-vs-serial identity pins. Witnesses: TPC-H `parallelism` family A (Q14/Q16 first — Q9/Q21/half-Q10 are join-order, not this task) |
| M0146-0003 | impl | Row-emitting PartialAgg — executes the filed M0141-S3→S4→S5→S6 chain (Partial-Sorted row emission → `aggRuntime` serde → GatherMerge-fed Finalize-Sorted → wire and measure). Unblocks PG's `Finalize→GatherMerge→Sort→PartialAgg` stack; re-opens M0137-0019a's re-evaluation |
| M0146-0004 | impl | Per-worker Memoize + Gather-over-Memoize admission (M0142-0005 resume option (a); child M0142-0005b): `nodeMemoize.c`'s `parallel_worker_number`-keyed cache, then relax `partialPathDrivingKind`'s lateral-probe branch; includes the scoping/floor-measurement pass M0142-0005 requires |
| M0146-0005 | impl | Join-order / candidate-pool divergence burn-down — the largest residual category (~90 SF0.25 `join-order` records). Per-family decomposition from 0001's census; owns Q8's `depth=3` residue (M0144-0011b) and the Q9/Q21/half-Q10 family. On completion, re-evaluates M0141-S7's S2b-9/S2b-8 per the owner hold |
| M0146-0006 | impl | Incremental Sort election — M0141-S7's resume under the owner hold: S2b-9 (Incremental Sort over the seed; Q4 `keys=1 ncommon=1`) and S2b-8 (the SORTED grouping arm; Q64's CTE). Sequenced after M0146-0005 |
| M0146-0007 | impl | `inline_cte` — single-reference CTE inlining into the jointree IR (PG's `inline_cte`, prepjointree.c). Divergence class D6; operates on the IR so it is a jointree-arm task by construction |
| M0146-0008 | impl | Leaf-count residual: re-census the opaque-leaf population on the new default (M0144-0003a successor — its probe showed the leaves are already-planned composites, not identity `*Project`s; whatever admission work survives the re-census lands here) |
| M0146-0009 | impl | Statistics/cardinality burn-down: the 53-finding ea-ratchet baseline (`analysis/planner-refactor-take3/c20a-estimator-census-20260922/ea-baseline.txt`) worked to zero NEW-permitted; includes the B-06 CTE-output residual M0145-0009's census named |
| M0146-0010 | impl | `Materialize` node — M0144-0011c's four slices (node+EXPLAIN+`createPlan` inert; `cost_material`/rescan arm; NL admission + `join_nl_stream.go` unwrapping; Q54-class `nlInnerWorkMemEnabled` cliff re-time). Expected `missingnode` 25→14 |
| M0146-0011 | recon | Lateral/parameterized-path post-cutover re-census (M0145-0010's residual): recount the `lateral` decline family on the new default (2 fires today — Q30/Q68); escalate to an impl task only if the population grows or `lateral_relids` become load-bearing for another admitted shape |
| M0146-0012 | impl | Correlated restrictions as base-rel index quals (M0145-0027's ledger residual): admit `OuterColumnRef` conjuncts as pseudo-constant restrictions (`is_pseudo_constant_for_index`, indxpath.c:4596), teach base-rel index pathgen to use them, then retire the flattening/restoring rules. PREREQUISITE: prove executor rescan semantics (chgParam/extParam analogue) — a cached correlated leaf must rebuild per outer binding. Witnesses: TPC-H Q17/Q20, TPC-DS Q41 |
| M0146-0013 | impl | `cost_qual_eval` port — per-clause qual cost ordering (`order_qual_clauses`, createplan.c:5420) at every Filter build site (M0145-0028's ledger residual) |
| M0146-0014 | recon | Parity-closure sweep: every remaining first-divergence record on both corpora is either assigned to a live task above or presented to the owner as a named, measured, waived residual. This is the milestone's exit report |

## Explicitly out of scope

- **The `:65433` TPC-H corpus rebuild in PG's ctid order** (M0142-0005's
  R1 — the `indexProbeCostMultiplier` honest exit). A reference-cluster
  lifecycle action; real-owner only, outside the delegated scope.
- **M0142-0005g** (patternsel 2× recon) — stays frozen per M0142-0005's
  disposition.
- **Partitionwise joins** — `reparameterize_path`'s named consumer does
  not exist; not filed (M0145-0010 closure rationale).
- Legacy-pipeline internals — deleted at the M0145-0008 cutover, which is
  this milestone's gate, not its member.

## Acceptance

- The parity instruments run against the new default arm and the
  category counts are re-baselined by M0146-0001; every subsequent task
  reports `Movement:` against that baseline.
- TPC-H SF1 and TPC-DS SF0.25/SF1 corpora remain value-identical and pass
  all gates at every step (executor capability proven before admission).
- M0146-0014's sweep completes: no unnamed first-divergence records
  remain on either corpus.
