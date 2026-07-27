# TPC-DS Benchmark on goopg — Workflow

> **Location note (2026-07-27):** everything TPC-DS lives under `bench/tpcds/`
> — clusters in `runtime_goopg/` (goopg SF=1 :65436, SF=0.5 :65437) and
> `runtime/pgdata` (PostgreSQL reference :65438, dbs `tpcds`/`tpcds05`).
> Env: `bench/tpcds/env_tpcds.sh`. Lifecycle:
> `bench/tpcds/server.sh {start|stop|status} [sf1|sf05|pg|all]`.
> Port map + cross-benchmark picture: repo-root `CLAUDE.md`.

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
  third-party/             bench/tpcds/runtime_goopg/    bench/tpcds/runtime_goopg/
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
- `bench/tpcds/runtime_goopg/tpcds-data/*.tsv` (25 COPY-ready files)
- `bench/tpcds/runtime_goopg/tpcds-data/queries/query*.sql` (99 PG-fixed queries)

### State 2 → State 3: Load Data

```bash
# Requires goopg running:
bench/tpcds/server.sh start sf1

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
- `bench/tpcds/runtime_goopg/tpcds-results/results.txt` — per-query timing + status
- `bench/tpcds/runtime_goopg/tpcds-results/qN_output.txt` — per-query full output

**Recovery:** If goopg crashes mid-query, the script auto-restarts it and continues.

## Individual scripts

| Script | Purpose |
| --- | --- |
| `tpcds-setup.sh` | Compile + generate + convert + fix (State 1→2) |
| `tpcds-load.sh` | Schema + COPY + ANALYZE (State 2→3) |
| `tpcds-run.sh` | Execute queries + record results (State 3→results) |
| `tpcds-bench-compare.sh` | SF=1 goopg + PG side-by-side sweep with EXPLAIN capture, per-timeout goopg restarts |
| `tpcds-sf05-regression.sh` | **SF 0.5 fast regression gate** — see the dedicated section below |
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

All other variables inherited from `bench/tpch/env_goopg.sh` (goopg SF=1 on port 65436; see env table).

---

# SF 0.5 fast regression gate (`tpcds-sf05-regression.sh`)

## Why it exists

A full SF=1 goopg-vs-PG sweep costs **4–5 hours**, almost all of it waiting out
the ~16 known 600 s planner timeouts, and it needs both engines run per query.
The SF 0.5 gate restructures the cost:

- the dataset is half-size, so completing queries run ~2× faster;
- PostgreSQL executes each query **once**, via `EXPLAIN (ANALYZE, TIMING OFF)`,
  which yields the plan **and** the authoritative row count in a single pass;
  the result is cached in an *oracle file* and reused forever after;
- the recurring cost is therefore **one goopg pass against the cached oracle**
  (~1 h at the default 300 s timeout; ~40 min at `TIMEOUT_SEC=120`).

First measured run (2026-07-27): oracle capture for 95/96 queries took
**20 minutes**; goopg load took **2.5 minutes**. The gate also produced two
discoveries the SF=1 sweep structurally could not: Q39's long-mysterious crash
finally landed in a logged window (variance-of-constant-group `big.Float`
panic), and Q51 — which always timed out at SF=1, so its rows were never
verifiable — completed and revealed a 0-vs-100 wrong answer that had been
hiding behind its timeout.

## What "SF 0.5" means here (read before trusting comparisons)

`dsdgen` cannot generate fractional scales — its `scale` parameter is
`OPT_INT`, integer gigabytes (DSGen `r_params.c:63`). The dataset is instead
derived **deterministically from the SF=1 TSVs**: the 7 fact tables keep the
rows whose shared key is even, dimensions are copied whole:

| table | sampling predicate |
| --- | --- |
| `store_sales`, `store_returns` | `ss_/sr_ticket_number % 2 == 0` |
| `catalog_sales`, `catalog_returns` | `cs_/cr_order_number % 2 == 0` |
| `web_sales`, `web_returns` | `ws_/wr_order_number % 2 == 0` |
| `inventory` | `inv_item_sk % 2 == 0` |
| all 18 dimension tables | full copy |

Parity on the *shared* key keeps every sales↔returns pair intact: a kept ticket
keeps all its line items and all its returns, so join semantics stay realistic.
Column ordinals are parsed from `tpcds.sql` at run time, never hardcoded.

Official-scale fidelity is irrelevant to correctness testing because the ground
truth is **PostgreSQL 18.3 executing the identical sampled data**. Result rows
differ from SF=1 — that is expected and fine.

Queries are the **same 99 PG-fixed files** as SF=1
(`tpcds-data/queries/query*.sql`), so per-query knowledge transfers across
scales. Skip policy is identical: Q36/Q70/Q86 (dsqgen artefacts that fail on PG
too) plus anything that errors on PG during oracle capture.

## Load procedure (one-time, ~30–45 min end to end)

Prerequisite: State 2 of the SF=1 flow (`scripts/tpcds-setup.sh` has produced
the SF=1 TSVs and queries), and the PG 18.3 instance on :65438 is running
(`bench/tpcds/server.sh start pg`).

```bash
# 1. Sample the SF=1 TSVs -> tpcds-data-sf05/   (~20 s, pure text processing)
scripts/tpcds-sf05-regression.sh build-data

# 2. Load PostgreSQL: drops+recreates db 'tpcds05' on :65438, schema, COPY,
#    ANALYZE (PG keeps stats persistently, so this is one-time)   (~1 min)
scripts/tpcds-sf05-regression.sh load-pg

# 3. Capture the oracle: every non-skipped query once on PG under
#    EXPLAIN (ANALYZE, TIMING OFF); writes per-query plans and
#    tpcds-results-sf05/oracle.txt   (~20 min; ORACLE_TIMEOUT=600 default)
scripts/tpcds-sf05-regression.sh oracle

# 4. Init + load a dedicated goopg cluster at bench/tpcds/runtime_goopg/data-sf05 on
#    port 65437 (cgroup-capped via goopg-test-run.sh, scope goopg-tpcds-sf05),
#    then stop it — the gate always starts its own fresh server   (~3 min)
scripts/tpcds-sf05-regression.sh load-goopg
```

`all` chains build-data → load-pg → oracle → load-goopg → sweep.

## The recurring gate

```bash
scripts/tpcds-sf05-regression.sh sweep            # default 300 s/query
TIMEOUT_SEC=120 scripts/tpcds-sf05-regression.sh sweep   # quick mode
```

Each query is classified **PASS / MISMATCH / ERROR / TIMEOUT / SKIP** against
the oracle. Reports land in `tpcds-results-sf05/sweep-<timestamp>.txt`. Exit
status is non-zero iff any MISMATCH or ERROR occurred — TIMEOUTs are reported
but non-fatal, so the gate is a *correctness* gate and perf is tracked, not
enforced. Run it after any planner/executor change, alongside (not instead of)
`scripts/tpch-spotcheck.sh`.

## Oracle file format

`tpcds-results-sf05/oracle.txt`, one line per query: `q|status|rows|secs`
where status ∈ `OK | TIMEOUT | PG_ERROR | SKIP_QUERYGEN | MISSING`. Only `OK`
entries are compared; everything else is skipped with the reason echoed.
Multi-statement templates (Q14/Q23/Q24/Q39) sum the per-statement top-node
`actual rows=` — the parser matches `actual rows=` specifically because the
same plan line carries the planner's *estimated* `rows=` first.

## Operational hazards the script codifies (all hit on 2026-07-27)

- **Sweep-tail collapse**: with `GOGC=off` + `GOMEMLIMIT`, one timeout query
  parks the heap at the limit and every later query thrashes GC, mimicking a
  code regression (Q6/Q7 measured 70 s→TIMEOUT this way). The gate restarts
  goopg after every goopg TIMEOUT (`RESTART_AFTER_TIMEOUT=0` to disable).
- **Orphaned PG backends**: `timeout N psql` kills the *client*; the server
  keeps executing until it next writes to the socket. After a PG-side timeout
  the script reaps backends older than the timeout — with the victim set
  **materialised** (`WITH … AS MATERIALIZED`) before `pg_terminate_backend`,
  because SQL guarantees no WHERE evaluation order and the naive single-WHERE
  form killed a healthy backend.
- **Contamination guard**: refuses to run while the SF=1 sweep harness is
  active (`FORCE=1` to override).
- **Determinism**: the gate always runs goopg **S-cold** (fresh server, no
  same-session ANALYZE). goopg loses `TableStats.RowCount` on restart anyway
  (design doc `tpcds-round2-fixes` §7.1), so S-cold is the reproducible state.

## Known limitations

- **Weak-signal queries**: an oracle of 0 rows passes trivially against a
  goopg 0 (the historical empty-btree bug hid behind exactly this). The oracle
  step prints the list — 12 queries at first capture
  (Q8 Q10 Q17 Q24 Q25 Q29 Q37 Q54 Q58 Q82 Q85 Q93).
- Halving data does **not** rescue the ~16 planner-timeout queries — they are
  join-order failures, not volume failures, and most still TIMEOUT at SF 0.5.
- Generated artefacts (`tpcds-data-sf05/`, `data-sf05/`, the plan captures,
  the `tpcds05` PG database) are runtime state, **not** git-tracked; they are
  fully reproducible from the tracked script + the SF=1 TSVs. **The one
  exception is `tpcds-results-sf05/oracle.txt`, which IS git-tracked** as a
  pinned fixture (~2 KB): the row counts are deterministic given the dataset
  and queries, so tracking it lets other machines and CI run the goopg sweep
  without the ~20 min PG capture. Re-run `oracle` (and commit the diff) only
  when the dataset or the query files change — a row diff there is itself a
  signal that ground truth moved.

## Environment knobs

| Variable | Default | Description |
| --- | --- | --- |
| `SF05_PORT` | `65437` | goopg SF0.5 port (see env_tpcds.sh port map) |
| `SF05_PG_DB` | `tpcds05` | PostgreSQL database name |
| `ORACLE_TIMEOUT` | `600` | per-query timeout for the one-time PG capture |
| `TIMEOUT_SEC` | `300` | per-query timeout for the goopg sweep |
| `RESTART_AFTER_TIMEOUT` | `1` | bounce goopg after each goopg TIMEOUT |
| `FORCE` | unset | run even while the SF=1 sweep harness is active |
