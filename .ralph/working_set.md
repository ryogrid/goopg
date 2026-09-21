(idle — nothing in flight)

# Loop #70 result — M0119-0006 (bu): datallowconn enforced at connect time

Banner: nightly run id UNCHANGED (20260922-004850), so PgoutputInterop stays
non-selectable. M0119 comes first among the pre-existing milestones, and
M0119-0006's bs slice named three recorded-not-fixed items; this closes the
first (template0 connectable).

## The catalog already told the truth; only the gate was missing
goopg rendered `datallowconn = false` for template0 — which is why
pg_amcheck's `--all` filter already skipped it — but nothing enforced it on
connect, so `psql -d template0` succeeded. That is the worst arrangement of
the two: every tool that politely asks the catalog is told the truth, and
every tool that simply connects is not. It is what let a CREATE EXTENSION
land in template0, from where it is copied into every future CREATE DATABASE
using it as a template.

goopg now raises upstream's FATAL 55000 `database "%s" is not currently
accepting connections` (postinit.c:361-365), verified byte-for-byte live.

## One rule, one place
New `catalog.DatabaseAllowsConnections` is the single source of truth; the
`pg_database` row builder AND the postmaster gate both read it. Hardcoding
the name twice is exactly how a catalog saying "closed" ends up beside a
server saying "come in".
`TestDatabaseAllowsConnectionsMatchesPGDatabaseRow` pins the agreement by
reading the RENDERED rows rather than restating the rule.

## Paired control (not optional)
`TestConnectTemplate1Accepted` — the cheap way to pass the rejection test is
to refuse every template database, and PG ALLOWS template1. Non-vacuity
verified: disabling the gate fails exactly the rejection test and leaves the
control green.

## Second divergence FOUND WHILE MEASURING, filed not fixed
`select current_database()` connected to **template1** returns `postgres`
on goopg; PG returns `template1`. template1 is legitimately connectable on
both, so this is NOT the datallowconn issue. Filed with the decisive first
question: is the session genuinely ROUTED to the postgres namespace (DDL
against template1 would land in the wrong database — far more serious) or is
only the reporting function wrong? `catalog.NamespaceDBOid` aliasing onto
`DefaultDBOid` (bq slice) is where to look. Left unfixed deliberately — the
right fix depends on which of those two it is.

## Gates (all green)
units; whole `TestPort_PgAmcheck*` family (the suite that filters on
datallowconn); tpch-spotcheck Q12=2/Q13=33; tpcds-sf025 `PLAN-SHAPE same=99
changed=0`; acceptance arm 24/24; pgbench smoke (the connection path is
exactly what it exercises).

## Next loop
Check the nightly run id FIRST — a new one unblocks PgoutputInterop, whose
inlined cluster.log tail is now the evidence to read. Otherwise M0119-0006's
remaining two bs items (template1 extension re-attribution on restart;
user-db-local `extnamespace` → "public" at reload), or the new
current_database() task, then M0122 → M0131 → M0134 → M0135/M0136 →
M0095/M0110.

## Owner escalations OPEN — three
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted. 3.
partition_aggregate's inventory row marks a never-passing case must-pass.
