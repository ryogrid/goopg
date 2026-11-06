# M0148\-0001c3 — partial indexes: predicate maintenance, NullTest proof, implied quals

**Parent:** M0148\-0001c · **Status:** landed 2026\-11\-03; closed (residue in
M0148\-0001c3a and M0148\-0001c3b)

## Divergence

Probing the task's NullTest cases against PG 18.3 found a correctness defect
underneath the planner gap:

| case | PG | goopg (before) |
|---|---|---|
| `CREATE UNIQUE INDEX up_a ON up (a) WHERE b > 0`, `INSERT (1,-1), (1,-2)` | accepted (neither row is in the index) | `duplicate key value violates unique constraint` |
| `INSERT (1,-5) ON CONFLICT (a) WHERE b > 0 DO NOTHING` beside `(1,1)` | inserted | skipped as a conflict |
| `INSERT (NULL, 3)` after `CREATE INDEX … (y) WHERE x IS NOT NULL` | no index entry | an entry, so a scan relying on the predicate would return the row |
| `UPDATE … SET x = 55` (x is a predicate column only) | non\-HOT, the row enters the index | HOT, no index entry |
| a partial index after a server restart | usable | unusable: `Index.Predicate` was nil, only `PredicateString` survived |
| `WHERE x IS NOT NULL AND y < 10` over `ON nt (y) WHERE x IS NOT NULL` | `Index Scan using nt_y_part`, `Index Cond: (y < 10)`, no Filter | Seq Scan (predicate not proven) |
| `WHERE y < 10 AND x > 0` over the same index | the partial index, `Filter: (x > 0)` | the predicate was not proven |

## PostgreSQL

- `ExecInsertIndexTuples` (execIndexing.c) evaluates each partial index's
  predicate on the new tuple and skips the index when it fails.
  `ExecCheckIndexConstraints` does the same for ON CONFLICT arbiters, so
  uniqueness applies only among admitted rows.
- `RelationGetIndexAttrBitmap` puts the key columns, INCLUDE columns,
  expression columns and predicate columns in the HOT\-blocking set. The KEY
  set (lock strength, `HEAP_KEYS_UPDATED`) holds key columns only.
- `predicate_implied_by_simple_clause` (predtest.c):
  - a clause implies itself (`equal()`);
  - `x IS NOT NULL` is implied by any clause strict for x
    (`clause_is_strict_for`, strong implication only).
- `create_indexscan_plan` and `create_bitmap_scan_plan` (createplan.c) leave
  out of qpqual any restriction the index predicate implies. They keep it
  for the result relation and row\-marked relations, whose EvalPlanQual
  recheck needs it.

## Change

- **Predicate source** (`optimizer/index_predicate.go`). `IndexPredicateExpr`
  returns the stored tree, or parses `PredicateString` once per distinct
  string. `ResolveIndexPredicateOf` replaces every
  `ResolveIndexPredicate(idx.Predicate, …)` call in the planner, REINDEX and
  `bt_index_check`, so a restarted partial index keeps its predicate.
- **Row maintenance** (`executor/partial_index_maintain.go`).
  `indexPredicateAdmits` evaluates the predicate over the row, re\-laid out
  in table column order. The row is skipped at:
  - `maintainUniqueIndexesForInsert` (INSERT, UPDATE, COPY, MERGE and the
    apply worker all file entries there);
  - the upsert non\-arbiter maintenance;
  - `checkUniqueIndexesForInsert`;
  - `checkUniqueIndexesForUpdate`, which also probes an update that moves
    the row into the predicate with its key unchanged;
  - `arbiterKey`: a nil key means no conflict and no entry.

  An unresolvable or erroring predicate admits the row, giving a superset
  that no reader treats as proven.
- **HOT.** `hotUpdateEligible` also refuses updates of INCLUDE and predicate
  columns. The key classification moved to `updateChangesIndexKeys`, which
  drives `StatusUpdate`, `HEAP_KEYS_UPDATED` and the non\-HOT stamp's
  `keysUpdated`. An INCLUDE\-only update therefore stays a no\-key update.
  Reusing HOT eligibility for it broke isolation `lock-update-traversal`,
  whose `PRIMARY KEY (key) INCLUDE (value)` update must not block FOR KEY
  SHARE.
- **Proof** (`provePartialIndexPredicate`): the two NullTest rules.
  - `nullStrictFor` proves strictness only through comparisons, arithmetic,
    LIKE/regex operators and casts.
  - Never through AND/OR or `||`: `array_append` is not strict.
- **Implied quals** (`partial_index_implied_quals.go`,
  `dropPartialIndexImpliedQuals`, from `Plan`'s tail). This post\-pass
  removes from a Filter directly over an Index Scan, an Index Only Scan, or
  a single\-index Bitmap Heap Scan every conjunct that the partial index's
  predicate implies. Plans that lock or modify rows are skipped entirely:
  goopg's EPQ recheck reads the Filter above the scan chain, as for
  M0148\-0001k.

## Verification

- `TestPartialUniqueIndexConstrainsOnlyAdmittedRows`: INSERT, UPDATE into
  the predicate, and ON CONFLICT over a partial arbiter.
- `TestPartialIndexScanDropsImpliedQuals`: implied qual dropped; strict
  clause kept as the Filter; FOR UPDATE keeps it. The rows include a
  post\-CREATE `(NULL, 3)` that must not appear, and a predicate\-column
  update that must.
- `TestProvePartialIndexPredicateNullTest`: positive and negative proof
  cases.
- Probe script against a scratch PG 18.3, covering the up/nt2/oc cases and a
  restart. Results are identical.
- Regress runner, HEAD → change, 13 suites (create\_index, select,
  btree\_index, inherit, aggregates, join, insert\_conflict, update, insert,
  indexing, arrays, portals\_p2, create\_index\_spgist): byte\-identical
  except a join row\-order hunk.
- `TestPort_RegressSuite`, `TestPort_PgDump*` and `TestPort_Isolation*`
  pass. The TPC gates show no plan change.

## Remaining

- **M0148\-0001c3a.** `col IS [NOT] NULL` as an index qual on searched
  paths (match\_clause\_to\_indexcol). PG prints `Index Cond: (x IS NOT
  NULL)` where goopg keeps `Filter:`. This needs an executor scan key for
  NULL entries.
- **M0148\-0001c3b.**
  - Index\-only scans over a proven partial index (`indexCoversColumns`
    refuses every partial index; check\_index\_only ignores columns of
    implied quals).
  - The unanalysed partial index's tuple count.
  - Expression columns in the HOT\-blocking set.
