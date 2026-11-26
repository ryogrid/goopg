# M0149\-0050 — Port subscription/t/014\_binary.pl

Status: in progress. TestPort\_Subscription014Binary is written \(three pairings\) and gated by GOOPG\_TAP\_014. The
test mirrors Cluster.pm's `log_statement = all` on both nodes.

## Landed — M0149\-0050a, a COPY statement is logged as PG logs it

exec\_simple\_query logs every statement under log\_statement as `LOG:  statement: %s`, including a COPY on a replication
connection \(tablesync's copy\_table\). 014 waits for `statement: COPY … TO STDOUT WITH \(FORMAT binary\)` on the
publisher. goopg had two gaps:

- `handleQueryOrCopy` dispatched a single COPY straight to the executor, bypassing `handleQuery`'s logStatement, so
  COPY was never logged. It now logs \(and times\) the COPY first.
- The statement line's message was `statement`, with the SQL in an attribute. It is now PG's errmsg,
  `statement: <sql>`, with the prefix / proto / xid attributes kept.

The TAP log reader \(tapnode.pgStyleLogLine\) puts goopg's `prefix` attribute, its rendered log\\_line\\_prefix, ahead of the
level, as PG writes it, and drops goopg's `proto` attribute. A statement line therefore reads
`<prefix>LOG:  statement: <sql>` and ends with the SQL, as 014's `TO STDOUT\n` pattern requires.

Test: TestPGStyleLogLine \(new case\). In TAP 014 goopg\\-publisher\\-pg18\\-subscriber, the binary\\-COPY log wait now passes.

## Open

- **M0149\\-0050b**: binary COPY of array columns. goopg's binary COPY TO refuses arrays with "binary COPY of array type
  integer\[\] is not supported", so a PG subscriber's binary table sync of test\_arrays fails. PG uses array\_send.
- **M0149\\-0050c**: a goopg subscriber with binary = true copies with `COPY … TO STDOUT WITH (FORMAT binary)`, asks
  pgoutput for binary \(the START\_REPLICATION `binary 'true'` option\), and applies `b` tuple columns through
  typreceive. It raises "no binary input function available for type %s" and "incorrect binary data format" as
  logicalrep / copy do.
- **M0149\\-0050d**: a goopg publisher honours the `binary` option, writing `b` columns through typsend
  \(logicalrep\_write\_tuple\).
- **M0149\\-0050e**: the custom\\-type DDL 014 runs — a shell type, `LANGUAGE internal` functions over varcharin /
  varcharout / varcharsend / varcharrecv, CREATE TYPE \(input, output\) and ALTER TYPE … SET \(send, receive\).
