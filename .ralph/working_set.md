(idle — nothing in flight)

# Loop #76 result — the template1 extension re-attribution IS the collision

Banner: nightly run id still 20260922-004850 → PgoutputInterop ×10 stay
blocked (wait for the inlined `cluster.log` tail; do NOT re-run locally).
Banner item 10 → M0119 before M0122, so took M0119-0006's bs residual — the
task loop #75 left with one open question. No production change.

## The open question is answered
`mirrorCatalogRelToPostgresDB` (`internal/executor/sys_catalog_postgres_db_mirror.go`)
copies `DefaultDBOid` → `PostgresDBOid` with the SOURCE HARD-WIRED, on every
extension write, so the reload's `cat.DBOID()` pass finds the row. Database-
agnostic, not a template1 special case. That removes the duplicate-row worry
#75 was blocked on — and refutes its conclusion.

## Loop #75's "not subsumed" is CORRECTED — one-line control
Same `CREATE EXTENSION` from a **postgres** connection, fresh cluster →
identical signature: `base/1/3079`=1, `base/5/3079`=1, `base/4`(template0
control)=0. So `base/1` is the SHARED `DefaultDBOid` heap, not template1's
own; #75's inference had no support. Paired control: an ordinary
`CREATE DATABASE`d db routes correctly to its own `base/<oid>/3079` — routing
WORKS; template1 fails only because its oid IS the sentinel.

## Why no reload-side fix exists (stronger than "deferred")
A `pg_extension` row's database scope lives ONLY in the in-memory registry; on
disk the sole scope carrier is WHICH `base/<dbOid>` heap holds it. template1's
row and a postgres row share one heap and are otherwise identical → no reader
can attribute it to template1. Scanning more directories cannot recover
information that was never written. Task marked `[!]` behind the collision.

## MEASUREMENT TRAP — cost two false defects
Heap pages are NOT flushed at `CREATE EXTENSION`. Reading `base/<db>/3079` on
a RUNNING server showed ZERO rows for writes that had succeeded; both
"new defects" dissolved after a clean shutdown. Stop the server before any
on-disk catalog probe.

## Gates
state guard; pgbench smoke via the hook. No value gates — ZERO production
diff (docs + .ralph only). Scratch clusters gt1x76/77/78 stopped and removed;
ports 5543-5545 confirmed free.

## Next loop
M0119-0006's bs residuals are now all closed or `[!]`. Move to the banner's
next milestone: **M0122** → M0131 → M0134 → M0135/M0136 → M0095/M0110.
Note M0122-0007 (per-database namespaces) is where Option B would land.

## Owner escalations OPEN — four (unchanged)
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted.
3. partition_aggregate's inventory row. 4. template1 namespace collision
(Option A vs B) — now with TWO dependent tasks: schema scoping and this one.
