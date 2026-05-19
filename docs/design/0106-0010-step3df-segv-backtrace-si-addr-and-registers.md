# M0106-0010 Step 3df — SIGSEGV backtrace: si_addr + saved x86_64 registers

**Status:** LANDED 2026-05-18
**Milestone:** M0106-0010
**Predecessor:** Step 3de — pg_authid heap row `rolname` byte-layout pinned.

## Problem

Step 3de falsified Step 3dd's working hypothesis. Both the leaf
`IndexTuple` inside `global/2676` and the heap-row `NameData` inside
`global/1260` are byte-exact for the seeded roles (`postgres`, `ryo`),
yet the upstream PG standby still SIGSEGVs in the same call chain
(`btnamecmp+0x52 → namecmp → __strncmp_avx2`) on the first client
backend's catalog lookup.

The symbolic backtrace Step 3dd added is no longer sufficient: it
tells us *which function* crashed but not *which pointer was bad*.
With both candidates byte-correct, the next investigation needs to
know whether the unmapped dereference happened on `arg1` (leaf
`NameData *`), `arg2` (scan-key `Name *`), or somewhere else
(e.g. a corrupted buffer-pool page mapping for `global/2676`).

The kernel already records the answer in `siginfo_t.si_addr` (the
faulting address) and in `ucontext_t.uc_mcontext.gregs[REG_*]` (the
saved register file at the moment of the trap). The Step-3dd shim
just doesn't emit either.

## Decision

Extend `tools/segv_backtrace/segv_backtrace.c` to write two new lines
before the existing symbolic backtrace, on every SIGSEGV the shim
catches:

```
[GOOPG_SEGV_BACKTRACE] si_addr=0x<16 hex>
[GOOPG_SEGV_BACKTRACE] regs: RDI=0x… RSI=0x… RDX=0x… RAX=0x… RIP=0x… RSP=0x…
```

The six registers chosen are the SysV-AMD64 calling-convention slots
that almost always identify the immediate cause:

- `RDI`, `RSI`, `RDX` — function arguments 1..3.
- `RAX` — return value of the most recent call.
- `RIP` — exact instruction that trapped (subtract the .so base and
  feed to `objdump -d` to confirm the disassembled mnemonic).
- `RSP` — stack pointer (useful for distinguishing a stack-overflow
  segfault from a heap-pointer deref).

For the current investigation: if `si_addr == RDI` and `RDI` is
outside the canonical user-space range, the bad pointer was the
leaf-side `NameData *`. If `si_addr == RSI`, the bad pointer was
the scan-key side (most likely a `Datum` constructed from
`MyProcPort->user_name`).

## Implementation

### Shim source

`tools/segv_backtrace/segv_backtrace.c` adds:

- `static void hex16(uint64_t, char[16])` — async-signal-safe nibble
  encoder.
- `static void write_reg(const char *label, size_t labellen, uint64_t)`
  — writes `<label>0x<16 hex>` via two `write(2)` calls. No
  `strlen`, no `printf`, no allocation.
- New header includes: `<stdint.h>` for `uint64_t`,
  `<sys/ucontext.h>` (guarded by `#if defined(__x86_64__)`).

The handler body adds three writes for `si_addr` (always emitted, even
on non-x86_64), then under `#if defined(__x86_64__)`, six `write_reg`
calls for `RDI/RSI/RDX/RAX/RIP/RSP` pulled from
`uc->uc_mcontext.gregs[REG_*]`.

All writes remain async-signal-safe — only `write(2)`, stack-resident
buffers, and the literal byte strings. No new state, no new threads,
no new fds.

### Build/embed sync

`internal/testutil/pgcluster/segv_backtrace_src.txt` is the embedded
copy compiled by `ensureSegvBacktraceSO`. The new shim source was
copied byte-for-byte into the embed; the existing pin
`TestSegvBacktraceSourceMatchesToolsCopy` re-locks the two files.

`ensureSegvBacktraceSO` derives its cache filename from
`sha256(segvBacktraceSource)[:16]`, so the new bytes automatically
trigger a re-compile of `libsegv_backtrace_<newhash>.so`. No manual
hash update is required — the hash is computed at build time.

### Regression coverage

`TestEnsureSegvBacktraceSOBuilds` was extended (not replaced) to
assert the new lines actually appear in the shim's stderr output:

- `si_addr=0x0000000000000000` — exact match, because the helper does
  `int *p=0;*p=1;` which makes `si_addr` exactly `NULL`.
- `regs:` plus every label in `{" RDI=0x", " RSI=0x", " RDX=0x",
  " RAX=0x", " RIP=0x", " RSP=0x"}` — labels are exact-match (the
  leading space prevents accidentally matching e.g. `tRDI=` somewhere
  else in a symbol).

Register *values* are deliberately not pinned: `RIP`/`RDI` are
call-site-specific and would force the test to track compiler / glibc
output. The label-presence pin is the right level for a
diagnostic-only shim — what we care about is that the data plumbing
works, not the specific hex digits emitted on a given libc build.

## Tests

```
go test -count=1 -run 'TestSegvBacktrace|TestEnsureSegvBacktraceSOBuilds|TestAppendLDPreload' -v ./internal/testutil/pgcluster/
```

All four tests PASS, including the four new label assertions and the
`si_addr=0x…0000` exact-match. `make ralph-state-guard` PASS.

## Out of scope

- Non-x86_64 architectures: the `regs:` line is gated by
  `__x86_64__`. `si_addr` is emitted on every architecture (it lives
  in `siginfo_t`, not `ucontext_t`). The CI host and the goopg E2E
  cluster both run x86_64, so this is acceptable.
- E2E re-run + crash attribution: that is the named work for the
  *next* step (3dg), which will read the captured `si_addr` and
  `RDI`/`RSI` from `tmp/m0106-step3df/e2e_run1.log` and decide
  whether the bad pointer is the leaf side, the scan-key side, or a
  buffer-pool mapping problem.
- Decoding `RIP` to a symbol+offset inside the shim: the existing
  `backtrace_symbols_fd` output already covers symbolic resolution.
  `RIP` here is the precise faulting instruction (which
  `backtrace_symbols_fd` rounds to the nearest function entry), and
  the caller can feed it to `addr2line -e postgres`.
