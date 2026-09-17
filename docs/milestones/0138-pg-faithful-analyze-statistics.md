# Milestone 0138 — PG-faithful ANALYZE statistics

**Status:** complete — 9 done, 0 open in `.ralph/fix_plan.md` (2026-09-17); order per the fix_plan banner
**Filed:** 2026-09-14 (user directive answering
`METHODOLOGY3/04-forward-plan.md` §1.2 Question 2)
**Priority placement:** second in the plan-parity group, after M0137. See the
`## Current Priority` banner in `.ralph/fix_plan.md`.
**Reference plan:** `.ralph/fix_plan.md` (M0138 section)
**Harness:** `AGENT.md` §"Plan-parity harness (M0137–M0143)"
**Prerequisites:** M0137 (this milestone moves estimates corpus-wide; without
stamped captures and a declared stats epoch its A/B is unreadable).

## The decision this milestone implements

`METHODOLOGY3/04-forward-plan.md` §1.2 proposed that goopg **keep** its more
accurate estimate and record queries reachable only through a PG estimation
error as out of target. **The goal's owner rejected that rule on 2026-09-14**
and directed the opposite: goopg reproduces PG's estimates, errors included,
*"because otherwise identical plan generation is impossible."*

Two consequences, both binding:

- **R79's verdict (b) — "keep the superior statistics, close Q4 elsewhere" — is
  OVERTURNED.** Do not cite it as a reason to decline sampling work.
- **Q9 stays in the parity target**, and M0142 (join-order costing) becomes
  scopeable. R130's table is the yardstick: Q9's actual output is **175** rows;
  goopg estimates **97** (1.8x low); **PG estimates 60,125 (344x high)**. After
  this milestone, goopg's estimate should move *toward PG's*, not toward truth.

### Frame this correctly — it is not error-injection

The programme's goal statement already requires the same plan *"reached by the
**same statistics**"*. goopg's sampler is a **divergence from PG**, so removing
it is PG-faithfulness work of exactly the same kind as porting a cost term.

**The method is: port `acquire_sample_rows` and let its output be whatever it
is.** Never tune a constant toward a target number, never special-case a query,
never add a fudge factor to "reach" PG's estimate. If a ported sampler still
disagrees with PG, that is a finding to be recorded, not a gap to be papered
over. A number matched by tuning proves nothing about the mechanism and is the
arbitrary forcing the goal forbids — the same reasoning K92 applies to a
mislabelled plan node, one layer down.

## What actually diverges

goopg is closer to PG here than the framing suggests, which is why this is a
bounded milestone rather than a rewrite. Already upstream-faithful:
`upstreamDefaultStatsTarget = 100` and the `targrows = stats_target * 300`
multiplier (`internal/executor/operators_analyze.go:581-589`), an Algorithm-R
reservoir, physical-order re-sort of the reservoir before column stats
(`analyzeSampleByTID`), and the Duj1 n-distinct estimator itself.

The measured divergence (R79 P0/P1) is the **block representation**:

| | goopg | PG 18.3 |
|---|---|---|
| blocks read | **every block** (full scan) | up to `targrows` **random** blocks via `BlockSampler_Init`/`_Next` (`src/backend/utils/misc/sampling.c:39,64`) |
| row selection | Algorithm-R reservoir over all rows | two-stage: block sampler, then `reservoir_get_next_S` + `sampler_random_fract` (`analyze.c:1228-1290`) |
| RNG | Go `math/rand` seeded by `analyzeSeedFor` | `pg_prng` (xoroshiro128\*\*) |

On insertion-ordered `l_orderkey` goopg's uniform sample is f1-rich: goopg reads
a flat ~1.17–1.21M at every usable statistics target, PG reads 343,831, and the
truth is ~1.5M. **PG's undercount is what Q4's needed 0.2317 semi-selectivity
derives from.**

One qualifier that must not be lost (`METHODOLOGY3/01` §F8): the two curves
**converge at target 1000** (1.206M vs 1.216M, ~0.8% apart) via PG's by-design
absolute->fraction switch. So on *that* witness PG's undercount is substantially
a `default_statistics_target` artefact. R130's Q9 344x is **not** a
sampling-target effect — the two arrivals at Question 2 are different findings
and must not be merged into one argument.

A second difference is the **representation**, and here goopg is closer than it
looks — do not re-implement what exists. goopg stores upstream's one signed
`stadistinct` as **two** fields, `NDistinct` (absolute) and `NDistinctFrac`
(`internal/catalog/catalog.go:1880-1903`), but `ColumnStats.StaDistinct()`
(`catalog.go:1942`) already reconstructs PG's signed convention **including
upstream's 10% absolute-to-fraction switch** (`analyze.c:2650-2658`), and it is
already consumed at the `pg_statistic` heap row, the `pg_stats` view and
`joinselectivity.go:235`. The open question is therefore not the convention but
(a) whether the switch fires where PG's does once the sample changes, and
(b) whether every consumer reads the reconstructed value rather than a bare
`NDistinct` — the take2 P2-09 bug class, where a column whose distinct count
scales with the table reads as absolute-zero. R78's witness: goopg `l_orderkey`
`-0.1956` vs PG's absolute `347537`.

## Per-task discipline (READ FIRST — binding)

1. **Design note when the task is selected** (`docs/design/<task-id>-NNNN-*.md`,
   indexed in `docs/design/README.md` same commit). Overrides
   `docs/milestones/README.md` §"Workflow Per Milestone" step 2 for this group.
2. **Cite the PG oracle by `file:function`** for every ported piece
   (`postgres/src/backend/commands/analyze.c`,
   `postgres/src/backend/utils/misc/sampling.c`). Never vendor or import from
   `./postgres/`.
3. **No tuning toward a number.** See the frame above. A task whose diff
   contains a constant chosen to make an estimate match is rejected.
4. **Same-epoch A/B only.** This milestone changes estimates corpus-wide; every
   comparison declares its stats epoch on both arms (M0137-0006).
5. **The values gates bind.** Large plan churn is expected and acceptable;
   wrong rows are not.

## Definition of Done

- goopg's ANALYZE reads the same blocks PG would, **by the same rule**, with
  PG's PRNG. **Bit-exact TID-set parity is achievable only where `relpages`
  already matches PG** — `BlockSampler_Init` is seeded by `nblocks`, so on a
  relation whose page count diverges the same rule cannot select the same
  blocks. K39 closed that gap for the fact tables (PG-faithful to 0.4%), but
  **K41 is open and unexplained on dimension tables** (`customer` 1,979 vs
  2,872; `item` 716 vs 1,284), and it is filed as out-of-reach under
  M0140-0005. So the bar is: identical TID set on `relpages`-matching
  relations, and **rule-faithfulness plus a recorded divergence** elsewhere.
  **Do not close the gap by touching the seed or the PRNG sequence** — that is
  the tuning this milestone forbids.
- `stadistinct` parity is **verified** at the catalog boundary — the existing
  `StaDistinct()` reconstruction still matches PG's signed convention and its
  switch point on the new sample, and no consumer reads a bare `NDistinct`.
  A change lands only where a real divergence is found.
- MCV, histogram and correlation are computed from that shared sample by PG's
  `compute_scalar_stats` / `compute_distinct_stats` selection rule.
- A committed per-column statistics diff vs PG 18.3 over both corpora, at a
  declared epoch, showing where goopg and PG now agree and where they do not —
  with every remaining disagreement either explained or filed as a ledger row.
- Q9's estimate is re-measured and **reported** against R130's table. Movement
  toward PG's 60,125 is the expected outcome; **absence of movement is an equally
  valid finding** and is not a failure of this milestone — it means M0142's
  premise must be re-examined before M0142 is scoped. The DoD is the
  measurement and the report, not the direction.
- `GOOPG_ANALYZE_SEED`'s role is reconciled with PG's own sequence and
  documented — either retired, or kept with a stated reason why both must exist.
