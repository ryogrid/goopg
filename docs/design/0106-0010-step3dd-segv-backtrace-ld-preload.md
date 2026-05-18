# M0106-0010 Step 3dd — SIGSEGV backtrace via LD_PRELOAD shim

**Status:** LANDED 2026-05-18
**Milestone:** M0106-0010 (Resolve array assertion + bootstrap pg_am and
related catalog tuples for upstream PG standby integration)
**Predecessor:** Step 3dc(1) — pg_proc I/O regproc heap rows seeded.

## Problem

After Step 3dc(1), the `TestE2E_FailoverGoopgToPG/async` capture shows:

- ✅ `cache lookup failed for type 23` ERROR is GONE (zero occurrences).
- ❌ The first client backend that runs after `consistent recovery state
  reached at 0/4288` is killed by **SIGSEGV with no preceding ERROR line**
  — the postmaster only logs `client backend (PID N) was terminated by
  signal 11: Segmentation fault` at `LogChildExit` (postmaster.c:2853),
  forcing `HandleChildCrash → terminating any other active server
  processes` and a full postmaster shutdown.

The cycle masks the actual fault site: there is no Go-side panic, no PG
ERROR/FATAL with backtrace, no core dump (PG normally relies on
`ereport(PANIC)` for backtraces, never on signal handlers — `pqsignal.c`
only `sigdelset(BlockSig, SIGSEGV)`s without installing a handler). The
next step cannot fix what it cannot name.

## Decision

Install a `SIGSEGV` handler in every PG process spawned by the E2E
cluster via an `LD_PRELOAD`'d shared library, gated by
`GOOPG_TEST_SEGV_BACKTRACE=1`. The handler writes
`backtrace_symbols_fd(STDERR_FILENO)` output (captured by the
postmaster's stderr → `pg.log` redirect set up by
`pgcluster.Cluster.Start`) before restoring `SIG_DFL` and re-raising
the signal so the kernel still produces the same termination the
postmaster expects to harvest. The backtrace appears in the standby's
`pg.log` under the Step-3cy `[m0102-pg-standby-log]` cleanup tag.

### Why LD_PRELOAD

PG installs no `SIGSEGV` handler of its own — `pqsignal.c` only removes
`SIGSEGV` from `BlockSig`. An LD_PRELOAD'd handler installed in the
shared-library constructor fires before the kernel terminates the
child. Forked client backends inherit the loaded library (and its
installed sigaction) from postmaster without re-exec, so a single
LD_PRELOAD on the postmaster command line covers every backend.

### Why not patch PG

The whole point of the E2E test is to exercise *unpatched upstream
PG 18.3* binaries against goopg-emitted catalog files. Patching PG would
defeat the test's purpose.

### Why opt-in via env gate

LD_PRELOAD'ing a non-trivial shim affects every test run. The shim is
diagnostic-only and must not be active for non-failure runs. The gate
default is OFF; the failure-investigation workflow flips it on.

## Implementation

### `tools/segv_backtrace/segv_backtrace.c` (canonical source)

- `__attribute__((constructor))` installs `sigaction(SIGSEGV)` with
  `SA_SIGINFO | SA_RESETHAND | SA_NODEFER` so the handler does not fire
  recursively if `backtrace_symbols_fd` itself crashes.
- Handler:
  - writes a fixed `[GOOPG_SEGV_BACKTRACE]` marker header via `write(2)`,
  - calls `backtrace(3)` + `backtrace_symbols_fd(3)` (both
    async-signal-safe per glibc man pages),
  - writes a fixed footer,
  - restores `SIG_DFL` for `SIGSEGV`,
  - `raise(SIGSEGV)` so the kernel produces the same termination.
- Only async-signal-safe APIs are used — the handler must not call
  `malloc`, `printf`, or anything PG-internal.

### `internal/testutil/pgcluster/segv_backtrace_src.txt` (embedded copy)

The same C source is embedded into the `pgcluster` package via
`//go:embed`. The file is named `.txt` (not `.c`) because Go's build
system rejects loose `.c` files in non-cgo packages
(`C source files not allowed when not using cgo or SWIG`).
`TestSegvBacktraceSourceMatchesToolsCopy` pins byte-equality with the
canonical `tools/` copy.

### `internal/testutil/pgcluster/segv_backtrace.go`

- `segvBacktraceLDPreload()` returns the path of the compiled `.so` if
  `GOOPG_TEST_SEGV_BACKTRACE=1`; else `("", false, nil)`.
- `ensureSegvBacktraceSO()` writes the embedded source to
  `os.TempDir()/goopg-segv-backtrace/segv_backtrace_<hash>.c` and
  compiles to `libsegv_backtrace_<hash>.so` (sha-256 content-addressed,
  so a source change automatically rebuilds and earlier `.so`s remain
  valid for in-flight processes). Compilation uses `$CC` (default
  `cc`) with `-shared -fPIC -O0 -g`. Build failures bubble up to the
  caller as `error`; `pgcluster.Cluster.Start` logs a single-line
  warning to `pg.log` and proceeds without LD_PRELOAD so the rest of
  the test still runs.
- `GOOPG_SEGV_BACKTRACE_SO=<path>` env var lets the operator pin a
  pre-built `.so`; useful for hermetic test environments.

### `internal/testutil/pgcluster/cluster.go`

- `Start()` now computes `env := c.env()`, asks
  `segvBacktraceLDPreload()`, and on success calls `appendLDPreload(env,
  soPath)` before assigning `cmd.Env`.
- `appendLDPreload` merges into any pre-existing `LD_PRELOAD` entry
  (space-separated per `ld.so(8)`), never overwriting it.

The other `exec.Command` call sites (`pg_ctl`, `psql`, `pgbench`) are
deliberately left alone — those are separate processes and the SIGSEGV
under investigation is in PG backends inherited from the postmaster.

## Regression pins

`internal/testutil/pgcluster/segv_backtrace_test.go`:

1. `TestSegvBacktraceSourceMatchesToolsCopy` — byte-equality between
   embedded `.txt` and canonical `tools/segv_backtrace/segv_backtrace.c`.
2. `TestSegvBacktraceLDPreloadGateOff` — gate-off returns
   `ok=false, soPath=""` (no LD_PRELOAD leak into production runs).
3. `TestEnsureSegvBacktraceSOBuilds` — full end-to-end shim
   verification: build the .so, exec a null-deref helper under
   `LD_PRELOAD`, assert the `[GOOPG_SEGV_BACKTRACE]` marker AND the
   footer appear on stderr.
4. `TestAppendLDPreloadMergesExisting` — verifies LD_PRELOAD merge for
   absent / empty-existing / pre-populated cases.

## E2E verification

`GOOPG_RUN_BLOCKED_M0102_E2E=1 GOOPG_TEST_SEGV_BACKTRACE=1 go test -v
-count=1 -timeout=900s -run 'TestE2E_FailoverGoopgToPG/async'
./internal/testport/` was run; the resulting `pg.log` (visible under the
Step-3cy `[m0102-pg-standby-log]` cleanup tag) now contains a
`[GOOPG_SEGV_BACKTRACE] SIGSEGV caught, backtrace follows:` block with
`backtrace_symbols_fd` frame pointers between each backend crash and the
postmaster's `HandleChildCrash` log line. The exact frame symbols
require `addr2line` resolution against the PG installation (see
`postgres/local_install/bin/postgres`) and are recorded in the fix-plan
entry for Step 3dd as the input to Step 3de.

## Cleanup obligation

The shim is diagnostic-only and stays opt-in. There is **no plan to
remove it** — it is cheap to keep and likely to be useful for any
future silent-SIGSEGV regression. Production builds skip it because the
gate env var is unset.
