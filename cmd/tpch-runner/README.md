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

### Step 7 — Compare two arms on VALUES, not row counts (`--digest`)

`rows=N` says nothing about the tuples. M0127-P5.9 run 1 produced a query with
the right row count and every column value shifted one relation-block from its
name, and the acceptance bar missed it on five queries
(`docs/design/leftdeep-joins/09-verification-and-acceptance.md` §3.1). `--digest`
appends three digests to each OK line so two arms can be compared on content:

```bash
/tmp/tpch-runner --digest                      # each OK line gains colsig/ordered/unordered
# Q1: OK elapsed=6.94s colsig=767ce848ef814ee2 ordered=133c5680f047d5ff unordered=bf655c039b9630a8 rows=4
```

| digest | what it covers |
|---|---|
| `colsig` | the column NAMES, in order — a moved header reports as `SCHEMA-DIFF` |
| `ordered` | every value, in scan order — sensitive to row order |
| `unordered` | the MULTISET of rows — unchanged by row order, but duplicate-sensitive |

Then diff two run logs (this mode never opens a connection):

```bash
/tmp/tpch-runner -diff arm-off.log arm-on.log   # exits 1 on any non-MATCH
```

Verdicts: `MATCH` · `VALUE-DIFF` (decisive — different tuples) · `ORDER-DIFF`
(same multiset, different order: a defect only if that query's `ORDER BY` is a
total order — the differ does not know, so it asks) · `SCHEMA-DIFF` ·
`ROWS-DIFF` · `STATUS-DIFF` / `ERROR-DIFF` / `BOTH-ERROR` · `NO-DIGEST` (one or
both arms ran without `--digest`, so values were never compared — a FAILING
verdict, so a digest-less pair cannot read as a pass).

`--digest` costs one Scan per row (~2 % of a TPC-H SF1 sweep) and stays off by
default so timings remain comparable with runs that predate it. `rows=N` is
kept as the last token on the line because `scripts/tpch-spotcheck.sh`,
`ci/batch/stages/stage-tpch.sh` and `scripts/tpch-relsize-arm.sh` extract it
with an end-of-line-anchored regex — `--digest` composes with those gates.

For the full two-arm acceptance protocol (fresh capped server per arm, server
age 0 at sweep start) use `scripts/tpch-acceptance-arm.sh`.

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
| `--cancel-after` | `0` (disabled) | Send a context cancel after this duration; lib/pq sends CancelRequest to the server, returning SQLSTATE 57014. Connection stays alive for the next query. Requires `--per-query-timeout` ≥ this value. |
| `--explain` | false | Issue `EXPLAIN <query>` instead of executing. |
| `--checkpoint` | false | Issue a `CHECKPOINT` and exit. Convenience wrapper around `goopg checkpoint`. |
| `--digest` | false | Append `colsig`/`ordered`/`unordered` digests to each OK line so two arms can be compared on values (see Step 7). Ignored under `--explain`. |
| `-diff` | false | Offline mode: `tpch-runner -diff A.log B.log` compares two `--digest` run logs and exits 1 on any non-MATCH. |

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
