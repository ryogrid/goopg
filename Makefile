# Makefile — convenience targets for the goopg lifecycle.
#
# Typical flow:
#   make build      # compile ./bin/goopg
#   make init       # create a data directory under $(DATA_DIR)
#   make start      # launch the server in the background, logging to $(LOG_FILE)
#   make psql       # open psql against the running server
#   make stop       # graceful shutdown
#   make clean      # remove the data directory and the bin
#
# All targets prepend $(PG_BIN_DIR) to PATH and $(PG_LIB_DIR) to
# LD_LIBRARY_PATH so the in-tree PostgreSQL client tools (psql, pg_ctl, …)
# under postgres/local_install/ are used without polluting the operator's
# global environment.

REPO_ROOT     := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
PG_BIN_DIR    := $(REPO_ROOT)/postgres/local_install/bin
PG_LIB_DIR    := $(REPO_ROOT)/postgres/local_install/lib

DATA_DIR      ?= $(REPO_ROOT)/tmp/goopg-data
LISTEN        ?= 127.0.0.1:5432
HBA_FILE      ?=
LOG_FILE      ?= $(REPO_ROOT)/tmp/goopg.log
PID_FILE      := $(DATA_DIR)/postmaster.pid
GOOPG_BIN     := $(REPO_ROOT)/bin/goopg

# Memory-cap wrapper (OOM containment — see scripts/goopg-test-run.sh and the
# "Memory-capped execution" section of .ralph/AGENT.md). Confines a goopg run
# to a cgroup v2 scope so a runaway query is SIGKILLed inside the scope instead
# of tripping the system-wide OOM killer, which on WSL2 can take down the VM.
# Override the limits per invocation, e.g. 'make start GOOPG_MEM_MAX=16G'.
TEST_RUN            := $(REPO_ROOT)/scripts/goopg-test-run.sh
GOOPG_MEM_HIGH      ?= 20G
GOOPG_MEM_MAX       ?= 24G
GOOPG_MEM_SWAP_MAX  ?= 0
export GOOPG_MEM_HIGH GOOPG_MEM_MAX GOOPG_MEM_SWAP_MAX

PSQL_HOST     := $(firstword $(subst :, ,$(LISTEN)))
PSQL_PORT     := $(word 2,$(subst :, ,$(LISTEN)))
PSQL_DBNAME   ?= postgres
PSQL_USER     ?= postgres

# Wrap shell invocations with the in-tree PostgreSQL paths.
ENV_PREFIX = PATH="$(PG_BIN_DIR):$$PATH" LD_LIBRARY_PATH="$(PG_LIB_DIR):$$LD_LIBRARY_PATH"

.PHONY: help build init start goopg-test-server stop restart psql status clean clean-data print-env install-hooks ralph-state-check ralph-state-repair ralph-state-guard ralph-metrics bench-build bench-build-optimized pgo-profile pgbench-compare pgbench-compare-matrix pgbench-compare-report plan-snapshot-build plan-snapshot-capture plan-diff plan-gate runtimeshim-matrix race-gate parity-dashboard nightly-batch

help:
	@echo "goopg lifecycle targets:"
	@echo "  make build           Build ./bin/goopg from ./cmd/goopg."
	@echo "  make init            Initialize data directory at DATA_DIR."
	@echo "  make start           Start the server in the background, memory-capped (logs to LOG_FILE)."
	@echo "  make goopg-test-server  Run the server in the foreground inside the memory-cap cgroup scope."
	@echo "  make stop            Send graceful shutdown to the server at DATA_DIR."
	@echo "  make restart         Stop then start."
	@echo "  make psql            Connect to the running server with the in-tree psql."
	@echo "  make status          Show whether a postmaster.pid exists in DATA_DIR."
	@echo "  make clean-data      Remove DATA_DIR (after stopping)."
	@echo "  make clean           Remove DATA_DIR and the goopg binary."
	@echo "  make print-env       Print the PATH/LD_LIBRARY_PATH this Makefile uses."
	@echo "  make install-hooks   Enable the committed git hooks (.githooks): pgbench CI-parity pre-commit gate."
	@echo "  make ralph-state-check  Validate .ralph/status.json and .ralph/progress.json consistency."
	@echo "  make ralph-state-repair Attempt safe auto-repair for .ralph/status.json and .ralph/progress.json."
	@echo "  make ralph-state-guard  Check Ralph state, auto-repair if needed, then verify again."
	@echo "  make runtimeshim-matrix Run internal/runtimeshim tests under every Go toolchain in PATH."
	@echo "  make pgbench-compare    Run pgbench comparison between goopg and PostgreSQL."
	@echo "  make pgbench-compare-matrix Run the full goopg pgbench matrix survey."
	@echo "  make pgbench-compare-report Generate markdown report from latest pgbench results."
	@echo "  make race-gate          Run concurrency-critical packages under -race (Go data race detector)."
	@echo "  make plan-gate          Diff EXPLAIN plans against latest baseline; SKIP when unavailable."
	@echo "  make parity-dashboard   Generate docs/parity-dashboard.md (GUC/SQLSTATE/catalog parity vs PG 18.3)."
	@echo
	@echo "  scripts/pg-oracle-diff.sh   Run SQL against goopg AND vanilla PG 18.3, diff output."
	@echo "  scripts/pg-regress-runner.sh  Run upstream regress .sql tests against goopg, report parity %."
	@echo
	@echo "Variables (override with 'make VAR=value'):"
	@echo "  DATA_DIR=$(DATA_DIR)"
	@echo "  LISTEN=$(LISTEN)"
	@echo "  HBA_FILE=$(HBA_FILE)"
	@echo "  LOG_FILE=$(LOG_FILE)"
	@echo "  PSQL_DBNAME=$(PSQL_DBNAME)"
	@echo "  PSQL_USER=$(PSQL_USER)"

# M0098-0007: default build uses GOAMD64=v3 and PGO (when default.pgo
# exists) so every binary — development and benchmark — benefits from
# the same optimisation tier. Override with GOAMD64=v1 for portability.
GOAMD64 ?= v3

build:
	@mkdir -p "$(REPO_ROOT)/bin"
	@if [ -f "$(REPO_ROOT)/default.pgo" ]; then \
		GOAMD64=$(GOAMD64) go build -pgo="$(REPO_ROOT)/default.pgo" -o "$(GOOPG_BIN)" ./cmd/goopg; \
	else \
		GOAMD64=$(GOAMD64) go build -o "$(GOOPG_BIN)" ./cmd/goopg; \
	fi

init: build
	@mkdir -p "$(dir $(DATA_DIR))"
	"$(GOOPG_BIN)" init -D "$(DATA_DIR)"

start: build
	@if [ -f "$(PID_FILE)" ]; then \
		echo "goopg start: $(PID_FILE) already exists; run 'make stop' first." >&2; \
		exit 1; \
	fi
	@mkdir -p "$(dir $(LOG_FILE))"
	@echo "Starting goopg on $(LISTEN) (memory-capped: MemoryMax=$(GOOPG_MEM_MAX), swap=$(GOOPG_MEM_SWAP_MAX)); log: $(LOG_FILE)"
	@$(ENV_PREFIX) GOOPG_CG_UNIT=goopg-server nohup "$(TEST_RUN)" "$(GOOPG_BIN)" start -D "$(DATA_DIR)" --listen "$(LISTEN)" \
		$(if $(HBA_FILE),--hba "$(HBA_FILE)") \
		>"$(LOG_FILE)" 2>&1 &
	@for i in $$(seq 1 50); do \
		if [ -f "$(PID_FILE)" ]; then \
			echo "goopg start: postmaster.pid is up"; \
			exit 0; \
		fi; \
		sleep 0.1; \
	done; \
	echo "goopg start: timed out waiting for $(PID_FILE); see $(LOG_FILE)" >&2; \
	exit 1

# goopg-test-server: bring up goopg in the FOREGROUND inside the memory-cap
# cgroup scope. Use this (or 'make start', which is also capped) whenever you
# start a server to test or benchmark against, so a runaway query is killed
# inside the scope rather than OOM-ing the host. Stop with Ctrl-C, or from
# another shell with 'goopg stop -D $(DATA_DIR)'.
goopg-test-server: build
	@mkdir -p "$(dir $(DATA_DIR))"
	@echo "Starting capped goopg on $(LISTEN) (MemoryMax=$(GOOPG_MEM_MAX), swap=$(GOOPG_MEM_SWAP_MAX)); Ctrl-C or 'goopg stop -D $(DATA_DIR)' to stop"
	@$(ENV_PREFIX) GOOPG_CG_UNIT=goopg-test-server "$(TEST_RUN)" "$(GOOPG_BIN)" start -D "$(DATA_DIR)" --listen "$(LISTEN)" \
		$(if $(HBA_FILE),--hba "$(HBA_FILE)")

stop:
	@if [ ! -f "$(PID_FILE)" ]; then \
		echo "goopg stop: no postmaster.pid at $(PID_FILE); nothing to do."; \
		exit 0; \
	fi
	"$(GOOPG_BIN)" stop -D "$(DATA_DIR)"

restart: stop start

psql:
	@$(ENV_PREFIX) psql -h "$(PSQL_HOST)" -p "$(PSQL_PORT)" -U "$(PSQL_USER)" -d "$(PSQL_DBNAME)"

status:
	@if [ -f "$(PID_FILE)" ]; then \
		echo "goopg: running (postmaster.pid present at $(PID_FILE))"; \
		cat "$(PID_FILE)"; \
	else \
		echo "goopg: not running (no $(PID_FILE))"; \
	fi

clean-data:
	@if [ -f "$(PID_FILE)" ]; then \
		echo "clean-data: server is running; run 'make stop' first." >&2; \
		exit 1; \
	fi
	rm -rf "$(DATA_DIR)"

clean: clean-data
	rm -f "$(GOOPG_BIN)"

print-env:
	@echo "PATH=$(PG_BIN_DIR):\$$PATH"
	@echo "LD_LIBRARY_PATH=$(PG_LIB_DIR):\$$LD_LIBRARY_PATH"

# install-hooks: point git at the committed .githooks directory so the
# pre-commit gate (CI-parity pgbench smoke) runs on every commit. Run once per
# clone — core.hooksPath is local git config, not tracked, so the committed
# hook + this target make enforcement reproducible without per-dev setup drift.
# Idempotent.
install-hooks:
	git config core.hooksPath .githooks
	chmod +x .githooks/pre-commit
	@echo "install-hooks: core.hooksPath -> $$(git config --get core.hooksPath); pre-commit gate active"

ralph-state-check:
	go run ./cmd/validate-ralph-state -status .ralph/status.json -progress .ralph/progress.json

ralph-state-repair:
	go run ./cmd/validate-ralph-state -status .ralph/status.json -progress .ralph/progress.json -fix

ralph-state-guard:
	@set -e; \
	if go run ./cmd/validate-ralph-state -status .ralph/status.json -progress .ralph/progress.json; then \
		echo "ralph-state-guard: already consistent"; \
	else \
		echo "ralph-state-guard: inconsistency detected, attempting safe repair"; \
		go run ./cmd/validate-ralph-state -status .ralph/status.json -progress .ralph/progress.json -fix; \
		go run ./cmd/validate-ralph-state -status .ralph/status.json -progress .ralph/progress.json; \
	fi

# Loop-health metrics (kaizen T1): free pipeline pass over the loop history.
# Prints success rate, cost, cache-read, status-block coverage, permission
# denials, and the failure breakdown. Nobody was watching these while the
# success rate sat at 29%. Read-only; no LLM.
ralph-metrics:
	@cd analysis/ralph-loop-kaizen/pipeline && \
		python3 extract_telemetry.py --out data >/dev/null && \
		python3 extract_telemetry.py --classify-failures --out data >/dev/null && \
		python3 assemble_corpus.py --out data >/dev/null && \
		python3 metrics_report.py

# M0107-0008 per-Go-minor maintenance: verify //go:linkname targets in
# internal/runtimeshim against every Go toolchain present in PATH (the
# default `go` plus any `go1.N`-style binaries installed via
# `go install golang.org/dl/go1.N@latest`). See
# docs/design/perf-optimize/08-runtime-internals.md §8.
runtimeshim-matrix:
	@bash scripts/runtimeshim_go_matrix.sh

# ---------------------------------------------------------------
# M0075-0007: build-toolchain optimisation targets.
#
# The bench flow uses tmp/goopg-bench-bin as the binary the
# tpch-runner connects against. `bench-build` produces the
# unoptimised default; `bench-build-optimized` adds:
#   - PGO via -pgo=default.pgo (Go 1.21+ GA)
#   - GOAMD64=v3 (AVX2/BMI2/FMA — verify CPU support before
#     enabling; default v3, override with `make GOAMD64=v1 ...`)
#   - -ldflags="-s -w" to strip DWARF + symbol table (smaller
#     binary; better i-cache locality; pprof unaffected)
#   - -trimpath for reproducible builds
#
# `pgo-profile` captures default.pgo from a TPC-H mixed workload
# (Q1+Q3+Q12+Q13+Q21) against an already-running goopg server.
# Run `make bench-build && nohup tmp/goopg-bench-bin start ...`
# in another terminal first; this target captures while the
# tpch-runner is driving load.
# ---------------------------------------------------------------

bench-build:
	@mkdir -p "$(REPO_ROOT)/tmp"
	go build -o "$(REPO_ROOT)/tmp/goopg-bench-bin" ./cmd/goopg

# Optimised bench build: PGO if default.pgo present, GOAMD64=v3,
# linker strip, trimpath. Fall back gracefully when default.pgo
# is missing.
bench-build-optimized:
	@mkdir -p "$(REPO_ROOT)/tmp"
	@if [ -f "$(REPO_ROOT)/default.pgo" ]; then \
		echo "Building optimised binary with PGO (default.pgo) + GOAMD64=$(GOAMD64) + -ldflags='-s -w' + -trimpath"; \
		GOAMD64=$(GOAMD64) go build \
			-pgo="$(REPO_ROOT)/default.pgo" \
			-ldflags="-s -w" \
			-trimpath \
			-o "$(REPO_ROOT)/tmp/goopg-bench-bin" \
			./cmd/goopg; \
	else \
		echo "Building optimised binary WITHOUT PGO (default.pgo not found) + GOAMD64=$(GOAMD64) + -ldflags='-s -w' + -trimpath"; \
		echo "  Run 'make pgo-profile' first to capture default.pgo for the full optimisation."; \
		GOAMD64=$(GOAMD64) go build \
			-ldflags="-s -w" \
			-trimpath \
			-o "$(REPO_ROOT)/tmp/goopg-bench-bin" \
			./cmd/goopg; \
	fi
	@ls -la "$(REPO_ROOT)/tmp/goopg-bench-bin"

# Capture default.pgo from a TPC-H mixed workload. Requires
# the bench server to be running (run `make bench-build` then
# start the server manually with the TPC-H runtime data
# directory). Captures over 480 s while the tpch-runner drives
# Q1+Q3+Q12+Q13+Q21 sequentially.
pgo-profile:
	@if ! curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:6060/debug/pprof/ 2>/dev/null | grep -q 200; then \
		echo "pgo-profile: pprof endpoint at 127.0.0.1:6060 not reachable. Start the bench server first." >&2; \
		exit 1; \
	fi
	@echo "Capturing PGO profile (480 s) into default.pgo while running Q1+Q3+Q12+Q13+Q21..."
	curl -s -o "$(REPO_ROOT)/default.pgo" \
		"http://127.0.0.1:6060/debug/pprof/profile?seconds=480" &
	@sleep 1
	"$(REPO_ROOT)/tpch-runner" --queries=1,3,12,13,21 \
		--per-query-timeout=620s --cancel-after=600s
	@wait
	@echo "Profile captured: default.pgo"
	@ls -la "$(REPO_ROOT)/default.pgo"

# ---------------------------------------------------------------
# pgbench Performance Comparison
#
# Runs pgbench comparison between goopg and PostgreSQL with:
#   - 3 workloads: standard, simple-update, select-only
#   - 100 clients, 100 threads, scale factor 100
#   - 3 minutes per test
#   - Alternating execution between systems
#   - Shared buffers: 2.5GB, WAL buffers: 100MB
#   - Checkpoint settings to prevent interruption
#
# Uses separate ports (5433 for goopg, 5434 for PostgreSQL)
# to avoid conflicts with existing instances.
#
# Preconditions:
#   - `make build` is run automatically by this target.
#   - Upstream PostgreSQL client/server tools must already exist under
#     `postgres/local_install/`:
#       `bin/{initdb,pg_ctl,psql,pgbench}` and matching shared libraries in
#       `lib/`.
#   - Benchmark clusters are created under `tmp/pgbench-compare/` on first run;
#     old clusters under `bench/pgbench-compare/` are not reused.
# ---------------------------------------------------------------

pgbench-compare: build
	@echo "Running pgbench performance comparison..."
	@"$(REPO_ROOT)/bench/pgbench-compare/run_comparison.sh"
	@echo ""
	@echo "Generating report..."
	@"$(REPO_ROOT)/bench/pgbench-compare/generate_report.sh"

pgbench-compare-matrix: build
	@echo "Running pgbench matrix validation..."
	@"$(REPO_ROOT)/bench/pgbench-compare/run_matrix.sh"

pgbench-compare-report:
	@"$(REPO_ROOT)/bench/pgbench-compare/generate_report.sh"

# ---------------------------------------------------------------
# M0076-0006: plan-snapshot regression harness.
#
# Capture EXPLAIN output for each TPC-H query against a
# running goopg cluster, compare against a saved baseline
# in plan_snapshots/<label>.txt. Provides fast feedback
# (~30s) for planner-only iterations vs the full 21-q
# sweep cost (~25min).
#
# Three equality modes (default: structural):
#   structural    — strips (rows=N) cost annotations;
#                   ignores cost variance.
#   strict-text   — byte-for-byte comparison.
#   semantic-cost — structural + cost ±10% tolerance.
#
# Usage:
#   make plan-snapshot-capture LABEL=m0076-baseline-ffc3429
#   make plan-diff             LABEL=m0076-baseline-ffc3429
#   make plan-diff             LABEL=m0076-baseline-ffc3429 MODE=strict-text
#
# Requires goopg-bench-bin running on 127.0.0.1:65433
# (the standard tpch-runner port).
# ---------------------------------------------------------------

PLAN_SNAPSHOT_BIN := $(REPO_ROOT)/tmp/plan-snapshot
PLAN_HOST    ?= 127.0.0.1
PLAN_PORT    ?= 65433
PLAN_DB      ?= tpch
PLAN_USER    ?= tpch
PLAN_PASS    ?= tpch
LABEL        ?=
MODE         ?= structural

plan-snapshot-build:
	@mkdir -p "$(REPO_ROOT)/tmp"
	go build -o "$(PLAN_SNAPSHOT_BIN)" ./cmd/plan-snapshot

plan-snapshot-capture: plan-snapshot-build
	@if [ -z "$(LABEL)" ]; then \
		echo "make plan-snapshot-capture requires LABEL=<name>"; exit 2; \
	fi
	"$(PLAN_SNAPSHOT_BIN)" capture --label "$(LABEL)" \
		--host "$(PLAN_HOST)" --port $(PLAN_PORT) \
		--db "$(PLAN_DB)" --user "$(PLAN_USER)" --password "$(PLAN_PASS)"

plan-diff: plan-snapshot-build
	@if [ -z "$(LABEL)" ]; then \
		echo "make plan-diff requires LABEL=<name>"; exit 2; \
	fi
	"$(PLAN_SNAPSHOT_BIN)" diff --label "$(LABEL)" --mode "$(MODE)" \
		--host "$(PLAN_HOST)" --port $(PLAN_PORT) \
		--db "$(PLAN_DB)" --user "$(PLAN_USER)" --password "$(PLAN_PASS)"

# ---------------------------------------------------------------
# plan-gate: run plan-diff against the latest baseline if one
# exists and the TPC-H server is available.  Used as a pre-commit
# gate for planner/executor changes.  Exits SKIP (0) when there is
# no data or no baseline so it never hard-blocks loops without data.
# ---------------------------------------------------------------
plan-gate: plan-snapshot-build
	@LATEST=$$(ls -t "$(REPO_ROOT)/plan_snapshots"/*.txt 2>/dev/null | head -1); \
	if [ -z "$$LATEST" ]; then \
		echo "plan-gate: SKIPPED (no plan_snapshots/*.txt baseline found)"; \
		exit 0; \
	fi; \
	if ! pg_isready -h "$(PLAN_HOST)" -p $(PLAN_PORT) -U "$(PLAN_USER)" -q 2>/dev/null; then \
		echo "plan-gate: SKIPPED (goopg not reachable on $(PLAN_HOST):$(PLAN_PORT) — start the bench server first)"; \
		exit 0; \
	fi; \
	LNAME=$$(basename "$$LATEST" .txt); \
	echo "plan-gate: diffing against baseline $$LNAME (mode=$(MODE))"; \
	"$(PLAN_SNAPSHOT_BIN)" diff --label "$$LNAME" --mode "$(MODE)" \
		--host "$(PLAN_HOST)" --port $(PLAN_PORT) \
		--db "$(PLAN_DB)" --user "$(PLAN_USER)" --password "$(PLAN_PASS)"

# ---------------------------------------------------------------
# race-gate: run concurrency-critical packages under -race to catch
# data races specific to goopg's thread-parallel architecture (as
# opposed to PG's process-parallel design where data races aren't
# possible between backends).
#
# Packages included: lock manager, MVCC/transaction state, storage
# (shared buffer pool), async I/O, WAL, B-tree access, autovacuum,
# activity tracking.  Excluded: packages requiring a live server
# (testport, server, testutil/cluster*) which are covered by the
# integration test suite.
#
# RACE_TIMEOUT defaults to 15m (race tests run ~2–3× slower than
# normal due to shadow memory overhead).
# ---------------------------------------------------------------
RACE_TIMEOUT ?= 15m
RACE_EXCLUDE = internal/testport|internal/server|internal/testutil/cluster|internal/testutil/replcluster|internal/testutil/pgcluster|internal/testutil/pubsubcluster|internal/testutil/tpch|/bench/

race-gate:
	@echo "race-gate: collecting packages (excluding server/cluster/bench)..."
	@PKGS=$$(go list ./... | grep -vE "$(RACE_EXCLUDE)"); \
	if [ -z "$$PKGS" ]; then \
		echo "race-gate: no packages selected" >&2; exit 1; \
	fi; \
	echo "race-gate: running go test -race on $$(echo "$$PKGS" | wc -l) packages (timeout $(RACE_TIMEOUT))..."; \
	go test -race -timeout $(RACE_TIMEOUT) $$PKGS

# ---------------------------------------------------------------
# parity-dashboard: diff PG 18.3's GUC list, SQLSTATE codes, and
# system catalog object names against goopg's implementations.
# Writes docs/parity-dashboard.md.
# ---------------------------------------------------------------
parity-dashboard:
	@bash "$(REPO_ROOT)/scripts/gen-parity-dashboard.sh"

# ---------------------------------------------------------------
# nightly-batch: run the whole-suite nightly regression batch
# (ci/batch/run-nightly.sh; design in ci/design/).  Single manual
# entrypoint; the scheduled firing uses the same script and the
# same run lock, so overlaps exit 5 immediately.
# ---------------------------------------------------------------
nightly-batch:
	@bash "$(REPO_ROOT)/ci/batch/run-nightly.sh"
