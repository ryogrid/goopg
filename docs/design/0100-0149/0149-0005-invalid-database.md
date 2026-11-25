# 0149\-0005 — recovery/t/037\_invalid\_database.pl: invalid databases and an interrupted DROP DATABASE

Status: landed 2026\-11\-23. TestPort\_Recovery037InvalidDatabase passes 11/11 assertions. The test has no crash
step, so there is no PG cold\-start variant.

## The test

- Part 1 marks a database invalid by hand \(`UPDATE pg_database SET datconnlimit = -2`\) and checks that:
  - connecting to it is FATAL;
  - ALTER DATABASE on it is FATAL;
  - it cannot be used as a template;
  - VACUUM FREEZE ignores its datfrozenxid;
  - it can be dropped, once.
- Part 2 holds `LOCK pg_tablespace` in a prepared transaction and runs DROP DATABASE from the same session. The
  DROP must wait for pg\\_tablespace; the test cancels it and expects the database to be left invalid.

## Changes \(each named by a failing assertion or a startup failure\)

- **GUCs.** goopg refused to start with `log_connections = receipt`, an unknown parameter.
  - `log_connections` is registered as PG 18's string GUC \(PGC\\_SU\\_BACKEND\): a comma list of receipt,
    authentication, authorization, setup\\_durations and all, or a pre\\-18 boolean alone \(check\\_log\\_connections\).
  - `log_disconnections` is registered too.
  - goopg still logs every connection either way.
- **ALTER DATABASE on an invalid database** is AlterDatabase's FATAL "cannot alter invalid database" with PG's hint;
  the session ends. It covers the option forms CONNECTION LIMIT, ALLOW\\_CONNECTIONS, IS\\_TEMPLATE and TABLESPACE.
  `databaseDDLError` gained `hint` and `fatal`; the simple\\-query bypass writes a FATAL and closes the connection.
- **CREATE DATABASE ... TEMPLATE invalid** is createdb's ERROR "cannot use invalid database ... as template".
- **DROP DATABASE follows dropdb's order:**
  - after its guards, the database is marked invalid \(datconnlimit = \\-2\) in memory and in its pg\\_database heap row,
    committed on its own, as PG's in\\-place update is durable at once;
  - it then takes remove\\_dbtablespaces' AccessShareLock on pg\\_tablespace under the connection's lock identity and
    the statement context;
  - pg\\_cancel\\_backend cancels the wait with 57014 "canceling statement due to user request". The database stays,
    invalid.
- **Shared catalogs lock with database 0** \(SET\\_LOCKTAG\\_RELATION's relisshared case\), in LOCK TABLE and in the DDL
  helper, so such a lock conflicts across databases.
- **PREPARE TRANSACTION hands its locks to the prepared transaction.** A detached prepared transaction keeps the lock
  identity its locks were taken under, and the connection gets a fresh one \(MarkAsPreparing / PostPrepare\\_Locks'
  dummy PGPROC\). Before, the same connection's next statement was granted those locks: the DROP did not wait.
- **Harness.** tapnode gained:
  - `BackgroundPSQL`, the equivalent of BackgroundPsql.pm, with Query, QuerySafe, Send and WaitStderr;
  - `PSQLExit`, Cluster.pm's psql returning the exit code with stdout and stderr.

## Not done

- The extended\\-protocol path reports the ALTER refusal as an ERROR, not a FATAL; it has no FATAL path for database
  DDL.
- goopg's DROP DATABASE is still not one transaction. The invalid mark, the catalog removal and the file removal are
  separate steps; PG deletes the pg\\_database tuple transactionally.
- The shared\\-catalog list for lock tags names the shared tables, not their indexes or TOAST relations.
- A SERIALIZABLE prepared transaction is not detached, so its connection keeps the locks' identity.
