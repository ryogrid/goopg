# 0118-0092 — Temp-type dependency cascade on DISCARD TEMP (M0118-0009: temp-schema-cleanup perm-2 enabler)

Status: accepted
Milestone: M0118-0009 (upstream isolation spec suite pass-through — misc/system-level)
Spec: `postgres/src/test/isolation/specs/temp-schema-cleanup.spec`
Predecessor: [0118-0091](0118-0091-temp-schema-cleanup.md) (per-session temp namespace + `pg_my_temp_schema()` + `DISCARD TEMP` cleanup)

## Summary

This is an **enabler, not a promotion**. It clears the first divergence of
`temp-schema-cleanup.spec` **permutation 2** (process-exit cleanup), advancing
the spec's first mismatch from line 80 → line 88.

Permutation 1 (DISCARD-TEMP cleanup) already passes byte-for-byte (0118-0091).
Permutation 2 re-runs the same session-setup step `s1_create_temp_objects`,
which includes:

```sql
CREATE TEMPORARY TABLE just_give_me_a_type(id serial primary key);
CREATE FUNCTION uses_a_temp_type(just_give_me_a_type) RETURNS int LANGUAGE sql AS $$SELECT 1;$$;
```

`uses_a_temp_type` is an ordinary (non-temp, `public`) function whose argument
type is the *implicit composite rowtype* of the temporary table
`just_give_me_a_type`. In PostgreSQL, `DISCARD TEMP` (run at the end of
permutation 1) drops every temp relation; dropping the table also drops its
rowtype, and `pg_depend` cascades that to `uses_a_temp_type`. So when
permutation 2 re-creates the function it succeeds.

goopg's `DISCARD TEMP` dropped the temp **relations** (0118-0091's
`DropSessionTempObjects`) but left the dependent routine in the registry, so
permutation 2 failed at line 80 with:

```
ERROR:  function already exists with the same argument types: public.uses_a_temp_type(just_give_me_a_type)
```

## Change

goopg has no OID-level type-dependency graph (`pg_depend`). A temporary table's
composite rowtype shares the table's (session-unique) name, so the **table name
is the dependency signal**: dropping temp tables `{T1, T2, …}` cascades to any
routine whose argument or return type name is one of `{T1, T2, …}`.

Three pieces, scoped to the temp-cleanup path so blast radius is bounded to
"a routine whose arg/return type name equals a temp table being dropped":

1. **`catalog.(*InMemory).SessionTempTableNames(owner) []string`** — read-only
   enumeration of the session's temp relation names. Called *before*
   `DropSessionTempObjects` (which removes the tables).

2. **`catalog.(*Routines).DropRoutinesReferencingTypes(typeNames) []*Routine`** —
   drops every registered routine whose `ReturnType.Name` or any `ArgTypes[i].Name`
   matches one of `typeNames` (case-insensitive), maintaining both the `byKey`
   and `byName` indices. Returns the dropped routines.

3. **`DISCARD TEMP` executor wiring** (`operators_utility_settings.go`) — captures
   `SessionTempTableNames(owner)`, runs `DropSessionTempObjects(owner)`, then
   `Routines().DropRoutinesReferencingTypes(names)`.

The helper pair is deliberately reusable by the future **backend-exit** cleanup
path (the rest of permutation 2): exit cleanup will perform the same
`SessionTempTableNames → DropSessionTempObjects → DropRoutinesReferencingTypes`
sequence before releasing session-level advisory locks.

## Why name-matching is acceptable here

`DropRoutinesReferencingTypes` is only ever fed the names of temp tables that
were *just dropped in the same operation*. A temp table's name in this spec is
generated/session-scoped (`just_give_me_a_type`, `invalidate_catalog_cache`),
and a permanent function taking a permanent type that merely shares a name with
a session's temp table is not a realistic collision. The faithful OID-based
`pg_depend` cascade is a separate, larger subsystem; this signal is the correct
behaviour for every `port` spec that exercises temp-type dependencies.

## Verification

- `internal/catalog`: new `TestSessionTempTableNamesAndTypeCascade` (lists temp
  names per owner, drops arg- and return-type dependents, leaves an unrelated
  routine + another session's temp table intact); `TestDropSessionTempObjects`
  / `TestTempNamespaceLifecycle` unchanged-PASS.
- `internal/executor`: DISCARD / temp-owner / routine targeted tests PASS.
- `TestSyntax_TempSchema_MyTempSchemaAndDiscard` (cluster, perm-1 DISCARD guard) PASS.
- `TestPort_IsolationInheritTemp` (cross-session temp regression guard) PASS.
- `TestPort_IsolationTempSchemaCleanup`: first divergence advanced L80 → L88
  (everything through L87 now byte-matches PG 18.3); still `defer`/skip until the
  process-exit slice lands.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Deferred (remaining for full `temp-schema-cleanup.spec` promotion)

Permutation 2's process-exit path (ledger 2026-06-25, carried):

1. `pg_terminate_backend(pg_backend_pid())` evaluator (self-termination).
2. isolationtester connection-death rendering — the self-terminating step emits
   `FATAL:  terminating connection due to administrator command` +
   `server closed the connection unexpectedly\n\tThis probably means…`; the
   blocked peer step (`s2_advisory`) then renders `<... completed>`.
3. On-disconnect cleanup ordering: `SessionTempTableNames` →
   `DropSessionTempObjects` + `DropRoutinesReferencingTypes` → `DropTempNamespace`
   → release session-level advisory locks (so `s2_advisory` observes a clean
   catalog only after temp cleanup — the spec relies on advisory locks being
   released *after* temp-table cleanup).

This enabler supplies piece (3)'s catalog half (the cascade helpers).
