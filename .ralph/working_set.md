Task: M0138-0006 — re-measure Q9's estimate vs R130's table + reconcile
`GOOPG_ANALYZE_SEED`. **COMPLETE and committed** this loop (`dc471b6d3`),
branch `plan-parity-with-pg-take2-ralph`. **M0138 is now fully landed and
measured (all six tasks [x])** — M0142's prerequisite gate is satisfied.

Files: `docs/design/0100-0149/m0138-0006-q9-estimate-remeasure-and-seed-reconcile.md`
(new), `docs/design/README.md` (+index row), `.ralph/fix_plan.md` (M0138-0006
checked off + DONE summary), `.ralph/deferral_ledger.md` (1 new row,
task-id `m0138-0006`), `.ralph/progress.json` (state-guard repair). No
production code touched — measurement-only recon.

What was done: built `/tmp/estimate-audit-m0138-0006`, ran
`-plan-only -queries 9` against the already-running, already-seed-pinned
(`GOOPG_ANALYZE_SEED=20260905`) `:65433`/`:65432` TPC-H lanes, compared the
plan text's top-level row estimate to R130's table.

Key findings (full detail in the design doc):
  1. Q9's HEAD estimate moved **97 -> 146** (post M0138-0002/-0003/-0004),
     closer to the actual **175** but explicitly **not** toward PG's
     **60,125** — the milestone's own pre-registered "moves toward PG"
     prediction did NOT hold.
  2. Leaf-level check (`partsupp ⋈ part` filtered on `p_name LIKE
     '%green%'`) shows the two engines now agree within 1.2x — the
     underlying per-column statistics converged (consistent with
     M0138-0005's corpus-wide `n_distinct` finding). So the 344x top-level
     gap is **not** a residual statistics-precision gap M0138 could fix.
  3. Hypothesis (filed, not adjudicated — recon-scoped, forcing shapes is
     against the milestone's goal): the gap is **join-shape-driven**.
     goopg drives Q9 with FK-indexed Nested Loops (each probe correctly
     `rows=1`); PG runs an all-Hash-Join chain where `eqjoinsel`/
     `calc_joinrel_size_estimate` compound independent per-join
     selectivities down a correlated FK chain with no extended statistics.
     Reproducing PG's estimate likely needs goopg to choose PG's Hash-Join
     shape first — a join-order/method question (M0142), not an
     ANALYZE-precision one (M0138). This narrows M0142-0001/-0002's
     starting hypothesis instead of leaving "re-examine the premise" open.
  4. `GOOPG_ANALYZE_SEED` reconciliation: PG's `acquire_sample_rows` draws
     from a process-global PRNG (`pg_prng_uint32(&pg_global_prng_state)`,
     `analyze.c:1227`) with no reproducibility GUC/env-var — there is
     nothing to retire the goopg knob in favour of. goopg's unset/zero
     path already reproduces PG's "fresh per-backend draw" property
     exactly; the pinned path is a harness-only determinism knob with no
     PG counterpart. **Verdict: keep, no code change** — confirmed the
     existing code comment's claim against the actual PG source.

Key symbols: `analyzeSeedEnv`/`analyzeSeedFor`
(`internal/executor/operators_analyze.go:702-760`), PG's
`acquire_sample_rows` (`analyze.c:1199-1227`) and `sampler_random_init_state`
(`sampling.c:52,139,234,271,286`), PG's `eqjoinsel`
(`postgres/src/backend/utils/adt/selfuncs.c:2280`) and
`calc_joinrel_size_estimate`
(`postgres/src/backend/optimizer/path/costsize.c:5501`) — the two functions
named as M0142's port target if the join-shape hypothesis holds.

Gates run: `go build ./...` clean (no source changed). `make
ralph-state-guard`: found and auto-repaired one stale status/progress.json
inconsistency (same pattern as the last two loops — the "completed" marker
from the prior loop's clean commit-and-exit, not a project-completion
signal), then consistent. No values-gate re-run needed — no production code
touched this loop, matching the M0138-0001/-0005 precedent.

Bench lanes: `:65433` (goopg TPC-H) and `:65432` (PG TPC-H reference) were
already up at loop start and were only read from (never restarted), per the
shared-`:6543x`-lane rule.

In-flight: none.

Next step: Per the banner, re-check `.ralph/fix_plan.md`'s `## Current
Priority` banner fresh. **M0138 is done**, so the M0138/M0139/M0140
independent trio narrows: M0139-S1 (hook point inside the join tree, gated
on M0137-0010, already unblocked) or M0140-0001 (re-measure the failing
test set under `GOOPG_GATHER_PATHS`) are the two open topmost picks in that
trio, OR — since M0138 "landed and been measured" is now true — M0142-0001
("entry recon: is the pricing blockage still the same one?") is *also*
newly unblocked and comes with a head start from this loop's join-shape
hypothesis. Banner order lists M0139/M0140 (step 2) ahead of M0141/M0142
(step 3), so the next loop should pick from the M0138/M0139/M0140 trio
first (M0139-S1 or M0140-0001) unless it judges M0142-0001 more valuable
given the freshly narrowed hypothesis — use judgment, the banner permits
either within its own ordering rules. Do not re-run the Q9 estimate capture
again without cause — it is committed and reproducible via the exact
command line in this loop's design doc.
