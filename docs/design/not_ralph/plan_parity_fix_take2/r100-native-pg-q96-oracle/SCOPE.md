# R100 SCOPE — isolated native-PG18.3 Q96 oracle from the reference cluster

R100 follows R99 (`abc2e88eb`), which proved that a Goopg-created
data/catalog directory is not executable by native PostgreSQL. A distinct,
promising source now exists: `bench/tpcds/runtime/pgdata` reports PG version
18, system identifier `7666072657399316226`, and `shut down` from
`pg_controldata`, with no `postmaster.pid` or native-PG process referencing
the directory. This is the designated native TPC-DS reference cluster in
`bench/tpcds/env_tpcds.sh`; it must remain untouched.

## Authorized work

Create one new `/tmp` destination by filesystem-copying that **verified
stopped** source exactly once. Record source/destination absolute paths,
`pg_control` checksum/system identifier/cluster state, and absence of native
postmasters before the copy and before destination start. Never copy it live,
start it in place, alter it, or share its destination/port with another
process.

Start only the destination with PG18.3 on a private port. Before `ANALYZE` or
any other mutation, establish it as an execution oracle:

1. record binary version, database/schema, planning GUCs, and data identity;
2. pin `tpcds025`/`public`, the exact Q96 query-file SHA-256, the complete
   planning-GUC command, and the SF0.25 identity through relation cardinality
   witnesses. Run Q96 with those settings twice for byte-identical JSON/text
   EXPLAIN and once for values; require a successful value result with its
   digest/direct cardinality witnesses equal to the named historical trusted
   SF0.25 baseline, and no postmaster/backend assertion/recovery; and
3. snapshot Q96 relation/index tuple/page/size metadata plus every referenced
   join/predicate column's `pg_stats`/`pg_statistic` inputs.

If all three pass, record whether existing stats reproduce the historical
reference. Only then, if stats are absent/stale and values remain safe, run
and record `ANALYZE` on the private destination and repeat all oracle checks.
If any data-identity, value-baseline, native execution, or catalog check fails,
stop immediately, preserve the source, report ORACLE INVALID, and do not run
ANALYZE or use that destination's plan or stats for Goopg work.

## Boundaries and gates

R100 changes no Goopg source, planner/cost/executor behavior, defaults,
queries, PostgreSQL source, historical capture, or reference cluster. It does
not authorize a Goopg fix or a PG GUC chosen to resemble Goopg. All mutations
are restricted to the disposable destination after it first passes a native
execution proof.

Before copy: agent review, corrections, `git commit -n`, and push. Afterward:
stop the destination, verify `git diff --check`, and commit/push an English
report with all provenance, commands, hashes, value/plan results, and oracle
verdict. A later Goopg change requires a separate reviewed scope and full
parity gates.
