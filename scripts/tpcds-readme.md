# TPC-DS SF=1 Benchmark on goopg — Workflow

## Prerequisites

```bash
# 1. Clone the TPC-DS toolkit (if not already done)
git clone https://github.com/celuk/tpcds-postgres.git third-party/tpcds-postgres

# 2. Python 3 available (for data conversion and query splitting)
python3 --version
```

## State machine

```
State 1                   State 2                       State 3
(clean clone)             (tools + data + queries)      (DB loaded + analyzed)
     │                         │                            │
     ├─ tpcds-setup.sh ───────►│                            │
     │                         ├─ tpcds-load.sh ───────────►│
     │                         │                            ├─ tpcds-run.sh ──► results
     │                         │                            │
     ▼                         ▼                            ▼
  third-party/             bench/tpch/runtime_goopg/     bench/tpch/runtime_goopg/
  tpcds-postgres/          tpcds-data/                   tpcds-results/
  (repo cloned)            ├── *.tsv (COPY-ready)        └── results.txt
                           └── queries/
                               └── query*.sql (PG-fixed)
```

## Step-by-step

### State 1 → State 2: Setup

```bash
# One-time: compile TPC-DS toolkit, generate SF=1 data,
# convert to COPY-ready TSV, generate & fix 99 queries
scripts/tpcds-setup.sh
```

**What it does:**
1. Compiles `dsdgen` + `dsqgen` (with `-fcommon` for GCC>=10)
2. Generates SF=1 `.dat` files (pipe-delimited) via `dsdgen`
3. Converts each `.dat` → `.tsv` (tab-delimited, `\N` for NULL) via `convert_tpcds.py`
4. Fixes `customer.dat` UTF-8 encoding corruption
5. Generates `query_0.sql` (all 99 queries) via `dsqgen` (netezza dialect)
6. Splits into `query1.sql`..`query99.sql` via `tpcds_split_queries.py`
7. Applies PG compatibility fixes:
   - `N days` → `INTERVAL 'N days'` (netezza → PG interval syntax)
   - Query 30: `c_last_review_date_sk` → `c_last_review_date`
   - Queries 36, 70, 86: wrap in `SELECT * FROM (...) AS sub`

**Output:**
- `bench/tpch/runtime_goopg/tpcds-data/*.tsv` (25 COPY-ready files)
- `bench/tpch/runtime_goopg/tpcds-data/queries/query*.sql` (99 PG-fixed queries)

### State 2 → State 3: Load Data

```bash
# Requires goopg running:
scripts/csq-bench-server.sh start

# Load TSV data into goopg + ANALYZE
scripts/tpcds-load.sh
```

**What it does:**
1. Creates TPC-DS schema (25 tables) via `tpcds.sql`
2. TRUNCATE + COPY each table from `.tsv` file
3. ANALYZE each table
4. CHECKPOINT — flushes all data to disk so WAL replay is not needed on restart

**Expect:** 25 tables loaded with row counts shown. Server can be restarted without WAL replay error.

### State 3 → Results: Run Queries

```bash
# Requires goopg running with data loaded

# Run all 99 queries (default 600s timeout each)
scripts/tpcds-run.sh

# Run single query
scripts/tpcds-run.sh 14

# Run specific queries
scripts/tpcds-run.sh 1,3,5-10
```

**Output:**
- `bench/tpch/runtime_goopg/tpcds-results/results.txt` — per-query timing + status
- `bench/tpch/runtime_goopg/tpcds-results/qN_output.txt` — per-query full output

**Recovery:** If goopg crashes mid-query, the script auto-restarts it and continues.

## Individual scripts

| Script | Purpose |
| --- | --- |
| `tpcds-setup.sh` | Compile + generate + convert + fix (State 1→2) |
| `tpcds-load.sh` | Schema + COPY + ANALYZE (State 2→3) |
| `tpcds-run.sh` | Execute queries + record results (State 3→results) |
| `convert_tpcds.py` | Pipe-delimited `.dat` → tab-delimited `.tsv` |
| `tpcds_split_queries.py` | Split `query_0.sql` → individual `queryN.sql` |

## PG compatibility notes

goopg uses PostgreSQL-compatible SQL syntax. The following netezza-isms are fixed:
- `N days` → `INTERVAL 'N days'` (interval arithmetic)
- `c_last_review_date_sk` → `c_last_review_date` (query 30 column name)
- Column-alias-in-WHERE for queries 36, 70, 86 → subquery wrapper

## Known goopg gaps

- **COPY `DELIMITER`/`CSV` options not supported** — use tab-delimited TEXT format with `\N` NULLs
- **Per-database catalog scoping incomplete** — use `postgres` database only
- **Some queries may crash goopg** — script auto-restarts and continues
- **Complex queries may exceed 600s timeout** — increase with `TPCDS_TIMEOUT=1200`

## Environment

| Variable | Default | Description |
| --- | --- | --- |
| `TPCDS_DB` | `postgres` | Database name |
| `TPCDS_TIMEOUT` | `600` | Per-query timeout (seconds) |
| `TPCDS_SCALE` | `1` | Scale factor (GB) |

All other variables inherited from `bench/tpch/env_goopg.sh` (port 65433, GOMEMLIMIT=12GiB, GOGC=off).
