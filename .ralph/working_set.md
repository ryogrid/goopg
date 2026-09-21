(idle — nothing in flight)

# Loop #74 result — per-database pg_namespace reload FIXED (M0119-0006 bw)

Banner: nightly run id UNCHANGED (20260922-004850) → PgoutputInterop blocked;
template1 collision is `[!]`. Took the remaining bs item that is NOT subsumed
by that escalation.

## The recorded symptom understated the bug
Item read "user-db-local `extnamespace` falls back to public at reload".
Measuring showed the extension was the SYMPTOM: **the schema itself
vanished**. A `CREATE SCHEMA` inside a `CREATE DATABASE`d database did not
survive a restart at all, so the extension reload could not resolve its OID
and fell back to `public`.

Root cause: `reloadUserSchemasFromHeap` scanned only `cat.DBOID()`, so a user
database's rows sat unread in `base/<thatDbOid>/2615`. Tables in the same
database reloaded fine (own per-database pass) — which is exactly what
disguised this as extension-only.

## The first fix attempt was WRONG; the live check caught it
Copying the extension reload's per-database loop into
`reloadUserSchemasFromHeap` changed nothing: that pass runs at
`open.go:1397` while `reloadDatabasesFromHeap` is at **1546**, so
`ListDatabases()` is still EMPTY. The extension reload only works because it
runs at 2401, after the database list exists.

Fixed by SPLITTING, not moving: the early pass keeps reading the connecting
catalog's heap (ts_dict/ts_config reloads follow it and need the schema OID
map, and they run before 1546); a new `ReloadUserDatabaseSchemasFromHeap`
runs immediately after the pg_database reload. Moving the whole pass late
would have broken those dependents.

## Verification
Live end-to-end on a fresh cluster: schema `uext` AND `extnamespace` both
survive; both were wrong before. `TestPort_PerDatabaseSchemaSurvivesRestart`
asserts BOTH halves — the extension alone would pass if the reload merely
guessed the right name; the schema's existence proves the heap was read.
Non-vacuous: removing the new pass fails both assertions.

## Filed, not fixed: schemas are process-wide
Measured directly — `CREATE SCHEMA` in a user database is visible from
`postgres`. `SchemaNameForOID` scans one global map. This reload pass
deliberately restores the RUNTIME behaviour rather than inventing scoping:
a schema present at runtime but absent after restart is the data-loss-shaped
bug; over-visibility is not. Same M0122-0007 family as the template1
escalation — **read that escalation before fixing**, since the owner's
choice there governs how schema scoping should be keyed.

## Gates (all green)
units; whole `TestPort_PgAmcheck*` family; tpch-spotcheck Q12=2/Q13=33;
tpcds-sf025 `PLAN-SHAPE same=99 changed=0`; acceptance arm 24/24; pgbench
smoke.

## Next loop
Check the nightly run id FIRST. M0119-0006's last bs item (template1
extension re-attribution) is likely subsumed by the `[!]` template1
escalation — verify before treating it as separate. Then M0122 → M0131 →
M0134 → M0135/M0136 → M0095/M0110.

## Owner escalations OPEN — four
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted.
3. partition_aggregate's inventory row. 4. template1 namespace collision
(Option A vs B) — now with a second dependent, the schema-scoping task.
