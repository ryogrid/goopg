(idle — nothing in flight)

# Loop #72 result — template1 namespace bug ROOT-CAUSED (not yet fixed)

Banner: nightly run id UNCHANGED (20260922-004850) → PgoutputInterop stays
non-selectable. M0119 first; took the template1-namespace task filed last
loop.

## ROOT CAUSE — an OID collision by construction
`const DefaultDBOid uint32 = 1` (`internal/catalog/catalog.go:4018`) is
goopg's internal "default namespace" key, and **1 is also template1's real
PostgreSQL bootstrap OID** — the constant's own neighbouring comment cites
`Template1ObjectId=1, PostgresObjectId=5`.
`ResolveDatabaseOid("template1")` returns 1 EXPLICITLY, while
`NamespaceDBOid` maps postgres (`PostgresDBOid = 5`) onto `DefaultDBOid = 1`.
Two databases therefore key the same `tableNamespace`.

**Last loop's suspicion was WRONG and is recorded as such**: template1 does
NOT resolve to 0 and fall through `NamespaceDBOid`'s zero case. Do not
re-investigate that.

## The dangerous dependency was MEASURED and does not exist
`CREATE DATABASE ... TEMPLATE` physically copies the template's relations
from `base/<dbOid>`, so the worry was that a database created from template1
would inherit postgres' user tables. With a `secret` table in postgres, BOTH
plain `CREATE DATABASE fresh` and explicit
`CREATE DATABASE fromt1 TEMPLATE template1` produce a database where
`secret` does not exist. The CREATE DATABASE path is unaffected; a fix need
not preserve any copying that depends on the collision.

## Landed this loop
Only a regression repair I caused in loop #70: the
`DatabaseAllowsConnections` doc comment had been inserted BETWEEN
`DatabaseConnLimit`'s doc comment and its declaration, orphaning it. The
function is moved below so each doc is adjacent to its own declaration.

## Next step (bounded now, one question left)
Give template1 a distinct INTERNAL oid while `databaseDisplayOID` keeps
showing 1 — goopg ALREADY separates displayed from real oids, so this fits
the design. That leaves template1 an empty namespace, which is what PG has.
**Check first**: what keys off `base/1` on disk, and what
`internal/initdb/catalog_heap_reload.go` does when it maps a connection
(PostgresDBOid=5) back onto DefaultDBOid=1 at startup — a new internal oid
must not make the reload attribute postgres' heap rows to template1. That
coupling is why this was not fixed in the same loop: the collision is baked
into a constant keying physical `base/<oid>` directories.

Also verify whether this is the SAME root cause as M0119-0006's remaining bs
item ("template1 install re-attributes to postgres on restart") — if the
install genuinely executes in postgres' namespace, the restart reports where
the row always was, and the two are ONE piece of work.

## Gates (all green)
units; tpch-spotcheck Q12=2/Q13=33; tpcds-sf025 `PLAN-SHAPE same=99
changed=0`; acceptance arm 24/24; pgbench smoke. (Diff is comment-only, but
`internal/catalog` is non-test code so the stamps are required.)

## Owner escalations OPEN — three
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted. 3.
partition_aggregate's inventory row marks a never-passing case must-pass.
