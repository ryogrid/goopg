(idle — nothing in flight)

# Loop #73 result — template1 namespace collision fully MAPPED, escalated `[!]`

Banner: nightly run id UNCHANGED (20260922-004850) → PgoutputInterop stays
non-selectable. Continued the template1 task. No production change.

## What was missing, and why two loops guessed wrong
**goopg has THREE database-oid spaces that do not agree.** Nobody had written
this down:

| space | postgres | template1 | CREATE DATABASEd |
|---|---|---|---|
| displayed (`pg_database.oid`) | 16384 | 1 | own |
| catalog namespace | `DefaultDBOid=1` | **1** | own |
| storage (`base/<DBOid>`) | `PostgresDBOid=5` | **5** | own |

MEASURED: `base/` after initdb holds 1, 4, 5. A table created on the
**postgres** connection and one created on the **template1** connection BOTH
landed in `base/5`; a table in a `CREATE DATABASE`d `isolated` landed in a
fresh `base/16408`. So per-database isolation WORKS; template1 alone misses
it — at BOTH the namespace and storage layers, not just the catalog one I
reported last loop.

## Merge point
`ResolveDatabaseOid("template1")` returns 1 explicitly and
`DefaultDBOid = 1`, so template1's namespace IS the default one;
`NamespaceDBOid` folds postgres (5) onto it too; storage derives FROM the
namespace, which is why template1's files follow postgres' into `base/5`
rather than the `base/1` initdb already laid out. Not a bug in any single
function — goopg picked `1` as its internal sentinel and `1` is template1's
real PG bootstrap OID.

## Two candidate blockers MEASURED as absent
- `CREATE DATABASE ... TEMPLATE` does not leak postgres' tables (both the
  default and explicit `TEMPLATE template1` paths, with a `secret` table).
- The startup reload folds `NamespaceDBOid(cat.DBOID())` for the POSTGRES
  connection specifically, so moving template1 off 1 leaves it correct.

## Why `[!]` instead of a fix — the options are not interchangeable
- **A**: stop `ResolveDatabaseOid` returning `DefaultDBOid` for template1.
  One loop. Routes AROUND the sentinel → the next database colliding with a
  bootstrap oid has the same problem.
- **B**: give postgres namespace 5, one oid per database at every layer.
  Architecturally right, removes it by construction — and is substantially
  M0122-0007's epic (slices 4b-4e open).
Recommendation recorded (A now, B named as the real fix), but accepting a
narrow fix that knowingly leaves a structural hazard is an owner call about
debt, not something to settle by picking the smaller diff.

## Gates
`go build ./...` OK; state guard OK; pgbench smoke via hook. No value gates —
ZERO production diff (verified over `internal/ cmd/`).

## Next loop
Check the nightly run id FIRST. M0119-0006's remaining bs items (template1
extension re-attribution — likely the SAME root cause as above, so do not
treat as separate until checked; user-db-local `extnamespace` → "public" at
reload), then M0122 → M0131 → M0134 → M0135/M0136 → M0095/M0110.

## Owner escalations OPEN — FOUR
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted.
3. partition_aggregate's inventory row marks a never-passing case must-pass.
4. **NEW:** template1 namespace collision — Option A vs Option B.
