# 0102-0004 — `promote.signal` File Watcher (pg_ctl promote Parity)

**Status:** draft
**Date:** 2026-05-13
**Milestone:** M0102-0004
**Upstream reference:** `postgres/src/backend/access/transam/xlogrecovery.c:4475` (`CheckForStandbyTrigger`), `postgres/src/bin/pg_ctl/pg_ctl.c:1186` (`do_promote`), `postgres/src/include/access/xlog.h:314` (`PROMOTE_SIGNAL_FILE` macro).

## Problem

PostgreSQL's `pg_ctl promote -D <datadir>` works by creating a
`promote.signal` file in the data directory; the startup process polls for
it on each WAL replay cycle (`CheckForStandbyTrigger`) and ends recovery
when the file appears, then deletes the file.

goopg already implements promotion logic in
`cmd/goopg/standby.go::standbyController.Promote(ctx)` — accessible via
goopg's own control socket (`goopg promote -D <datadir>`). But upstream
`pg_ctl promote` cannot drive it, which forces the M0102 E2E tests to embed
goopg-specific RPC logic. For test simplicity and operator parity, the
goopg standby should also recognise `promote.signal` and trigger the same
Promote path.

## Upstream contract

From `postgres/src/backend/access/transam/xlogrecovery.c:4475`
`CheckForStandbyTrigger`:

1. Check `promote.signal` in datadir; if exists, set `LocalPromoteIsTriggered = true`,
   remove the file, return true.
2. Check `fallback_promote` (deprecated; out of scope).
3. Return false otherwise.

The function is called from the standby's WAL replay loop on each replay
cycle. The first `true` causes the loop to exit cleanly, transitioning to
the promote path (TLI bump, history file write, accept writes — covered in
0102-0002).

## Solution

### Add a poller goroutine to `standbyController`

In `cmd/goopg/standby.go`, alongside the existing replayer + walreceiver
goroutines, add a `promoteSignalWatcher`:

```go
func (sc *standbyController) promoteSignalWatcher(ctx context.Context) {
    path := filepath.Join(sc.rt.DataDir, "promote.signal")
    t := time.NewTicker(promoteSignalPollInterval) // 250 ms
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-t.C:
            if _, err := os.Stat(path); err == nil {
                // Remove first (idempotency: if Promote fails partially,
                // re-creating the file re-triggers).
                _ = os.Remove(path)
                if err := sc.Promote(ctx); err != nil {
                    sc.rt.Log.Error("promote.signal triggered Promote failed", "err", err)
                }
                return
            }
        }
    }
}
```

Started by `standbyController.Start`, cancelled on shutdown or after a
successful Promote.

### Idempotency with the existing control-socket PROMOTE

`Promote(ctx)` already has `promoteOnce sync.Once` (per
`cmd/goopg/standby.go:65`), so a concurrent control-socket PROMOTE and a
`promote.signal` arrival do not double-promote. The error from a
second-time invocation is surfaced via `promoteErr` for the caller; the
signal watcher just logs.

### Polling interval

`promoteSignalPollInterval = 250 * time.Millisecond` matches PG's
`min_recovery_apply_delay` granularity. Faster (~10 ms) is unnecessary;
slower (>1 s) makes the M0102 tests slow.

### Signal-file location

Match upstream exactly: `<datadir>/promote.signal`. Do **not** use
`pg_wal/promote.signal` or any goopg-specific path.

## Files to create / modify

| File | Change |
|---|---|
| `cmd/goopg/standby.go` | Add `promoteSignalWatcher` goroutine + start/stop wiring |
| `cmd/goopg/standby_test.go` | New test: write `promote.signal`, assert standby promotes within 1 s |
| `docs/design/0005-0005-promotion.md` | Cross-reference update: M0102-0004 added file-trigger path |

## Verification

```bash
# Manual
./bin/goopg start -D /tmp/standby & STANDBY=$!
touch /tmp/standby/promote.signal
sleep 1
./postgres/local_install/bin/psql -h 127.0.0.1 -p <port> -c "SELECT pg_is_in_recovery();"
# expect: f

# E2E via pg_ctl (will be used by M0102-0006/0007 tests)
./postgres/local_install/bin/pg_ctl promote -D /tmp/standby
```

Unit test in `cmd/goopg/standby_test.go`: spin up `standbyController` in a
goroutine, drop `promote.signal`, observe `rt.Standby` flip to `false` within
the test timeout.

## Risks

- **File-race on shared filesystem (NFS).** Unlikely in M0102 tests (all
  local tmpfs). Upstream uses the same approach with the same caveat.
- **Stale signal file from a previous run.** If a crashed promote left a
  `promote.signal` behind, the next standby start will immediately promote.
  Mitigation: at standby init time, log a warning and remove any pre-existing
  `promote.signal` so the operator must re-trigger intentionally. Mirror PG's
  behaviour here if it differs (verify `StartupXLOG` initialisation).
- **Interaction with `goopg promote` subcommand.** Both must converge on the
  same `Promote(ctx)`. The `promoteOnce` guard ensures correctness; document
  it.
