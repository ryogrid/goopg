# 0123-0003 — canonical `pg_attrdef.adbin` writer + reload wiring (M0123-S2, sub-slice 2)

**Status:** landed (writer + reload sibling pair; canonical `stxexprs` and the
adversarial standby-EVAL gate deferred — see below and the deferral ledger).

**Parent design:** `docs/design/wal-pg-identical-stream/02e-content-fidelity-and-durability.md` §3.
**Predecessor:** `docs/design/0123-0002-pgnodes-scalar-resolver.md` (S2 sub-slice 1 — resolver/rebuild/shape-check).
**Milestone:** `docs/milestones/0123-canonical-pg-node-tree-serialization.md`.

## What landed

The column-DEFAULT half of S2's wiring — the `writeAttrdefRow` writer and the
`loadColumnDefaultsFromHeap` reload — now round-trip **canonical PG18
`pg_node_tree`** bytes for the supported scalar subset, discriminated from
goopg's legacy SQL text by a leading `{`.

| file | change |
|------|--------|
| `internal/pgnodes/resolver_expr.go` | new `ResolveForColumn(e, targetType) (Node, bool)` — the writer-facing entry point. Returns canonical IR only when the whole expression resolves **and its top-level result type OID equals `targetType` exactly**. |
| `internal/executor/sys_pg_attrdef.go` | new `canonicalAttrdefText(col)` — emits `pgnodes.Out(ResolveForColumn(...))` when supported, else `catalog.FormatExprForAttrdef` (SQL text). |
| `internal/executor/operators_ddl.go` | the `writeAttrdefRow` funnel now stores `canonicalAttrdefText(col)`. |
| `internal/initdb/catalog_heap_reload.go` | new `rebuildAttrdefExpr(adbin)` — `adbin[0]=='{'` → `pgnodes.Read → Rebuild`, else `parser.ParseExpr`. The reload stays standalone-unconditional per `NamespaceDBOid`. |

### The exact-type-match guard (`ResolveForColumn`)

`ResolveExpr` types an integer literal purely by magnitude (int4 if it fits, else
int8) — it ignores the column context beyond int8-widening / text-coercion. That
is fine for the resolver's own round-trip, but a **writer** must not emit an int4
`Const` for a `numeric`/`smallint` column: PG's `build_column_default`
(`postgres/src/backend/catalog/heap.c` → `coerce_to_target_type`) returns the
stored default already coerced to the attribute type, so a canonical tree whose
top node's type disagrees with the column would misinsert on a standby.
`ResolveForColumn` therefore returns `(nil, false)` unless `resultType ==
targetType`, degrading `DEFAULT 0` on numeric, `DEFAULT 5` on smallint, and a
string literal on a non-text column to SQL text. This is stricter than
`SupportsExpr` (whose already-tested semantics are left unchanged for the
resolver's round-trip gate).

### Why `adbin` stays a plain-`string` datum (no `NewBytesDatum`)

`nodeToString` output is pure ASCII even for by-reference datums (their bytes are
rendered as space-separated **decimals**, e.g. `constvalue 5 [ 20 0 0 0 120 ]`),
so the canonical form stores byte-identically through the existing
`NewStringDatum` and decodes unchanged through the reload's `StringValue()`. No
codec change and no `writeAttrdefRow` signature change were needed; the leading
`{` is the only discriminator. (The sub-slice-1 design's `NewBytesDatum`
suggestion was unnecessary given this ASCII property.)

## Gate

Fast unit tests (no real PG needed — the canonical bytes are already pinned
byte-for-byte against live PG18.3 goldens in `internal/pgnodes/pgnodes_test.go`
and `resolver_expr_test.go`):

- `internal/pgnodes` `TestResolveForColumn` / `TestResolveForColumnRoundTripDiscriminator` — the exact-type-match accept/reject table + the leading-`{` property + `Out→Read→Rebuild`.
- `internal/executor` `TestCanonicalAttrdefText` — the writer wrapper's canonical-vs-SQL-text branch, incl. `now()`→canonical (funcid 1299, type matches) and CASE/numeric/smallint→text.
- `internal/initdb` `TestRebuildAttrdefExpr` — the reload discriminator round-trips both forms to the same SQL, plus malformed-value error handling.
- `TestE2E_FailoverGoopgToPG` still passes (bench_log has no defaults; canonical adbin only affects defaulted tables, of which the test has none on the replicated path).

## Deferred — discovered this sub-slice (see the deferral ledger, 2026-07-19)

1. **Adversarial standby-EVAL gate blocked by `pg_attrdef` catalog completeness.**
   The milestone's E2E (a promoted PG18 running `INSERT … DEFAULT VALUES` and
   computing `42`/`'X'`/`-1` itself) cannot pass yet — and the blocker is
   **orthogonal to node-tree serialization**. Two gaps surfaced when a real PG18
   standby tried to consume goopg's replicated `pg_attrdef`:
   - `pg_attrdef` is a **non-nailed** catalog, so PG rebuilds its tuple
     descriptor from the streamed `pg_attribute` rows (relid 2604) at relcache
     time; goopg's on-disk `pg_attribute` for 2604 does not expose a usable
     `adbin` column (a direct `pg_get_expr(adbin, adrelid)` on the standby fails
     `column "adbin" does not exist`).
   - PG's `AttrDefaultFetch` (`relcache.c`) opens `pg_attrdef` **by** its
     `adrelid/adnum` index (`AttrDefaultIndexId` = 2656), which goopg does not
     materialize (`could not open relation with OID 2656`).
   Both are pre-existing `pg_attrdef` surface gaps (the second fires *before* PG
   parses `adbin`, so it is independent of the adbin format). Resume point: make
   goopg's initdb bootstrap complete `pg_attribute` rows for 2604 and materialize
   the 2656/2657 index files (mirroring the pg_class/pg_attribute index
   machinery), then re-add the standby-eval assertion to `TestE2E_FailoverGoopgToPG`.

2. **Canonical `stxexprs` blocked on a `List` IR node.** `pg_statistic_ext.stxexprs`
   is a `pg_node_tree` holding a **`List` of expression trees** (`(...)`, not a
   single `{...}`). The `internal/pgnodes` IR has no `List` node yet (that arrives
   with S3/S4's `Query`/target-list work), so the `stxexprs` writer keeps storing
   the goopg `text[]` literal of SQL text unchanged. Resume point: add a `List`
   node + `Out`/`Read` arms, then wire `sys_pg_statistic_ext.go`'s `stxexprs`
   emit + `loadStatisticsExtFromHeap` behind the same `{`/`(` discriminator.
