# M0144-0003b-1 — carry each set operation's SETOP rel to the link above

Status: LANDED (inert on both corpora — admission-only, see §5)
Parent: the UNION-ALL-flattening item filed by M0144-0003 item (b)
Kind: impl

## 1. The filed premise, and what measurement said instead

M0144-0003b was filed as "goopg has no flattening — `addPartialSetOpPath`
produces at the `*SetOp` plan-node level and **admits 0/99 corpus-wide**",
with the implied scope of building an appendrel representation inside the
searched problem.

A probe of the producer's own gates refutes the premise's factual half.
Instrumenting `createSetOpPaths` on the TPC-DS SF0.25 corpus (private lane
`:5595`, db `postgres`) shows the producer **already admits**: on Q14, Q71
and Q76 the pure arm files a partial SetOp path, and on Q14 and Q71 the
`Gather` over it wins the link's tournament outright (`cand[0] kind=11`).
What fails is not admission — it is **composition**.

Observed on the baseline binary, one line per `createSetOpPaths` call:

```
Q14  left=*Project:rel(pp=1 cp=true) right=*Project:rel(pp=1)  chain=true/true  filed=1
Q14  left=*Gather:nil-rel            right=*Project:rel(pp=1)  chain=false/true filed=0
Q76  left=*Project:rel(pp=1 cp=true) right=*Project:rel(pp=1)  chain=true/true  filed=1
Q76  left=*SetOp:nil-rel             right=*Project:rel(pp=1)  chain=false/true filed=0
```

The INNERMOST link of every chain admits. Every OUTER link reports no rel at
all on the side that holds the rest of the chain, so `ConsiderParallel` goes
false and `addPartialSetOpPath` returns before either arm runs.

## 2. Why the outer link was blind

PG never meets the case. `pull_up_simple_union_all` (prepjointree.c:1617,
reached from `pull_up_subqueries`, planner.c:754) flattens
`a UNION ALL b UNION ALL c` into ONE appendrel with three children, so
`add_paths_to_append_rel` (allpaths.c:1321) sees every branch at once and a
single Parallel Append covers the whole union.

goopg builds a LEFT-DEEP `*SetOp` chain instead — one two-child node per
`UNION ALL` keyword — and prices each link on its own SETOP rel. The branch
accessor is `searchedRelOf` (searchedtree.go:169), which stops at any node
with more or fewer than one boundary child (:184). A `*SetOp` has two. So
the inner link's rel — the one that just successfully filed a partial path —
was unreachable from the link above, and died there.

The same hole swallowed the `*Gather` that `createSetOpPaths` returns when
the inner link's partial path wins: it is a plain wrapper to `searchedRelOf`,
which descends past it into the `*SetOp` underneath and stops there too.

## 3. The change

`setopbranchrel.go` adds `setOpBranchTag`, the same mechanism
`searchedTree` already uses to carry the join search's upper rel out on the
node it publishes:

- `*SetOp`, `*Gather` and `*GatherMerge` — the three kinds
  `createSetOpPaths` can return — embed the tag.
- `createSetOpPaths` stamps its own `setOpRel` on the node it returns.
- `setOpBranchRelOf` is `searchedRelOf` widened by exactly one terminus: a
  node carrying a SETOP rel answers with it. Carrier check precedes the
  descent, so a `*Gather` over a partial `*SetOp` answers with the Gather's
  own rel — the rel matching the node the parent actually holds.
- `setOpBranchPartialChainOK` accepts a carrier as an admissible terminus,
  or the rel would be threaded and then refused one line later.
- `createSetOpPaths` reads its two branches through the new accessor.

Only `createSetOpPaths` stamps, so a `*Gather` or `*SetOp` built anywhere
else reads nil and every other caller keeps the old answer —
`TestSetOpBranchRelOfKeepsSearchedRoots` pins that the two accessors agree on
a searched root.

Row-exactness of what the carry newly offers: the rel's `PartialPathlist`
holds a partial SetOp path, whose branches are block-claimed across
participants, so the link emits each of its rows exactly once across the
whole worker set. That is precisely the contract a partial subpath owes its
parent, and it is the same contract PG's flattened appendrel children hold.

## 4. Absorption — what the carry admitted

Re-probed after the change, same corpus, same lane, same seed:

```
Q14  left=*Gather[pp=1 cp=true,chain=true]  filed=1   (was nil-rel, filed=0)  x2
Q76  left=*SetOp [pp=1 cp=true,chain=true]  filed=1   (was nil-rel, filed=0)
```

Three previously-dead outer links now file a partial path; the non-streaming
links (`streams=false`, UNION-distinct) correctly still file 0, and Q5's
branches stay `nil-rel` for a different reason entirely — its branch
subtrees are not searched roots at all, which is the pre-DP-unnest wall
tracked under `M0142-0008a-3i-lateral-route`.

**The quantity, stated before any parity number: the change adds partial
paths at 3 links across the SF0.25 corpus and removes none.** Whether any of
them wins its link's tournament is cost's decision, not the carry's, and the
carry makes no cost change whatsoever.

## 5. Parity outcome

None of the three newly-filed partials wins. TPC-DS SF0.25 gate: `same=99
changed=0 added=0 removed=0`, `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`,
verdict-changes=none. An A/B EXPLAIN sweep of the two binaries over the same
99 queries on the private lane is byte-identical independently.

TPC-H is **structurally incapable** of exercising this change and is not
cited as a parity result: the canonical 22-query corpus contains no set
operation at all — a fresh plan capture
(`analysis/leftdeep-joins/m0144-0003b1-on-parallel.plans.txt`) has zero
`Append` and zero `SetOp` nodes across all 22 plans, so `createSetOpPaths`
is never called. It is run as a regression check only: spotcheck PASS
(Q12=2, Q13=33), acceptance arm rc=0.

**Movement: none.**

This is the expected shape for an admission-only change and is recorded as
such rather than reported as a win: the structural blindness is gone, the
election is unchanged, and a later cost-side task can now be measured
against a producer that actually offers the candidate.

## 6. What remains of M0144-0003b

The residual is genuinely the filed scope minus this slice: branches whose
subtree never reached the search at all (`*Project:nil-rel`, Q5's shape)
cannot offer a rel no matter how the accessor is widened, because there is
no rel. That is the two-phase pre-DP unnest wall, and it is the same blocker
four other tasks already sit behind.
