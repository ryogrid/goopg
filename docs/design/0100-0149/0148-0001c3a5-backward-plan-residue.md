# M0148\-0001c3a5 — the plans behind the last `… Backward` regress lines

**Parent:** M0148\-0001c3a3 · **Status:** item \(d\) landed 2026\-11\-14
\(except `b = true` simplification\); items \(a\)–\(c\) open

Backward scans landed in M0148\-0001c3a3
\([0148\-0001c3a3\-backward\-index\-scans.md](0148-0001c3a3-backward-index-scans.md)\).
The remaining `… Backward` regress lines each wait on another planner
feature, and the forward plan is wrong too. The items, from
`.ralph/fix_plan.md`:

- \(a\) inherit: the partition\\-ordered plain Append, and min/max over
  partitions;
- \(b\) partition\\_join: the fractional partitionwise join;
- \(c\) btree\\_index: row\\-comparison index quals;
- \(d\) create\\_index: boolean index quals and equality\\-prefix pathkeys.

## Item \(d\) — boolean index quals and constant\-column pathkeys

create\_index's `boolindex (b bool, i int, unique (b, i))` queries:
`WHERE b ORDER BY i LIMIT 10`, `WHERE NOT b`, `WHERE b IS TRUE / IS FALSE`
\(and DESC\). PG answers each one with an Index Scan \(Backward\) using
`Index Cond: (b = true)` and no Sort. goopg planned Seq Scan \+ Sort, for
two reasons:

- **The index qual:** match\_boolean\_index\_clause turns a boolean column
  alone, negated, or under IS TRUE / IS FALSE into the indexable `b = true`
  / `b = false`. goopg took only `col = const` binary operators, and it
  rejected a boolean constant as a key outright.
- **The ordering:** build\_index\_pathkeys skips an index column whose
  pathkey is redundant, i.e. its EquivalenceClass holds a Const, or the
  column is a boolean constant for the query
  \(indexcol\_is\_bool\_constant\_for\_query\). goopg's buildIndexPathkeys
  stopped at the first column the query neither sorts nor merges on. So an
  index on `(b, i)` with `b` pinned offered no ordering on `i`.

### Change

- **Index qual:** `booleanIndexQual` \(pathindexrestrict.go\) recognises the
  four boolean shapes. `restrictionEqualityOn` files them as `col = true` /
  `col = false` clauses, which carry their conjunct, so the Filter drops it.
  `restrictionKeyUsable` admits a boolean constant key on a boolean column.
  The restriction and index\\-only producers share both.
- **Ordering:**
  - `addConstEquatedColumns` \(pathkeysindex.go\) marks, in the per\\-rel
    `colExprs` map, every column the rel's restriction pins to one value:
    `col = const` \(not NULL\) and the boolean shapes, through
    `constEquatedColumn`, which reuses booleanIndexQual.
  - `buildIndexPathkeys` skips a marked column instead of stopping.
  - The restriction, index\\-only and ordered producers apply the marks.
  - Because every row the rel emits has that value, the index's later
    columns still deliver their order, in either direction.

### Verification

- **Live server against a scratch PG 18.3** \(2000 analysed rows\): the
  same plans, with Index Cond `(b = true)`.
- **`TestBooleanIndexQualAndConstantPrefixPathkeys`:** PG's 4 plans
  \(forward and backward\) and rows.
  - Its `i` is NOT NULL, because the unit fixture's cluster keeps no
    NULL\\-keyed index entries and so refuses a probe that leaves a
    nullable `i` unbound.
  - This was the cause of the fixture's Seq Scan, not costing.
- **Regress:**
  - create\\_index 2801→2750. `NOT b`, `IS TRUE` and `IS FALSE`, forward and
    backward, now match.
  - Its one new mismatch line is the `b = true` case's Recheck Cond \(see
    Not covered\).
  - No other suite moved.
- **Gates:**
  - units, tpch\\-spotcheck, sf025 99/99 \(plan shapes identical\);
  - fire\\-set fires=none on sf025, sf1 and TPC\\-H;
  - acceptance 24 MATCH.

### Not covered

- **`b = true` written out:** PG's eval\_const\_expressions rewrites it to
  `b` \(simplify\_boolean\_equality; the Filter prints `b`\). The column then
  gets boolvarsel's estimate, 0.5 without statistics. goopg keeps the
  `b = true` OpExpr and its 0.005 equality default, so the bitmap \+ Sort
  wins on an empty table.
- **Boolean selectivity in general:** a bare boolean column and IS TRUE /
  IS FALSE get goopg's 1/3 default. PG uses boolvarsel / booltestsel: 0.5
  without statistics, var\_eq\_const with them. Rows show 667 against PG's
  1000.
