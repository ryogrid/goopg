# tpch-runner — TPC-H Query Driver for goopg

`tpch-runner` is a lightweight Go program that submits the HammerDB
TPC-H Q1–Q22 SQL against a running goopg cluster, one query at a time,
with configurable per-query and cancel timeouts. It is designed for
development and debugging, where HammerDB's 7200-second all-or-nothing
budget is too coarse.

> **Note:** This tool does not produce TPC-H compliant benchmark
> results. Use HammerDB (`bench/tpch/run_power_test_goopg.sh`) for
> official measurements.

---

## Prerequisites

1. **goopg binary** built at `tmp/goopg-bench-bin` (from the repo root):
   ```
   cd <repo-root>
   go build -o tmp/goopg-bench-bin ./cmd/goopg
   ```

2. **tpch-runner binary**:
   ```
   go build -o /tmp/tpch-runner ./cmd/tpch-runner
   ```

3. **HammerDB** checked out at `<repo-root>/HammerDB/` (the repo
   already has it as a submodule/directory).

4. A **running goopg server** with the SF=1 TPC-H schema loaded
   (see §Full Workflow below).

---

## Full Manual Workflow

### Step 1 — Set up the cluster and load data

```bash
cd <repo-root>

# Start a fresh cluster and load SF=1 data (takes ~12 minutes).
bash bench/tpch/setup_goopg.sh --reset
bash bench/tpch/build_schema_goopg.sh
```

Wait for `FINISHED SUCCESS` in the terminal (or in
`bench/tpch/logs/build_goopg_*.log`).

### Step 2 — Issue a CHECKPOINT before any measurements

Always issue a checkpoint after loading data, before running queries.
This flushes all dirty pages and gives measurements a clean I/O baseline.

```bash
./tmp/goopg-bench-bin checkpoint -D bench/tpch/runtime_goopg/data
# Output: goopg checkpoint: complete
```

### Step 3 — Run individual queries

```bash
# Run Q14 only (quick sanity check, ~30 s):
/tmp/tpch-runner --queries=14

# Run Q9 with a 10-minute budget:
/tmp/tpch-runner --queries=9 --per-query-timeout=600s

# Run Q20 then Q6:
/tmp/tpch-runner --queries=20,6 --per-query-timeout=900s
```

Output format: `Q<N>: OK elapsed=<s>s rows=<r>` or `Q<N>: ERROR ...`.

### Step 4 — Run the full 22-query stream

```bash
# All queries in HammerDB's canonical stream order (1..22).
# Default per-query timeout is 600 s.
/tmp/tpch-runner
```

### Step 5 — Cancel a running query (M0057-0004)

Once M0057-0004 is implemented, use `--cancel-after` to interrupt a
long-running query without closing the connection:

```bash
# Interrupt Q20 after 2 minutes; move to next query automatically.
/tmp/tpch-runner --queries=20 --cancel-after=120s
# Q20: CANCELLED elapsed=120.Xs
```

Until M0057-0004 lands, the existing `--per-query-timeout` will close
the connection (the query continues server-side until the next write).

### Step 6 — EXPLAIN mode

Print the query plan without executing the query:

```bash
/tmp/tpch-runner --queries=9 --explain
```

---

## Flags Reference

| Flag | Default | Description |
|------|---------|-------------|
| `--host` | `127.0.0.1` | goopg server host |
| `--port` | `65433` | goopg server port |
| `--db` | `tpch` | Database name |
| `--user` | `tpch` | User name |
| `--password` | `tpch` | Password |
| `--queries` | (all 1–22) | Comma-separated query numbers, e.g. `9,20,6`. Empty = all in 1..22 order. |
| `--per-query-timeout` | `600s` | Per-query wall-clock budget. Timeout closes the connection (see M0057-0004 for true cancel). |
| `--cancel-after` | (unset) | **(M0057-0004)** Send CancelRequest after this duration; connection stays alive. |
| `--explain` | false | Issue `EXPLAIN <query>` instead of executing. |
| `--checkpoint` | false | Issue a `CHECKPOINT` and exit. Convenience wrapper around `goopg checkpoint`. |

---

## Troubleshooting

### `dial tcp 127.0.0.1:65433: connect: connection refused`
The goopg server is not running. Start it:
```bash
./tmp/goopg-bench-bin start \
  -D bench/tpch/runtime_goopg/data \
  --listen 127.0.0.1:65433 \
  --hba bench/tpch/runtime_goopg/data/pg_hba.conf &
```

### `pq: relation "lineitem" does not exist`
Data has not been loaded. Run the build script (Step 1 above).

### `Q9: ERROR ... pq: canceling statement due to user request`
This is the expected output when `--per-query-timeout` or
`--cancel-after` fires. It is not a bug.

### Query takes much longer than in HammerDB
This is usually a buffer-pool warm-up effect. Issue a `CHECKPOINT`
(Step 2) and re-run. If it still differs, check whether background
workers are flushing mid-query (see M0057-0001).

### `BUG(M0042-0004): Pool.FlushAll called from client_backend goroutine`
This was a bug fixed in commit `f5021c8`. Rebuild the binary:
```bash
go build -o tmp/goopg-bench-bin ./cmd/goopg
```

---

## Architecture notes

- Uses `database/sql` + `github.com/lib/pq` (pure Go PostgreSQL driver).
- Opens **one** TCP connection per query (via `db.SetMaxOpenConns(1)`).
- Q15 is special-cased: `CREATE VIEW revenue0 ... AS ...` before the
  main SELECT, `DROP VIEW revenue0` after.
- `context.WithTimeout` is used for per-query budgets; when it fires
  the TCP connection is closed (server-side query keeps running until
  its next write attempt). M0057-0004 will improve this to use the
  proper PostgreSQL `CancelRequest` message.
