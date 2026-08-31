# 0123-0001 — `internal/pgnodes` scalar `pg_node_tree` serializer (M0123-S1)

**Status:** landed (S1). Follow-ups: S2 (resolver + attrdef/stxexprs writer wiring),
S3 (Query/view rewrite), S4 (more datum types + byte-diff oracle gate).

**Parent design:** `docs/design/wal-pg-identical-stream/02e-content-fidelity-and-durability.md` §3.
**Milestone:** `docs/milestones/0123-canonical-pg-node-tree-serialization.md`.

## Goal

A canonical PostgreSQL 18 `pg_node_tree` codec so a real PG18 standby can EVALUATE
goopg's stored user column DEFAULTs (`pg_attrdef.adbin`), extended-statistics
expressions (`pg_statistic_ext.stxexprs`), and later view rewrite actions
(`pg_rewrite.ev_action`). goopg has no OID-resolved node tree today (name-based
AST; runtime resolves by name), so the full subsystem is a resolver + serializer
+ datum codec. S1 delivers the **codec** — the serializer/reader for a scalar IR
— with a golden gate against real-PG `adbin` bytes. No resolver and no writer
wiring yet (those are S2).

## What S1 contains

New leaf package `internal/pgnodes` (no goopg deps — pure codec):

| file | role |
|------|------|
| `ir.go` | scalar IR node structs: `Const`, `FuncExpr`, `OpExpr`, `RelabelType`, `CoerceViaIO`, `SQLValueFunction`. One struct field per serialized PG field, in PG order. |
| `datum.go` | `Const` value ↔ raw PG datum wire bytes + typed constructors (`NewInt4Const`, `NewInt8Const`, `NewBoolConst`, `NewTextConst`) and the type/collation OID constants. |
| `outfuncs.go` | `Out(Node) string` → canonical S-expression text; field order mirrors `postgres/src/backend/nodes/outfuncs.c` per tag. |
| `readfuncs.go` | `Read(string) (Node, error)` → IR; a `pg_strtok`/`nodeRead` mirror of `read.c` + `readfuncs.c`. Unknown tag = clean error (the future "not canonical → fall back to SQL text" signal). |
| `pgnodes_test.go` | golden round-trip gate. |

## The output format (mirrored exactly from PG source)

`nodeToString` — which is literally what `pg_attrdef.adbin` stores — writes each
node as `{TAG :field value :field value ...}`. The macros (`outfuncs.c`):

- `WRITE_OID_FIELD` → `:name %u`; `WRITE_INT_FIELD`/`WRITE_ENUM_FIELD`/
  `WRITE_LOCATION_FIELD` → `:name %d`; `WRITE_BOOL_FIELD` → `:name true|false`;
  `WRITE_NODE_FIELD` → `:name {..}` or `<>`; a List → `:name (elem elem ...)` or
  `<>` when empty (NIL).

### Datum wire form (`outfuncs.c:outDatum`) — the traps

- **By-value** (int2/int4/int8/oid/bool): the length prefix is `constlen`
  (4/8/1/…) **but PostgreSQL always emits `sizeof(Datum) == 8` bytes** — the raw
  little-endian Datum word. Each byte is a **signed** decimal (`%d` of a `char`),
  so `0xFF` prints `-1`.
  - Negative `int4` **sign-extends** (`Int32GetDatum(-1)` → all `0xFF`):
    `:constvalue 4 [ -1 -1 -1 -1 -1 -1 -1 -1 ]`.
  - `oid`/positive values **zero-extend**: `16384` → `[ 0 64 0 0 0 0 0 0 ]`.
  - `bool true`, constlen 1, still 8 bytes: `1 [ 1 0 0 0 0 0 0 0 ]`.
- **By-reference** (text/varlena): length prefix = `VARSIZE`; emit that many
  bytes. The in-memory Const holds a **4-byte-header** varlena: header =
  `VARSIZE << 2` little-endian (both low flag bits 0). `'x'` → VARSIZE 5,
  header `5<<2 = 20`: `:constvalue 5 [ 20 0 0 0 120 ]`.

### Const-wrapping surprises captured in goldens

- `oid DEFAULT 16384` serializes as a **`RelabelType`** (int4 literal binary-cast
  to oid), not a bare oid Const.
- `smallint DEFAULT -2` serializes as a **`FuncExpr`** (`int4 → int2` cast fn),
  not an int2 Const. (S1 tests the scalar building blocks; the resolver that
  reproduces these wrappings from goopg AST is S2.)

## The gate (adversarial oracle)

`pgnodes_test.go` pins `Out` against **real PG18.3 `pg_attrdef.adbin` strings**
captured verbatim from a live server (`CREATE TABLE t(a int DEFAULT 42, …)` then
`SELECT ad.adbin FROM pg_attrdef …`). Because `adbin == nodeToString(default)`,
byte-equality is a true oracle, not a hand-derived approximation. `Read(golden)`
must then reconstruct an IR `reflect.DeepEqual` to the hand-built node and
re-`Out` to the same bytes. 20 subtests, all green; covers positive/negative
sign-extension, oid zero-extension, text varlena header, int8 full-width, bool
short-len, and the OpExpr/FuncExpr/RelabelType nestings.

## Deferred to later slices

- **S2**: `resolver_expr.go` (goopg `parser.Expr` + catalog + the S0 operator/proc
  OID lookups → IR), `rebuild.go` (IR → goopg AST for reload), `unsupported.go`
  (all-or-nothing scalar shape check → fall back to SQL text), and wiring
  `writeAttrdefRow` / the `stxexprs` writer + the `loadColumnDefaultsFromHeap` /
  `loadStatisticsExtFromHeap` reload paths.
- **S3**: `Query`/`RangeTblEntry`/`Var`/`TargetEntry` for `pg_rewrite.ev_action`.
- **S4**: numeric/timestamptz/… datums, `CASE`/`BoolExpr`/`NullTest`, and the
  full byte-diff oracle over emitted `adbin`/`ev_action` vs real PG for identical
  DDL.

See `.ralph/deferral_ledger.md` (2026-07-19, M0123-S1) for the tracked follow-up.
