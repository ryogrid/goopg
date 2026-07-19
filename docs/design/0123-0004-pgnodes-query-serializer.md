# 0123-0004 — `internal/pgnodes` query-tree serializer + resolver (M0123-S3, sub-slices 1–2a)

Status: accepted
Milestone: M0123 (canonical `pg_node_tree` serialization), slice S3
Depends on: [0123-0001](0123-0001-pgnodes-scalar-serializer.md) (scalar codec),
[0123-0002](0123-0002-pgnodes-scalar-resolver.md) (scalar resolver)

## Why

`pg_rewrite.ev_action` stores a view's rewrite rule as
`nodeToString(list_of_Query)` — a canonical PG18 `pg_node_tree`. Today goopg
persists views as SQL text and cannot present a queryable `pg_rewrite` to a real
PG18 standby: a standby that reads goopg's streamed catalog rows would find no
usable `ev_action` and could not plan `SELECT * FROM v`. To close that gap
(M0123-S3), the `internal/pgnodes` codec must be able to serialize — and read
back byte-for-byte — the query-tree subset a simple view produces.

This sub-slice adds **only the codec** (the `Out`/`Read` node set below), mirror­
ing how S1 landed the scalar codec before S2 wired a resolver. No resolver
(`*parser.SelectStmt` → IR `Query`), no `writeViewRewriteRow` wiring, no
`relhasrules`/`loadViewsFromHeap` change — those are sub-slice 2, gated by the
standby-query E2E. Landing the codec first keeps the risky, purely-mechanical
byte-fidelity work isolated behind a fast golden gate.

## Scope

The single-base-relation `SELECT` view shape:

```sql
CREATE VIEW v AS SELECT a, b FROM t WHERE a > 0;      -- one RTE_RELATION, optional qual
CREATE VIEW v2 AS SELECT upper(b) AS u FROM t;        -- computed FuncExpr target, no qual
```

That covers: one `RTE_RELATION` range-table entry with its `Alias`/`colnames`,
an optional `WHERE` qual (reusing S1's `OpExpr`/`Const` plus the new `Var`), a
flat target list of `Var`s or scalar expressions (S1's `FuncExpr` etc.), and the
matching `RTEPermissionInfo` with a `selectedCols` `Bitmapset`.

## New IR nodes and primitives (`ir_query.go`)

| tag | struct | notes |
|-----|--------|-------|
| `QUERY` | `Query` | only `commandType`/`rtable`/`rteperminfos`/`jointree`/`targetList` modeled; the other ~40 fields are fixed at view defaults |
| `RANGETBLENTRY` | `RangeTblEntry` | `rtekind == RTE_RELATION` (0) only |
| `RTEPERMISSIONINFO` | `RTEPermissionInfo` | `requiredPerms` is `uint64` (AclMode); cols are `Bitmapset` |
| `FROMEXPR` | `FromExpr` | `fromlist` + optional `quals` |
| `RANGETBLREF` | `RangeTblRef` | `rtindex` |
| `TARGETENTRY` | `TargetEntry` | |
| `VAR` | `Var` | full field set (varno…location) |
| `ALIAS` | `Alias` | `aliasname` + `colnames` (List of String value nodes) |

Two new wire primitives PostgreSQL uses here that the scalar codec did not:

- **Bitmapset** (`type Bitmapset []int32`) — serialized `(b m0 m1 ...)`, empty
  `(b)`. Members are the raw on-wire integers; any producer-side offset encoding
  (e.g. `selectedCols` stores `attno - FirstLowInvalidHeapAttributeNumber`,
  which is `-7` in PG18, so `client`/`src` at attnos 1/2 serialize as `8`/`9`)
  is the **resolver's** concern (sub-slice 2), not the codec's.
- **String value node** — `Alias.colnames` is a `List` of `T_String`, and
  `outNode` writes each `T_String` wrapped in double quotes (`"client"`),
  distinct from a `WRITE_STRING_FIELD` char\* like `aliasname` which uses bare
  `outToken` (unquoted for a plain identifier). `outToken` is ported faithfully
  (escapes leading special/digit chars and embedded specials) so non-identifier
  names round-trip.

## `Out`/`Read` and the shape gate

`outfuncs_query.go` mirrors `outfuncs.c` field order per tag exactly; the
`Query` skeleton emits all ~45 fields (the fixed ones at their view-default
literals) so the byte stream matches PG. `OutRuleAction([]Node)` writes the
outer `(...)` list wrapper for a whole `ev_action`.

`readfuncs_query.go` is the inverse and **doubles as the "is this a supported
canonical view?" gate**: `readQuery` validates every fixed `Query` field
(`hasAggs`, `hasSubLinks`, `mergeActionList`, `limitCount`, …) against its
view-default; `readRangeTblEntry` rejects any non-`RTE_RELATION` kind and any
`tablesample`/`securityQuals`. Any deviation is returned as an error — exactly
the caller's signal (in sub-slice 2) to keep the view as SQL text rather than
emit a partial `pg_node_tree` that would FATAL a standby's relcache (02e §3).

## Gate

`query_roundtrip_test.go` pins the codec against **two live-captured PG18.3
`ev_action` goldens** (views `v` with a `WHERE` qual and `v2` with a computed
`upper()` target and no qual): `Out(Read(golden)) == golden` byte-for-byte, plus
a structural spot-check (relid, relkind, colnames, `selectedCols == [8 9]`, the
qual `OpExpr.opno == 521`) so a byte-identical but semantically wrong decode
can't slip through, plus a negative test that a `hasAggs true` query is rejected
by the shape gate. `go test ./internal/pgnodes/` green; `go vet` clean.

## Sub-slice 2a — the resolver (`resolver_query.go`)

`ResolveViewQuery(*parser.SelectStmt, RelationResolver) (*Query, error)` is the
forward direction (the query-tree analogue of S2's `ResolveExpr`), a pure
`internal/pgnodes` addition with **no engine wiring yet**. It converts a goopg
view definition into the IR `Query` whose `OutRuleAction` bytes are identical to
what PG stores in `pg_rewrite.ev_action` for the same DDL.

- **Scope** = exactly the shape the codec round-trips: `SELECT <targets> FROM
  <one table> [WHERE <qual>]`, target list of column refs / scalar exprs, no
  alias, no `*`. Everything else → `ErrUnsupported` (writer stores SQL text,
  keeps `relhasrules=false`; all-or-nothing per 02e §3).
- **`RelationResolver`** is a small interface (`LookupRelation(schema, name) →
  *RelationInfo`) the wiring layer (sub-slice 2c) implements against the live
  catalog; the leaf package stays dependency-free of the executor. `RelationInfo`
  carries the base relation's OID/relkind and its full column list
  (name/attno/type/typmod/collation).
- **What it computes** (matching PG parse-analysis):
  `Var.varno`=1 / `varattno`=attno / syn fields; the `selectedCols` Bitmapset
  (each referenced attno biased by `+selectedColsBias`, where
  `selectedColsBias = -FirstLowInvalidHeapAttributeNumber = 7`,
  `postgres/src/include/access/sysattr.h`); `TargetEntry.resorigtbl/resorigcol`
  (relid+attno for a plain `Var`, 0 for a computed expr); `resname` (AS alias,
  else column name, else function name); the fixed `RTE_RELATION` /
  `AccessShareLock` (`rellockmode`=1) / `ACL_SELECT` (`requiredPerms`=2) /
  `perminfoindex`=1 skeleton; `inh`=true.
- **Sharing with S2**: the operator/function builders (`buildOpExpr`,
  `buildFuncExpr`, `funcCallGuard`) were extracted from `resolver_expr.go` so the
  scalar and query-scoped resolvers construct byte-identical `OpExpr`/`FuncExpr`;
  the only new leaf is the column reference → `Var`.
- **Gate** (`resolver_query_test.go`): resolving each of the two golden views'
  goopg SELECT AST → `OutRuleAction` == the live PG18.3 `ev_action` golden
  byte-for-byte; a resolve→`Out`→`Read`→re-`Out` round-trip; a structural
  spot-check (`selectedCols`, `resorigtbl/resorigcol`); and a 10-case
  `ErrUnsupported` matrix (GROUP BY / ORDER BY / LIMIT / DISTINCT / aliased FROM /
  `*` / two relations / unknown column / unknown relation / set op).

## Deferred to sub-slice 2 (M0123-S3 wiring)

- `rebuild.go`: IR `Query` → goopg view AST for the reload path.
- Wire `writeViewRewriteRow` to store canonical `ev_action`; set
  `catalog.Table.RuleIsCanonical`; flip `pg18_user_catalog_rows.go` `relhasrules`
  to read it; swap `loadViewsFromHeap`; update the `relhasrules=false` test
  lock-ins.
- Gate: standby-query E2E (goopg primary `CREATE VIEW` + a PG standby
  `SELECT * FROM v` returning rows `==` goopg's own).
