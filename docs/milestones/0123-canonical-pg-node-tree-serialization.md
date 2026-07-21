# Milestone 0123 — Canonical `pg_node_tree` serialization

**Status:** in-progress
**Filed:** 2026-07-18
**Reference plan:** `.ralph/fix_plan.md` (M0123 section)
**Design:** `docs/design/wal-pg-identical-stream/02e-content-fidelity-and-durability.md` §3
**Branch:** `wal-pg-nodetree`

## Goal

Give goopg a **canonical PostgreSQL `pg_node_tree` serializer** so a real PG18
standby can *evaluate and query* goopg's user column DEFAULTs, extended-statistics
expressions, and views — the last content-fidelity gap after Phase B closed the
rmid-128 WAL retirement.

Today three catalog columns store goopg **SQL text** instead of PG's OID-resolved
node tree, and `pg_class.relhasrules` is forced `false` for user views:

- `pg_attrdef.adbin` — column DEFAULT expressions (writer
  `internal/executor/sys_pg_attrdef.go:writeAttrdefRow`)
- `pg_statistic_ext.stxexprs` — statistics ON-expressions
  (`internal/executor/sys_pg_statistic_ext.go`)
- `pg_rewrite.ev_action` — view/matview defining query
  (`internal/executor/sys_pg_rewrite.go:writeViewRewriteRow`)

A heap INSERT of these replays on a standby, but PG cannot **parse** the goopg SQL
text as a node tree, so it cannot evaluate a default or expand a view rule.
goopg's own restart and goopg↔goopg replication already work via the SQL-text
convention; the payoff of this milestone is **standby-side querying/evaluation**
(and, as a side effect, closing the "renaming a table breaks a view stored by
name" gap, since views become OID-resolved).

## Why this is a milestone, not a patch

goopg has **no OID-resolved node tree to serialize**. Its parser AST is
name-based (`ColumnRef`/`FuncCall` hold strings; no `varno`/`funcid`/`consttype`),
the analyzer only type-checks (never rebuilds a resolved tree), and the runtime
resolves functions/operators by name at eval time. PG's `adbin`/`ev_action` are
*post-analysis, post-rewrite* S-expressions (`{CONST :consttype 23 … :constvalue 4
[ 42 0 0 0 ]}`, `{QUERY … {VAR :varno 1 :varattno 1 :vartype 23 …}}`). So the work
is **four net-new pieces**: a resolver (goopg AST + catalog → OID-resolved IR),
an `outfuncs` printer (IR → PG S-expression), a `readfuncs` reader (text → IR, for
goopg's own reload round-trip), and exact **binary datum encoding** for `Const`.

New leaf package **`internal/pgnodes`** (depends only on `internal/catalog` +
`internal/parser`; `executor`/`initdb` call into it):

| File | Responsibility |
|---|---|
| `ir.go` | Resolved-IR structs (PG primnodes/parsenodes subset). |
| `datum.go` | `Const` value ↔ raw PG in-memory datum bytes, per type. |
| `outfuncs.go` | IR → S-expression; field order mirrors `outfuncs.funcs.c` per tag. |
| `readfuncs.go` | S-expression → IR (`pg_strtok`/`nodeRead` mirror). |
| `resolver_expr.go` / `resolver_query.go` | goopg AST + catalog → IR. |
| `rebuild.go` | IR → goopg parser AST (for the reload path). |
| `unsupported.go` | Shape-detection driving graceful degradation. |

## Slices

Each slice is one gated increment: build/vet + touched-package unit suites +
testport + `TestE2E_FailoverGoopgToPG`, plus the slice's standby assertion.

| Slice | Content | Gate | Status |
|---|---|---|---|
| **S0** | Forward operator/proc OID indexes from the existing seed (`catalog.LookupOperatorForNode` / `LookupProcForNode`) + `BinaryOp.Op`→spelling. | Pinning test (15 operators, 6 procs, negatives), deterministic. | **done** (`10d26374`) |
| **S1** | `internal/pgnodes`: `ir.go` + `datum.go` + `outfuncs.go` + `readfuncs.go` for the scalar subset. | Golden round-trip vs real-PG `nodeToString` (`scripts/pg-oracle-diff.sh`, `:location`→-1) + `Read` deep-equal. No writer wired → no e2e. | planned |
| **S2** | `resolver_expr.go` + scalar `rebuild.go`; wire `adbin` + `stxexprs` (guarded by `unsupported.go`); swap the two scalar reload passes. | Adversarial standby-eval E2E: standby `INSERT … DEFAULT VALUES`, row `==` goopg's (covers sign-extension, text header/collation). | planned |
| **S3** | `resolver_query.go` + Query/RTE/`TargetEntry`/`Var` tags; wire `ev_action` + the per-table `relhasrules=true` flip; swap `loadViewsFromHeap`. | Standby-query E2E: standby `SELECT * FROM v` `==` goopg's; incl. a view over a user-defined function (≥16384 OID path). | planned |
| **S4** | Coverage + hardening: more datum types, `CASE`/`BoolExpr`/`NullTest`, and the byte-diff oracle gate (emitted `ev_action`/`adbin` `==` real-PG18's). | Incremental sub-slices. | planned |

## Invariants (from 02e §3)

- **Graceful degradation is mandatory.** `unsupported.go` runs an all-or-nothing
  subset check before serializing; on reject, defaults/stats fall back to SQL text
  and views additionally keep `relhasrules=false` for that one object. Never
  FATAL, never partial-emit.
- **`relhasrules=true` is per-table and hard-coupled** to a canonical `ev_action`
  (via a new `catalog.Table.RuleIsCanonical` flag set only when serialization
  succeeded) — a non-parseable `ev_action` with `relhasrules=true` FATALs PG's
  relcache `RelationBuildRuleLock → stringToNode`.
- **Verification is adversarial** — the standby *computes* and the result is
  asserted equal to goopg's own, not merely "replays without FATAL." A wrong OID
  or datum byte silently mis-evaluates.
- **Datum traps:** by-value sign-extension (negative int4 → all-`0xFF` high bytes;
  oid zero-extends), signed-char decimal wire form, text 4-byte varlena header,
  numeric reuses goopg's existing encoder, `constcollid=100` /
  `consttypmod=n+4` for collatable/typmod'd types.

## Out of scope (detected → degraded)

Multi-table/join views, subqueries, aggregates/GROUP BY/window, set ops, LATERAL,
exotic/composite types not in the datum table. These fall back to SQL text +
`relhasrules=false` and are not FATAL.
