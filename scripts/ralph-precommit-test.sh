#!/usr/bin/env bash
#
# ralph-precommit-test.sh — run the unit/component Go test suite that the
# "Run unit and component tests" step of .github/workflows/test.yml runs.
#
# This mirrors that CI step exactly, minus the bits that only make sense in
# CI (repository checkout / submodule git handling, Go toolchain + apt
# package install). It is the pre-commit gate the Ralph agent must pass
# before every commit (see .ralph/AGENT.md).
#
# Excluded packages all require a running goopg or PostgreSQL cluster and are
# exercised by other CI steps, so they are filtered out here just as in CI:
#   internal/testport               — full port suite, cluster-backed
#   internal/server                 — starts a goopg instance per test
#   internal/testutil/cluster       — crash recovery needs a cluster
#   internal/testutil/replcluster   — multi-node replication
#   internal/testutil/pgcluster     — upstream PostgreSQL cluster
#   internal/testutil/pubsubcluster — pub/sub multi-node
#   internal/testutil/tpch          — TPC-H parity against PostgreSQL
#   /bench/                         — benchmarks; smoke test run separately
#
# Exit status is the `go test` exit status: 0 = all pass, non-zero = a
# failing (or flaky) package. A build/`go list` error also fails (non-zero),
# so a broken tree never passes the gate green.
set -euo pipefail

# Always operate from the repository root, regardless of caller cwd, so
# `go list ./...` covers the whole module.
cd "$(dirname "$0")/.."

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
