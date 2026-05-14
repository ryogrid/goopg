# Design 0103-0019 — LATERAL `pg_catalog`-qualified SRF parser dispatch

**Status:** accepted (2026-05-14)

**Milestone:** M0103-0008 rung 13 — live-probe ladder against
`TestPort_PgoutputInteropGoopgToPG`.

**Closes:** `fetch_table_list_from_publisher` parser-side failure that
caused `CREATE SUBSCRIPTION` on a PG subscriber to register zero
tables in `pg_subscription_rel`, which in turn caused the apply
worker to silently skip every Insert/Update/Delete from a goopg
publisher.

## Problem

With M0103-0008 rungs 1–12 closed, the live interop test still
failed with the same observable mode: 'w' frames carrying
Begin/Relation/Insert/Commit reached the PG subscriber (received_lsn
advanced, the apply worker's debug log emitted CONTEXT lines naming
each message kind), but the replication origin's `remote_lsn` stayed
at 0/0 and `SELECT count(*) FROM public.t` on the subscriber stayed
at 0. No error message ever surfaced because the apply worker's
filter path is silent.

Adding state-introspection diagnostics revealed
`pg_subscription_rel = EMPTY` on the subscriber, which is what
`should_apply_changes_for_rel` consults to decide whether to apply
any change for a given relation. When the table list is empty, every
change for every relation is skipped silently.

PG's `CreateSubscription` populates `pg_subscription_rel` from the
result of `fetch_table_list_from_publisher`. The exact query
upstream issues is (postgres/src/backend/commands/subscriptioncmds.c):

```sql
SELECT DISTINCT t.schemaname, t.tablename
  , (CASE WHEN (array_length(gpt.attrs, 1) = c.relnatts)
         THEN NULL ELSE gpt.attrs END)
  , gpt.qual
FROM pg_catalog.pg_publication_tables t
     JOIN pg_catalog.pg_class c
       ON (c.oid = (quote_ident(t.schemaname) || '.'
                    || quote_ident(t.tablename))::regclass)
     ,
     LATERAL pg_catalog.pg_get_publication_tables(t.pubname) AS gpt
WHERE ...
```

Running this query verbatim against the goopg publisher raised
`syntax error at or near "expected ')' after subquery in FROM (got ()"`
at the LATERAL function's opening paren — column 357. The error
caused `CreateSubscription` to register zero tables and proceed
silently, which set up the downstream apply-side skip behaviour.

## Root cause

`internal/parser/select.go::parseRangeVar` recognised
table-valued-function FROM items (M0096-0006 generate_series,
M0103-0008 earlier rungs pg_get_publication_tables, etc.) only when
the function name was UNQUALIFIED:

```go
if p.cur().Kind == TokenSymbol && p.cur().Value == "(" && obj.Schema == "" {
    lower := strings.ToLower(obj.Name)
    switch lower {
    case "generate_series", "pg_input_error_info", "parse_ident",
        "pg_get_publication_tables":
        srfFuncName = lower
    }
}
```

When the input was `pg_catalog.pg_get_publication_tables(t.pubname)`,
`obj.Schema == "pg_catalog"` and the dispatch was skipped. The
parser then fell through to the derived-subquery branch (which
expects `(SELECT …)`), saw `(t.pubname)` instead of `(SELECT`, and
emitted the "expected ')' after subquery" message at the opening
paren.

Upstream PG resolves the function via the catalog search path:
`pg_catalog.pg_get_publication_tables` and unqualified
`pg_get_publication_tables` resolve to the same proc; libpqwalreceiver
emits the schema-qualified form to avoid search-path surprises on
the publisher side.

## Fix

Extend the SRF dispatch gate to accept both shapes:

```go
if p.cur().Kind == TokenSymbol && p.cur().Value == "(" &&
    (obj.Schema == "" || strings.EqualFold(obj.Schema, "pg_catalog")) {
    lower := strings.ToLower(obj.Name)
    switch lower {
    case "generate_series", "pg_input_error_info", "parse_ident",
        "pg_get_publication_tables":
        srfFuncName = lower
    }
}
```

`strings.EqualFold` matches PG's case-insensitive identifier
semantics for the unquoted `pg_catalog` prefix. The four SRF names
are kept as the canonical lowercase keys that downstream planner /
executor lookups use; the schema qualifier is discarded once
dispatch fires (these functions live conceptually in pg_catalog
regardless of how the call site spells them).

The change is local to `parseRangeVar`. Behaviour for unqualified
calls is byte-identical to the pre-fix path (the dispatch fires on
the same branch). Schema qualifiers OTHER than `pg_catalog` still
fall through to the derived-subquery branch and surface the same
error as before — goopg has no per-schema function namespacing
beyond `pg_catalog` in v0, so this is the correct trade-off.

## Verification

* `internal/parser/select_test.go::TestParseLateralPgCatalogQualifiedSRF`
  pins the rung 13 closure by parsing the canonical libpqwalreceiver
  shape and asserting the resulting AST has a `TableFuncRef` with
  `Name == "pg_get_publication_tables"`.
* `go test -race -count=1 -timeout 300s ./internal/parser/
  ./internal/planner/ ./internal/analyzer/ ./internal/executor/
  ./internal/server/ ./internal/wal/ ./internal/catalog/` — all
  green.
* Live diagnostic run with the `t.Skip` removed (rolled back before
  commit) confirmed the failure mode shifted observably: the
  `fetch_table_list` SQL now parses and reaches the executor, where
  it surfaces the rung-14 surface (`pg_class.relnatts` column
  missing — SQLSTATE 42703). The rung-14 diagnosis is recorded in
  the restored `t.Skip` message verbatim so the next loop can resume
  from the exact failing surface.

## Out of scope for this rung

* Adding `relnatts` to goopg's `pg_class` virtual view (rung 14).
* Supporting non-`pg_catalog` schema-qualified SRFs (no upstream
  probe needs them in v0).
* Folding the SRF whitelist into the planner so schema-qualified
  forms work in non-FROM positions (planner doesn't currently
  surface this requirement).
