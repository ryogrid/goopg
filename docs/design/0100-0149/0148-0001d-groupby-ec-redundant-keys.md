# M0148-0001d — GROUP BY keys made redundant by an EquivalenceClass already in the list

**Parent:** M0148-0001 `aggregates.sql` regress coverage closure · **Status:**
slice 1 landed `5ae6f5416` (run-2 loop #10), slice 2 (hunk 1271) landed
`896430e99` (run-2 loop #11, 2026-11-02) — task closed
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

## Slice 2 — hunk 1271: the partitioned parent names Append-level columns

`select * from p_t1 group by a,b,c,d` over a two-partition table printed
`Group Key: p_t1_1.a, p_t1_1.b`; PG prints `p_t1.a, p_t1.b`. PG's Append
carries the partitioned parent as apprelids (ExplainPreScanNode adds them to
rels_used), so set\_rtable\_names names the parent RTE and every column above
the Append deparses as its Var. goopg's partition fan-out let the first leaf
reuse the RTE's RTID, so the first leaf owned the name.

The multi-leaf fan-out (planner.go, the PartitionKey branch of the FROM-item
builder) now gives the RTE's RTID and refname to the top SetOp link
(`SetOp.RTID` / `AppendRelName`, introduced by M0148-0001c (c2)) and
allocates a fresh RTID per leaf; a single leaf still reuses the RTE's RTID.
Leaf labels keep their relation names.

Regress runner, HEAD vs change: aggregates 24 → 23 hunks; partition\_join
−383 / −376 diff lines; partition\_aggregate −60 / −65; inherit −7 / −6;
partition\_info unchanged; partition\_prune +6 / −1 — member labels such as
`mc3p0 t2_1` matched before only because goopg expands the default partition
first (PG orders by bound, default last); with the parent claiming the bare
alias as in PG, that ordering divergence shows (M0148-0001k). Pin
`TestPartitionedGroupKeyNamesParent`.
