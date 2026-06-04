# Practice card — running / manually testing a goopg server

**Load when** the task starts a goopg server, runs `psql`/`pgbench` against it,
or does TPC-H / perf / oracle / integration runs.

**Why:** these runs repeatedly waste whole loops on the same environment
foot-guns (`goopg_manual_server_test_workflow`,
`concurrent_ralph_loops_corrupt_tree`, `pattern_ralph_isolation_ports_paths`).

## Memory-cap is MANDATORY (WSL2 OOM containment)

A runaway query thrashes swap and trips the system-wide OOM killer (kills the
whole distro). Always go through the cap:

- `make start` / `make goopg-test-server` / `make stop` (already capped), **or**
- wrap manually: `GOOPG_CG_UNIT=goopg-test scripts/goopg-test-run.sh ./bin/goopg start -D <dir> --listen 127.0.0.1:5533`
- each concurrent capped run needs its own `GOOPG_CG_UNIT`.

## Foot-guns

- **`pkill -f goopg` self-matches the Bash shell** running the command (exit
  144). Use `run_in_background` + a PID-file kill instead.
- **Re-init the data dir between runs** — stale state causes misleading results.
- The flag is **`--listen 127.0.0.1:PORT`**, *not* `-p`.
- **Isolation:** use ports **5533/5534** + `tmp/perf-optimize/` to avoid
  colliding with Ralph's own 5433/5434 + `tmp/pgbench-compare/`.

## Concurrency guard

Do **not** run a second ralph loop against the same working tree — two loops
clobber each other's edits and shared `.ralph` state. Check `pgrep -f ralph_loop`
first; if another loop is live, yield.

## Compatibility oracle

Use `psql`/`pgbench` from `./postgres/local_install/{bin,lib}` (PG 18.3) with
`PGPORT`/`PGUSER` set to the goopg instance to compare behavior.
