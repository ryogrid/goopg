# Module: `internal/nodes`

The plan/expression node representation shared across the parser → optimizer →
executor pipeline, plus its serializer/deserializer pair and a resolver that
mirrors PostgreSQL's `parse_expr.c` name/type resolution over the IR. It is the
Go analogue of PG's `src/backend/nodes/{nodes,outfuncs,readfuncs}` plus the
planner-side part of `parse_node.c`.

Two node families live here:

1. **The parser AST** (`ir.go`, `ir_query.go`) — the `Node` interface and the
   concrete expression nodes (`Const`, `FuncExpr`, `OpExpr`, `BoolExpr`,
   `CaseExpr`, `NullTest`, `BooleanTest`, `RelabelType`, `CoerceViaIO`,
   `SQLValueFunction`, `DistinctExpr`, …). These are the same types
   `internal/parser` builds.
2. **The optimizer IR** (`rebuild.go`, `rebuild_query.go`) — the plan-node tree
   (`optimizer.Node`) the planner emits and the executor consumes.

The package's reason to exist as its own boundary: the parser cannot import the
optimizer (layer rule), so the serializable/analyzable expression layer lives in
`nodes`, shared by both.

## Key Files

- `datum.go` (1,615) — `Datum` value representation helpers used across nodes
  (the executor's own `executor/datum.go` is separate; this one serves the
  serializer/resolver).
- `resolver_expr.go` (1,813) — `ResolveExpr` + every `resolve*` helper: the
  PG `transformExpr` analogue that turns a parsed expression into a typed IR
  node (function-call resolution, operator resolution, cast coercion,
  type-mod length wrapping, literal folding).
- `rebuild.go` (884) — plan-node cloning/rebuilding (`Clone`, deep-copy helpers).
- `readfuncs.go` (586) — `Read`: text-mode deserializer (the `readNode`/`read*`
  family), the inverse of `outfuncs`.
- `resolver_query.go` (485) — query-level resolution (`ResolveQuery` / query
  statement transform).
- `readfuncs_query.go` (485) — query-node deserialization.
- `outfuncs.go` (358) — `Out`: text-mode serializer producing PG-compatible
  `(node …)` S-expression strings (used for `pg_attrdef.adbin`, `pg_rewrite`
  rule text, `pg_get_expr`, and the `pg_node_tree` format).
- `numeric_storage.go` (356) — numeric (decimal) node storage / serialization.
- `ir.go` (242) — the `Node` interface and expression node structs.
- `ir_query.go` (131) — query node structs.
- `unsupported.go` (22) — `ErrUnsupported` marker for unresolvable constructs.

## Public API

```go
// Serialization pair (PG "nodeToString" / "stringToNode")
func Out(n Node) string                     // -> "(SELECT ...)" S-expression
func Read(s string) (Node, error)           // <- parse it back
func OutList(nodes []Node) string
func ReadQuery(s string) (Node, error)      // query-flavored variant

// Resolution (PG transformExpr analogue)
func ResolveExpr(e Expr, s scope) (Node, error)
func ResolveForColumn(...)                  // typed column resolution
func ResolveQuery(...)                      // full-statement resolution
```

## Internal structure

- **Serializer** (`outfuncs.go`) — walks each node struct and emits the PG
  `(tagname field1 field2 …)` text format. Field order and naming must match
  what PG's `nodeToString`/`stringToNode` expect so the output is
  interchangeable (goopg writes `adbin`/`ev_action` bodies a real PG can read
  back, and reads `pg_get_expr`/`pg_rewrite` bodies PG wrote).
- **Deserializer** (`readfuncs.go`) — a small tokenizer over that S-expression
  grammar with per-field readers (`readUint32`, `readInt32`, `readBool`,
  `readDatum`, `readNode`, `readNodeList`).
- **Resolver** (`resolver_expr.go`) — the biggest and most active file. It
  implements overloaded operator/function resolution (`resolveFuncCall`,
  `resolveBinaryOp`, `buildOpExpr`), type coercion insertion
  (`resolveCastExpr`, `wrap*LengthCoercion`, the numeric-family cast helpers),
  literal typing/folding (`resolveIntLiteral`, `resolveNumericLiteral`,
  `foldStringLiteralConst`), and the Boolean/CASE/DISTINCT/NULL-test special
  cases — each with a `…With` sibling that threads a destination type for
  better inference.

## Dependencies

- **Used by** — `internal/parser` (AST), `internal/optimizer` (IR),
  `internal/executor` (evaluates IR; serializes `adbin`/`ev_action`),
  `internal/catalog` (`pg_node_oid_lookup`), `internal/initdb` (bootstrap seeds).
- **Uses** — `internal/utils/mmgr` (allocation), `internal/utils/adt/*`
  (numeric/bit/datetime type helpers in the resolver), `internal/parser`
  (expression/AST types, for the resolver's input).

## Notable patterns / gotchas

- **`Out`/`Read` must round-trip** — a serializer change without the matching
  deserializer change corrupts every persisted `pg_node_tree` (attrdef defaults,
  view rules, index expressions). Treat the pair as one unit (Hard-won Rule #2).
- **PG-compat is byte-level** — `Out` emits field names and numbers in PG's
  exact order; a real PG 18.3 must be able to `stringToNode` goopg's output and
  vice versa. `numeric_storage.go` keeps the numeric node's serialized shape
  PG-compatible.
- **Resolver twin search** — the `resolve*`/`resolve*With` pairs exist because
  expression resolution needs to thread a known destination type when the parent
  supplies one (e.g. a cast target or a column type). Forgetting the sibling in
  a refactor silently degrades type inference (the M0134 class of bugs).
- **Two node families, one package** — the parser AST and the optimizer IR both
  live here; do not "move" expression nodes to the parser without breaking the
  layer rule (parser cannot import optimizer).
- **`ErrUnsupported`** — the resolver declines constructs it cannot type
  (`unsupported.go`); callers must fall back rather than panic.