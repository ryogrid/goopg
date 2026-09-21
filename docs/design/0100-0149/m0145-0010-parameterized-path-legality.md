# Parameterized-path legality: `required_outer` at rel level (M0145-0010)

Status: recon + design, 2026-09-21. Scopes (a)/(b)/(c) unimplemented; scope (c)'s
jointype half was landed separately (see "What already landed") and scope (d) is
a standing rule, not a deliverable.

Task: `.ralph/fix_plan.md` M0145-0010. Parent: none. Kind: impl (this document
is its recon).

## The objective, stated precisely

PG decides nested-loop legality from **parameterization**, not from an
enumerated jointype set. goopg does the opposite: it carries a jointype
whitelist at four gates, and it refuses LATERAL statements wholesale before the
join search ever runs. The task is to replace the enumeration with the
derivation.

## What already landed, and why it is NOT the task

Two loops (2026-09-21) widened the jointype whitelists to
`{INNER, LEFT, SEMI, ANTI}` across both partial nested-loop families, matching
PG's nestloop dispatch set (`joinpath.c:1842-1846`) minus RIGHT/FULL. Recorded
in `docs/design/parallel-query/09-verification-and-measurement.md`.

That work was worth doing on its own terms — it found and fixed a live
wrong-answer defect, and closed a gate drift — but it must not be mistaken for
this task. **goopg now reaches PG's answer by an enumerated set while PG derives
it.** The enumeration is still there; it is merely wider.

## Gap 1 — no rel-level parameterization at all

`RelOptInfo` carries `Relids`, `Rows`, `Width`, `NCols`. It has no
`lateral_relids`, no `direct_lateral_relids`, no `param_info`. PG's
`join_is_legal` reads the first two directly (`joinrels.c:569-596`):

```c
lateral_fwd = bms_overlap(rel1->relids, rel2->lateral_relids);
lateral_rev = bms_overlap(rel2->relids, rel1->lateral_relids);
if (lateral_fwd && lateral_rev)
    return false;              /* refs in both directions — impossible */
if (lateral_fwd) {
    /* must be a nestloop with rel1 on the left */
    if (match_sjinfo && (reversed || unique_ified ||
                         match_sjinfo->jointype == JOIN_FULL))
        return false;          /* not implementable as a nestloop */
    if (!bms_overlap(rel1->relids, rel2->direct_lateral_relids))
        return false;          /* only INDIRECT refs — reject */
}
/* mirrored for lateral_rev */
```

Two distinct sets are needed, not one: the transitive `lateral_relids` decides
direction, and `direct_lateral_relids` rejects an indirect reference whose
intermediate rels must be joined first. goopg's `RelSet` maps onto
`bms_overlap` directly, so the predicate itself ports cleanly.

## Gap 2 — the lateral refusal is a statement-level veto

`joinsearchseam.go:477`: if `chainCarriesLateral(chain)` the seam declines the
WHOLE statement and the join search never runs for it. There is no per-pair
legality question asked at all, because there is nowhere to ask it.

The marker that drives this lives on the `*Join` chain node (`j.Lateral`), and
the flattening that builds the leaf array **discards that node** — which
`chainCarriesLateral`'s own comment states. So the hard part of scope (a) is
not adding the fields; it is deriving, at leaf-extraction time, which leaf
references which, before the carrier is thrown away.

## Gap 3 — no `reparameterize_path`

`Path.RequiredOuter` exists and the join calculators are ported
(`calcNestloopRequiredOuter` = `calc_nestloop_required_outer` pathnode.c:2592;
`calcNonNestloopRequiredOuter` = :2618), along with the three parameterized-path
discipline rules (`pathparam.go`). What is missing is
`reparameterize_path`/`get_param_path_clause_serials` (pathnode.c:4242, :1910,
invoked FROM joinpath.c): re-pricing an EXISTING path under additional outer
parameterization. Without it, a path that could serve a more-parameterized
consumer is simply not offered.

## The census, re-measured — and the earlier figure was stale

The task text and the ledger both cite "8 corpus fires" for the `lateral`
decline family, from `tmp/m0144-0011a2-census.log` (pre-slice-3, when the seam
declined 132 times in total). Re-measured today on TPC-DS SF0.25:

| decline reason | fires |
|---|---|
| `leaf-count` | **26** |
| `semianti-not-tail` | 3 |
| `outer-over-derived` | 3 |
| `outer-spine` | 2 |
| **`lateral`** | **2** |
| total | **36** |

The lateral family shrank from 8 to **2** as other milestone work landed, and
the total from 132 to 36. **`leaf-count` now dominates at 26 of 36** — and that
class is ledgered as executor-substrate-blocked (FULL-join folds), not as
something this task can reach.

## What this means for sequencing — and the honest value case

Scope (a) as motivated by the `lateral` family is now a **2-fire** consumer.
That is a weak justification on its own and this document should not pretend
otherwise.

But the task's value does not rest solely on that count, and the distinction
matters:

- **Scope (a)'s lateral half** buys 2 corpus fires directly. Weak.
- **Scope (b), `reparameterize_path`,** is not a lateral feature at all. It
  governs whether an existing path can be offered to a more-parameterized
  consumer, which is general NLI admission. Its consumer population has never
  been measured, and it should be before it is built.
- **Scope (c)** — legality derived rather than enumerated — is the
  faithfulness objective. Its value is structural: it removes four whitelists
  that must be kept in sync by hand, and the 2026-09-21 wrong answer is the
  standing evidence for what hand-synced gates cost.

## Recommended slicing

1. **Measure scope (b)'s population FIRST** (recon, cheap): instrument the
   points where a parameterized path is refused for want of re-pricing, and
   count corpus fires. If it is also ~2, the whole task should be escalated for
   re-scoping rather than built.
2. Only then scope (a): derive per-leaf lateral relids at extraction time —
   both the transitive and the DIRECT set — and store them on the leaf rel.
   This is the real work and it is where the carrier-discarded problem lives.
3. Port PG's legality block into `joinIsLegal` (`joinsearchlevel.go:198`),
   reading those sets.
4. Lift `chainCarriesLateral`'s statement-level veto LAST, so the per-pair rule
   is in place and tested before the blanket refusal is removed.
5. Scope (b) proper, extending `Path.RequiredOuter` rather than building anew.

Scope (d) binds throughout: for every newly admitted shape, verify executor
capability by MEASUREMENT before admitting it. The two whitelist loops both
found N-copy signatures that way, one of them a live defect.

## Escalation

The task was filed with an 8-fire lateral consumer that is now 2. Whether that
still justifies a rel-level refactor is a scoping decision for the banner's
owner, not for the loop. Step 1 above is the cheap measurement that would inform
it, and is what the next loop should do rather than starting the refactor blind.
