# Concurrent sessions: a CTE name intermittently fails to resolve (found 2026-10-19)

Repro (any goopg cluster, current HEAD and e91816974 alike):

    printf 'with x as (select 1 as a) select a from x;\n' > mini.sql
    for i in $(seq 8); do pgbench -n -h 127.0.0.1 -p <port> -U postgres -c 1 -t 50 -f mini.sql postgres & done; wait

Some clients abort with `ERROR:  relation "x" does not exist` at `command 0 query 0`
(the first statement of the connection). TPC-DS query2.sql x4 concurrent `psql -f`:
0-6 errors per 20 runs; serial runs never fail.

Bisect over the 26 code commits e91816974..ee18b4649: every tested rev fails, incl.
e91816974 itself (1/20, 6/20) — pre-existing, not a regression of that range.
A pinned :65433 binary showed 0/8 aborts in one round (intermittent; inconclusive).
Related ledger class: 2026-07-29 M0125-0003 row (no lock around planning; package-level
planner state).
