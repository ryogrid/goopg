# M0119-0006 (44th slice) — draining the frontend's COPY stream after a failed COPY FROM

Status: accepted / landed
Date: 2026-08-13
Area: `internal/server` (v3 frontend/backend protocol main loop)

## The defect

A `COPY … FROM STDIN` that fails mid-stream left the session permanently one
`ReadyForQuery` ahead of the client. Reproduced on a live goopg (throwaway
cluster, port 5533) with a four-line input whose second line is a bad integer:

```
psql: ERROR:  COPY: column "a": invalid integer "notanint"
ERROR:  message type 'c' not yet supported
psql: message type 0x5a arrived from server while idle
```

Every statement after the failed COPY was answered against the wrong frame, so
the connection was effectively dead even though the COPY had correctly failed.
The wrong-answer risk is zero — the COPY did reject the bad row — but the
session desync is total.

## Why it happened

The failure and the frontend's stream are inherently concurrent. goopg reports
the decode error the moment the offending line is pushed
(`handleCopyInFrame`, `internal/server/copy.go:542-560`): it rolls the COPY
transaction back, writes `ErrorResponse` + `ReadyForQuery`, and returns
`done=true`, which clears `copyIn` in the main loop
(`internal/server/server.go:1604-1606`).

The client cannot know that yet. libpq has already pushed the remainder of the
file and follows it with `CopyDone`. Those frames arrive at a loop that is no
longer in COPY mode, fall through the main `switch` to its `default` arm, and
each one draws `message type %q not yet supported` **plus a second
`ReadyForQuery`** — which is what puts the session out of step.

The same exposure exists on the batch path: `runInlineCopyFromStdin`
(`copy.go:364`) returns `errQueryErrorSent` on a decode error and the dispatch
loop abandons the batch, so the frontend's trailing frames land in exactly the
same place.

## Upstream behaviour

`postgres/src/backend/tcop/postgres.c:5004-5013`:

```c
			case PqMsg_CopyData:
			case PqMsg_CopyDone:
			case PqMsg_CopyFail:

				/*
				 * Accept but ignore these messages, per protocol spec; we
				 * probably got here because a COPY failed, and the frontend
				 * is still sending data.
				 */
				break;
```

Three properties matter and all three are load-bearing:

1. All **three** frame types are covered, not just `CopyDone`.
2. There is **no** `ErrorResponse` — the failure was already reported.
3. There is **no** `ReadyForQuery` — emitting one is precisely the desync.

PG's skip-until-`Sync` state is unaffected: `SocketBackend`
(`postgres.c:435-444`) only clears `doing_extended_query_message` for these
types, and the `ignore_till_sync` test still skips them. goopg's equivalent
guard (`server.go:1612`) already has the matching shape, so it needed no change.

## The change

One arm added to the post-startup main loop's `switch`, ahead of `default`
(`internal/server/server.go`). It is deliberately empty, mirroring upstream's
bare `break`. Placing it after the `copyIn != nil` fast path at `server.go:1596`
means a live COPY is unaffected — only frames arriving once the server has left
COPY mode reach it.

## Verification

- `internal/server/copy_error_drain_test.go`, two tests on the
  executor-backed harness (`startCopyExecServer`, table `items`) — the
  storage-less `startTestServer` cannot reach the COPY decode path at all
  (its COPY FROM falls into the row-counting stub and accepts `notanint`).
  - `TestCopyFromStdinErrorDrainsTrailingFrames` — bad line, read the error,
    then push the pipelined `CopyData`/`CopyDone` and assert the next
    `SELECT 1` produces exactly `TDCZ`.
  - `TestCopyFailAfterCopyEndedIsIgnored` — a late `CopyFail` after a
    *successful* COPY draws no `ErrorResponse`.
  - Both verified fail-when-broken by deleting the new case: each reports
    `frames="EZ"`, the exact live symptom.
- E2E on a capped throwaway goopg (5533) and on the PG 18.3 oracle (65432),
  same script: both engines report the COPY error, keep the session alive, and
  leave `count(*) = 0`.

## Deferred (see `.ralph/deferral_ledger.md`)

goopg's COPY error message and its absence of a `CONTEXT:` line diverge from
upstream — measured side by side in this slice and filed rather than fixed,
since it is a diagnostics-formatting concern in the executor, not a protocol
one.
