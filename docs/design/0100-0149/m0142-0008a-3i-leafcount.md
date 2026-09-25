# M0142-0008a-3i-leafcount — increment (i) is not implementable at Phase B, for the same reason as the Lateral case

Status: MEASURED 2026-09-20 — increment (i) redirected; `M0142-0008a-3`'s two
remaining increments now both depend on the route-order task
Kind: recon
Parent: M0142-0008a-3
Milestone: M0142
Evidence: `analysis/m0142/m0142-0008a-3i-leafcount-probe.txt`

## 1. What was left selectable, and what it assumed

After `M0142-0008a-3i-reach-verify` and `M0142-0008a-3i-lateral`, exactly one
thing remained selectable under `M0142-0008a-3`: **increment (i) leaf
admission**, to be measured on **Q16, Q94 and Q35** — the three witnesses that
decline at `leaf-count` rather than at `lateral`.

Increment (i) is filed as: *"make the decorrelated RHS a real DP-search
participant (extend `runJoinSearchBelowPinned` to walk the pinned join's RIGHT
child too, give its base rel(s) `RelSet` bits in the same per-search-call
numbering as the LHS `bindings`)"*. That wording assumes the **LHS is already
decomposed into base rels** and only the RHS is missing.

The check it must satisfy is `joinsearchseam.go:325`:

```go
if len(scans) != nprefix+len(semiAnti) {
        traceSeamDecline("leaf-count", nrels, len(scans))
```

## 2. Measurement

Throwaway instrumented build (probe reverted, not committed), private `:5595`
lane, `GOOPG_PGSHAPED_DP_TRACE=1`, `EXPLAIN` only:

```
Q16  nrels=4 nprefix=4 scans=3 semiAnti=2      Q35  nrels=3 nprefix=3 scans=2 semiAnti=1
       scan[0]=*optimizer.Project                     scan[0]=*optimizer.NestedLoopIndexJoin
       scan[1]=*optimizer.SeqScan                     scan[1]=*optimizer.Gather
       scan[2]=*optimizer.SeqScan
Q94  nrels=4 nprefix=4 scans=3 semiAnti=2  (identical to Q16)
```

Two things fall out, and neither matches the filing.

**(a) The gap runs the wrong way, and it is large.** The check wants
`scans == nprefix + semiAnti`: 6 for Q16/Q94, 4 for Q35. It gets 3 and 2. The
shortfall is **3 and 2 leaves**, not the single missing synthetic RHS leaf the
increment was scoped to add. Adding the RHS as a participant would take Q16
from 3 to 5 against a required 6 — still a decline.

**(b) The "leaves" are already-planned composite subtrees.** `scan[0]` for
Q16/Q94 is a `*Project`; for Q35 the two leaves are a
`*NestedLoopIndexJoin` and a **`*Gather`**. A Gather is not something a join
search can treat as a base relation — it is a node that only exists *after*
planning, complete with a chosen worker count.

## 3. The unifying root cause

This is the **same finding** as `M0142-0008a-3i-lateral`, generalised.

Phase B searches a tree that Phase A has **already planned**. In the Lateral
case the consequence was that a *dependency* had been lowered out of searchable
form (`RequiredOuter` → `OuterColumnRef{Level: 1}` + `BindLateralOuter`). Here
the consequence is that the *relations themselves* have been lowered out of
searchable form: four FROM items arrive as three built subtrees, one of which
may be a Gather.

`leaf-count` and `lateral` are therefore two symptoms of one thing, and the
`leaf-count` one is not the milder of the two — a `*Gather` leaf is further
from a base rel than a Lateral marker is from a `RequiredOuter`.

PG has no equivalent because it never interleaves the two: one DP search over
the flattened join list, with `create_plan` running only after the best path is
chosen (`postgres/src/backend/optimizer/plan/planner.c:441`).

## 4. Consequence for M0142-0008a-3

**Increment (i) is not implementable at Phase B for any of the five
witnesses.** Q69/Q10 are blocked by the lowered dependency; Q16/Q94/Q35 by the
lowered relations. Implementing (i) as filed would add the RHS participant and
still decline, on every query the census named.

Increment (ii) — legality wiring and retiring
`runJoinSearchBelowPinned`'s splice-and-reresolve path — is downstream of (i)
and inherits the same blocker.

So `M0142-0008a-3` does not have a selectable increment left. Both now depend
on **`M0142-0008a-3i-lateral-route`**, whose scope this doc widens from "Q69
and Q10's Lateral dependency" to "Phase B must search before Phase A lowers
anything — relations as well as dependencies".

That task keeps its id (it is the same route-order change) and its overlap with
**M0144-0003**, the owner-prioritised route-order verification.

## 5. What was NOT done, deliberately

No production code changed. The probe that produced §2 was a throwaway build
reverted before commit — it is the same technique the M0144-0011a-3 loop used,
and it is not a `Kind: impl` change because nothing was kept.

Increment (i) was not implemented "as far as it goes". A partial that adds the
RHS participant and still declines on 5/5 witnesses is not a coherent green
stopping point; it is code with no consumer, which is what the
`unwinnable path is an untested path` lesson in this repo's own instincts warns
against.

## 6. Reporting

`Movement: none` — a measurement and a redirect; no production code changed and
no parity arm was run against a change.
