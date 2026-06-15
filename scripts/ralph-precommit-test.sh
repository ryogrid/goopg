#!/usr/bin/env bash
#
# ralph-precommit-test.sh — pre-commit gate for the Ralph agent. It mirrors
# the test-relevant steps of .github/workflows/test.yml so that what the loop
# commits is what CI would accept. Two parts run in order:
#
#   1. Unit/component Go suite — the "Run unit and component tests" CI step:
#      the whole module minus the cluster-backed packages that need a live
#      goopg/PostgreSQL server (those are exercised by other CI steps).
#
#   2. pgbench smoke — the CI pgbench steps: build goopg, init a throwaway
#      data directory, start the server, load the pgbench tables, then run the
#      standard / simple-update / select-only workloads against it.
#
# CI-only concerns (repo checkout, submodule git handling, Go toolchain + apt
# install) are intentionally omitted. Any failing/flaky test or a non-zero
# pgbench/server step fails the whole script (set -e), so the gate is red.
#
# Excluded packages in part 1 (kept in sync with the CI EXCLUDE list):
#   internal/testport               — full port suite, cluster-backed
#   internal/server                 — starts a goopg instance per test
#   internal/testutil/cluster       — crash recovery needs a cluster
#   internal/testutil/replcluster   — multi-node replication
#   internal/testutil/pgcluster     — upstream PostgreSQL cluster
#   internal/testutil/pubsubcluster — pub/sub multi-node
#   internal/testutil/tpch          — TPC-H parity against PostgreSQL
#   /bench/                         — benchmarks; smoke test run separately
#
# Overridable via env: RALPH_PRECOMMIT_PGPORT (default 5535, kept off the
# standard 5432 and off Ralph's 5433/5434 + perf 5533/5534 to avoid clashing
# with a server a concurrent loop may have running).
set -euo pipefail

# Always operate from the repository root, regardless of caller cwd, so
# `go list ./...` covers the whole module and relative paths resolve.
cd "$(dirname "$0")/.."

# Scope selector. "full" (default) runs the unit/component suite (Part 1) AND
# the pgbench smoke (Part 2). "smoke" runs ONLY the pgbench smoke. The
# .githooks/pre-commit hook uses "smoke" so EVERY commit pays the ~2-3 min
# pgbench cost — the CI-parity workload the Ralph loop was otherwise blind to
# (it only ran targeted `go test`, never pgbench, so the TPC-B concurrency
# regression class slipped straight through to CI). The ~10 min unit suite is
# still covered by CI and explicit agent runs, so the hook skips it to keep
# per-commit latency acceptable. Override: RALPH_PRECOMMIT_SCOPE=smoke|full.
SCOPE="${RALPH_PRECOMMIT_SCOPE:-full}"

# --------------------------------------------------------------------------- #
# Part 1 — unit/component Go suite (full scope only)
# --------------------------------------------------------------------------- #
if [ "$SCOPE" = "smoke" ]; then
  echo "ralph-precommit-test.sh: SCOPE=smoke — skipping Part 1 (unit/component suite); running pgbench smoke only"
else

# Keep this pattern in sync with the EXCLUDE list in
# .github/workflows/test.yml ("Run unit and component tests").
EXCLUDE='internal/testport|internal/server|internal/testutil/cluster|internal/testutil/replcluster|internal/testutil/pgcluster|internal/testutil/pubsubcluster|internal/testutil/tpch|/bench/'

# Build the package list on its own line so a `go list` failure (e.g. a
# package that fails to compile) is caught by `set -e`/`pipefail` instead of
# being swallowed inside a command substitution and yielding an empty,
# falsely-green run.
pkgs=$(go list ./... | grep -vE "$EXCLUDE")
if [ -z "$pkgs" ]; then
  echo "ralph-precommit-test.sh: no packages selected — refusing to report a green run" >&2
  exit 1
fi

go test -timeout 10m $pkgs

# --------------------------------------------------------------------------- #
# Part 1b — optional race detector pass (RALPH_PRECOMMIT_RACE=1 to enable)
#
# Runs the same package set under -race to catch data races specific to
# goopg's thread-parallel architecture.  Off by default because it adds
# ~5–10 min and is most valuable after changes to lock/mvcc/storage.
# Enable with: RALPH_PRECOMMIT_RACE=1 scripts/ralph-precommit-test.sh
# --------------------------------------------------------------------------- #
if [ "${RALPH_PRECOMMIT_RACE:-0}" = "1" ]; then
  echo "ralph-precommit-test.sh: Part 1b — race detector pass (RALPH_PRECOMMIT_RACE=1)..."
  go test -race -timeout 15m $pkgs
  echo "ralph-precommit-test.sh: race pass PASS"
fi

fi  # end Part 1 (skipped when SCOPE=smoke)

# --------------------------------------------------------------------------- #
# Part 2 — pgbench smoke against a live goopg server
#
# Mirrors the test.yml steps: build goopg, init a data dir, start the server,
# `pgbench -i` (load), then the standard / -N / -S workloads.
# --------------------------------------------------------------------------- #

# Pick a listen port that is actually free. A fixed port is unsafe here: a
# stray goopg/PostgreSQL left over from a crashed run (or a concurrent loop) may
# already hold it, in which case our `pg_isready` wait would connect to the
# WRONG server and the gate would falsely pass (pgbench against the stray) or
# fail confusingly ("could not read postmaster.pid"). So probe from the
# requested port upward and use the first free one.
port_in_use() {
  local p="$1"
  if command -v ss >/dev/null 2>&1; then
    # Compare the port field (last colon-separated token) of each LISTEN
    # socket's local address, so 127.0.0.1:P / *:P / [::]:P all match P.
    ss -ltn 2>/dev/null | awk -v port="$p" \
      'NR>1 { n=split($4,a,":"); if (a[n]==port) found=1 } END { exit(found?0:1) }' \
      && return 0
    return 1
  fi
  # Fallback when ss is unavailable: a successful TCP connect => in use.
  if (exec 3<>"/dev/tcp/127.0.0.1/$p") >/dev/null 2>&1; then
    exec 3>&- 3<&- 2>/dev/null || true
    return 0
  fi
  return 1
}

REQ_PORT="${RALPH_PRECOMMIT_PGPORT:-5535}"
PORT=""
for cand in $(seq "$REQ_PORT" $((REQ_PORT + 50))); do
  if ! port_in_use "$cand"; then PORT="$cand"; break; fi
done
if [ -z "$PORT" ]; then
  echo "ralph-precommit-test.sh: no free port in [$REQ_PORT, $((REQ_PORT + 50))]" >&2
  exit 1
fi
if [ "$PORT" != "$REQ_PORT" ]; then
  echo "ralph-precommit-test.sh: port $REQ_PORT busy; using free port $PORT instead"
fi

DATADIR="tmp/ralph-precommit-goopg-data"
LOGFILE="tmp/ralph-precommit-goopg.log"
CG_UNIT="ralph-precommit-goopg"

# Prefer the project's pinned PG 18.3 client tools (pgbench / pg_isready) under
# postgres/local_install/bin when present — that is the compatibility oracle
# this repo standardises on (see .ralph/PROMPT.md). Fall back to whatever is on
# PATH (as CI does, where postgresql-client is apt-installed). The matching
# lib dir must lead LD_LIBRARY_PATH too: these binaries link libpq.so.5 and
# pick up an older system libpq otherwise (symbol-lookup error at startup).
if [ -x postgres/local_install/bin/pgbench ]; then
  PATH="$PWD/postgres/local_install/bin:$PATH"
  export LD_LIBRARY_PATH="$PWD/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
fi
for tool in pgbench pg_isready; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "ralph-precommit-test.sh: required tool '$tool' not found on PATH or in postgres/local_install/bin" >&2
    exit 1
  fi
done

# server_pid is the real goopg PID (read from postmaster.pid), not the cap
# wrapper / shell job, so the teardown SIGKILLs the server itself.
server_pid=""
cleanup() {
  status=$?
  # On a red gate, surface the server log (as CI does on failure) before we
  # tear everything down — otherwise the cause is lost with the data dir.
  if [ "$status" -ne 0 ] && [ -f "$LOGFILE" ]; then
    echo "ralph-precommit-test.sh: gate failed (exit $status); tail of $LOGFILE:" >&2
    tail -n 40 "$LOGFILE" >&2 || true
  fi
  # Force-terminate the goopg server we started, no matter how the script
  # exits (pgbench failure, Ctrl-C, or success). kill -KILL guarantees the
  # server is gone even if a graceful stop would hang, so no orphan server
  # lingers on the port for the next gate run.
  if [ -n "$server_pid" ] && kill -0 "$server_pid" 2>/dev/null; then
    kill -KILL "$server_pid" 2>/dev/null || true
  fi
  # Stop the transient memory-cap scope by name so its unit is reliably freed
  # for the next run (the GOOPG_CG_UNIT name is reused, and systemd refuses to
  # start a scope whose name is still in use). --collect usually reaps it once
  # the cgroup is empty, but doing it explicitly removes the race. No-op when
  # the run was uncapped (no scope) or systemctl is unavailable.
  if command -v systemctl >/dev/null 2>&1; then
    systemctl --user stop "${CG_UNIT}.scope" 2>/dev/null || true
    systemctl --user reset-failed "${CG_UNIT}.scope" 2>/dev/null || true
  fi
  # Drop the throwaway data dir so a re-run starts clean.
  rm -rf "$DATADIR"
}
trap cleanup EXIT INT TERM

mkdir -p tmp
rm -rf "$DATADIR"

go build -o bin/goopg ./cmd/goopg

./bin/goopg init -D "$DATADIR"

# Start the server under the memory cap (AGENT.md mandates that any goopg
# server / pgbench run go through scripts/goopg-test-run.sh on this WSL2 box;
# the wrapper degrades to an uncapped run with a warning where cgroup
# delegation is unavailable, e.g. CI).
GOOPG_CG_UNIT="$CG_UNIT" scripts/goopg-test-run.sh \
    ./bin/goopg start -D "$DATADIR" --listen 127.0.0.1:"$PORT" \
    > "$LOGFILE" 2>&1 &

# Wait up to 30 s for the server to accept connections. PORT is passed through
# the environment (PGPORT) rather than interpolated into the bash -c string.
PGPORT="$PORT" timeout 30 bash -c \
  'until pg_isready -h 127.0.0.1 -U postgres -q; do sleep 1; done'

# Capture the real goopg PID for the SIGKILL teardown. By the time the server
# accepts connections it has written postmaster.pid (first line = pid).
server_pid="$(head -n1 "$DATADIR/postmaster.pid" 2>/dev/null || true)"
if [ -z "$server_pid" ]; then
  echo "ralph-precommit-test.sh: could not read goopg pid from $DATADIR/postmaster.pid" >&2
  exit 1
fi

# Load the pgbench tables, then run the three CI workloads.
pgbench -i              -h 127.0.0.1 -p "$PORT" -U postgres postgres
pgbench -T 30 -c 2 -j 2 -P 5    -h 127.0.0.1 -p "$PORT" -U postgres postgres
pgbench -T 30 -c 2 -j 2 -P 5 -N -h 127.0.0.1 -p "$PORT" -U postgres postgres
pgbench -T 30 -c 2 -j 2 -P 5 -S -h 127.0.0.1 -p "$PORT" -U postgres postgres

# --------------------------------------------------------------------------- #
# Part 3 — optional plan-diff gate (RALPH_PRECOMMIT_PLAN_DIFF=1 to enable)
#
# Runs `make plan-gate` which diffs EXPLAIN plans against the latest
# baseline snapshot. Only meaningful for planner/executor changes; off
# by default because it requires the TPC-H bench server on port 65433.
# Enable with: RALPH_PRECOMMIT_PLAN_DIFF=1 scripts/ralph-precommit-test.sh
# --------------------------------------------------------------------------- #
if [ "${RALPH_PRECOMMIT_PLAN_DIFF:-0}" = "1" ]; then
  echo "ralph-precommit-test.sh: Part 3 — plan-diff gate (RALPH_PRECOMMIT_PLAN_DIFF=1)..."
  make plan-gate
  echo "ralph-precommit-test.sh: plan-diff gate PASS"
fi

# Server teardown (kill -KILL) + data-dir cleanup run from the EXIT trap.
