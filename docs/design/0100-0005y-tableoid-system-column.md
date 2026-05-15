# 0100-0005y — `tableoid` system column (M0100-0005)

## Status
accepted

## Context

`partition-key-update-4.spec` exercises cross-partition UPDATE with
EvalPlanQual recheck. After every test permutation it sanity-checks
the partition layout with:

```sql
SELECT tableoid::regclass, * FROM foo ORDER BY a;
```

Pre-fix, that query failed at name resolution:

```
ERROR:  column "tableoid" does not exist
```

Both the analyzer (`internal/analyzer/analyzer.go`
::`resolveColumnRefTypeAt`) and the planner
(`internal/planner/planner.go`::`resolveColumnRefAt`) only consulted the
catalog `Columns` slice when looking up a name. Neither knew about the
PostgreSQL system column `tableoid`. The result was a 42703 from the
analyzer, which short-circuited the planner before any query targeting
`tableoid` reached the executor.

`tableoid` is special in two ways:

1. **The value depends on the actual source relation, not on the
   FROM-clause name.** A query against a partitioned parent must
   report each row's *leaf* OID, not the parent's. Upstream PG hides
   this inside the per-leaf scan node — every `Var` carrying
   `varattno = TableOidAttributeNumber` reads from the slot's
   `tts_tableOid`, which the leaf scan stamps before yielding.
2. **`tableoid::regclass` renders as the relation name.** The result
   datum is an OID, but `regclass` has a custom `out` function
   (`regclassout`) that resolves the OID through the catalog and
   returns the qualified relname.

## Decision

Three coordinated changes plumb `tableoid` end to end.

### 1. Per-binding system-column lookup

`rangeBinding` gains a `tableOidColIdx int` field. When > 0 it holds
the relative offset within the binding's row of a synthetic
`tableoid` column emitted by the per-leaf wrapper (see (3) below).
Zero means "the binding's table OID is fixed at plan time" — the
constant case for non-partitioned base relations.

`resolveColumnRefAt` (planner) and `resolveColumnRefTypeAt`
(analyzer) both grow a fall-through arm: if the catalog lookup
misses but the column name is `tableoid` (case-insensitive), they
synthesise the system-column resolution. The planner side delegates
to a new helper, `resolveTableoidForBinding`, which returns either
an `(Outer)ColumnRef` into the synthetic slot (when
`tableOidColIdx > 0`) or a constant `*TableOidExpr{TableOID:
b.table.OID}`.

The unqualified path checks all bindings; multiple matches surface
as 42702 ("column reference is ambiguous"), matching upstream PG.

### 2. `TableOidExpr` plan expression

A new planner expression type:

```go
type TableOidExpr struct {
    pos      int
    TableOID uint32
}
```

`exprType` reports `oid`. `targetMeta` recognises a `Cast` whose
operand is `TableOidExpr` and labels the projection `tableoid`
(matches PG's `FigureColname` for system-column casts — without this
the column header would be the cast type label `regclass`).

The executor (`internal/executor/expr.go`::`evalExprSlot`) emits
`Datum{Kind: KindInt, Int: int64(x.TableOID)}` for `*TableOidExpr`.

### 3. Per-leaf wrapper for partition / inheritance unions

`internal/planner/planner.go`::`planFromTable` already turns a
partitioned scan into `SetOp(SeqScan(leaf1), SeqScan(leaf2), …)`.
Each leaf is now wrapped in a `Project` that copies the SeqScan's
schema 1:1 and adds a trailing `tableoid` column populated with the
constant `IntegerConst{Value: int64(leaf.OID)}`. The new helper:

```go
func wrapWithTableoid(child Node, tableOID uint32, sourceIdx int16, pos int) Node
```

handles the schema rewrite and the per-target ColumnRef list.

After the union is built, `b.tableOidColIdx` is set to
`len(b.table.Columns)` and `ctx.schema` is replaced with the
union's wider output (N + 1 columns). Downstream binding offsets
(`leftCtx.schema` / `rightBinding.offset`) are schema-driven, so
JOIN / Sort / Aggregate / Project all see consistent widths
without further changes.

`expandStarTarget` iterates `b.table.Columns` (the catalog columns)
and is naturally unaffected: `*` continues to expand to N columns,
not N + 1 — the trailing `tableoid` is reachable only by name,
matching PG semantics for system columns.

### 4. `regclass` cast on OID input

`evalExprSlot`'s `*planner.CastExpr` arm grows a special case: when
`v.Kind == KindInt && strings.EqualFold(x.TargetType, "regclass")`
and the catalog is reachable, it calls `LookupTableByOID(uint32(v.Int))`
and returns the table's `Name` as a `KindString` Datum. Unknown
OIDs (or non-`*catalog.InMemory` catalogs) fall through to
`evalCastTyped`, which preserves the integer value as-is.

`*catalog.InMemory` gains the public `LookupTableByOID(oid)
(*Table, bool)` accessor (a read-locked wrapper around the
existing private `tableByOID`). The constraint to `*catalog.InMemory`
is fine for v0 — the only production catalog is `InMemory`; future
schema-stored catalogs can implement the same public method.

## Drive-by fix: seqScanOp.Close double-RUnlock

The new test `SELECT tableoid::regclass FROM t LIMIT 1` triggered a
latent bug in `seqScanOp.Close`: the lock-scoping change from
M0100-0005e moved the page RLock acquisition inside `Next()`
(acquired before `PageGetHeapTuple`, released before yielding the
slot), and `releasePinned` was updated to drop only the pin. But
`Close()` still called `o.pinned.RUnlock()` before `Unpin`, which
is a double-RUnlock for any consumer that closes the scan early
(Limit, top-N Sort, lib/pq's auto-close on `LIMIT 1`, etc.). The
runtime catches this with `sync: RUnlock of unlocked RWMutex` and
fatal-panics the connection.

`Close()` now drops only the pin, mirroring `releasePinned`.

## Consequences

Positive:

- `partition-key-update-4` gets past the system-column resolution
  failure on every permutation. The remaining diffs are real
  cross-partition UPDATE EPQ-recheck engine bugs (separate scope —
  the SET expression is not re-evaluated against the EPQ-refetched
  row, so `b = b || ' update1'` ends up using the stale `b`).
- `SELECT tableoid::regclass FROM t LIMIT 1` no longer crashes the
  connection (regression pin: `TestTableoidRegclass_NonPartitioned`).
- All five M0100-0005 already-passing isolation specs continue to
  pass: `LockCommittedUpdate`, `InsertConflictDoUpdate`,
  `InsertConflictDoNothing`, `FkSnapshot`, `PartitionKeyUpdate{1,2,3}`.

Limitations / known gaps:

- The catalog interface does not yet expose `LookupTableByOID`. The
  cast is gated on `*catalog.InMemory` so non-default catalogs (none
  exist today) would fall through to the bare integer.
- `cmin` / `cmax` / `xmin` / `xmax` / `ctid` / `oid` system columns
  are still unrecognised. They follow the same shape — add the
  per-name arm in `resolveColumnRefAt` / `resolveColumnRefTypeAt`
  and a synthetic constant or per-row slot when needed.

## Regression pins

- `internal/server/tableoid_test.go`:
  - `TestTableoidRegclass_NonPartitioned` — qualified + unqualified
    `tableoid::regclass` against a plain table; verifies the
    `TableOidExpr` constant path and the `evalExprSlot` `regclass`
    cast OID-to-name lookup. Also exercises the early-Close path
    via `LIMIT 1` (catches the M0100-0005e double-RUnlock).
  - `TestTableoidRegclass_Partitioned` — `tableoid::regclass`
    against a partitioned table; verifies that each row carries its
    leaf OID via the per-leaf `wrapWithTableoid` Project, not the
    partitioned parent's OID.

## Related

- M0100-0005e: per-tuple page RLock scoping (fixed `releasePinned`,
  missed `Close`).
- M0096-0007: partition-aware scan as `SetOp` over leaves
  (provides the structural shape this fix slots into).
- M0103-0008 (rung 16): `pg_class.oid` numeric flip — established
  the precedent that `regclass` resolves text → OID via catalog
  lookup; this fix adds the reverse OID → text direction for the
  cast path.
