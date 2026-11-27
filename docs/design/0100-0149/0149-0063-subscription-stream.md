# 0149\-0063 — Port subscription/t/015\_stream.pl \(streaming of in\-progress transactions\)

Status: in progress. The port TestPort\_Subscription015Stream is gated by GOOPG\_TAP\_015.

- The goopg publisher → PG 18.3 subscriber pairing passes in full, including PG's parallel apply and deadlock
  sections.
- The two goopg\-subscriber pairings stop at the first parallel\-apply log check: a goopg subscriber does not apply
  streams yet \(0063b, 0063c\).

015\_stream.pl streams transactions larger than `logical_decoding_work_mem = 64kB`. It runs streaming = on and
streaming = parallel, in text and binary mode, and covers:

- interleaved transactions and a deadlock between parallel apply workers;
- `debug_logical_replication_streaming = immediate` on the subscriber.

## Landed — M0149\-0063a \(publisher\-side streaming; bytea in pgoutput\)

### The decoder streams large transactions

This mirrors ReorderBufferCheckMemoryLimit and ReorderBufferStreamTXN.

- The reorder buffer counts the bytes its queued changes hold, per transaction and in total
  \(ReorderBufferChangeSize: the tuple bytes plus a fixed per\-change overhead\).
- With streaming enabled \(`Decoder.SetStreaming`\), `ApplyChange` checks the limit. While the buffer is at or over
  `logical_decoding_work_mem`, the largest transaction goes out as one stream segment: stream start, its queued
  changes, stream stop.
  - `debug_logical_replication_streaming = immediate` streams every change.
  - A trailing DELETE is held back, since the next change may be the INSERT half of the same UPDATE.
- A committed streamed transaction sends what was queued since its last segment, then a stream commit. An aborted one
  sends a stream abort.
- Nothing is streamed before start\_decoding\_at \(ReorderBufferCanStartStreaming\).
- Nothing is streamed while an origin filter is set: goopg learns a transaction's origin only at its commit, so a
  streamed segment could not be filtered.

### pgoutput's stream messages

- `'S'` xid first\_segment, `'E'`, `'c'` xid flags commit\_lsn end\_lsn commit\_time, and `'A'` xid subxid
  \[abort\_lsn abort\_time under streaming = parallel\].
- Inside a segment every change message \(R I U D T M\) carries the xid after its type byte.
- The relations whose schema a streamed transaction sent count as sent for the session only once it commits
  \(RelationSyncEntry streamed\_txns, cleanup\_rel\_sync\_cache\).
- The walsender parses the `streaming` option \(on / off / parallel; PG's error otherwise\). It enforces
  proto\_version ≥ 2 \(≥ 4 for parallel\), and sizes the decoder from the server's `logical_decoding_work_mem` and
  `debug_logical_replication_streaming`.
  - Both GUCs are now registered with PG's defaults.
  - `replication.Config.Setting` reads them.

### bytea, on both sides

- A goopg publisher sent a bytea value's raw bytes as its text form. It now sends byteaout's hex form.
- A binary\-format bytea \(a binary\-mode subscription's `'b'` column, binary COPY\) came back as a string Datum that
  the heap encoder ran through byteain. It is now the value itself \(bytearecv\).

Tests: TestDecoderStreamsLargeTransactions, TestPgOutputStreamMessages, TestPgoutputByteaTextIsHex,
TestCopyBinaryByteaIsBytes.

## Landed — M0149\-0063b \(the goopg subscriber applies streamed transactions\)

- `pg_subscription.substream` is stored: `Subscription.Streaming`, with CREATE SUBSCRIPTION's default `p` \(PG 18\),
  ALTER ... SET \(streaming\), the heap row, the restart reload and both virtual\-view builders.
  - Both builders also hard\-coded `suborigin`; it now shows the stored value too \(0062a's missed sibling\).
  - The launcher restarts a worker whose streaming mode changed.
- The receiver asks for streaming as libpqrcv\_startstreaming does: `"streaming" 'on'` at proto\_version 2, or
  `'parallel'` at 4.
  - It reads with `xlog.DecodeMessageInStream`, which parses `S` / `E` / `c` / `A` and takes the xid off change
    messages inside a segment.
  - `S` and `E` track whether a segment is open. A reconnect forgets partial streams, and the publisher sends them
    again from their first segment.
- The apply worker spools the change messages of a segment, with the xid removed, under the stream's xid
  \(handle\_streamed\_transaction → stream\_write\_change; PG writes a file, goopg keeps them in memory\).
  - At stream commit it replays them in one transaction \(apply\_spooled\_messages\), and end\_lsn is the
    applied position.
  - A stream abort drops the spool: the whole of it for the top\-level xid, or a subtransaction's changes from its
    first one on.
  - Transactions sent between segments apply as usual.

Every goopg subscription now asks for streaming by default, as PG 18's do. A PG 18 publisher with
`logical_decoding_work_mem = 64kB` streams a 5000\-row transaction to a goopg subscriber
\(`pg_stat_replication_slots.stream_txns = 1`\), and the rows match.

Test: TestApplyWorkerAppliesStreamedTransactions.

## Open

- **0063c.** Parallel apply \(streaming = parallel\):
  - a parallel apply worker per streamed transaction;
  - the DEBUG lines 015 waits for \("finished processing the STREAM COMMIT command", "applied N changes in the
    streaming chunk"\);
  - deadlock detection between the leader and workers;
  - immediate\-mode serialization to files.
