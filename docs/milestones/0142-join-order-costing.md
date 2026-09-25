# Milestone 0142 — Join-order costing

**Status:** in-progress — 74 done, 4 open, 4 blocked/frozen in `.ralph/fix_plan.md` (2026-09-17); order per the fix_plan banner
**Filed:** 2026-09-14 (user directive, from
`METHODOLOGY3/04-forward-plan.md` Phase 3, unblocked by the Question 2 answer)
**Priority placement:** in the plan-parity group, after M0138 has landed and
been measured. See the `## Current Priority` banner.
**Reference plan:** `.ralph/fix_plan.md` (M0142 section)
**Harness:** `AGENT.md` §"Plan-parity harness (M0137–M0143)"
**Prerequisites:** **M0138** (PG-faithful statistics) and M0137.

## Why this milestone exists now

`join-order` is the largest category on both corpora — **TPC-H 14, TPC-DS 89** —
and `METHODOLOGY3/04` gated it on the Question 2 answer. That answer is **yes**:
goopg reproduces PG's estimates, errors included. Q9 therefore stays in the
target and this milestone becomes scopeable.

Two facts to start from rather than rediscover:

- **The candidate half is already open at HEAD.** `joinsearchseam.go:461` calls
  `inferTransitiveEqualities` **unconditionally**, and R51 adjudicated both
  Slice-3 tests against PG. **K26 §9.3's "constants-only" description is a
  pre-R51 snapshot — do not schedule against it.** What remains is the
  **costing** half alone.
- **Opening candidates moved join-order by zero.** R51 landed the transitive
  equalities and measured TPC-H `join-order` **18->18**, TPC-DS **95->95**;
  K26 §9.2 confirmed it empirically: *the DP now enumerates PG's pairs but
  still prices other orders.*

Three attribution rounds converged independently on pricing — R53 and R68 on
Q9 (sizing, admission and parameterisation all ruled OUT; pricing IN), R96 on
Q96 (margin **157.50, 0.68%**; rows ruled out). And **every pricing round then
terminated blocked**: R70 on fragility, R89 C2 on missing inputs, R98
UNOBSERVABLE, R99 ORACLE INVALID.

That history is why this milestone opens with a recon rather than a campaign.

## Per-task discipline (READ FIRST — binding)

1. **Design note when the task is selected**, indexed in
   `docs/design/README.md` in the same commit. Overrides
   `docs/milestones/README.md` §"Workflow Per Milestone" step 2 for this group.
2. **The entry recon comes first and is measurement-only.** Its question is
   narrow: *is the pricing blockage still the same one it was before M0138
   changed every estimate?* A campaign that assumes the answer repeats the
   pattern that produced four blocked rounds.
3. **Margins here are fractions of a percent and the elections are exact.**
   Q9's L6 contest is 2.5%; Q96's L3 is 0.68%; `add_path` uses a 1% fuzzy
   comparator but **`setCheapest` takes the exact raw minimum on both engines**.
   Instrument the term — never infer it from the sum (K61/K64 were both wrong
   for exactly that reason).
4. **Re-measure Q9 against R130's table** as an explicit task. goopg's estimate
   moving *toward PG's 60,125* is the empirical test of the Question 2
   decision; if it does not move after M0138, that is the finding and this
   milestone's premise must be re-examined before slices are scoped.

## Definition of Done

- The entry recon has a documented verdict on whether the pricing blockage is
  unchanged, with M0138's new statistics in place.
- Q9's estimate is re-measured and reported against R130's table (actual 175;
  pre-M0138 goopg 97; PG 60,125).
- Costing slices filed by the recon are each landed or carry a ledger row with
  a resume point.
- `join-order` category movement is reported on both corpora with `shape-delta`
  alongside; the non-regression floor holds.
