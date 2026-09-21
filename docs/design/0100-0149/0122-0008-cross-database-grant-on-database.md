# M0122-0008: `GRANT … ON DATABASE` reaches databases other than the connected one

Status: LANDED 2026-09-22. Closes `unimplemented_feat.json` entry
"Implement multi-database support …" (marked `resolved`); two residuals
ledgered.
Kind: impl
Parent: M0122-0008
Movement: none — TPC-DS SF0.25 `PLAN-SHAPE same=99 changed=0`; a DDL/catalog
conformance fix, which none of S3's three instruments measures.

## The defect

`GRANT CONNECT ON DATABASE otherdb TO r1`, issued from a session connected to
`postgres`, reported `GRANT` and changed nothing. Measured on a fresh goopg
cluster against a PG 18.3 oracle running the identical script:

| statement | goopg (before) | PG 18.3 |
|---|---|---|
| `GRANT CONNECT ON DATABASE otherdb TO r1` | `otherdb.datacl` stays NULL | `{=Tc/postgres,postgres=CTc/postgres,r1=c/postgres}` |
| `GRANT CONNECT ON DATABASE nosuchdb TO r1` | `GRANT` (silent success) | `ERROR: database "nosuchdb" does not exist` |
| `GRANT … ON DATABASE postgres …` | works | works |

The silent-success arm is the worse half: a typo in a database name granted
nothing and said so in no way a client could detect.

## Root cause — a v0 assumption that outlived its premise

`execDatabaseACLChange` compared every named database against
`ctx.CurrentDatabase` and returned `nil` when none matched. Its own comment
stated the reasoning: *"goopg v0 has a single logical connected database …
GRANT ON DATABASE naming any OTHER database name is a silent no-op"*.

That premise is no longer true — goopg creates, persists and routes real
per-database catalogs — but the restriction it justified stayed. Upstream's
`ExecGrant_Database` (`postgres/src/backend/catalog/aclchk.c`) resolves each
name against the SHARED `pg_database` catalog and never consults
`MyDatabaseId`; `pg_database` is cluster-wide precisely so that a session can
administer a database it is not connected to.

## The fix is a sibling pair (Hard-won Rule #2)

Fixing only the writer would have stored an ACL that no `SELECT` could show.

- **Writer** — `execDatabaseACLChange` resolves every named database through
  `catalog.ResolveDatabaseOid`. Names are resolved for the WHOLE list before
  any is applied, so a statement naming one good and one bad database changes
  nothing before it errors, matching upstream's single-transaction objects
  loop. An unresolvable name raises `3D000` with upstream's message.
  The per-database body moved into `applyDatabaseACLChange`, so "only the live
  database" can no longer be a property of the statement rather than of the
  loop driving it.
- **Reader** — the `pg_database` virtual row builder rendered `datacl` only
  for the `postgres` row, keyed by `c.DBOID()`. It now looks every row up.

`ResolveDatabaseOid` is the right key for both halves for a specific reason:
it returns `DBOID()` for `"postgres"`, which is the key the single-database
code already used. The connected database's behaviour is therefore unchanged
**by construction**, not by coincidence — which is what makes it safe to touch
the row that every connection reads.

## Verification

Live, end to end, against a PG 18.3 oracle cluster built for the comparison
(not a reference cluster): cross-database GRANT, cross-database REVOKE, the
multi-name list form, the `3D000` refusal, and the connected-database path all
match. The on-disk half was confirmed separately — `global/1262` carries a new
row version per resync for the non-connected databases, so an attached PG
standby sees the ACL too.

Both halves are pinned by tests with non-vacuity checks: neutralising the
writer fails exactly the three cross-database arms, and neutralising the reader
fails the render test while leaving the writer's tests green. The tests'
`postgres`-row and untouched-live-database assertions are paired controls, not
duplication: an implementation that applied the grant to whatever database it
had in hand would satisfy every otherdb assertion while corrupting the
connected one.

## Two residuals, both ledgered

1. **`pg_database.datacl` does not survive a restart** — and this is
   pre-existing, not introduced here: it affects the `postgres` row exactly as
   much as the new cross-database ones. The heap resync writes the aclitem
   array to `global/1262` correctly, but nothing repopulates the in-memory ACL
   store from it at startup, and the renderer reads the store. Same shape as
   the per-database reload gaps closed under M0119-0006 (bs/bw slices).
2. **aclitem array ORDER differs from PG.** goopg renders
   `{postgres=CTc/postgres,=Tc/postgres,r1=c/postgres}`; PG renders
   `{=Tc/postgres,postgres=CTc/postgres,r1=c/postgres}` — world entry first.
   Pre-existing in the shared ACL renderer, so it affects `relacl`/`typacl`
   identically and should be fixed in one place rather than per catalog.

Neither was folded in here. (1) is a startup-reload change in a different
subsystem, and (2) touches every ACL-bearing catalog at once; doing either
inside this change would have made a bounded conformance fix unreviewable.
