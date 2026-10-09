# M0146-0098 — no parameterised Append over a subquery-RTE member

Status: done 2026-10-08 (f636d7a75). Parent: M0146-0093.

## Problem

`addParameterizedAppendPaths` (M0146-0049b) builds a parameterised Append
when every UNION ALL member is `[Project →] [Filter →] scan`, so a member
with a WHERE clause was probed by the join key too. For example:

```sql
li x (SELECT item, amt FROM cs1 WHERE amt > 5 UNION ALL SELECT item, amt FROM ws1)
```

goopg planned a Nested Loop over index probes into both members. PG 18.3
hash-joins it, with `Subquery Scan on "*SELECT* 1"` over the WHERE member.

## PG behaviour

- `is_safe_append_member` refuses a member with a join or any WHERE qual,
  so the member stays a subquery RTE under the appendrel.
- `set_subquery_pathlist` gives a subquery RTE paths parameterised only by
  its own LATERAL references (`rel->lateral_relids`), never by a join
  clause.
- For any join parameterisation, that child's
  `get_cheapest_parameterized_child_path` is therefore NULL, and
  `add_paths_to_append_rel` abandons the parameterisation for the whole
  appendrel.

## Change

- **The skip.** The set-op fold already stamps such members on their UNION
  ALL links (`SetOp.appendMemberLeft/Right`, M0146-0093). The stamps live
  until `Plan()`'s tail, so the join search can still read them.
  `unionAllHasSubqueryMember` walks the chain, and
  `addParameterizedAppendPaths` skips a leaf that has a stamped member.
- **Pushed quals don't count.** A member whose WHERE came only from a pushed
  restriction (M0146-0094) is not stamped: `is_safe_append_member` judges
  the original member. It keeps its parameterised path, as in PG.

## Verification

- **`TestParameterisedAppendOverUnionAll`.**
  - A union of two plain members keeps PG's parameterised Nested Loop.
  - The WHERE-member union plans PG's Hash Join with `Subquery Scan on
    "*SELECT* 1"`.
  - The value checks are unchanged.
  - Disabling the skip fails the new assertion.
- **Corpus.** The TPC-DS fire sets show no changed query at either scale.
- **Regress A/B.** union, inherit, partition_prune and subselect are
  identical; join differs only in its row-order flap.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet all PASS.

## Not covered

A LATERAL UNION ALL member's own parameterised subquery paths (PG's
`lateral_relids` arm) are outside this pass. goopg's lateral Append
handling (M0146-0009m) is a separate route.
