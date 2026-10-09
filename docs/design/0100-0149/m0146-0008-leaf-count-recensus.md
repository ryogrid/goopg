# M0146-0008 — leaf-count residual re-census: the filed population is gone

Status: complete 2026-09-28 — closes by measurement.

## Mandate

M0146-0008 is M0144-0003a's successor. The pre-flip censuses
(`m0144-0003a-opaque-leaf-census`, `m0144-0003a-project-descent-refuted`)
established that all 26 `leaf-count` seam declines across 11 SF0.25 TPC-DS
queries were opaque `*Project` leaves wrapping already-planned composites
(`*Gather`, `*CTEScan`, `*Filter`, `*Join`, `*NestedLoopIndexJoin`) — proof
that no leaf-admission predicate could fix them, and the fourth independent
confirmation of the route-order thesis. The named wall (M0145-0003's
jointree-level pull-up) landed and the M0145-0008 cutover made the jointree
arm the only pipeline. This task re-censuses the population on that arm.

## Method

HEAD `81abd893a`, private `cp -a` clone of
`bench/tpcds/runtime_goopg/data-sf025` on :5591,
`GOOPG_PGSHAPED_DP_TRACE=1`, all 99 query files EXPLAINed
statement-by-statement with per-query log-delta attribution. A temporary
probe at the `leaf-count` decline (joinsearchseam.go:414) printed each scan
leaf's Go type, `Join.Type`, `nprefix` and `nSynthetic`; reverted before
commit — no production change landed.

## Result

| corpus | leaf-count declines | opaque-leaf kinds |
|--------|--------------------|-------------------|
| pre-flip (M0144-0003a) | 26 over 11 queries | `*Project` 53, `*SeqScan` 4, `*NestedLoopIndexJoin` 1, `*Gather` 1 |
| jointree arm, HEAD | 2 (Q51, Q97) | `*Join` with `Type == JoinTypeFull` |

The already-planned-composite leaf population the task was filed to
re-census is **zero** — jointree pull-up feeds the seam real FROM-item
leaves instead of finished plan fragments. The `*Project` class is extinct,
not merely reduced.

## The residual belongs to the FULL-join pin

Both surviving declines are `nrels=2 nleaves=1` with a single
`*Join{Type: JoinTypeFull}` leaf:

- Q51 — `web_v1 FULL OUTER JOIN store_v1` (over CTE references).
- Q97 — `ssci FULL OUTER JOIN csci`.

`extractSearchLeaves` appends any join type outside
{Cross, Inner, Left, Right, Semi, Anti} as one opaque leaf
(joinsearchseam.go:1808-1813), so a two-relation FULL chain counts 1 leaf
against `nprefix=2`. This is the designed decline: `joinPinned` pins FULL
(and outer-over-FULL) precisely because the DP search has no ordering
freedom for it, and the file header names leaf-count as its decline site.
At `nprefix=2` the fall-back is cost-free — the syntactic tree is the only
possible order and join-method selection still runs on it. Making FULL
chains searchable is a `joinPinned`/pin-semantics task, not leaf admission;
it is not filed here because the census was scoped to the leaf population,
and the pin wall is already documented in `joinsearchseam.go` and the
M0145-0005 design trail.

## Verdict

`Movement: none` — no production change; the measurement instrument for
this task is the decline census itself (26 → 2), which is not one of the S3
instruments. M0146-0008 closes: there is no leaf/admission work left that
the census can see.
