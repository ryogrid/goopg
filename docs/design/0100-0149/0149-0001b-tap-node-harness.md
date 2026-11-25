# M0149\-0001b — `tapnode`: one Node API over goopg and PostgreSQL 18\.3

Status: landed 2026\-11\-15 with the first port that uses it,
`TestPort_Recovery034CreateDatabase` \(M0149\-0001\).
Parent: M0149\-0001 \(port `recovery/t/034_create_database.pl`\).

## Why

M0149's contract C3 asks every ported TAP test to run its upstream topology
twice:

- all\-goopg;
- at least one mixed variant against real PostgreSQL 18\.3.

For single\-node tests the mixed variant is a cross\-check: a PG 18\.3 server
started on a copy of the goopg data directory must reach the same observable
state.

goopg already had two separate harnesses with different method sets:
`internal/testutil/cluster` \(goopg\) and `internal/testutil/pgcluster`
\(PG\). A port written against either one cannot run its assertions against
the other without duplicating them. Upstream writes each test once against
`PostgreSQL::Test::Cluster` \(`src/test/perl/PostgreSQL/Test/Cluster.pm`\),
so the Go side needs the same single surface.

## API \(`internal/testutil/tapnode`\)

| Cluster\.pm | tapnode | goopg | PG 18\.3 |
|---|---|---|---|
| `new` \+ `init` | `NewGoopg\(t, repo, name\)` | `cluster.New` \+ `Init` | — \(PG nodes come from `ColdStartPG` so far\) |
| `start` | `Start\(\)` | `cluster.Start` | `pgcluster.Start` \+ `WaitReady` |
| `stop\('fast'\)` | `Stop\(StopFast\)` | `goopg stop -mode fast` | SIGINT |
| `stop\('immediate'\)` | `Stop\(StopImmediate\)` | `goopg stop -mode immediate` | `pg_ctl -m immediate stop` |
| `safe_psql\(db, sql\)` | `SafePSQL\(db, sql\)` | PG 18\.3 `psql -XAtq -v ON_ERROR_STOP=1` | same |
| `psql\(db, sql\)` | `PSQL\(db, sql\)` \(returns the error\) | same | same |
| — \(C3\) | `ColdStartPG\(t, src, name\)` | copy the stopped data dir, drop the stale `postmaster.pid`, start PG 18\.3 on the copy | — |

- goopg nodes are durable: `SyncInit` and `SyncRuntime` are on. Ported
  recovery tests assert the durable path itself
  \(test\-gate\-speedups/02 §4\).
- Every node is stopped immediately at test cleanup if it is still running.
- Client tools are always the PG 18\.3 binaries from
  `postgres/local_install/bin`, as C3 requires.
- `ColdStartPG` copies rather than starts PG on the original directory, so
  the goopg node can still be restarted afterwards. Copying before goopg's
  own restart makes both servers recover the same crash.

## Growth rule

A method is added when the first port that needs it lands, as that port's
implementation child, and it must behave the same for both flavors. Examples:
`backup`, `init_from_backup`, `promote`, `poll_query_until`, `wait_for_log`,
`kill9`, `append_conf`.

The two existing harnesses stay the implementation layer. `tapnode` is the
port\-facing surface.

## First user: 034\_create\_database

- Upstream's steps on a goopg node:
  1. CREATE DATABASE test\_db\_1 STRATEGY WAL\_LOG TEMPLATE template1;
  2. CREATE TABLE in template1;
  3. CHECKPOINT;
  4. stop\('immediate'\).
- Then two variants run:
  - `pg18-cold-start`: PG 18\.3 recovers a copy and answers A1 = 1 and
    A2 = 0.
  - `all-goopg`: goopg restarts and answers A1 = 1 and A2 = 0.
- Both pass, 2/2 assertions each.
- The mixed variant depends on the M0149\-0001a1 layout \(template1 at
  `base/1`\) and on M0149\-0001a \(template1's full image at initdb\).

## Statement-at-a-time scripts and append\_conf \(M0149\-0002\)

- `PSQL` / `SafePSQL` write the SQL to a file and run `psql -f <file>`.
  - Cluster.pm feeds the SQL on stdin \(`psql -f -`\), so psql sends each
    statement as its own query, in its own transaction.
  - `-c` sent a multi-statement script as one query. PG runs that as one
    implicit transaction block, which refuses VACUUM and changes what
    TRUNCATE logs.
  - 036's scripts ran wrongly under `-c`; 034's single statements were not
    affected.
- `AppendConf(line)` is Cluster.pm's append\_conf\('postgresql.conf', line\)
  for a goopg node.

## Background sessions, exit codes and tablespace snapshots \(M0149\-0003, M0149\-0005\)

- `BackgroundPSQL(db, onErrorStop)` is BackgroundPsql.pm: a psql whose stdin stays open.
  - `Query` sends a statement and a banner and returns stdout up to the banner.
  - `QuerySafe` also fails on stderr output.
  - `Send` writes without waiting \(query\_until\).
  - `WaitStderr` is pump\_until on stderr.
- `PSQLExit(db, sql)` is Cluster.pm's psql: it returns the exit code \(0, 2 for a lost connection or FATAL, 3 for an
  error under ON\_ERROR\_STOP\) with stdout and stderr.
- `SnapshotDir(dir)` copies a directory aside and returns a restore function. ColdStartPG cannot isolate an external
  tablespace: PG's redo of XLOG\_TBLSPC\_CREATE points the copy's link at the logged location. A test therefore
  restores the tablespace between the PG cold start and its own restart.
