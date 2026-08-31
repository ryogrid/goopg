# 0097-0024e — COPY in multi-statement simple-query batches (psql `\;`)

Status: accepted
Milestone: M0097-0024 (Port COPY / sequence / identity regress tests)
Date: 2026-05-25

## Problem

psql's `\;` separator joins several commands into a **single** simple-query
`Query` message (the wire string carries internal `;`). The server runs them
in order, emitting one `CommandComplete` per statement and a **single**
trailing `ReadyForQuery` for the whole message — PostgreSQL's
`exec_simple_query` semantics. `copyselect` exercises this with COPY embedded
in the batch:

```sql
copy (select 1) to stdout\; select 1/0;                                  -- row, then error
select 1/0\; copy (select 1) to stdout;                                  -- error only
copy (select 1) to stdout\; copy (select 2) to stdout\; select 3\; select 4;  -- 1 2 3 4
select 0\; copy test3 from stdin\; copy test3 from stdin\; select 1;     -- COPY FROM STDIN x2
```

goopg mishandled COPY inside a batch two ways:

1. **Routing.** `handleQueryOrCopy` sent the *whole* message to the
   single-COPY path (`dispatchCopyViaExecutor`) whenever it started with
   `COPY `. That path parses and requires exactly one statement, so a batch
   beginning with COPY (cases 1 and 3) was rejected with
   `expected exactly one COPY statement`.

2. **No executor path.** A batch *not* starting with COPY (case 4) went to the
   multi-statement dispatcher, whose per-statement loop handed the
   `*planner.Copy` node to the executor — which has no COPY operator and
   leaked the internal `planner.Copy has no executor path yet` (`0A000`).
   (Case 2 happened to work: the leading `select 1/0` errors before the COPY
   is reached.)

COPY is driven from the wire layer, not the executor, which is why it needs
special handling in the statement loop rather than an executor operator.

## Fix

Two changes, both in `internal/server`:

1. **Route multi-statement batches uniformly** (`handleQueryOrCopy`,
   `copy.go`). When the normalised query contains an internal `;`
   (`strings.ContainsRune(matchable, ';')`, where `matchable` already has
   trailing `;` stripped), route to `handleQuery` → the multi-statement
   dispatcher even when it starts with `COPY `. Single COPY statements (no
   internal `;`) keep the existing `dispatchCopyViaExecutor` fast path, which
   returns the connection's `copyInState` for COPY FROM STDIN. A `;` inside a
   string literal can over-classify a single COPY as multi-statement, but the
   dispatcher handles a lone inline COPY correctly, so the result is the same.

2. **Run COPY inline in the statement loop** (`dispatchSimpleQueryViaExecutor`,
   `dispatch.go` + new `runInlineCopy`, `copy.go`). The loop intercepts
   `*parser.CopyStmt` *before* the plan-cache / `executeOneSimpleStmt` path
   (and after the per-statement RC snapshot refresh, so `ectx.Snap` is
   current). `runInlineCopy`:
   - plans the COPY against `s.cfg.Catalog` (planner errors + hints forwarded
     via the existing `planErrorFields`/`planErrorHintFields` helpers);
   - for **CopyTo**, calls the shared `runCopyToStream` (which stops short of
     `CommandComplete`/`ReadyForQuery`) and writes only `CommandComplete
     "COPY n"` — the loop emits the single trailing `ReadyForQuery`;
   - for **CopyFrom + file endpoint**, runs `executor.RunCopyFromFile` inline
     (no wire interaction) and writes `CommandComplete`;
   - runs within the **batch's shared transaction** (`ectx.Tx`), so COPY
     (DML … RETURNING) writes commit atomically with the rest of the batch —
     unlike the single-COPY path, which uses a dedicated COPY-internal txn.

   On a handled error the write helper has already emitted `ErrorResponse` +
   `ReadyForQuery` and returns the `errQueryErrorSent` sentinel; the loop
   treats it exactly like a failed `executeOneSimpleStmt` — aborts the rest of
   the batch (PG aborts the whole message on error) and returns without a
   second `ReadyForQuery`.

### Deferred: COPY FROM STDIN inside a batch (case 4)

COPY FROM STDIN mid-batch needs synchronous `CopyData` reads interleaved with
the statement loop. The single-COPY path drives that through the connection's
`copyInState` returned up to `serveConn`, and `handleCopyInFrame` writes its
own `ReadyForQuery` on `CopyDone` — both incompatible with running inside the
batch loop without threading the `FrameReader` down and a batch-aware
CopyData/CopyDone variant. `runInlineCopy` therefore returns a clean
`0A000 "COPY FROM STDIN is not supported inside a multi-statement query"`
rather than the previous internal "no executor path" leak. This is the only
remaining `copyselect` gap.

## Tests

`internal/server/copy_executor_test.go`:
- `TestCopyToInMultiStatementBatch` — `copy (select 1) to stdout; copy
  (select 2) to stdout; select 3` streams both COPYs inline + one SELECT, with
  one `CommandComplete` each and a single trailing `ReadyForQuery`.
- `TestCopyToBatchStopsOnError` — `copy (select 1) to stdout; select 1/0`
  streams the COPY row, then the division-by-zero error aborts the batch.
- `TestCopyFromStdinInBatchDeferred` — `select 0; copy items from stdin;
  select 1` runs `select 0`, then surfaces the clean `0A000` deferral (asserts
  the internal "no executor path" text does **not** leak).

Verified live on port 5599 against the exact `copyselect.out` shapes: cases 1,
2, 3 match PostgreSQL byte-for-byte; case 4 emits the documented deferral.

## Related

- [[0097-0024d-copy-query-form-syntax-errors]] — prior gap in the same line of
  work; same wire-rendering helpers.
- [[0097-0009b-copy-from-view-rejection]] — planner-hint forwarding pattern.
- [[pattern_sibling_paths_must_agree]] — single-COPY vs multi-statement COPY
  are sibling wire paths; both must agree on COPY semantics.
