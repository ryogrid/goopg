# C-08 (P3-07) — `param_source_rels` derivation + star-schema semantics

Status: accepted. Implements TODO_ALL.md C-08.

## 1. Objective

Replace the constant-empty `paramSourceRels()` with PG's per-joinrel
derivation, keeping the `allow_star_schema_join` escape already
implemented. Gate: take3 09 §5 P3 (PP both suites).

## 2. Oracle

`postgres/src/backend/optimizer/path/joinpath.c:227-263` (quoted in
full — the rule is small enough to port verbatim):

- Start empty. For each sjinfo in `join_info_list`: if the joinrel
  overlaps `min_righthand` but not `min_lefthand`, add
  `all_baserels − min_righthand`. FULL constrains symmetrically
  (overlap LHS but not RHS → add `all_baserels − min_lefthand`).
- Union the joinrel's `lateral_relids`.
- Rationale (same comment): no parameterised result unless a join-order
  restriction prevents joining an input rel directly to the parameter
  source (else the restriction bounds higher-level parameterisations
  for free). `allow_star_schema_join` (`joinpath.c:363`, already
  `allowStarSchemaJoin` in goopg): partially-satisfied inner (outer
  supplies SOME but not all of the parameterisation) is still admitted
  — refusing costs a plan (fact probed by two dimensions joined at
  different levels) rather than saving work.

## 3. What the tree already does (do not re-build)

- `paramSourceRels() RelSet { return 0 }`
  (`internal/optimizer/joinpathsnli.go:44`) with a documented
  derivation (03 §4.4 pins outer/semi/anti outside the search →
  `join_info_list` empty for searched problems → PG's loop yields
  empty). That derivation is now STALE in one respect: C-01 populates
  Min/Strict and the FULL `ctx.joinInfoList` reaches prefix problems
  (`joinsearchseam.go:395`), so mixed comma+outer problems CAN yield
  non-empty under PG's rule (prefix joinrel overlapping an outer SJI's
  RHS but not its LHS). The constant is still right for pure-inner
  problems, wrong in general.
- `allowStarSchemaJoin` implemented + used by the NLI arm.
- Consumption points (3): NLI admission
  (`internal/optimizer/joinpathsnli.go:217`), merge admission
  (`internal/optimizer/joinpathsmerge.go:345` inside
  `tryMergeJoinPath` — the mergeouter path routes through the same
  function). NLI has a SECOND, goopg-only gate refusing ALL
  still-parameterised results (`joinpathsnli.go:220-232`, P5.6 ledger),
  so NLI never emits a parameterised join either way. Merge refuses
  still-parameterised results at the same overlap test (no star-schema
  escape — matches PG).
- goopg sets for the derivation: `all_baserels` = problem initials per
  the §4 remap (provably equivalent, not substituted silently);
  `lateral_relids` = 0 with anchored reasons, not a blanket claim:
  LATERAL plan joins are marked (`planner.go:2261-2270` INNER,
  `:2715` comma CROSS, `:3044` JOIN build, all via
  `nodeReferencesOuter`) and declined on every road into the search —
  `chainCarriesLateral` at seam entry (`joinsearchseam.go:240`;
  CROSS/INNER flags checked, all other shapes fall to
  `nodeReferencesOuter`), `spineLinkSearchable` (`:548-560`,
  `!j.Lateral && !nodeReferencesOuter(j.Right)`), non-spine outer
  links via `extractSearchLeaves`/leaf-count mismatch. Revisit with an
  explicit per-rel lateral set if LATERAL ever enters the search —
  never silently.

## 4. Change (one commit, one planner input)

Frame rule first, because it decides everything: SJI Min hands are
STATEMENT-leaf-global (`sjiScope` leaves run consecutively across comma
items; `joinlistRelSet` over leaf indexes), but each sub-problem
renumbers its items to `1<<i` (`searchOneProblem` → `buildInitialRels`)
while receiving the statement-global `prob.joinInfoList` unchanged.
Comparing the two frames directly admits and refuses wrongly (the same
exposure `joinIsLegal`/`joinOrderRestricted` already carry — pre-existing,
not mine to fix; a new derivation must not add a new wrong-decision
site). The derivation therefore REMAPS, with an exactness precondition:

- Let `lo = items[0].lo`, `n = len(items)`. Require every item to span
  exactly one consecutive statement leaf (`items[i].lo == lo+i &&
  items[i].hi == lo+i+1` — `joinlistRel.lo/hi`, contiguity invariant);
  else return 0 (today's constant). The collapse-ON flattened prefix
  (`lo == 0`, enforced `joinsearchseam.go:227`) always qualifies;
  everything else keeps legacy behaviour.
- Remap: `min >> lo`, masked to `n` bits; `allBaserels` = problem
  initials (`(1<<n)-1`); `joinrelids` are already problem-frame.
  EXACT (not merely fail-closed): dropped outside bits can never change
  an overlap verdict for problem-internal joinrelids/req, and
  `(global−minR) ∩ problem == problem−(minR ∩ problem)`, so the
  admission outcome equals PG's on aligned problems. Proof sketch
  pinned by unit (aligned problem vs statement-global hands fixtures).
- `paramSourceRelsForProblem(joinrelids, joinInfoList, items) RelSet`
  — PG's loop verbatim on remapped hands (RHS rule + FULL symmetric
  rule; lateral union is 0 by the §3 invariant). Nil/empty list → 0.
- Compute ONCE per `addPathsToJoinrel` call and pass the `RelSet` down
  (PG computes per call, `joinpath.c:242-276`); no per-arm
  recomputation. `s.joinInfoList` is already on `searchCtx`
  (`joinsearch.go:167-172`); `addNLIPaths`/`addPathsToJoinrel` already
  take `s` — only `tryMergeJoinPath` needs the set threaded in (its
  sibling closures already close over `s`).
- `allowStarSchemaJoin` untouched (already PG-faithful).
- "Only baserels, not OJ relids" (PG `:238-240`) holds vacuously:
  goopg `RelSet` is base-leaves-only with `ojrelid ≡ 0`
  (`specialjoin.go:94-96,118`; the "base+OJ relids" struct comments
  describe PG's domain, not goopg's). PG's merge-side ojrelid refusal
  (`:1066-1069`) is vacuous for the same reason — named, not implied.
- Observable surface (stated, not hidden): TPC-DS is NOT pure-inner
  (all 12 explicit-JOIN queries contain an outer join; Q72's inner
  prefix sits under a spine) — mixed comma+outer shapes are
  IN-CORPUS, so parameterised MERGE paths may be admitted there and PP
  moves are a live possibility with a mechanical trigger (any PP move
  → values sweep; §6). Pure-inner problems: derivation provably empty
  → zero moves. NLI's second gate keeps NLI unparameterised regardless.

## 5. Safety contract

Fail-closed throughout: an SJI the derivation cannot read exactly
(nil hands? — impossible post-C-01, but code defensively) contributes
nothing (skip, never widen). The FULL arm is ported though FULL
declines the search (dead in production, pinned by unit). Lateral
union is 0 by stated invariant, not by omission.

## 6. Gate

Unit (derivation table: empty list→0; partial-RHS overlap→
`all−RHS`; FULL symmetric; lateral union; star-schema escape
unchanged; remap-exactness: aligned problem equals statement-global
hands; misaligned/gapped items → 0) + optimizer/executor suites +
RALPH units scope + PP both suites at join-method granularity
(structural pin; shape-identity ⇒ no-execution-change holds at that
granularity — stated assumption) + spotcheck. TPC-DS values sweep is
CONDITIONAL but LIVE (not dismissed): any PP move triggers it, because
parameterised merge paths admitted on mixed comma+outer shapes execute
differently when chosen.
