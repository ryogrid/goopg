# 0110-0004 — pg_resetwal TAP test port (001_basic CLI tier)

Status: accepted (partial)
Milestone: M0110-0004
Date: 2026-06-13

## Goal

Port the CLI-decidable tier of `postgres/src/bin/pg_resetwal/t/001_basic.pl`
into a Go test under `internal/testport/`, following the incremental tier
strategy established by M0110-0001 (pg_dump), M0110-0002 (pg_waldump) and
M0110-0003 (pg_amcheck).

## What 001_basic.pl contains

Unlike the earlier three ports, `001_basic.pl` is *not* purely CLI — it is a
247-line test with two interleaved tiers:

1. **CLI-handling tier.** `program_help_ok` / `program_version_ok` /
   `program_options_handling_ok`, then a large block of argument-handling and
   option-argument-validation cases:
   - `too many command-line arguments` (`foo bar`)
   - `no data directory specified` (no args; PGDATA is *not* consulted —
     upstream comment: `# not used`)
   - `could not read permissions of directory` (nonexistent dir `foo`)
   - `-c` / `-e` / `-l` / `-m` / `-o` / `-O` / `-u` / `-x` / `--wal-segsize` /
     `--char-signedness` invalid-argument and out-of-range cases.

2. **Server-dependent tier.** Inits a cluster, runs `pg_resetwal -n /
   --pgdata / --force`, starts the server and `SELECT 1`s, asserts the
   `lock file ... exists` / `was not shut down cleanly` paths, then drives the
   SLRU-derived control-override options (`--commit-timestamp-ids`,
   `--multixact-ids`, `--multixact-offset`, `--oldest-transaction-id`,
   `--next-transaction-id`) computed from the real `pg_commit_ts`,
   `pg_multixact/{offsets,members}` and `pg_xact` segment files, and verifies
   via `--dry-run` that the control file was rewritten.

## Key source observation: ordering in `pg_resetwal.c main()`

The CLI tier is server-free *and* data-directory-free because of the order of
operations in `postgres/src/bin/pg_resetwal/pg_resetwal.c`:

1. `--help` / `--version` short-circuit at the top of `main()`.
2. The `getopt_long` loop parses every option; each invalid-argument /
   out-of-range case emits its error and `exit(1)` **inside** the loop
   (`invalid argument for option -c`, `must not be 0`, `greater than`,
   `must be between 0 and 4294967295`, `must be a power`, `must not be -1`,
   `invalid value`, …).
3. Only **after** the loop: the `too many command-line arguments` and
   `no data directory specified` checks.
4. Only **after that**: `GetDataDirectoryCreatePerm(DataDir)` →
   `could not read permissions of directory`, then `chdir`,
   `CheckDataVersion`, the `postmaster.pid` lock-file check, and
   `read_controlfile()`.

So every assertion in tier 1 is decided before any directory access. The port
passes a deliberately **nonexistent** data directory to the option-validation
cases: the option error fires during step 2, before step 4 would touch the
path, so the observable result (non-zero exit + identical error text) matches
the upstream invocation while keeping the port server-free.

## Decision

Port the CLI tier as `TestPort_PgResetwal001Basic`
(`internal/testport/pgresetwal_port_test.go`), reusing the existing
`programHelpOk` / `programVersionOk` / `programOptionsHandlingOk` /
`commandFailsContaining` / `clientToolBin` / `runTool` helpers. pg_resetwal does
**not** link libpq (it never connects to a server), so no `LD_LIBRARY_PATH`
shim (cf. M0110-0003's `runToolWithLib`) is needed — plain `runTool` works.

The two upstream cases that *succeed* against a real directory (`-m 0,10`, and
the control-override block) belong to the server tier and are **not** ported
here.

**Defer the server-dependent tier** under CSV row `RW-002`: it needs
pg_control byte-level read/write round-trip compatibility (M0106) plus on-disk
SLRU-segment-layout parity (`pg_commit_ts`, `pg_multixact`, `pg_xact`).
`002_corrupted.pl` is deferred under the same row.

## Update (loop #45): server-tier pg_control round-trip ported (RW-003)

The pg_control read/write round-trip half of the server tier is now ported.
Empirically, upstream `pg_resetwal` already reads goopg's `global/pg_control`
fully (`-n` prints the complete checkpoint dump), rewrites it (`--pgdata` resets
the WAL + control file), and goopg restarts cleanly from the reset directory.
The one remaining blocker was a **clean-shutdown state bug**, fixed here.

### Root cause: clean shutdown left `DB_IN_PRODUCTION`

`wal.Checkpointer.runCheckpoint` unconditionally stamped
`pg_control.State = DB_IN_PRODUCTION` on every checkpoint, including the final
shutdown checkpoint taken in `Runtime.Close`. So after a clean `goopg stop`,
`pg_controldata` reported `Database cluster state: in production`, and
`pg_resetwal` refused without `--force`:

```
pg_resetwal: error: database server was not shut down cleanly
```

PostgreSQL's `CreateCheckPoint(CHECKPOINT_IS_SHUTDOWN)` path sets
`ControlFile->state = DB_SHUTDOWNED` once a shutdown checkpoint completes
(`postgres/src/backend/access/transam/xlog.c`). External tools gate on this
byte to decide whether recovery is required.

### Fix

- `runCheckpoint(ctx, spread, shutdown bool)` — new `shutdown` flag selects
  `DBStateShutdowned` vs `DBStateInProduction` for the on-disk `State`.
- `Checkpointer.CheckpointShutdown()` — public entry that runs the checkpoint
  with `shutdown=true`.
- `Runtime.Close` calls `CheckpointShutdown()` for the *final* durable
  checkpoint (after which no further WAL is written). The earlier `OnStop`
  checkpoint deliberately stays `DB_IN_PRODUCTION`, so a crash in the
  `OnStop`→`Close` window is still correctly flagged unclean.

goopg's own startup replays WAL regardless of `State` (verified: no startup
path branches on it), so this is purely an on-disk compatibility surface — a
clean restart and `SELECT 1` still succeed, and crash recovery is unaffected.

### Known minor divergence (follow-up, not blocking)

A *running* goopg shows `State=DB_SHUTDOWNED` from restart until the first
online checkpoint flips it back to `DB_IN_PRODUCTION` (PostgreSQL stamps
`DB_IN_PRODUCTION` at the end of `StartupXLOG`). No functional path depends on
this; a startup stamp was intentionally deferred because a standby in recovery
must *not* be marked in-production, which widens blast radius into the
replication paths.

### Test: `TestPort_PgResetwal001BasicServer`

Drives the upstream `pg_resetwal` binary against a real goopg cluster:
PGDATA 0700/0600 perms; `-n` prints the checkpoint dump; refuses while the
server runs (`postmaster.pid` lock); `--pgdata` succeeds after a clean shutdown
**without** `--force`; `SELECT 1` works after the reset; a `--next-oid 100000`
override is applied and spot-checked; server works after the override reset.

## Update (loop #47): SLRU-derived override round-trip ported

`TestPort_PgResetwal001BasicServer` was extended to cover the SLRU-derived
control-override **round-trip**, faithfully mirroring upstream
`001_basic.pl` l.184-242:

1. A **supported-override restart** tier — `--epoch` / `--next-oid` plus
   `--multixact-ids` / `--multixact-offset` / `--commit-timestamp-ids`. goopg
   both round-trips these through `pg_control` *and* restarts and serves
   `SELECT 1` afterwards (these ids do not force a large CLOG xid walk).
2. A **full maximal-override round-trip** — the test now reads the cluster's
   real SLRU segment files (`pg_commit_ts`, `pg_multixact/{offsets,members}`,
   `pg_xact`) and derives `--commit-timestamp-ids` / `--multixact-ids` /
   `--multixact-offset` / `--oldest-transaction-id` / `--next-transaction-id`
   with the same block-size multipliers as the upstream Perl
   (`get_slru_files` + `hex()` bounds, helpers `slruBounds` / `parseBlockSize`
   / `parseHexSeg`), applied together with `--epoch` / `--next-wal-file` /
   `--next-oid` / `--wal-segsize`. The override applies and the control change
   is spot-checked — proving goopg's on-disk SLRU layout + `pg_control`
   read/write parity under upstream `pg_resetwal` for **every** override field.

That the test passes confirms goopg already presents the `pg_commit_ts`,
`pg_multixact`, and `pg_xact` SLRU directories with the segment-file layout the
override computation reads — the SLRU-parity prerequisite that previously
blocked these cases is satisfied.

**Still deferred (RW-002 remainder):** the *final restart* after the maximal
override (upstream l.244-245). `--next-transaction-id` advances `NextXID` far
past the bootstrap `pg_xact` segment, and `CLog.MarkUnknownAsAborted`
(`internal/mvcc/clog.go`) walks the whole xid range with an fsync per step
during `initdb.Open`, so startup is pathologically slow (looks hung). The
supported-override restart already exercises the post-reset restart path; only
the advanced-CLOG id blocks it. The fix — a PG-style `StartupCLOG` page-fill
instead of a per-xid walk — is a separate WAL/MVCC task. Also still deferred:
the unclean-shutdown/`--force` branch (no goopg crash state in v0).

## Update (loop #48): `002_corrupted.pl` ported (RW-004)

`TestPort_PgResetwal002Corrupted` ports `002_corrupted.pl` faithfully. It inits
a goopg cluster (never started), then corrupts `global/pg_control` two ways and
drives the upstream `pg_resetwal` binary:

1. **All zeroes** — `pg_resetwal` cannot match `PG_CONTROL_VERSION`, warns
   `pg_control exists but is broken or wrong version; ignoring it`,
   `GuessControlValues()`, and under `--dry-run` prints the dump (which begins
   `pg_control version number`). Exit 0.
2. **16-byte header restored, body zeroed** — `pg_control_version` now matches
   `PG_CONTROL_VERSION`, taking the "different code path" (`read_controlfile`
   passes the version check but the CRC fails over the zeroed body), so it warns
   `pg_control specifies invalid WAL segment size (0 bytes); proceed with
   caution`, marks the values guessed, and again prints the dump. Exit 0.
3. **Plain run** (no `--force`) — guessed values block the reset:
   `not proceeding because control file values were guessed`. Exit 1.
4. **`--force`** — proceeds and rewrites the control file. Exit 0.

Steps 1/3/4 are generic `pg_resetwal` logic. The single goopg dependency is
step 2: goopg's `pg_control` header must carry a `pg_control_version` upstream
`pg_resetwal` recognizes — already proven by RW-003. Crucially this test needs
**no server start**, so it is *not* blocked on goopg's lack of a crash/unclean
shutdown state (the earlier deferral note that paired `002_corrupted.pl` with
the `--force` branch was wrong: corrupting `pg_control` on disk directly drives
the guessed-values path without any unclean shutdown).

## CSV rows

- `RW-001` → `port` / `pass_required=yes`: `001_basic.pl` CLI tier =
  `TestPort_PgResetwal001Basic`.
- `RW-003` → `port` / `pass_required=yes`: `001_basic.pl` server-tier
  pg_control round-trip = `TestPort_PgResetwal001BasicServer`, now including
  the SLRU-derived maximal-override round-trip (loop #47).
- `RW-004` → `port` / `pass_required=yes`: `002_corrupted.pl` =
  `TestPort_PgResetwal002Corrupted` (loop #48).
- `RW-002` → `defer` / `pass_required=no`: the remaining server tier — the
  advanced-CLOG maximal-override **restart** (blocked on a PG-style
  `StartupCLOG` page-fill) and the unclean-shutdown/`--force` branch (no goopg
  crash state in v0).

## Verification

- `gofmt -l` clean; `go vet ./internal/testport/` clean.
- `go test -run TestPort_PgResetwal ./internal/testport/` → PASS (CLI tier,
  server tier, and `002_corrupted.pl` = `TestPort_PgResetwal002Corrupted`).
- `go test -run TestCheckpointerShutdownSetsDBShutdowned ./internal/wal/` → PASS.
- `go test -race ./internal/wal/ ./internal/control/ ./internal/initdb/` → PASS.
- `go test -run TestPort_Recovery ./internal/testport/` → PASS (clean-shutdown
  state change does not regress recovery).
- `go run ./cmd/gen-oracle-port-status` regenerated the `.md` view.

## Resume point

`002_corrupted.pl` is now ported (loop #48, RW-004). Promote the rest of
`RW-002` to `port` once (a) `CLog.MarkUnknownAsAborted` is replaced by a
PG-style `StartupCLOG` page-fill so a cluster whose `NextXID` was advanced by
`--next-transaction-id` restarts in bounded time (enabling the upstream
l.244-245 final restart for the maximal override); and (b) goopg gains a true
unclean/crash shutdown state so the `--force`-after-unclean-shutdown branch of
`001_basic.pl` can be reproduced.
