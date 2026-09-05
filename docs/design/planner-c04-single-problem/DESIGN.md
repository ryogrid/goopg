# C-04 (P3-04) — Delete `splitOuterSpine` + `pinnedOuter()` decline

Status: accepted. Implements TODO_ALL.md C-04. After B-01 (done).
**Implementation starts after C-03a/b/c land** (jointype-aware path
generation — see §4 prerequisite; this design may be reviewed and
committed before that, but no code moves until paths carry jointypes).

## 1. Objective

Mixed comma + `LEFT JOIN` FROM becomes **one** search problem (Q72
witness): delete `splitOuterSpine` + `pinnedOuter()` decline so outer
links enter the search and `join_is_legal` (C-01/C-03) governs them.
**Unblocks P1-18 (C-05).** Gate: take3 09 §5 P3 — PP both suites +
timing.

## 2. Oracle

`deconstruct_jointree` builds one jointree incl. outer joins
(initsplan.c:1190-1248); `make_rel_from_joinlist` hands pinned joins to
`make_join_rel`/`join_is_legal`, which honour `jointype`. No peel, no
spine, no per-type decline — legality is entirely in the SJI test.

## 3. What the tree does today (delete list with reasons)

1. `splitOuterSpine` (`internal/optimizer/joinsearchseam.go:502`) +
   `innerPrefixBelowOuterSpine` (`internal/optimizer/collapse.go:311`):
   peel pinned outer links off the chain top; search the inner prefix;
   re-stack the spine above. Delete both (spine walk, agreement check,
   `spineLinkSearchable`; `prefixNullable` hold replaced per §3.5).
2. `joinPinned` deconstruction pinning
   (`internal/optimizer/collapse.go:431,465-472`, unconditional for
   outer): relax for LEFT (then RIGHT in C-04b) to collapse-dependent,
   with the SJI attach site (`collapse.go:442`) riding along — WITHOUT
   this the pins stay in the joinlist and §3.3 hands the search nested
   2-way subproblems (governed but never one enumeration). Oracle
   fidelity: upstream pins only FULL.
3. `pinnedOuter()` decline in `makeRelFromJoinlist`
   (`internal/optimizer/relfromjoinlist.go:304`): pinned outer
   subproblem → whole-statement fallback. Delete the arm for the
   admitted types: hand the pin to the search like any other item (the
   comment there already states exactly this as the fix direction).
4. `extractSearchLeaves` INNER/CROSS-only gate
   (`internal/optimizer/joinsearchseam.go:615`): admit the slice's
   types (leaves flatten like inner ones; quals route via ON lists +
   `partitionConjunctsForJoinPlanning`; the non-zero-offset ON-qual
   decline (`joinsearchseam.go:631`, `base != 0`) extends to admitted
   outer links — misattributed coordinates are wrong answers — as does
   the stay-at-link requirement for outer ON quals). FULL stays
   declined; SEMI/ANTI stay on the `runJoinSearchBelowPinned` path.
   `chainCarriesLateral` (`joinsearchseam.go:659`) descends the
   admitted types and checks the `Lateral` flag on them (today only
   CROSS/INNER + `nodeReferencesOuter` fallback — an admitted lateral
   outer link must not reorder across its dependency).
5. `prefixNullable` whole-WHERE hold
   (`internal/optimizer/joinsearchseam.go:294-299,523`): replace with
   per-qual delay proof (C-02a `delayedAboveOJ` over C-01 SJIs,
   conjunctive along the spine — the spine IS a descent path). A qual
   that proves at every spine link distributes; the rest stays above.
   This is the C-02 payload the spine was holding. LOAD-BEARING for
   correctness, not a follow-up: the hold only ever fired on non-LEFT
   spines, so retaining it protects nothing on LEFT chains — without
   this, single-binding WHERE quals on the nullable side fall into
   `partitionConjunctsForJoinPlanning` locals (no nullable-side guard)
   and attach to leaves pre-search, evaluating BELOW the LEFT join
   (`t LEFT JOIN p … WHERE p.y > 5` keeps extra rows — wrong answers).
6. FULL/SEMI/ANTI non-slip (carve-outs done right — `pinnedOuter()`
   is one function, so "delete minus carve-outs" is incoherent as a
   code edit): FULL safety rests BINDINGLY on C-03b's pathgen
   FULL-decline (zero paths → empty pathlist → fallback) — §4 names it
   as prerequisite, not assumption; `joinPinned(FULL)` is RETAINED
   (tree side: FULL link → opaque leaf → leaf-count decline holds);
   SEMI/ANTI never slip because they are not parser-FROM-derivable
   into `ctx.joinlist` (parser produces only INNER/LEFT/RIGHT/FULL/
   CROSS) and the tree side descends below them via
   `runJoinSearchBelowPinned` (`predp.go:83-115`). If any of these
   stops holding, add an explicit narrow decline — do not rely on the
   deleted function.

## 4. Prerequisite (binding — do not start code before it holds)

C-03a/b/c landed: `Path.Jointype` exists, `addPaths` builds only the
legal direction per SJI orientation AND declines FULL (zero paths —
the §3.6 non-slip mechanism for FULL depends on it, not on any
refusal), `createPlanNode` emits the SJI jointype with left-only
SEMI/ANTI schemas. Without it, enumerated outer pairings build INNER
plans (dropped unmatched rows — wrong answers, the exact failure
`relfromjoinlist.go:304` exists to prevent). C-03d evidence may run in
parallel. Cross-jointype pruning answered: `setCheapest`/
`comparePathCosts` stay jointype-blind AND mixed-jointype pathlists
cannot co-occur on the admitted shapes yet (per-pair legality yields
at most one SJI per pairing; the enum-trace gate asserts it — a
pairing offering two jointypes voids the run as HARNESS FAULT, the
same discipline as P5.9-l-ii controls). C-05 (sizing switch) comes
AFTER, but not unguarded: C-04a carries a one-line LEFT/RIGHT floor
(rows ≥ preserved-side rows — INNER-math under-estimates LEFT joins
unboundedly below, `|L|·|R|·sel` vs true `≥ |L|`, inviting catastrophic
choices on exactly the newly searchable shapes). The floor needs the
sjinfo at sizing time: `sizeJoinRel` (`joinsearchlevel.go:54`, called
at `:570`) gains it (interface change with the test stub) — name it,
do not smuggle it.

## 5. Slices — vertical per-type cuts (one checkbox ≈ one commit)

Per-§3-item cuts are vacuous (C-04a as §3.1+§3.2 alone moves nothing —
outer links stay opaque → leaf-count decline → fallback) and unsafe
(C-04a without §3.5 misplaces nullable-side quals). Each slice below
is one join type admitted END TO END (peel + pinning + refusal +
leaf-flatten + lateral + locals-guard together), so every slice is
independently correct and independently gated.

- **C-04a — LEFT admission (Q72 witness).** §3.1 (LEFT spines) +
  §3.2 (LEFT pinning relax) + §3.3 (LEFT pins to search) + §3.4 (LEFT
  leaf-flatten + lateral descend + ON-qual stay-at-link) + §3.5
  (per-qual delay proof) + §4 floor. RIGHT/FULL/SEMI/ANTI decline
  exactly as today. Q72's mixed comma + LEFT JOIN becomes one
  11-leaf problem (`used==true`, DPPATH OFFERED/ACCEPTED on the LEFT
  pairings); join order may move (reported with cost roll-up). Gate:
  PP both suites + behavioral Q72 pin + enum-trace + R8 values-diff
  both suites (incl. a nullable-side single-relation WHERE fixture —
  Q72's WHERE is all prefix-side and cannot catch finding-1's shape)
  + timing arm (S-cold and WARM; name Q72's 5s–300s cap-straddle so a
  flip is read correctly).
- **C-04b — RIGHT admission.** Same vertical cut for RIGHT spines +
  below-inner + non-first-comma RIGHT links (§3.1–§3.5 with LEFT↔RIGHT
  mirrored; `prefixNullable`-equivalent reasoning per link, not whole
  WHERE). Gate as C-04a + enum-trace DPPATH for the newly offered
  pairings.
- **C-04c — below-inner + non-first-comma LEFT links.**
  Non-spine LEFT links (below an inner link, non-first comma items)
  admitted with the same per-link machinery. Gate as C-04a.

Out of scope: FULL execution (ledgered); SEMI/ANTI search entry (C-09
track); GEQO+outer interplay (pin one unit or decline explicitly at
C-04a — specify at implementation).

## 6. First-slice gate (C-04a)

PP both suites + behavioral Q72 pin (`used==true`, one 11-leaf
problem, DPPATH on LEFT pairings) + R8 values-diff both suites
(incl. nullable-side WHERE fixture) + timing (S-cold and WARM per
C-21 convention; server age held constant across arms). Bars: no
values-diff failure tolerated (outer joins move rows across the
null-extension boundary — counts lie, digests don't). GEQO+outer:
pin one unit or decline explicitly — a checkbox in the C-04a commit,
not prose (jointype flows via `freshEvalCtx`, `geqo.go:341-343`).
