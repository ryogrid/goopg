# 0103-0015: Publication Table Canonicalization (M0103-0008 rung 10)

Status: accepted
Owner: rung-10 of M0103-0008
Date: 2026-05-14

## Diagnosis

With rungs 1–9 of M0103-0008 closed (see 0103-0006…0103-0014), dropping
`t.Skip` on `TestPort_PgoutputInteropGoopgToPG` exposed a fresh,
quieter failure mode: the libpqwalreceiver connection stays alive past
the 60 s `wal_receiver_timeout` window, the goopg `SlotDecoder` runs
without errors, but the PG subscriber sees zero rows. No `'w'` CopyData
frame carrying pgoutput `B/R/I/U/D` reaches the wire even though the
publisher just executed four DML statements.

Adding diagnostic logging upstream of `walsenderPgoutputAdapter.Write`
confirmed the apparent miss: the WAL records exist, the
`SlotDecoder → Decoder → pgoutput.Change` chain fires, and
`PgOutput.Change` reaches the filter check — which rejects every
change. The publication-filter `byTable` map is keyed by the strings
goopg's executor stored at `CREATE PUBLICATION` time. The harness runs

```
CREATE TABLE public.t (id int PRIMARY KEY, v text);
CREATE PUBLICATION p FOR TABLE t;
```

The first statement stores `t` in the catalog with
`Schema="public"`. The second statement parses to
`CreatePublicationStmt.Tables = [ObjectName{Schema:"", Name:"t"}]`,
which `execCreatePublication` (executor/operators_ddl.go) renders via
`qualifiedTableName(t)` → the literal string `"t"`. The publication is
therefore stored with `Tables = ["t"]`.

At `runLogicalWalsender` time, the catalog snapshot's relation entry
sets `rel.Schema = "public"` and `rel.Name = "t"`, so
`relQualifiedName(rel)` (`internal/server/logicalwalsender.go`)
returns `"public.t"`. The filter does
`byTable["public.t"]` → not present → `Allows(...)` returns false →
every change is silently dropped. `'R'` is never emitted either (the
filter gate sits ahead of the relation-emission cache), and no `'B'`
either (Begin is emitted only when the transaction has at least one
in-publication change ready to ship — actually: `Begin` is emitted
unconditionally by the decoder per-xact, but with zero `'R'`+`'I'`
emissions the apply worker has nothing to write to the heap, and
existing rungs already keep the connection alive on the no-change
path).

Upstream PostgreSQL avoids this by resolving the table reference at
DDL time and storing the OID in `pg_publication_rel`. goopg's `PubSub`
registry keys by qualified-name string instead — equivalent only if
the stored string matches the canonical schema-qualified form the
WAL relation will carry at decode time. The fix is to canonicalise at
DDL time so the two ends of the comparison always agree.

## Decision

`execCreatePublication` resolves each `parser.ObjectName` in
`s.Tables` through `o.ctx.Catalog.LookupTable` and stores the
canonical qualified name (`*catalog.Table.QualifiedName()`) on the
new publication. Unqualified names fall back to the `public` schema
before reporting `42P01: relation … does not exist`, matching PG's
default search-path behaviour for `CREATE PUBLICATION FOR TABLE`.

This:

* Pairs every `byTable` key with the exact string `relQualifiedName`
  produces at filter time, regardless of whether the user wrote
  `public.t`, `t`, or `Public.T` at DDL time. The publication-side
  canonicalisation is the load-bearing piece — the filter loop on
  the walsender side needs no change.
* Surfaces non-existent tables loudly at `CREATE PUBLICATION` time
  rather than silently storing dead names whose changes will never
  match anything. Mirrors upstream PG: `CREATE PUBLICATION p FOR
  TABLE nosuchtable` raises a `relation "nosuchtable" does not
  exist` error there too.

Out of scope: schema-qualified resolution against a configurable
`search_path` (goopg has no GUC for `search_path` yet — every
non-test caller writes either bare names or `public.`-qualified ones,
and PG's effective fallback chain ends at `public`). The two-element
fallback (`as-given`, then `public.<name>`) covers every shape the
M0103 harness and the existing executor regression tests exercise; a
future loop can swap the fallback for a real search-path walk when
GUC support lands without touching the publication-registry surface.

## Implementation

* `internal/executor/operators_ddl.go::execCreatePublication`
  - Replace the `qualifiedTableName(t)` append with a catalog-driven
    resolve loop.
  - For each `t` in `s.Tables`: try `o.ctx.Catalog.LookupTable(t)`
    first; if absent and `t.Schema == ""`, retry with
    `{Schema:"public", Name:t.Name}`. On success append
    `tbl.QualifiedName()`; on failure return
    `&ExecError{Code:"42P01", Pos:s.Pos(), Message:"relation \"…\"
    does not exist"}`.

* `internal/executor/operators_ddl_pubsub_test.go` (new file —
  separate from the existing `operators_pg_get_publication_tables_test.go`
  so the canonicalisation pin lives next to its own focused test
  surface):
  - `TestCreatePublicationStoresCanonicalQualifiedName` — creates
    `public.t`, runs `CREATE PUBLICATION p FOR TABLE t`, asserts
    `PubSub.LookupPublication("p").Tables == ["public.t"]`.
  - `TestCreatePublicationExplicitSchemaName` — creates a table with
    explicit `public.items`, runs `CREATE PUBLICATION p FOR TABLE
    public.items`, asserts the stored name stays `public.items`.
  - `TestCreatePublicationUnknownTableErrors` — runs `CREATE
    PUBLICATION p FOR TABLE nosuchtable`, asserts the
    `*ExecError{Code:"42P01"}` shape.

* `internal/server/logicalwalsender_test.go` already has direct-Go
  PubSub fixtures that pin filter behaviour against pre-canonicalised
  `"public.items"` strings; no change needed there. The new
  executor-side test is the integration pin for the SQL path.

## Verification

`go test -race -count=1 -timeout 300s ./internal/parser/
./internal/planner/ ./internal/analyzer/ ./internal/executor/
./internal/server/ ./internal/wal/ ./internal/catalog/`

Plus a manual rerun of `TestPort_PgoutputInteropGoopgToPG` with the
`t.Skip` lifted to confirm the failure mode advances past rung 10.
The interop test stays `t.Skip`'d in tree so each rung lands with its
own design doc + targeted unit pin.

## Follow-up

Once the live interop test surfaces the next quiet failure mode
(candidates: pgoutput Begin/Commit emission for xacts with zero
in-publication changes, catalog-snapshot timing for relations
created after slot creation), file rung 11 with its own design doc.
