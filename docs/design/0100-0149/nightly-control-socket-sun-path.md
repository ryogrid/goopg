# Control socket vs `sun_path`: the nightly `PgoutputInterop` start failures

Status: landed 2026-09-23 (`0734cab60`). Task: `.ralph/fix_plan.md` M-NIGHTLY
"testport/TestPort_PgoutputInterop\* subscriber/publisher-start failures"
(AI-20260922-004850-006 … -015, AI-20260921-000212-008 … -017). Kind: test-fix.

## Symptom

Two consecutive nightlies failed ten of the twenty `TestPort_PgoutputInterop*`
cases with "subscriber start: start failed; process exited early". The failures
interleaved with passes, were consistently ~0.2 s faster than passes (death at
startup), and never reproduced locally — single case, all ten, or the whole
`internal/testport` package, all green at HEAD. The only evidence (the case's
`cluster.log`) lived in the nightly's throwaway worktree and was deleted with
it, so a previous loop made `cluster.Start` inline the log tail into the error.

## Root cause (from the inlined tail, nightly `20260923-001346`)

Every failing subscriber logged a healthy start through "goopg listener bound",
then:

```
goopg start: control listener: control: listen ".../<case>-sub/.goopg.ctl.sock":
  listen unix ...: invalid argument
```

`sockaddr_un.sun_path` is 108 bytes on Linux and needs the terminating NUL, so
a socket path of 108+ bytes cannot be bound. The nightly runs from
`tmp/nightly-src-<run>/`, which lengthens every case's data directory:

| socket path length | cases |
|---|---|
| 108 | `pg2g-pgb-ins`, `pg2g-pgb-tpc` (fail) |
| 109 | `pg2g-rid-full` (fail) |
| 134–136 | `pgoutput-interop-pg2g-fulldml` / `-batchdml` (fail) |
| ≤ 107 | every passing case |

That explains every observation at once: the interleaving (case-name length,
not order), the fast failures (death at the control-socket bind), and the local
zero hit rate (the repo-root prefix is shorter than the worktree prefix).

## Fix

`control.SocketPathFor(dataDir)` (`internal/access/transam/control/control.go`)
chooses where the server binds:

- `<dataDir>/.goopg.ctl.sock` when the path is ≤ 103 bytes (fits both Linux's
  108-byte and macOS's 104-byte `sun_path`) — every existing cluster keeps its
  layout;
- otherwise `os.TempDir()/goopg-<first 16 hex of sha256(abs dataDir)>.ctl.sock`
  (`/tmp` if even that overflows) — short, deterministic per cluster, distinct
  between clusters.

Clients need no change: `goopg stop|reload|status|checkpoint|promote` already
dial the `SocketPath` line of `postmaster.pid`, which records the chosen path.
Two servers on one data directory are already excluded by the pidfile, so the
fallback name cannot collide. The listener's existing stale-socket unlink and
close-time removal apply to the fallback unchanged.

PostgreSQL has no counterpart — this socket replaces pg_ctl's signal-based
control. For its own client sockets PG refuses an over-long path at startup
(`Unix-domain socket path "…" is too long`); goopg's control socket is an
implementation detail of its CLI, so relocating it is preferable to refusing
to start.

## Verification

- End to end on a 143-byte socket path: a HEAD binary exits with the nightly's
  exact `control listener … invalid argument`; the fixed binary starts, answers
  `goopg status` and a `psql` query, stops, and removes both the fallback
  socket and the pidfile.
- `internal/access/transam/control/socketpath_test.go`: in-dir case kept;
  fallback fits the limit, is deterministic and distinct per data directory;
  a listener bound at the fallback answers PING.
- Gates: units, `tpch-spotcheck`, SF0.25 sweep (plans same=99), two
  `PgoutputInterop` cases.
- The nightly is the final confirmation: the ten cases should pass on the next
  run from the long worktree prefix.
