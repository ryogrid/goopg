# 0118-0091 — Per-session temporary namespace + `pg_my_temp_schema()` + `DISCARD TEMP` cleanup (M0118-0009 slice: temp-schema-cleanup)

Status: accepted
Milestone: M0118-0009 (Misc / system-level isolation specs)
Spec: `postgres/src/test/isolation/specs/temp-schema-cleanup.spec`

## Problem

`temp-schema-cleanup.spec` verifies that a backend's temporary objects are
cleaned up (a) during `DISCARD TEMP` and (b) during backend exit. Its check
queries pivot on the session's temporary namespace OID:

```sql
INSERT INTO s1_temp_schema SELECT pg_my_temp_schema();   -- s1 setup
...
SELECT oid::regclass FROM pg_class WHERE relnamespace = (SELECT oid FROM s1_temp_schema);
SELECT oid::regproc FROM pg_proc  WHERE pronamespace = (SELECT oid FROM s1_temp_schema);
SELECT oid::regproc FROM pg_type  WHERE typnamespace = (SELECT oid FROM s1_temp_schema);
```

Against goopg the spec failed on the very first session-setup step:
`ERROR: function pg_my_temp_schema does not exist`. The function was seeded in
`pg_proc` (OID 2854) but had no evaluator, goopg modeled temporary relations
only via `Table.Temp`/`Table.TempOwner` with **no per-backend namespace OID**
(every temp relation rendered `relnamespace = public`), and `DISCARD TEMP` was a
silent no-op (it never dropped any relation).

## What landed this loop (permutation 1)

A faithful per-session temporary-namespace model in the shared in-memory
catalog, plus the `pg_my_temp_schema()` evaluator and real `DISCARD TEMP`
cleanup. This makes **permutation 1** (`s1_create_temp_objects` →
`s1_discard_temp` → `s2_check_schema`) pass byte-for-byte.

### Catalog (`internal/catalog/catalog.go`)

- New `InMemory.tempNamespaces map[string]uint32`: temp-owner token (`s<id>`,
  mirroring `executor.sessionTempOwner`) → namespace OID. Established lazily on
  the first `CREATE TEMPORARY` object and persists for the life of the session
  (PostgreSQL reuses `pg_temp_N` even after every temp object is dropped).
- `EnsureTempNamespace(owner)` (lazy alloc via `nextOID`, idempotent),
  `TempNamespaceOID(owner)` (lookup, 0 if none), `DropTempNamespace(owner)`
  (session-exit removal), and the lock-free `tempNamespaceOIDLocked` for callers
  already holding `c.mu`. Blank owner (session-less / unit contexts) → 0, so the
  legacy single-session behaviour is unchanged.
- `allSchemasLocked` now also emits each live temp namespace as
  `pg_temp_<id>` so a cross-session `pg_namespace` join and the
  `oid → nspname` cast resolve to a real catalog row (PostgreSQL's
  `pg_namespace` is a shared catalog).
- The `pg_class` `VirtualRows` builder renders a temp relation
  (`t.Temp && t.TempOwner != ""`) in its owner's `pg_temp_<id>` namespace
  instead of `public`, so `WHERE relnamespace = pg_my_temp_schema()` matches it
  (and finds nothing once it is cleaned up). Falls back to the schema OID for
  legacy session-less temp relations.
- `DropSessionTempObjects(owner)` removes every relation owned by the session
  (with its indexes + ACLs) and returns the count; it deliberately keeps the
  namespace registration.

### Executor

- `expr.go`: `pg_my_temp_schema()` evaluator → `TempNamespaceOID(sessionTempOwner(ctx))`
  (0/InvalidOid when the session has no temp namespace).
- `operators_ddl.go`: both `CREATE TABLE` temp paths (plain + partition leaf)
  call `EnsureTempNamespace` after stamping `TempOwner`.
- `operators_utility_settings.go`: `DISCARD TEMP` / `DISCARD ALL` now calls
  `DropSessionTempObjects(sessionTempOwner(ctx))`.

### Planner

- `exprType` maps `pg_my_temp_schema` → `oid` so the wire column type is correct.

## Gates

- `TestSyntax_TempSchema_MyTempSchemaAndDiscard` (testport, cluster-backed):
  hard-guards `pg_my_temp_schema()` lifecycle (0 → non-zero, persists across
  drop), `pg_class.relnamespace == pg_my_temp_schema()`, `pg_namespace`
  exposure, and `DISCARD TEMP` cleanup with namespace persistence.
- `TestTempNamespaceLifecycle` / `TestDropSessionTempObjects` (catalog unit):
  registry idempotence/distinctness, `pg_temp_<id>` naming, owner-scoped drop
  leaving other sessions + permanent tables intact, namespace persistence.
- `TestPort_IsolationTempSchemaCleanup` anchor (`runIsoSpec`, skips until the
  full spec matches): permutation 1 already matches; permutation 2 is deferred.
- `internal/catalog`, `internal/executor`, `internal/planner` suites PASS;
  `go build ./...` clean; pgbench smoke = pre-commit hook.

## Deferred (permutation 2 — ledger 2026-06-25)

Permutation 2 (`s1_advisory` `s2_advisory` `s1_create_temp_objects` `s1_exit`
`s2_check_schema`) needs a distinct cluster of capabilities, none of which the
permutation-1 model requires:

1. **`pg_terminate_backend(pid)`** evaluator (sibling of the existing
   `pg_cancel_backend`) that closes the target backend's connection.
2. **Isolationtester connection-death rendering** in `IsolationRunner`: a
   self-terminating step must emit `FATAL:  terminating connection due to
   administrator command` + the `server closed the connection unexpectedly` /
   `This probably means the server terminated abnormally …` block, and the
   blocked peer step (`s2_advisory`) must then report `<... completed>`.
3. **Session-exit temp cleanup ordering**: on disconnect, drop the session's
   temp objects (`DropSessionTempObjects`) **then** the namespace
   (`DropTempNamespace`) and only then release its session-level advisory locks,
   so the unblocked `s2_advisory` observes an already-clean catalog.
4. **Temp-type dependency cascade**: the non-temp function
   `uses_a_temp_type(just_give_me_a_type)` depends on a temp table's rowtype;
   PostgreSQL drops it when the temp type is dropped, so re-running
   `s1_create_temp_objects` in permutation 2 succeeds. goopg currently keeps it,
   so the second creation fails `function already exists`.

Resume point: implement (1)+(2) first (they unblock the bulk of the diff), then
(3) ordering, then (4) the dependency cascade.
