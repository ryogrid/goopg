# 0123-0004 — `internal/pgnodes` query-tree serializer + resolver + rebuild (M0123-S3, sub-slices 1–2b)

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

## Sub-slice 2b — the rebuild inverse (`rebuild_query.go`)

`RebuildViewQuery(*Query) (*parser.SelectStmt, error)` is the reload-time inverse
of `ResolveViewQuery` (the query-tree analogue of S2's scalar `Rebuild`), still a
pure `internal/pgnodes` addition with **no engine wiring yet**. On restart the
per-database view reload reads the stored `ev_action` (`ReadRuleAction`) and
`RebuildViewQuery` turns the IR `Query` back into a goopg view-definition AST so
goopg re-registers/plans the view exactly as before.

- **Self-describing rebuild — no live catalog lookup.** The `Query` already
  carries everything the inverse needs: the FROM item's name is the single RTE
  `eref.aliasname` and every column name is that `eref.colnames` list (the
  forward resolver populates it with the relation's full column list in
  attribute-number order), so a `Var`'s `varattno` maps straight back to a
  column reference via `colnames[attno-1]`. This is why the rebuild takes no
  `RelationResolver` — unlike the forward direction, it needs no external
  metadata.
- **Fixed point.** `ResolveViewQuery → OutRuleAction … ReadRuleAction →
  RebuildViewQuery → ResolveViewQuery` reproduces the input `Query`
  byte-for-byte. The one subtlety is the target `resname`: `rebuildTarget`
  emits an explicit `AS` alias **only** when the stored `resname` differs from
  the name the forward `queryScope.targetName` would auto-derive (a column
  reference's column name, a function call's name) — the exact inverse of that
  rule — so plain column targets round-trip without redundant aliases while an
  aliased computed target (`upper(src) AS us`) keeps its alias.
- **Sharing with S2's `rebuild.go`.** `rebuildOpExpr`/`rebuildFuncExpr` were made
  recursion-injectable (`rebuildOpExprWith`/`rebuildFuncExprWith(node, rec)`) so
  the query scope reuses the identical opno→spelling→`OpCode` and
  funcid→proname reconstruction while supplying its own recursion that also
  handles column `Var`s; the scalar path passes `Rebuild` unchanged. The only
  new leaf is `Var` → `parser.ColumnRef`.
- **Gate** (`rebuild_query_test.go`): for both golden views, resolve → rebuild →
  re-resolve → `OutRuleAction` == the live PG18.3 `ev_action` golden
  byte-for-byte; a structural inspection of the rebuilt AST (FROM item;
  no-redundant-alias on a plain column target; `WHERE` operator shape; explicit
  alias retained for an aliased computed target); and a producer/reader-mismatch
  matrix (non-SELECT command, empty rtable, empty target list, out-of-range Var
  attno).

## Sub-slice 2c — the engine wiring (LANDED 2026-07-19)

The resolver (2a) and reload inverse (2b) are now wired into the runtime so a
plain view's `pg_rewrite.ev_action` is a canonical PG18 node tree end-to-end.

**Write path.** `executor.canonicalViewEvAction(ctx, tbl, sqlText)` runs
`pgnodes.ResolveViewQuery` (via the new `viewRelationResolver`, a
`pgnodes.RelationResolver` backed by the live catalog) and returns
`(OutRuleAction bytes, true)` for a supported plain view, else `(sqlText,
false)`. `syncTableToCatalogHeap` calls it **before** `buildUserPGClassRow` and
stores the result in `tbl.RuleIsCanonical` — this ordering is load-bearing: the
streamed `pg_class` heap row is what a PG standby reads for `relhasrules`, so the
flag must be set before that row is built (an earlier draft set it inside
`writeViewRewriteRow`, which runs *after* the pg_class write, and the standby saw
`relhasrules=false`). The resolved `ev_action` is threaded to
`writeViewRewriteRow` so resolution runs once.

**Var type fidelity.** `viewColumnCanonicalType` derives each column's
`atttypid/atttypmod/attcollation` by reading them back out of
`buildUserPGAttributeRow` — the exact bytes the standby's `pg_attribute` carries
— so a serialized `Var`'s `vartype/varcollid` cannot drift from the standby's own
catalog. Array/domain/enum/composite/range columns (OID ≥ `FirstUserOID`) fall
out of the subset → SQL-text fallback.

**`relhasrules`.** Both the heap row (`buildUserPGClassRow`) and the virtual
`pg_class` builder (`catalog.go`) now read `tbl.RuleIsCanonical`; every
system/`information_schema` relation keeps it false (they never set the flag).

**Reload.** `loadViewsFromHeapForDB` calls `rebuildViewFromEvAction`, which
discriminates on the leading `"({"`: canonical → `ReadRuleAction` →
`RebuildViewQuery` (restores `RuleIsCanonical=true`), else `parser.Parse`.

**Gates.**
- `internal/pgnodes` unchanged (still green).
- `executor` `TestViewColumnCanonicalType` / `TestViewAttrIndexConstants`.
- `initdb` `TestRebuildViewFromEvAction` (both ev_action forms + garbage).
- `testport` `TestPort_ViewsSurviveRestart` now asserts `relhasrules=true`
  survives a goopg restart (canonical write → reload round-trip).
- `testport` `TestE2E_FailoverGoopgToPG`: a real PG18 standby reports
  `relhasrules=true` and, via `pg_get_viewdef`, PARSES the canonical `ev_action`
  through its own `stringToNode` and DEPARSES it back to the exact defining
  SELECT — the adversarial proof of byte-level serializer compatibility.

**Deferred (ledger 2026-07-19):** a direct `SELECT * FROM v` on the promoted
standby still fails `42809` — PG's rewriter uses the relcache rule lock
(`rd_rules`), not the direct `pg_rewrite` scan `pg_get_viewdef` uses, and the
copied `pg_internal.init` caches a ruleless relcache entry for the view.
Row-level standby evaluation waits on relcache `rd_rules` population; the
canonical serializer + wiring are proven independently of it.
