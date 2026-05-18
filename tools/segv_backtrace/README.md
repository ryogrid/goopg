# tools/segv_backtrace

Diagnostic-only `LD_PRELOAD` shim that installs a `SIGSEGV` handler in
upstream PostgreSQL processes spawned by goopg E2E tests. The handler
writes `backtrace_symbols_fd` output to `STDERR` (captured by
`pgcluster.Cluster.logPath`) before restoring `SIG_DFL` and re-raising
the signal so the kernel produces the same termination the postmaster
expects.

Used by Step 3dd of milestone M0106-0010 to identify the silent SIGSEGV
that fires in upstream PG client backends after Step 3dc(1) closed the
`cache lookup failed for type 23` ERROR.

## Build / use

The shared library is built automatically by
`internal/testutil/pgcluster.Cluster` on first call to `Start()` when
`GOOPG_TEST_SEGV_BACKTRACE=1` is set in the environment. The compiled
`libsegv_backtrace.so` is placed in `tools/segv_backtrace/build/`
(gitignored) and reused across runs based on source mtime.

To manually compile and verify the .so:

```
cc -shared -fPIC -O0 -g -o tools/segv_backtrace/build/libsegv_backtrace.so \
    tools/segv_backtrace/segv_backtrace.c -ldl
```

`cc` and the GNU libc `<execinfo.h>` header must be available; the
shim degrades gracefully — if the build fails, the cluster Start path
logs a single-line warning and proceeds without LD_PRELOAD so the
existing test still runs.
