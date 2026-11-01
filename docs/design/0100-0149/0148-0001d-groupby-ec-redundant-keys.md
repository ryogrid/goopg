# M0148-0001d — GROUP BY keys made redundant by an EquivalenceClass already in the list

**Parent:** M0148-0001 `aggregates.sql` regress coverage closure · **Status:**
slice 1 landed `5ae6f5416` (run-2 loop #10, 2026-11-02); hunk 1271
(partitioned-parent Group Key Vars) remains
**Target:** aggregates hunks at expected-lines 1211 / 1225 (`Group Key: t1.a,
t1.b` and `Group Key: t1.a, t1.b, t2.z`)

## Why

```
select * from t1 inner join t2 on t1.a = t2.x and t1.b = t2.y
group by t1.a, t1.b, t1.c, t1.d, t2.x, t2.y, t2.z;
```

goopg already removed `t1.c`, `t1.d` (t1's primary key `(a, b)` is in the
list) and `t2.z` (t2's primary key `(x, y)` is in the list) —
remove\_useless\_groupby\_columns, `pruneUselessGroupByColumns`. It kept
`t2.x`, `t2.y`; PG 18.3 groups by `t1.a, t1.b` alone.

## Mechanism in the oracle

- `standard_qp_callback` (planner.c:3502-3525) converts the plain GROUP BY
  list with `make_pathkeys_for_sortclauses_extended(..., remove_redundant =
  true)` and keeps in `root->processed_groupClause` only the items whose
  pathkey survived (`parse->groupClause` keeps them all — the columns still
  appear in the output).
- `pathkey_is_redundant` (pathkeys.c) has two arms: the pathkey's
  EquivalenceClass contains a constant (`WHERE x = 5 GROUP BY x`), or the
  EquivalenceClass is already in the pathkey list built so far.
- The inner-join ON clause `t1.a = t2.x and t1.b = t2.y` merges
  `{t1.a, t2.x}` and `{t1.b, t2.y}` (process\_equivalence), so `t2.x` and
  `t2.y` reach the second arm. An outer join's ON clause merges nothing
  (it is an outer-join clause, not a restriction), so a LEFT JOIN keeps both
  keys.

## Design

`redundantConstGroupKeys` (groupkeyconst.go) was the first arm only
(M0146-0005ak). It now builds the classes it needs:

- union-find over input-column indices, fed by top-level `col = col`
  conjuncts of the WHERE clause and of every INNER join's ON clause;
- only when no FROM item joins LEFT / RIGHT / FULL (`fromHasOuterJoin`) and
  only between columns of the same type (fail-closed);
- `col = pseudo-constant` conjuncts of the WHERE mark their column's class as
  constant, so the constant now propagates through merged columns as PG's
  EC does (`c = d AND d = 2` drops both keys);
- walking the keys in GROUP BY order, a key is dropped when its class is
  constant or when a DIFFERENT column already represents the class; a repeat
  of the same column (`GROUP BY a, a`) is left alone, as before, so it never
  turns into a passthrough.

Dropped columns ride the existing functionally-determined passthrough (the
`pruned` map), exactly as the constant arm's do: every row of a group carries
the representative's value.

## Verification

- Scratch PG 18.3 vs goopg: six shapes (the two regress queries, a
  `group by t2.z, t1.c` over `t1.c = t2.z` → `Group Key: t2.z`, the LEFT
  JOIN keeping both, `where c = d` → `Group Key: c`, `where c = d and d = 2`
  → no Group Key) and their values agree.
- `TestGroupByKeysRedundantByEquivalence`; disabling the class merge fails it.
- `scripts/pg-regress-runner.sh -v aggregates`: 26 → 24 hunks; only
  1211 / 1225 moved.
- Gates: spotcheck Q12=2/Q13=35, sf025 PASS=99 shapes 99 same, acceptance
  arm 24 MATCH, fireset SF0.25+SF1 `fires=none`.

## Not covered (deferral ledger)

- Cross-type equalities (`int4 = int8`) stay unmerged; PG merges any pair in
  one btree opfamily.
- Witnessed, pre-existing: PG rewrites the scan qual `c = d AND d = 2` to
  `(c = 2) AND (d = 2)` (generate\_base\_implied\_equalities\_const); goopg
  filters `((c = d) AND (d = 2) AND (c = 2))`.

## Remaining (fix\_plan M0148-0001d)

- Hunk 1271: `select * from p_t1 group by a,b,c,d` over a partitioned table
  prints `Group Key: p_t1_1.a, p_t1_1.b` (the first leaf's Vars) where PG
  prints the parent's `p_t1.a, p_t1.b` — the Append's tlist carries the
  appendrel parent rel's Vars in PG, and goopg's key chase descends into the
  first member.
