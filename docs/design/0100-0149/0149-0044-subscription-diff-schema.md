# M0149\-0044 — Port subscription/t/008\_diff\_schema.pl

Status: in progress. TestPort\_Subscription008DiffSchema is gated by GOOPG\_TAP\_008. The goopg publisher with a PG
18.3 subscriber passes \(M0149\-0040f\). With a goopg subscriber the copy, apply and extra\-column checks pass
\(M0149\-0044a\). The last check needs ALTER SUBSCRIPTION … REFRESH PUBLICATION \(M0149\-0044b\). All\-goopg also
stalls after a refresh because the apply worker does not resume \(M0149\-0042b\).

## Landed — M0149\-0044a, table sync into a table with extra local columns

PG's tablesync `copy_table` works in four steps:

1. It reads the publisher's column list \(fetch\_remote\_table\_info: `attnum > 0`, not dropped, not generated\).
2. It asks the publisher for exactly those columns: `COPY rel (cols) TO STDOUT`.
3. It loads them locally with `COPY local_rel (cols) FROM`, so the subscriber's DEFAULTs, identity and serial
   columns fill the extra columns.
4. If a replicated column is missing locally, it raises 42P10 "logical replication target relation "s.t" is
   missing replicated column\(s\): …".

goopg had five gaps:

- **No column list.** goopg copied the whole table into every local column in order. 008's subscriber table
  stayed empty after a sync that reported ready. `FetchRemoteColumns` now queries pg\_attribute on the publisher,
  `TableSyncConfig.Columns` narrows the COPY, and `refreshOpenWriter` maps the remote names onto local columns
  \(`copyColumnIndex`, with PG's 42P10 text\).
- **Swallowed commit error.** The writer's commit error was discarded. It is now the rel's result error.
- **No initial sync on CREATE.** CREATE SUBSCRIPTION seeded no pg\_subscription\_rel rows and copied nothing. With
  connect = true it now runs the refresh hook, like CreateSubscription's fetch\_table\_list loop. Its rows are
  'i' for copy\_data = true and 'r' for copy\_data = false.
- **COPY with a column list skipped identity and serial.** COPY FROM with a column list filled DEFAULTs but not
  identity or serial columns. `autoGenerateSerialValues` now runs after `fillDefaults`.
- **Applied INSERTs skipped identity and serial.** The apply worker's INSERT did not fill identity or serial
  columns, and now does, as PG's ExecInsert does through the column defaults.

The regress pins improve: copy2 goes from 248 to 225 mismatched lines and identity from 318 to 312, while copy and
sequence are unchanged. Measured against a HEAD worktree.

The table sync still runs synchronously inside CREATE / ALTER SUBSCRIPTION, where PG uses asynchronous tablesync
workers. A copy failure therefore fails the command, while PG logs it from the worker \(deferral ledger\).

## Open — M0149\-0044b, REFRESH PUBLICATION

`ALTER SUBSCRIPTION … REFRESH PUBLICATION` parses to a compatibility no\-op, so nothing is synced. 008's test\_tab2
check passes only because the apply worker applies changes for any same\-named local table. The test\_tab3 check
needs two things:

- the refresh itself;
- PG's asynchronous outcome: the command succeeds and the tablesync worker logs the 42P10 ERROR.
