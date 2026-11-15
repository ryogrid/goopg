# M0149\-0001 — port recovery/t/034\_create\_database.pl

Status: started 2026\-11\-15 \(baseline measured; prerequisites filed as
children M0149\-0001a, M0149\-0001b\).

## Upstream test

`postgres/src/test/recovery/t/034_create_database.pl` checks WAL replay of
`CREATE DATABASE … STRATEGY WAL_LOG` after DDL on the template database:

1. `CREATE DATABASE test_db_1 STRATEGY WAL_LOG TEMPLATE template1` \(on
   `postgres`\).
2. `CREATE TABLE tab_db_after_create_1 (a INT)` on `template1`.
3. `CHECKPOINT`, then `stop('immediate')` and `start` \(crash recovery\).

### Assertion map

| \# | `.pl` line | assertion | expected |
|---|---|---|---|
| A1 | `is($result, "1", …)` after the restart | `SELECT count(*) FROM pg_class WHERE relname LIKE 'tab_db_%'` on `template1` | `1` |
| A2 | `is($result, "0", …)` | the same query on `test_db_1` | `0` |

## Baseline at HEAD \(2026\-11\-15, before any child\)

- All\-goopg: A1 = 1, A2 = 0, so 2/2. The steps were run by hand against
  `goopg init` / `start` / `stop -mode immediate` / `start`.
- WAL produced by goopg, read with PG 18.3 `pg_waldump`:
  - `Database CREATE_WAL_LOG create dir 1663/16406`;
  - per\-relation `XLOG FPI` copies of the new database's relations;
  - `RelMap UPDATE database 16406`, the `pg_database` heap insert and the
    COMMIT.
  - This is the PG record sequence of `CreateDatabaseUsingWalLog`
    \(`dbcommands.c`\). The filing hint "XLOG\_DBASE\_CREATE\_WAL\_LOG emission
    and replay" is therefore already satisfied.
- PG 18.3 `pg_controldata` reads goopg's `global/pg_control` \(state
  `in production` after the immediate stop\).
- C3 single\-node cross\-check: PG 18.3 `postgres` started on a copy of the
  crashed goopg data directory runs crash recovery \(`redo starts at …`,
  end\-of\-recovery checkpoint\) and accepts connections.
  - A2 on PG: `0`, which passes.
  - A1 on PG: the connection to `template1` fails with `FATAL: could not open
    file "base/1/pg_filenode.map": No such file or directory`.
- Baseline tap count: all\-goopg 2/2, mixed 1/2 \(3/4 over both runs\). No
  `TestPort_` function exists yet.

## Prerequisite children

- **M0149\-0001a — template1 carries the full PG per\-database catalog
  image, including `base/1/pg_filenode.map`.**
  - Upstream: initdb's bootstrap writes `base/1` as the complete template1
    \(`bootstrap.c`, `relmapper.c` `write_relmap_file`\). PG's `base/1` holds
    300 files; goopg's holds 138 and no relation map.
  - `internal/initdb/initdb.go` `refreshTemplate0Image` notes that base/5
    "additionally carries pg\_filenode.map and the per\-database placeholder
    files", and base/1 lacks them.
  - Verification: PG 18.3 cold start on a goopg directory connects to
    `template1` and returns A1 = 1.
- **M0149\-0001b — Go `Cluster.pm`\-equivalent harness for single\-node
  ports, with goopg and PG 18.3 nodes behind one API.**
  - The methods 034 needs: `init`, `start`, `stop('immediate')`, per\-database
    `safe_psql`, and the C3 copy\-and\-cold\-start cross\-check \(copy a
    stopped goopg data directory, start PG 18.3 `postgres` on it, run the
    same assertions\).
  - Existing pieces: `internal/testutil/cluster` \(goopg\) and
    `internal/testutil/pgcluster` \(PG\); the precedent is
    `e2e_pg_crashstart_on_goopgdata_test.go`.

## Mixed topology \(C3\)

034 is single\-node. Its mixed variant is the artifact cross\-check:

- PG 18.3 `pg_waldump` decodes goopg's WAL across the CREATE DATABASE;
- PG 18.3 `pg_controldata` reads goopg's `pg_control`;
- PG 18.3 started on a copy of the post\-crash goopg directory answers A1
  and A2 identically.

There is no non\-mixable step.
