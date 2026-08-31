# 0097-0024f — COPY FROM STDIN inside a multi-statement `\;` batch (M0097)

Status: accepted
Date: 2026-05-25
Milestone: M0097-0024 (Port COPY / sequence / identity regress tests)

## Problem

psql's `\;` joins commands into one simple-query Query message (the server
sees one message with internal `;`). The server runs the statements in order,
emitting one `CommandComplete` per statement and a single trailing
`ReadyForQuery` for the whole message.

`COPY … TO STDOUT` and server-side `COPY … FROM 'file'` inside such a batch
already worked (see [[0097-0024e-copy-in-multi-statement-batch]]) because
neither needs client→server streaming. **`COPY … FROM STDIN`** mid-batch was
deferred: it returned a clean `0A000 "COPY FROM STDIN is not supported inside a
multi-statement query"`. This is the `copyselect` shape

```
select 0\; copy test3 from stdin\; copy test3 from stdin\; select 1; -- 0 1
1
\.
2
\.
select * from test3;
```

where each COPY consumes one `\.`-terminated data block. It is the last
COPY-family wire-protocol gap blocking `copyselect`.

## Why it was hard

The single-COPY path drives CopyData/CopyDone via the connection's
`copyInState` + `handleCopyInFrame`, which the main read loop
(`runPostStartupLoop`) pumps frame by frame — and `handleCopyInFrame` writes
its **own** `ReadyForQuery` on CopyDone. That is incompatible with a
mid-batch COPY, which must write only `CommandComplete` and let the dispatch
loop emit the single trailing RFQ for the whole message. The dispatch loop
also never had access to the `*protocol.FrameReader`, so it could not read the
client's CopyData frames synchronously.

## Fix

Thread the connection's `*protocol.FrameReader` (`r`) down the simple-query
path so the mid-batch COPY can read its own data frames synchronously:

```
runPostStartupLoop → handleQueryOrCopy(r, …) → handleQuery(r, …)
  → dispatchSimpleQueryViaExecutor(r, …) → runInlineCopy(r, …)
```

`runInlineCopy`'s `CopyFrom`/STDIN branch now calls the new
`runInlineCopyFromStdin(r, w, ectx, plan)` (`internal/server/copy.go`):

1. Build the `executor.CopyFromExecutor` against the batch's shared `ectx.Tx`.
2. Write `CopyInResponse` and **flush** (the client only sends CopyData after
   it sees CopyInResponse, so the buffered frame must reach the wire before we
   block on `ReadFrame`).
3. Loop reading frames from `r`:
   - `CopyData` (text): buffer + split on `\n`, push each line via
     `PushLine`; skip the deprecated `\.` end-of-data marker (`isCopyTextEOD`).
   - `CopyData` (binary): `PushBinaryData`; a trailer inside the chunk ends
     the COPY like CopyDone.
   - `CopyDone`: flush any partial trailing line, then write
     `CommandComplete "COPY n"` and return — **no commit, no RFQ**.
   - `CopyFail`: `writeQueryError(57014, msg)` (ErrorResponse + RFQ +
     `errQueryErrorSent`) so the dispatch loop aborts the rest of the batch.
   - decode/insert error: same `writeQueryError` path.
   - `Flush`: no-op. `Terminate`: return `io.EOF` (connection teardown).

Crucially, unlike the single-COPY path this **neither commits nor emits
ReadyForQuery**: the COPY shares the batch's transaction (`ectx.Tx`), which
the dispatch loop commits once at the end, and the dispatch loop emits the
single trailing RFQ. This keeps `COPY (DML … RETURNING)` and ordinary table
COPY atomic with the rest of the batch and matches PG's `exec_simple_query`
(one RFQ per message, one CommandComplete per statement).

Because the main read loop is parked in `handleQueryOrCopy` for the duration
of the batch, consuming frames directly inside `runInlineCopyFromStdin` is
safe and preserves statement ordering — there is no second consumer of `r`.

## Tests

- `TestCopyFromStdinInMultiStatementBatch` (`internal/server/copy_executor_test.go`):
  the `select 0\; copy … from stdin\; copy … from stdin\; select 1` shape with
  two `\.`-terminated data blocks; asserts the full wire frame sequence
  (one CopyInResponse + CommandComplete per COPY, single trailing RFQ) and that
  both rows are committed and visible to a follow-up `COPY … TO STDOUT`.
- `TestCopyFromStdinInBatchAbortsOnFail`: a `CopyFail` mid-batch surfaces a
  clean `57014` ERROR + single RFQ and aborts the rest of the batch.

Verified end-to-end via `GOOPG_REGRESS_DIFF_DIR`: the `copyselect`
`select 0\; copy test3 from stdin\; …` block now matches PG byte-for-byte
(`select * from test3` returns rows `1` and `2`).

## Remaining `copyselect` gap (newly surfaced, next COPY wins)

The parenthesised-query COPY form does not accept the bare (non-`WITH`) legacy
option trail: `copy (select t from test1 where id = 1) to stdout csv header
force quote t` fails in the parser (the query form's trailing-clause handling
stops at `csv`), so the CSV `t` header line and the `"a"` force-quoted value
are missing from the output. `COPY (query) TO STDOUT WITH (format csv, header,
force_quote (t))` parses fine. Closing this needs (a) the parenthesised-query
form to accept `parseCopyLegacyTrail` plus the legacy `FORCE QUOTE <cols>`
syntax, and (b) CSV `HEADER` output + `FORCE_QUOTE` rendering in the COPY-TO
executor. That is the only remaining `copyselect` diff (2 lines).

Sibling-path class: [[pattern_sibling_paths_must_agree]] (single-COPY
`handleCopyInFrame` vs. mid-batch `runInlineCopyFromStdin` must stay in sync on
text/binary decode, `\.` skipping, and partial-line flush).
