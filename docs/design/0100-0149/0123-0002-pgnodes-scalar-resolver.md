# 0123-0002 — `internal/pgnodes` scalar resolver + rebuild (M0123-S2, sub-slice 1)

**Status:** landed (S2 sub-slice 1 — resolver/rebuild/shape-check only; writer +
reload wiring and the standby-eval E2E gate are S2 sub-slice 2, still open).

**Parent design:** `docs/design/wal-pg-identical-stream/02e-content-fidelity-and-durability.md` §3.
**Predecessor:** `docs/design/0123-0001-pgnodes-scalar-serializer.md` (S1 codec).
**Milestone:** `docs/milestones/0123-canonical-pg-node-tree-serialization.md`.

## Why sub-divide S2

S2 as written bundles four things: (a) the `parser.Expr → IR` resolver, (b) the
`IR → parser.Expr` reload rebuild, (c) the all-or-nothing shape check, and (d)
wiring `writeAttrdefRow` / the `stxexprs` writer + the two reload paths behind a
`{`-discriminator, gated by an **adversarial standby-eval E2E** (goopg primary
emits canonical `adbin`, a real PG18 standby `INSERT … DEFAULT VALUES` and the
row is asserted `==` goopg's own). (d) changes what bytes land in the per-DB
`pg_attrdef` heap and must not regress the M0114 cache-hit reload path, so it is
gated by the expensive E2E and kept as its own commit. (a)–(c) are pure
`internal/pgnodes` additions with zero integration risk — this sub-slice lands
them behind unit tests, mirroring how S1 landed the codec un-wired.

## What this sub-slice contains

| file | role |
|------|------|
| `resolver_expr.go` | `ResolveExpr(parser.Expr, targetType) (Node, error)` — goopg default-expr AST → scalar IR, using S0's `catalog.LookupOperatorForNode`. |
| `rebuild.go` | `Rebuild(Node) (parser.Expr, error)` — the reload inverse (canonical bytes, once read, back to a goopg AST goopg re-evaluates). |
| `unsupported.go` | `SupportsExpr(parser.Expr, targetType) bool` — the all-or-nothing subset predicate (02e §3: partial-emit FATALs PG's relcache, so it is text-or-nothing). |
| `resolver_expr_test.go` | canonical-`Out` pins + resolve→Out→Read→Rebuild→re-resolve round-trip + shape-check accept/reject table. |

`internal/pgnodes` now imports `internal/parser` and `internal/catalog` (both of
which are lower in the graph and do **not** import `pgnodes`, so it stays
acyclic; only the resolver files carry those imports — the S1 codec files remain
dependency-free).

## Supported subset (this sub-slice)

- **Integer literals** typed by magnitude the way `make_const` does: int4 when
  the (post-fold) value fits a signed int4, else int8. An int8 `targetType`
  widens (e.g. `DEFAULT 5` on a bigint column → int8 Const).
- **Unary minus on an integer literal** folded into one negative Const
  (PG `doNegate`); the datum word sign-extends to all-`0xFF` (`Int32GetDatum`).
- **Text literals** in a text context (bare string literal is `unknown`; coerced
  to text). `constcollid = 100`.
- **Binary operators** (`OpExpr`) whose operand type OIDs forward-resolve to a
  built-in `pg_operator` row via `LookupOperatorForNode(spelling, l, r)`; the
  `opno`/`opfuncid`/`opresulttype` come straight from the seed row so a PG
  standby resolves the same OIDs. Collation follows PG: `inputcollid`/`opcollid`
  are `100` only when a text operand/result is involved, else `0`.
- **Plain built-in function calls** (`FuncExpr`): the `funcid` forward-resolves
  by `(proname, actual-arg-type OIDs)` via `LookupProcForNode`, and
  `funcresulttype` comes from the generated `pgProcRetTypeByOID` leaf map
  (`catalog.ProcResultType`, emitted by `cmd/gen-pg-proc-data -names`). Only the
  ordinary-call shape is accepted — no aggregate/window decoration (`OVER`,
  `FILTER`, `ORDER BY`, `WITHIN GROUP`, `DISTINCT`), no star argument, no
  `VARIADIC` spread, and only unqualified / `pg_catalog`-qualified names.
  `funcformat = 0` (`COERCE_EXPLICIT_CALL`); `funccollid`/`inputcollid` are `100`
  when a text result/operand is involved, else `0`.

Everything else (non-built-in casts, numeric/other datums, a string literal in a
non-text context, column references, subqueries) returns `ErrUnsupported` so the
writer keeps SQL text. `ResolveExpr` is all-or-nothing: any unsupported sub-node
aborts the whole resolution, which is exactly the `SupportsExpr` predicate.

## The gate

`resolver_expr_test.go` (10 subtests via the shared package):
- `Out(ResolveExpr(parse(sql)))` pinned byte-for-byte for `42`, `-1` (all-`0xFF`
  sign-extension, matching the S1 golden), a bigint literal, and a text literal.
- `40 + 2` forward-resolves to an `OpExpr` with a non-zero seed `opno`/`opfuncid`
  and int4 result/args.
- Full reload round-trip resolve → `Out` → `Read` → `Rebuild` → re-`ResolveExpr`
  is byte-stable for `42`, `-1`, a bigint, `40 + 2`, `1 + 2 * 3`, and
  `upper('x')` — proving `Rebuild` is a faithful inverse without depending on
  parser node positions (a `FuncExpr` rebuilds via `funcid → proname → re-resolve`).
- `upper('x')` `Out` pinned byte-for-byte against a **live PG18.3
  `pg_attrdef.adbin`** golden captured from `b text DEFAULT upper('x')` (funcid
  871, funcresulttype 25/text, `funccollid`/`inputcollid` 100).
- `SupportsExpr` accept/reject table (accepts `upper('x')`; rejects `'5'`::int
  context, `1.5`, `a + 1`).

## Resolved after the initial sub-slice-1 commit

- **`FuncExpr` resolution** landed alongside sub-slice 1 (`e85ccb53`) — the
  generator now emits a `pgProcRetTypeByOID` leaf map, `catalog.ProcResultType`
  reads it, and `resolveFuncCall`/`rebuildFuncExpr` handle `parser.FuncCall`. A
  follow-up loop (2026-07-19) validated the output byte-for-byte against a live
  PG18.3 `adbin` golden and reconciled the previously-red `SupportsExpr` test
  (it had still asserted `upper('x')` was unsupported). This unblocks the
  `DEFAULT upper('x')` arm of the S2 E2E gate.

## Landed in S2 sub-slice 2 — see `0123-0003-pgnodes-attrdef-writer-reload-wiring.md`

- **`pg_attrdef` writer wiring**: `writeAttrdefRow` now stores canonical bytes via
  `pgnodes.ResolveForColumn` (exact-type-match) → `Out`, else SQL text — as a
  plain `string` datum (nodeToString is pure ASCII, so no `NewBytesDatum` / codec
  change was needed after all).
- **`pg_attrdef` reload swap**: `loadColumnDefaultsFromHeap` branches on the
  leading `{` → `pgnodes.Read` → `Rebuild`, else `parser.ParseExpr`; still
  standalone-unconditional per `NamespaceDBOid`.

## Still deferred (see the deferral ledger, 2026-07-19)

- **`stxexprs` canonical writer/reload**: blocked on a `List` IR node
  (`pg_statistic_ext.stxexprs` is a `List` of trees, `(...)` not `{...}`); the
  writer keeps the goopg `text[]` SQL-text literal until S3/S4 adds `List`.
- **Adversarial standby-eval E2E**: blocked by `pg_attrdef` catalog completeness
  on the standby, NOT by the canonical bytes — goopg's streamed `pg_attribute`
  (relid 2604) does not give a real PG18 a usable `adbin` column, and PG's
  `AttrDefaultFetch` opens the unmaterialized `adrelid/adnum` index (OID 2656).
  Both are orthogonal `pg_attrdef`-surface gaps; resume point in 0123-0003.
