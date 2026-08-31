# Expression key columns in the B-tree bulk build (CREATE INDEX / REINDEX)

- Task: M0119-0006 (7th slice)
- Status: accepted
- Date: 2026-08-10
- Code: `internal/executor/operators_ddl.go`, `internal/executor/operators_reindex.go`
- Test: `internal/executor/expression_index_build_test.go`

## The gap

goopg stores an expression index key column as `idx.Columns[i] == ""` with the
parsed AST in `idx.ColExprs[i]` (`CREATE INDEX ON t(lower(b))`). Two code paths
turn a heap row into that index's key bytes, and they had diverged:

| path | site | expression key columns |
|---|---|---|
| runtime maintain (INSERT) | `encodeExprIndexKey` (`operators_storage.go`) | resolved + evaluated + `encodeArbiterExprKey` |
| bulk build (CREATE INDEX / REINDEX / matview refresh) | `collectBTreeEntries` → `encodeCompositeBTreeKey` (`operators_ddl.go`) | **skipped outright** |

`createBTreeIndex` left `cols[i] = nil` for an expression column and
`encodeCompositeBTreeKey` `continue`d past every nil entry, so:

1. **`CREATE INDEX ON t(lower(b))` over pre-existing rows built a physically
   EMPTY index.** With only expression key columns the encoder produced no bytes
   at all, `key == nil`, and `collectBTreeEntries` dropped the row. Rows inserted
   *after* the build were indexed normally, so the index silently held a suffix
   of the table.
2. **REINDEX on an expression index discarded live entries.** `rebuildIndex` and
   `buildIndexShadow` (REINDEX ... CONCURRENTLY) reuse the same bulk build after
   truncating the index, so a REINDEX turned an index the maintain path had
   correctly populated into an empty one.
3. **A mixed index (`(a, lower(b))`) stored WRONG keys** — non-empty, because the
   plain column contributed bytes, but missing the expression component
   entirely, so the stored order did not match the order any probe would compute.

This is the sibling-paths failure mode (encode ↔ encode): a green test on the
INSERT twin proved nothing about the build twin.

## The change

`encodeCompositeBTreeKeyWithExprs(ctx, row, cols, keyExprs, pos)` extends the
composite key encoder with a `keyExprs []planner.Expr` slice parallel to `cols`,
non-nil exactly where `cols[i]` is nil. For such a column it evaluates the
resolved expression against the row and appends `encodeArbiterExprKey(v)` —
deliberately the *same* evaluator and the *same* encoder the runtime maintain
path uses, so the two produce byte-identical keys by construction rather than by
review. `encodeCompositeBTreeKey` becomes a thin `keyExprs == nil` wrapper, which
keeps every plain index on an unchanged path.

Three outcomes, matching the maintain path's own semantics:

- expression yields NULL → `hasNullKey`; the row is not indexable (goopg's
  byte-key B-tree has no per-attribute null bitmap), same as a NULL plain column;
- expression result kind has no B-tree encoding (see the kind table below) or
  fails to evaluate → `key == nil`, and the caller's existing
  "skip this row" branch applies — a build must not fail on data the runtime
  path would also decline to index;
- otherwise the bytes join the composite key in key-column order.

### Expression-key kind coverage (2026-08-10)

`encodeArbiterExprKey` originally covered `KindString` and `KindInt` only, so an
expression key column of any other result type encoded to nil and every row was
declined by **both** paths — `CREATE INDEX ON t ((n * 2))` on a numeric column
built an empty index and stayed empty across subsequent INSERTs. It is now the
expression-side sibling of `encodeBTreeKeyForColumn`, and each arm reuses the
encoder the equivalent declared column type would use, so bytewise comparison of
encoded keys reproduces the expression result type's SQL ordering:

| Datum kind | encoder | equivalent column path |
|---|---|---|
| `KindString` | `EncodeVarchar` | text/varchar/uuid/name |
| `KindInt` | `EncodeInt8` | int2/int4/int8 widened to int64 |
| `KindNumeric` | `EncodeNumericKey(mantissa, scale)` | numeric |
| `KindTime` | `EncodeTimestamp` (micros since the PG epoch) | timestamp/timestamptz/date |
| `KindBool` | `EncodeInt8(0/1)` | — (no bool column arm exists yet) |
| `KindEnum` | `EncodeFloat8(sort order)` | enum |
| `KindBytes` | `EncodeVarchar` (escaped, bytewise-order preserving) | bytea |

Dispatch is on the **runtime Datum kind**, not on a declared type: an expression
key column has no catalog column to consult (`idx.Columns[i] == ""`), and
`planner.inferExprType` is unexported and too weak to give a reliable static
result type. That is sound as long as an expression yields a stable kind across
rows — true for every builtin goopg evaluates today; the mixed-kind case is a
ledger row, not a silent assumption.

`KindTime` deliberately carries no subtype tag: date, timestamp and timestamptz
all encode as micros-since-epoch, which is order-preserving within any one
subtype and therefore correct for the only situation a single index can contain.

`resolveIndexKeyExprs(tbl, idx)` resolves `idx.ColExprs` through
`planner.ResolveIndexPredicate` once per build and returns nil for any index
without expression keys.

Plumbing: `bulkBuildBTree`/`bulkBuildBTreeWithPredicate` collapse into a single
`bulkBuildBTreeFull(idxRel, tbl, cols, keyExprs, unique, nullsNotDistinct,
indexName, pos, predExpr)` — the two thin wrappers had no remaining callers once
all four build sites (`createBTreeIndex`, `rebuildIndex`, `buildIndexShadow`,
the matview-refresh index rebuild) started passing `resolveIndexKeyExprs`.

## Verification

`TestExpressionIndexBuildIndexesExistingRows` and
`TestReindexExpressionIndexKeepsEntries` assert on **physical index contents**
(`btree.RangeScan` entry count), not on query results: a goopg index scan can
fall back to a sequential scan and would report the right rows over an empty
index, hiding exactly this bug.

Confirmed non-vacuous — with `resolveIndexKeyExprs` forced to return nil the
tests fail with `0 index entries after build over 3 pre-existing rows` and
`REINDEX left 0 entries, want 2`.

The kind widening adds `TestEncodeArbiterExprKeyCoversNonTextKinds` (no kind
encodes to nil), `TestEncodeArbiterExprKeyOrderPreserving` (bytewise comparison
of two encoded keys reproduces SQL order — an arm that returns bytes in the
wrong order is worse than returning nil) and `TestExpressionIndexBuildNonTextKeyKinds`
(physical entry counts for numeric- and bool-valued key expressions, build path
and post-build INSERT). Also confirmed non-vacuous: reverting the new arms
reproduces `0 index entries` on both paths.

## Deferred

- ~~`encodeArbiterExprKey` still encodes only `KindString` and `KindInt`~~ —
  **RESOLVED 2026-08-10**, see "Expression-key kind coverage" above.
- Kind dispatch assumes an expression's result kind is stable across rows. An
  expression that yields `KindInt` for one row and `KindNumeric` for another
  would mix two incomparable encodings inside one index. PostgreSQL cannot hit
  this because an index expression has a single resolved result type
  (`ComputeIndexAttrs` in `postgres/src/backend/commands/indexcmds.c`); goopg
  needs `planner.inferExprType` exported and strengthened to close it.
- `KindInterval` and `KindToastPointer` expression results are still declined
  (nil key ⇒ row not indexed).
- A NULL expression result is not indexed, whereas PostgreSQL does index NULL
  index entries. This is goopg's pre-existing byte-key-B-tree limitation (design
  0119-0004 §3), not new here — it is now merely reached by one more path.
- `ddlOp.backfillBTree` (the pre-M0047 Create+backfill flow) still skips
  expression columns; it has had no callers since the bulk-build path replaced it.

Ledger rows recorded in `.ralph/deferral_ledger.md`.
