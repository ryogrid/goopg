# M0142-0008a-3i-lateral — may a chain carrying goopg's OWN Phase-A Lateral splice be searched?

Status: DECIDED 2026-09-20 — **no, not as it stands**; `chainCarriesLateral`'s
refusal is correct rather than conservative, and Q69/Q10's route is route-order,
not a gate widening
Kind: recon
Parent: M0142-0008a-3
Milestone: M0142
Predecessor: `docs/design/0100-0149/m0142-0008a-3i-reach-verify.md`

## 1. The question

`M0142-0008a-3i-reach-verify` measured TPC-DS **Q69** and **Q10** declining at
`chainCarriesLateral` (`joinsearchseam.go:330`) rather than at `leaf-count`,
over a `Lateral` join **no user wrote** — neither query contains the word
`LATERAL`. The arm that catches it was added deliberately by
`M0142-0008a-3i-plumbing-c14`.

So: is that refusal a real correctness boundary, or is it conservative scope
that could be widened the way `M0137-0019b` widened the partial-NL jointype
gates?

## 2. Where the Lateral join comes from

`predp.go`'s pre-DP unnest runs **two** searches:

- **Phase A** searches `origChain` — the Semi/Anti join's inner chain — and
  splices its winning tree in (`predp.go:153-162`).
- **Phase B** then searches from the outermost pinned Semi/Anti join,
  "attempted AFTER Phase A's splice above so it sees the … searched
  `origChain` already in place" (`predp.go:164-167`).

Phase B's call is the one that declines. What it sees inside the Semi/Anti's
`Left` is Phase A's **built plan**, and when Phase A's winner contained a
parameterised index probe, `createNestLoopIndexJoinPlan` has already lowered
it (`createplannl.go:364-383`):

```go
j := &Join{
        Type: jtNLI,
        Algo: JoinAlgoNestedLoop,
        // Lateral marks the right child as referencing the left row:
        // the driver re-opens it per outer tuple with that tuple in
        // scope (BindLateralOuter contract, plan.go). Without it the
        // generic nestloop would Materialize the probe once and replay
        // it — returning every row for the first outer tuple's key.
        Lateral:   true,
        Left:      in.outer,
        Right:     is,   // *IndexScan whose Keys are OuterColumnRef{Level: 1}
        ...
}
```

## 3. The decision, and why it is correctness and not scope

**The refusal is correct. Do not widen `chainCarriesLateral`.**

The probe's dependency on its outer is, at this point, expressed as
`OuterColumnRef{Level: 1}` on `is.Keys` plus the `BindLateralOuter` execution
contract — a **positional, immediate-parent binding**. Reordering the chain so
the probe's outer is no longer its immediate left silently breaks that binding;
the comment above says what happens if the marker is merely dropped ("returning
every row for the first outer tuple's key"). A search that may reorder across
it is a search that may produce wrong rows.

Nothing at Phase B's layer can re-lift the dependency into a searchable form,
because the form that *is* searchable — a path's `RequiredOuter` — was consumed
when Phase A built its plan. This is the same class of problem
`M0142-0008a-3i-recon2` already flagged as unresolved for
`reresolveJoinByName`'s post-search splice, and it is not made easier by the
Semi/Anti work.

So `-plumbing-c14` did not add a conservative guard; it closed a real hole. The
contrast with `M0137-0019b` is exact and worth stating: there, four gates all
said in their own comments that the refusal was scope-minimisation, and the
executor was independently shown to be worker-local. Here the refusal protects
a binding the chain genuinely depends on.

## 4. What PG does, and why it never has this problem

PG has no phases. `query_planner` runs one DP search over the whole flattened
join list; a semi-join is a `SpecialJoinInfo` the search reads, and a
parameterised path's dependency lives in `param_info`
(`postgres/src/backend/optimizer/util/pathnode.c:188, 293`), which `add_path`
and the join-order search consult directly. Plan construction runs **after**
the search finishes — `top_plan = create_plan(root, best_path)`,
`postgres/src/backend/optimizer/plan/planner.c:441`.

So upstream never lowers a dependency to a positional binding while a search
that might reorder it is still to come. goopg's two-phase pre-DP unnest is the
divergence, and it is the thing that makes Q69/Q10 unsearchable — not the gate.

## 5. The actual route for Q69 and Q10

Phase B must see the chain **before** the dependency is lowered, or Phase A's
result must retain a path-level `RequiredOuter` that Phase B's search can read.
Either is a **route-order** change — which is the subject the owner already
prioritised as **M0144-0003** ("several landed fixes never moved a plan because
goopg's processing route diverges from PG's upstream of the fix", banner item
2) — and it is structurally larger than anything in the `-3i-plumbing` chain.

Filed as **M0142-0008a-3i-lateral-route** rather than attempted here: it
changes when plans are built relative to when they are searched, which is a
planner-architecture decision and not a gate edit.

## 6. What this leaves selectable under M0142-0008a-3

Increment **(i) leaf admission**, measured on **Q16, Q94 and Q35 only** — the
three that decline at `leaf-count`. Q69 and Q10 are out of its reach for the
reason above, and the reach-verify doc already warns that measuring (i) against
Q69 would read as the increment failing.

Whoever lands (i) must also close the P0-H11 `cumulativeFromSpans` span
round-trip in the same change: it is the task that first lets a link through,
and the audit named it a latent correctness gate for exactly that moment.

## 7. Reporting

`Movement: none` — a decision and a design note; no production code changed and
no parity arm was run against a change.
