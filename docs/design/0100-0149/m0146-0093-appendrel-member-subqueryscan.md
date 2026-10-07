# M0146-0093 — `Subquery Scan on "*SELECT* n"` over UNION ALL members

Status: done 2026-10-08 (0fc895974). Parent: M0146-0066 (class C of the
Subquery Scan recon). Evidence: `analysis/m0146/m0146-0093/`.

## Problem

PG renders `Subquery Scan on "*SELECT* n"` under the Append for some
members of a FROM-clause UNION ALL. goopg planned every member bare:

- TPC-DS Q5: `"*SELECT* 2"`, the LEFT JOIN member of `wsr`.
- TPC-DS Q71: `"*SELECT* 1..3"`, three comma-join members with WHERE
  clauses.

## PG behaviour

- `pull_up_simple_union_all` (prepjointree.c) turns every leaf into a
  subquery RTE `"*SELECT* n"`, with leaves counted left to right.
- `pull_up_subqueries_recurse` pulls a leaf into the appendrel only when
  both checks hold:
  - `is_simple_subquery`;
  - `is_safe_append_member`: the jointree holds exactly one RTE and **no
    WHERE quals**.
- A join member, a member with any WHERE clause, or a grouped member
  therefore stays a subquery. It is planned on its own and scanned by a
  SubqueryScan.
- `create_append_plan` plans its children with CP_EXACT_TLIST. The scan's
  tlist is the appendrel's needed columns, and setrefs keeps it unless the
  parent reads the member's columns whole and in order.

Probes on PG 18.3 (`probe-appendrel-members.sql`):

| shape | PG |
|---|---|
| WHERE member, subset read | kept |
| WHERE member, all columns in order | stripped |
| join member, all columns in order | stripped |
| join member, read out of order | kept |
| join member under a join | kept |
| statement-level UNION ALL with a join arm | none |

## Design

### Labelling after path selection

The wrapper is a label. It must not change what the search chooses. Two
earlier designs put it into the SetOp path spec, and both failed:

- **Pricing the wrapper** moved the partial-path election. Q76's store
  member lost its partial path to a claimed-whole arm, and the upper plan
  regressed from Partial HashAggregate to HashAggregate over a Gather.
- **Admitting the wrapper into the partial-branch chain**
  (`setOpBranchPartialChainOK`, `spliceBranchEmission`) crashed Q76 at
  `assertBoundaryProjectionIntact`. The member's boundary Project then sat
  over a substituted partial emission with a different column layout.

The landed design records which members are wrappers, and adds the
wrappers to the finished plan:

1. **Stamps.** The set-op fold stamps every UNION ALL link of an appendrel
   (`SetOp.appendRel`, `appendMemberLeft`, `appendMemberRight`) with the
   1-based member numbers of its unsafe sides.
   - `isSafeAppendMember` decides from the member's AST and its planned
     branch (Aggregate, WindowAgg and ProjectSet nodes).
   - The fold runs only when the new `appendrelLabel` planner setting is
     on. It is set on both of `planSubqueryRangeVar`'s arms; a non-first
     FROM item (Q71's `tmp`) plans through the lateral-context arm, which
     never set `appendrelMember`.
2. **Copies keep the stamps.** `createSetOpPlan` copies the spec, and
   `orderParallelAppendArms` carries each member number with its arm when
   it re-sorts a Parallel Append.
3. **Binding.** One `appendRelLabel` is shared by the whole chain.
   `planSubqueryRangeVar` fills it with the appendrel binding's sourceIdx
   and column names, which are the coordinates the parent's ColumnRefs use.
4. **Wrapping.** `wrapAppendRelMembers` wraps the stamped arms at
   `Plan()`'s tail, before `renumberRTIDsFlatRtableOrder` and the strip.
   - The wrappers mark PG's subquery levels, so alias numbering now follows
     PG's (`date_dim`, not `date_dim_1`).
   - The pass works in place, because the strip identifies derived subtrees
     by pointer.
   - It consumes the stamps, so a re-entrant `Plan()` (EXPLAIN's inner
     statement) cannot wrap twice.
   - Sublink bodies get the same pass before their own strip.

### The strip decision

A member wrapper's consumption is the appendrel binding's, read in the
region the appendrel sits in:

- **Region.** It is the outer region when the chain's top SetOp, or a
  Gather above it, is the recorded derived root, and the SetOp's own
  region when the SetOp is a lowered copy. It passes through chained
  links, a link's Gather, and a branch's type-coercion Project; PG has no
  such Project, because its member types already matched.
- **Counted references** (`appendMemberUsedPositions`):
  - The first Project reached from the region root is the level's final
    target list, and it counts.
  - Deeper Projects that only pass columns through (bare columns or NULL
    placeholders) are the join search's leaf projection and upper
    narrowing. They list every column whatever the parent needs, so they
    do not count.
  - Projects that compute something count.
- **Differing branch types.** A chain whose branch types differed
  (`TlistTypesDiffer` on any link) has no appendrel in PG, so its member
  wrappers are stripped.

### EXPLAIN

The Subquery Scan alias goes through `quote_identifier`, as in
`ExplainTargetRel`: `Subquery Scan on "*SELECT* 1"`.

## Verification

- **Probes.** All probe shapes match PG 18.3 (`probe-appendrel-members.*`,
  `probe-tpcds-shapes.sql`):
  - the Q5 LEFT JOIN member with casts, kept;
  - a union second in FROM, kept;
  - three members under a forced Parallel Append, kept, with labels
    following their arms;
  - a Q76-shaped full-read union, no wrappers.
- **Unit test.** `TestSubqueryScanAppendRelMembers` covers seven shapes.
  Never wrapping fails its three keep cases.
- **TPC-DS fire sets** (both scales):
  - The Subquery Scan total went from 26 to 30 (PG 32 at SF1, 31 at
    SF0.25). Q5 (3→4) and Q71 (0→3) now equal PG's counts.
  - Q14, Q66 and Q76 change aliases only, now PG's.
  - Executed results are identical.
  - CATEGORIES-EXCL-MATCH SF0.25: join-order 48→47, scan-type 27→26 (Q71).
    SF1 is unchanged.
- **Regress A/B.** union, with, subselect, select_parallel and inherit are
  identical. partition_prune renumbers one alias that already diverged
  from PG (`ab_a2_b1` vs PG's `ab_4`).
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet all PASS.

## Deferred (ledgered)

- **A parameterised Append over a subquery member** (filed as M0146-0098).
  PG gives a member it keeps as a subquery no parameterised path, so
  `li × (SELECT … FROM cs1 WHERE amt > 5 UNION ALL …)` hash-joins in
  PG 18.3. goopg still drives that member by `item = li.id` and now labels
  it `"*SELECT* 1"` (TestParameterisedAppendOverUnionAll's plan).
- **The wrapper is never priced.** PG's `cost_subqueryscan` adds per-row
  CPU to the member path. Labelling after selection keeps goopg's costs
  as they were.
- **A latent splice hazard.** `spliceBranchEmission` re-applies a branch's
  boundary Project unchanged over a substituted partial emission. Q76's
  catalog member showed that the layouts can differ.
- **Other appendrels are not covered.** Inlined-CTE UNION ALL appendrels
  (M0146-0065, `wrapInlinedCTEScans`) are not labelled.
- **Unnamed subquery alias.** PG names an unaliased FROM subquery
  `unnamed_subquery`; goopg says `__sq_N`.
