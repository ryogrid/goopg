# M0146-0179 — a semijoin under grouping sets stays a semijoin

Status: closed 2026-10-13.

## Problem

TPC-DS Q70 filters `s_state IN (SELECT s_state FROM (… GROUP BY s_state)
tmp1 WHERE ranking <= 5)` under `GROUP BY ROLLUP(s_state, s_county)`.

- PG 18.3 hashes the two-row `tmp1` in a Hash Semi Join under the main join's
  Gather.
- goopg elected an inner Nested Loop: `tmp1` outer, and a Materialize over the
  Gather inner.

## Diagnosis

The join-search trace shows goopg generated PG's Hash Semi Join, and it was
the cheapest path. An inner Nested Loop over `tmp1` evicted it:

- Totals and startups were equal under `STD_FUZZ_FACTOR`, and the Nested Loop
  carried `s_state` pathkeys, so `add_path` kept it.

PG has no such ordered path. This is confirmed on a PG 18.3 build with
`OPTIMIZER_DEBUG` (built in `tmp/`, never in the read-only tree):

- At the top joinrel, the Hash Semi Join keeps no pathkeys.
- `tmp1`'s Subquery Scan path has none either.

A bisection on the same build gives the rule:

- PG keeps the semijoin under ROLLUP, CUBE, or two or more grouping sets, even
  when the semijoin column is not grouped.
- It reduces the semijoin to an inner join under a plain GROUP BY, and under
  `GROUPING SETS ((a, b))`, which plans as a plain GROUP BY.

The mechanism is in `pull_up_simple_subquery` (prepjointree.c):

- With `parse->groupingSets` set, `wrap_option = REPLACE_WRAP_ALL` wraps every
  pulled output in a PlaceHolderVar.
- The semijoin's RHS operand is therefore no plain Var.
- `rel_is_distinct_for` cannot prove uniqueness, so `reduce_unique_semijoins`
  does not fire.
- `create_unique_path` cannot take its free `UNIQUE_PATH_NOOP` arm. Its paid
  sort/hash path has `pathkeys = NIL`.

## Change

- **`statementHasGroupingSets`** (groupingsets.go) answers PG's
  `parse->groupingSets != NIL`: true for ROLLUP, CUBE, mixed forms, and more
  than one set.
- **`resolveContext.groupingSetsWrapAll`** is set before the sublink pull-up.
  Under it, `classifyPulledQuals` (jointreepullup.go):
  - skips the `reduce_unique_semijoins` proof (`pulledSemiRhsIsUnique`);
  - leaves `SemiRhsDistinct` false, so unique-ifying the RHS is the paid kind.
- **`nestLoopPlainOuters`** (pathgen.go) gives `JOIN_UNIQUE_OUTER` the
  unique-ified outer, as `match_unsorted_outer` does for every nested loop of
  that branch (`outerpath = create_unique_path(...)`). The probe arm already
  did.
  - The plain and materialised arms used the raw cheapest outer.
  - That is only correct when unique-ification is free.
  - With a paid unique path, the raw outer also keeps its ordering, which is
    what won Q70's tie.
  - A paid unique-outer plain Nested Loop over a non-distinct RHS could in
    principle join duplicates; no corpus or regress case was found to elect
    it.

## Evidence

Files are in `analysis/m0146/m0146-0179/`.

- **Q70 at SF0.25:** plans PG's Hash Semi Join; its result equals PG's.
- **TPC-DS fire set:** SF0.25 match 75 → 76, SF1 62 → 63. Only Q70 fires.
- **TPC-H:** the acceptance arm is value-identical.
- **Regress A/B:**
  - `subselect`, `groupingsets` and `aggregates` are identical.
  - `join` reshuffles one plan, the empty-table `sj_t*` self-join case. It is
    41 lines from PG's expected text both before and after.
- **Tests:**
  - `TestStatementHasGroupingSets` pins PG's grouping-set rule.
  - `TestUniqueOuterNestLoopUsesUniquePath` fails on the raw-outer code.

## Not ported (ledgered)

- PlaceHolderVars themselves (M0146-0067). goopg mirrors only the two
  consequences above.
- On a plain-GROUP-BY fixture whose RHS filters a `rank()` output, goopg's
  `reduce_unique_semijoins` port does not prove the body distinct, while PG
  reduces it.
- A Merge Semi Join: goopg's merge executor has no SEMI. That gap is
  pre-existing.
