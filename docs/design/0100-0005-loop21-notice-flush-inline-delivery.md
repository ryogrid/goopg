# 0100-0005-loop21: Inline NOTICE Delivery via NoticeFlush

## Problem

The PostgreSQL `eval-plan-qual` and `eval-plan-qual-trigger` isolation specs
expect NOTICE messages (emitted by `RAISE NOTICE` in PL/pgSQL predicates and
trigger bodies) to appear in the test output **before** the `step <name>:
<sql> <waiting ...>` header when a step blocks on a row-level lock.

Previously, goopg buffered all NOTICE messages in `ctx.Notices` and emitted
them at `CommandComplete` time (via the accumulated-notices loop in
`dispatch.go`). This meant:

- Pre-blocking NOTICEs (emitted before `WaitForXID`) never reached the client
  before the isolation runner's `blockDetectWait` (300 ms) fired.
- The runner's `q.drain()` at blockDetectWait found an empty queue.
- NOTICEs appeared after `<... completed>` instead of before the step header.

Expected output (PostgreSQL isolationtester):
```
s2: NOTICE:  upid: text checking = text checking: t
s2: NOTICE:  up: numeric 600 > numeric 200.0: t
step wnested2: ... <waiting ...>
```

Actual output (before fix):
```
step wnested2: ... <waiting ...>
step c1: COMMIT;
s2: NOTICE:  upid: text checking = text checking: t   ← wrong position
```

## Root Cause

`ctx.AddNotice(msg)` buffered every NOTICE in `ctx.Notices`. The server only
emitted these at statement completion (before `CommandComplete`). Since the
blocking point occurs mid-execution, the pre-blocking NOTICEs were never
flushed to the TCP socket before the runner's blockDetectWait fired.

## Fix

### 1. `executor/context.go` — inline flush via `NoticeFlush`

`AddNotice` now calls `ctx.NoticeFlush(msg)` immediately and returns early
(skips buffering) when `NoticeFlush` is wired. This ensures the NOTICE is
written to the wire before execution reaches any blocking point.

```go
func (c *Context) AddNotice(msg string) {
    if c.NoticeFlush != nil {
        c.NoticeFlush(msg) // send immediately; do NOT double-buffer
        return
    }
    c.Notices = append(c.Notices, msg)
}
```

### 2. `server/dispatch.go` — wire `NoticeFlush` per connection

```go
ectx.NoticeFlush = func(msg string) {
    _ = w.WriteNoticeResponse([]protocol.ErrorField{...})
    _ = w.Flush()
}
```

`w.Flush()` ensures the bufio buffer is flushed to the TCP socket immediately.
`pq`'s read loop in the goroutine running `conn.QueryContext` receives the
`NoticeResponse` inline and calls the session notice handler, populating the
notice queue before `blockDetectWait` fires.

Protocol correctness: `NoticeResponse` is a legal PostgreSQL wire message at
any position in a query response stream. Sending it before `RowDescription`
is protocol-compliant.

### 3. `testport/framework/isolation_runner.go` — two harness fixes

**(a) Remove `queue.drain()` from `execStepFromQueue`.**
Previously, the goroutine for each new step cleared the session notice queue
at start. With inline flushing, re-evaluation notices from a concurrent
pending step (e.g. `wnested2` after `c1` commits) may already be in the
queue when a later step on the same session (e.g. `c2`) starts its goroutine.
The early drain was silently discarding those notices. Removing it lets the
main goroutine drain the queue at the correct moment (inside the pending-step
wait or `drainWithTimeout`).

**(b) Alphabetical ordering for "unused step name" output.**
PostgreSQL isolationtester enumerates steps via a hash table iterated in
sorted order, not definition order. `eval-plan-qual-trigger.spec`'s `s3_del_a`
must appear before `s3_r` (alphabetical: `d` < `r`). Changed from iterating
`spec.StepOrder` to sorting a `[]string` slice with `sort.Strings`.

## Effect

| Test | Before | After |
|------|--------|-------|
| `eval-plan-qual` | first divergence L394 | first divergence L411 |
| `eval-plan-qual-trigger` | first divergence L4 | first divergence L38 |

14 previously-passing tests: no regression.

## Remaining Gaps

**`eval-plan-qual`**: re-evaluation after EPQ generates fewer `noisy_oper`
NOTICE calls than PostgreSQL (EPQ re-scan semantic difference). Inline flush
is necessary but not sufficient; EPQ call-count parity requires separate work.

**`eval-plan-qual-trigger`**: BEFORE/AFTER triggers are fired after all rows'
WHERE predicates are evaluated. PostgreSQL fires BEFORE triggers inline during
the scan (row-by-row interleaving). Separate work needed to restructure
`updateOp` to fire BEFORE triggers mid-scan.
