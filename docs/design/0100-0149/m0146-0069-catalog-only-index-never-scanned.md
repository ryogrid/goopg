# M0146-0069 — never scan a catalog-only index

Status: done 2026-10-07 (`a6327df08`).

## Symptom

```sql
CREATE TABLE ge (i int4[]);
CREATE INDEX ON ge USING gin (i);
SET enable_seqscan = off;
SELECT * FROM ge WHERE i = '{47,77}';
-- ERROR:  short read at block
```

The same failure appears in regress `create_index` (GIN, on
`array_index_op_test`), `stats` (BRIN, on `brin_hot_3`), `brin` and
`spgist`, whenever an index path on such an index wins the cost comparison.

## Cause

`CREATE INDEX` (`operators_ddl.go`) builds physical storage only for btree.
`USING hash` is built on btree and keeps `Method` "btree". gist, spgist,
gin and brin are registered in the catalog only, so `pg_index`,
`pg_get_indexdef` and reloptions round-trip but no file exists. The
path producers enumerated every index from `IndexesOnTable`:

- `addBaseRelBitmapPaths` and `addParameterizedBitmapPaths`;
- the param-append bitmap arm;
- the restriction, ordered and index-only producers.

So a Bitmap Index Scan or Index Scan on a storage-less index could be
planned. Its first read failed. Several helpers already filtered to btree:
the min/max helpers, the NL-index and skip-run pickers,
`tryPromoteOrderedIndexOnlyScan` and `indexOrderedAggInput`.

## Change

- `catalog.Index.HasStorage()` is true for `Method` "" or "btree".
- Every unguarded `IndexesOnTable` loop in a path producer skips an index
  without storage: `pathbitmap.go` (two loops), `paramappend.go`,
  `pathindexonly.go`, `pathindexordered.go` and `pathindexrestrict.go`.

Uniqueness proofs, equivalence-class orientation and ON CONFLICT arbiter
resolution still see every index. They consult only unique btree indexes.

## Verification

- `TestCatalogOnlyIndexIsNeverScanned` runs, under `enable_seqscan = off`
  and through both builders:
  - GIN equality;
  - BRIN equality, range and min/max;
  - GiST containment.

  Every want is PG's result. At HEAD the test fails with `short read`.
- A server probe is identical to PG; HEAD failed three of its queries.
- Regress A/B over 13 files:
  - `create_index`: four `short read` errors become query results. The
    table is empty because of a pre-existing COPY gap.
  - `stats`: the BRIN count matches PG.
  - `brin`, `spgist`: errors become results.
  - The other nine files are identical.

  The only new divergences are EXPLAIN shapes. PG scans the
  gin/brin/spgist index; goopg plans a Seq Scan.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24;
  - sf025 96/96, plan shapes 99/99;
  - fire set: no fires;
  - ea-ratchet PASS.

## Not covered

The gist, spgist, gin and brin access methods have no storage and no scan
in goopg, so every plan PG builds on one of them differs. This was already
true; it is ledgered against this task, which removes only the crash.
