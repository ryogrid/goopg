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

PSQL_HOST     := $(firstword $(subst :, ,$(LISTEN)))
PSQL_PORT     := $(word 2,$(subst :, ,$(LISTEN)))
PSQL_DBNAME   ?= postgres
PSQL_USER     ?= postgres

# Wrap shell invocations with the in-tree PostgreSQL paths.
ENV_PREFIX = PATH="$(PG_BIN_DIR):$$PATH" LD_LIBRARY_PATH="$(PG_LIB_DIR):$$LD_LIBRARY_PATH"

.PHONY: help build init start stop restart psql status clean clean-data print-env

help:
	@echo "goopg lifecycle targets:"
	@echo "  make build           Build ./bin/goopg from ./cmd/goopg."
	@echo "  make init            Initialize data directory at DATA_DIR."
	@echo "  make start           Start the server in the background (logs to LOG_FILE)."
	@echo "  make stop            Send graceful shutdown to the server at DATA_DIR."
	@echo "  make restart         Stop then start."
	@echo "  make psql            Connect to the running server with the in-tree psql."
	@echo "  make status          Show whether a postmaster.pid exists in DATA_DIR."
	@echo "  make clean-data      Remove DATA_DIR (after stopping)."
	@echo "  make clean           Remove DATA_DIR and the goopg binary."
	@echo "  make print-env       Print the PATH/LD_LIBRARY_PATH this Makefile uses."
	@echo
	@echo "Variables (override with 'make VAR=value'):"
	@echo "  DATA_DIR=$(DATA_DIR)"
	@echo "  LISTEN=$(LISTEN)"
	@echo "  HBA_FILE=$(HBA_FILE)"
	@echo "  LOG_FILE=$(LOG_FILE)"
	@echo "  PSQL_DBNAME=$(PSQL_DBNAME)"
	@echo "  PSQL_USER=$(PSQL_USER)"

build:
	@mkdir -p "$(REPO_ROOT)/bin"
	go build -o "$(GOOPG_BIN)" ./cmd/goopg

init: build
	@mkdir -p "$(dir $(DATA_DIR))"
	"$(GOOPG_BIN)" init -D "$(DATA_DIR)"

start: build
	@if [ -f "$(PID_FILE)" ]; then \
		echo "goopg start: $(PID_FILE) already exists; run 'make stop' first." >&2; \
		exit 1; \
	fi
	@mkdir -p "$(dir $(LOG_FILE))"
	@echo "Starting goopg on $(LISTEN); log: $(LOG_FILE)"
	@$(ENV_PREFIX) nohup "$(GOOPG_BIN)" start -D "$(DATA_DIR)" --listen "$(LISTEN)" \
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
