# M0146-0005 slice 17 (M0146-0005q): SETOP_SORTED for INTERSECT / EXCEPT

Witnesses: TPC-DS Q38 (INTERSECT chain) and Q87 (EXCEPT chain), both with
first divergences `SetOp` vs `HashSetOp`. Each arm is a sort-based SELECT
DISTINCT (Unique over Sort), so it already delivers the set operation's
ordering. PG 18's `generate_nonunion_paths` offers a SETOP_SORTED path
beside the hashed one. `create_setop_path` prices it with the inputs'
startups as startup and the same total as the hashed arm, so over presorted
inputs it wins add_path on startup. goopg had only the hashed executor
form.

## Change

- Executor (`operators_setop.go`, `nextSorted`): nodeSetOp.c's sorted mode.
  Each run of equal left rows is matched against the right run after the
  right side advances past smaller keys, and emits:
  - INTERSECT: 1 if both runs are non-empty;
  - INTERSECT ALL: min(nLeft, nRight);
  - EXCEPT: 1 if the right run is empty;
  - EXCEPT ALL: max(nLeft − nRight, 0).

  It reuses the merge-append plumbing (`MergeKeys`, `mergeKeysLess`); NULLs
  compare equal. `TestSortedSetOpMatchesHashed` checks the sorted form
  against the hashed form for all four commands.
- Planner (`windowsetoppaths.go`, `addSortedSetOpPath`): the SETOP_SORTED
  candidate when both arms are sorted on every column ascending. Accepted
  arms are a Unique (DistinctOn/Distinct) over such a Sort, or a nested
  sorted INTERSECT/EXCEPT. Its cost follows create_setop_path, and it
  carries the ordering as pathkeys.
- EXPLAIN renders the sorted form as `SetOp <cmd>`.

## Results

- `census-diff-sf025.txt`: Q38's first divergence moves from depth 2 to 3
  and Q87's from 1 to 4. SF1 is unchanged.
- The Q38 residue (`q38-plans.txt`) is PG's INTERSECT input swap:
  `generate_nonunion_paths` puts the input with fewer groups on the left;
  goopg keeps the written order (ledgered).
- Sweep 96/96 with checksums (2 plans changed; normal timings, the nightly
  batch had ended). Fire set Q38/Q87 with no timeouts. TPC-H 5/22 and
  census identical; spotcheck PASS; the acceptance arm has 24 MATCH.

## Not ported (ledgered)

- The sorted candidate over explicitly Sorted inputs (or the input rel's
  cheapest presorted path).
- The INTERSECT smaller-input swap.
- The hash-memory `disabled_nodes` rule for the hashed form.
