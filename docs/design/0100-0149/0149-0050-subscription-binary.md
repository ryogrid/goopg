# M0149\-0050 — Port subscription/t/014\_binary.pl

Status: done. TestPort\_Subscription014Binary passes 3/3 in all three pairings.

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

## Landed — M0149\-0050b, binary COPY of array columns

goopg refused an array column in binary COPY \("binary COPY of array type integer\[\] is not supported"\). A PG
subscriber's binary table sync of test\_arrays therefore failed. `arraySendBinary` / `arrayRecvBinary`
\(internal/executor/copy\_binary\_array.go\) port array\_send / array\_recv \(arrayfuncs.c\) for one\-dimensional arrays:

- Layout: ndim, has\-nulls, element type OID, then per dimension the length and a lower bound of 1, then each element
  as an int4 length \(\-1 for NULL\) plus its own send form. An empty array is ndim 0.
- Send parses array\_out's text element by element through COPY's text input for the element type, then that type's
  binary send. Receive checks the element type OID, decodes each element with its binary receive, and rebuilds
  array\_out's text in ISO / hex styles.
- Element types array.ElemTypeInfo does not know keep the 0A000 refusal. Multi\-dimensional or explicitly bounded
  arrays are refused with 0A000.
- encodeNumericBinary accepts numeric text, which is how an array element arrives.

Measured byte\-identical with PG 18.3's COPY … WITH \(FORMAT binary\) of `int4[]`, `numeric[]` and `text[]` columns,
including NULL elements and an empty array. PG's binary stream reloads into goopg. Tests:
TestCopyBinaryArrayMatchesPG and TestCopyBinaryArrayElementTypes \(replacing the refusal test\).

In TAP 014 goopg\-publisher\-pg18\-subscriber, the first six assertions now pass, including binary apply \(the PG
subscriber accepts goopg's text tuple columns\). The pairing stops at the custom\-type DDL \(0050e\).

## Landed — M0149\-0050e, ALTER TYPE … SET \(send, receive\)

014's custom\-type DDL ran on goopg up to `ALTER TYPE myvarchar SET (send = myvarcharsend, receive = myvarcharrecv)`,
which was a syntax error. The shell type, the `LANGUAGE internal` varchar I/O functions and `CREATE TYPE … (input,
output)` were already accepted.

- **Grammar.** goopg\_ext.y alter\_type\_action gains `SET '(' type_set_list ')'` \(gram.y's operator\_def\_list
  subset: ColLabel = ColId \| Sconst; NONE is a ColId\). The hand\-written parseAlterType has the same arm. The
  conflict pin stays at 61, and the goldens change only by the new `SetOptions` field.
- **Executor.** `execAlterTypeSetOptions` is AlterType for a base type. It accepts receive / send / typmod\_in /
  typmod\_out / analyze / subscript / storage, raises 42601 "type attribute … not recognized" otherwise, and records
  send / receive on the type's registration \(`CompositeType.SendFunc` / `ReceiveFunc`; NONE clears them\).

Tests: TestAlterTypeSetOptions \(parser\), TestAlterTypeSetSendReceive \(executor\). TAP 014
goopg\-publisher\-pg18\-subscriber passes 9/9.

## Landed — M0149\-0050c, a goopg subscriber in binary mode

- **Option.** `Subscription.Binary` \(pg\_subscription.subbinary\) is set by CREATE SUBSCRIPTION … WITH \(binary = true\)
  and by ALTER SUBSCRIPTION … SET \(binary = …\). It is journaled in the heap row, reloaded and shown in the views. The
  apply launcher restarts a worker whose binary setting changed \(maybe\_reread\_subscription\).
- **Streaming.** The receiver adds `"binary" 'true'` to START\_REPLICATION. The pgoutput decoder accepts `b` columns,
  and the apply worker reads them with the type's receive \(copyBinaryToDatum\).
- **Table sync.** copy\_table uses `… TO STDOUT WITH (FORMAT binary)` when the subscription is binary, read fresh on
  each attempt so a retried sync follows an ALTER. The stream goes through CopyFromExecutor.PushBinaryData.
  BinaryInputUnavailable raises 42883 "no binary input function available for type %s" for a user base type without
  a receive function \(BeginCopyFrom's getTypeBinaryInputInfo\).
- **Errors.** A fixed\-width field of the wrong length is 22P03 "incorrect binary data format" when it has bytes left
  over, as CopyReadBinaryAttribute reports, and 08P01 "insufficient data left in message" when it is short
  \(binaryLengthError\). A receive function's ExecError now reaches the client unwrapped.
- **Apply gate.** should\_apply\_changes\_for\_rel: INSERT / UPDATE / DELETE apply only to a READY
  pg\_subscription\_rel row. A relation the subscription does not track yet \(published before REFRESH PUBLICATION\)
  is skipped; its rows arrive with the table sync. goopg applied such rows, so the later sync copied them a second
  time. UPDATE and DELETE had no gate at all.
- **Commit order.** RunTableSync commits the copied rows \(TableSyncConfig.Commit\) before advancing the rel to `s`.
  Before, a reader could see the table synchronized before its rows were visible.

Tests: TestApplyBinaryTupleColumns, TestBinaryInputUnavailable. TestPort\_Subscription014Binary is ungated and passes
3/3 in all three pairings.

M0149\-0050d \(binary tuples from a goopg publisher\) proved not to be a 014 requirement: a PG 18 subscriber in binary
mode applies text columns. It stays open as PG parity.

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
