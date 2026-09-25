# Parameterized-path legality: `required_outer` at rel level (M0145-0010)

Status: recon + design, 2026-09-21, REVISED the same day. Scopes (a)/(b)/(c)
unimplemented. **Both premises the task was filed on turned out to be wrong** —
see "Scope (b) has no consumer in goopg" below. The task needs owner re-scoping
before any of it is built.

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


## Scope (b) has no consumer in goopg (measured 2026-09-21)

The previous section recommended measuring scope (b)'s population before
building it, on the grounds that it is "not a lateral feature" and might carry
the justification scope (a) lacks. That measurement is done, and it says the
opposite of what was hoped — twice over.

### The raw surface is large

Widening the `NLIGATE` census from SEMI/ANTI to every jointype (it had been
scoped to semijoins for M0145-0007 and so reported nothing about the general
population — a blind spot of exactly the kind this milestone keeps finding),
TPC-DS SF0.25 gives:

| gate | fires |
|---|---|
| `no-parameterised-inner` | **15 014** |
| `filed` | 10 814 |
| `inner-rejected` | 1 625 |
| `jointype-right` | 77 |

15 014 decision points where the inner rel offered no parameterized path at
all. At first reading that is scope (b)'s population and it is enormous.

### But that is the wrong attribution

`reparameterize_path` does not create parameterized paths for a relation. It
RE-PRICES an existing path under a LARGER `required_outer`, and refuses outright
if the request is not a superset
(`pathnode.c:4249`: `if (!bms_is_subset(PATH_REQ_OUTER(path), required_outer)) return NULL`).

The paths that populate `cheapest_parameterized_paths` in the first place come
from `create_index_paths`' join half (`indxpath.c:446-544`) — **which goopg
already ports**, in `pathparamindex.go`. So the 15 014 are cases where no
parameterized path EXISTS for the inner rel, i.e. no usable index clause. PG
would find nothing there either, and `reparameterize_path` would not be called.

### And its real callers are a feature goopg does not have

The task text says `reparameterize_path` is "invoked FROM `joinpath.c`". It is
not. Its callers upstream are:

- `get_cheapest_parameterized_child_path` (`allpaths.c:2096`) — **appendrel
  CHILD paths, i.e. partitionwise joins**;
- its own recursion for `Append`/`Material`/`Memoize` subpaths
  (`pathnode.c:4328,4352,4364`).

goopg has no partitionwise joins at all — `pathparam.go`'s own comment already
records it: "goopg's search has no partitionwise counterpart — every relid in a
RelSet is a top-level base relation". So scope (b)'s primary upstream consumer
does not exist here, and porting it would be building machinery for a feature
the planner does not implement.

## Conclusion: both premises were wrong

| premise as filed | measured |
|---|---|
| scope (a): "8 corpus fires" for the `lateral` family | **2** |
| scope (b): `reparameterize_path` "invoked FROM joinpath.c" | invoked from `allpaths.c` for **partitionwise children**, which goopg lacks |

What survives is scope (c)'s structural argument, which needs neither: replacing
four hand-synced jointype whitelists with a derived test is worth doing because
hand-synced gates drift, and the 2026-09-21 wrong answer is the standing
evidence. But that argument does not require the rel-level parameterization
refactor scopes (a) and (b) describe, and it is largely satisfied already by the
shared predicates the two widening loops introduced.

## Recommendation (owner decision, not the loop's)

Re-scope or close M0145-0010. Concretely, one of:

1. **Narrow it to the lateral admission alone** — 2 fires, honestly priced, and
   judged on whether Q30/Q68 matter enough on their own;
2. **Defer it behind partitionwise joins**, since that is what makes
   `reparameterize_path` meaningful upstream;
3. **Close it**, recording that scope (c)'s structural intent was met by the
   shared jointype predicates and that the remaining scopes have no measured
   consumer.

The loop does not choose among these. What it can say is that starting the
refactor as filed would be building for consumers that measurement says are not
there.
