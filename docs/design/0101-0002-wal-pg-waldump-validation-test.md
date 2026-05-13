# 0101-0002 — WAL pg_waldump Validation Test

**Status:** draft
**Date:** 2026-05-13
**Milestone:** M0101-0003

## Problem

There is no automated test that verifies goopg's emitted WAL can be parsed by
`pg_waldump`. The format has evolved across multiple milestones (M0014, M0098,
M0099) with no regression gate, making it easy for future changes to silently
break PG-tool compatibility.

## Solution

Add `TestPort_WALPgWaldumpCompat` to `internal/testport/` — a TAP-style oracle
test that runs `pg_waldump` against WAL segments produced by a live goopg cluster.

### Test flow

```
1. Start a fresh goopg cluster (via cluster.NewCluster / mustInitStart).
2. Run a short representative workload via psql:
     CREATE TABLE t (id int PRIMARY KEY, v text);
     INSERT INTO t SELECT i, 'x' FROM generate_series(1,100) i;
     CHECKPOINT;
3. Stop the cluster cleanly (ShutdownSmart or ShutdownImmediate).
4. Enumerate all segment files in <datadir>/pg_wal/.
5. For each segment, run:
     ./postgres/local_install/bin/pg_waldump --quiet <segment>
6. Assert exit code = 0.
7. Additionally check:
     pg_waldump --stats <first-segment>
   and assert output contains "XLOG" or at least one Rmgr record count > 0.
```

### Binary location

```go
const pgWaldumpBin = "postgres/local_install/bin/pg_waldump"
```

Skip the test (via `t.Skip`) if the binary does not exist at that path.

### Exit-code semantics for pg_waldump

- Exit 0: all records parsed without structural error.
- Exit 1: at least one structural parse error.

`--quiet` suppresses per-record stdout but still exits 1 on errors. Use it to
avoid Rmgr payload decode failures (goopg's payload bytes differ from PG's) from
being treated as compatibility failures. The compatibility contract is:
**structural WAL framing is parseable**; Rmgr payload semantics are out of scope
until M0014 completes full Rmgr payload mapping.

### Error reporting

If `pg_waldump` exits non-zero, capture stderr and include it in the test failure
message so the developer can see the exact byte offset and error.

### Placement in the test inventory

- File: `internal/testport/wal_pg_waldump_test.go`
- Build tag: none (no `integration` tag needed — the cluster lifecycle already
  works in the default suite via `mustInitStart`).
- CSV: Add a row to `docs/test-port/postgres-oracle-port-status.csv`:
  - `test_id`: `wal-pg-waldump-compat`
  - `status`: `port`
  - `pass_required`: `yes`
  - `rationale`: `TestPort_WALPgWaldumpCompat`

### Fast-fail heuristic

Before starting a cluster, verify the magic in any existing segment:

```go
func checkWALMagic(segPath string) error {
    f, _ := os.Open(segPath)
    defer f.Close()
    var magic [2]byte
    f.Read(magic[:])
    if binary.LittleEndian.Uint16(magic[:]) != 0xD118 {
        return fmt.Errorf("legacy WAL magic 0x%04X; PageHeaders not enabled",
            binary.LittleEndian.Uint16(magic[:]))
    }
    return nil
}
```

This gives an immediate, actionable error if someone accidentally disables
`PageHeaders` in a future change.

## Files created / modified

| File | Action |
|---|---|
| `internal/testport/wal_pg_waldump_test.go` | New — `TestPort_WALPgWaldumpCompat` |
| `docs/test-port/postgres-oracle-port-status.csv` | Add `wal-pg-waldump-compat` row |
| `docs/test-port/postgres-oracle-port-status.md` | Regenerate via `go run ./cmd/gen-oracle-port-status` |

## Verification

```bash
go test -v -run TestPort_WALPgWaldump ./internal/testport/
```

Expected output: `PASS: wal pg_waldump compatibility (N segments, all exit 0)`

## Risks

- **`pg_waldump --quiet` still exits 1 on Rmgr decode errors in some PG
  versions.** Mitigation: use `pg_waldump --stats-per-record` and only assert
  that at least one WAL record was successfully counted, rather than asserting
  exit code 0. If `--quiet` proves noisy, add a normalization layer.
- **Segment file is pre-allocated zeros.** If the workload is too light, the
  active WAL segment may contain leading valid records followed by zeros. This is
  valid PG behavior (pg_waldump stops at the zero-padding); verify it exits 0.
- **Test cluster port collision.** Use the cluster framework's existing port
  allocation; no hardcoded ports.
