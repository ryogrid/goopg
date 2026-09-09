# R26/3 — decline re-audit after R27: 7 blocked queries → 5

*2026-09-09. K29 step three begins by re-attributing, because Q49's
cause is fixed and assuming the survivors share it would be wrong.*

## 1. Before / after

| query | before R27 | after R27 |
|---|---|---|
| **Q49** | 3 × `outer-link-no-sjinfo` | **admitted** |
| **Q93** | 1 × `outer-link-no-sjinfo` | **admitted** |
| Q78 | 4 × (`outer-link-no-sjinfo`, `outer-over-derived`) | unchanged |
| Q77 | 2 × `outer-over-derived` | unchanged |
| Q51 | `outer-spine` | unchanged |
| Q97 | `outer-spine` | unchanged |
| Q68 | `lateral` | unchanged |
| **total** | **13 declines / 7 queries** | **9 declines / 5 queries** |

TPC-H remains **0 declines**.

## 2. What that means

Two queries moved from *ineligible* to *eligible*. That is the axis
category counts cannot see (`METHODOLOGY.md` §3): before, no amount of
costing work could have brought Q49 or Q93 toward PG's plan, because
they never entered the PG-shaped search. Now they can lose on cost like
the other 92 — which is progress even though neither matches yet.

Q49 also improved on the visible axis: 3 `Hash Left Join` → 0, with 5
of its 6 join nodes now carrying PG's node type.

## 3. Why the survivors are NOT the same problem

`outer-link-no-sjinfo` still appears, on Q78 — but Q49's instance of it
is fixed, so the remaining one has a *different* cause and must be
diagnosed on its own evidence. Assuming otherwise is the error this
workstream keeps recording: a shared symptom is not a shared cause.

The three remaining reasons, and what is known about each:

- **`outer-over-derived` (3: Q77 ×2, Q78)** — the largest group now.
  Unexamined.
- **`outer-spine` (2: Q51, Q97)** — unexamined.
- **`outer-link-no-sjinfo` (1: Q78)** — the residue of the group R27
  addressed; re-diagnose from scratch rather than extending R27.
- **`lateral` (1: Q68)** — the same guard the Q30 CTEScan fix touched;
  check whether it is another bound-ref-read-as-escaping case before
  assuming a genuine LATERAL.

## 4. Next

Take `outer-over-derived` first: it is the largest group, and Q77 is
the cleaner subject (2 declines, one reason, no interaction with the
others). Instrument before theorising — the trace channel has answered
every question in this family in one run.
