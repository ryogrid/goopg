#!/usr/bin/env python3
"""SessionStart concurrency guard (kaizen T7 / R5).

Encodes the [[concurrent_ralph_loops_corrupt_tree]] lesson as a guard instead of
a hope: two writers on one working tree clobber each other's edits and shared
.ralph state. At SessionStart this checks for running ralph loops and, if found,
prints a prominent warning that Claude Code injects as session context.

WARN-ONLY by design: it always exits 0 and never blocks a session — a false
positive must not be able to stop legitimate work. It only surfaces the risk so
the operator/agent can yield.

False-positive fix (2026-06-06): `ralph_loop.sh` in --live mode runs claude via a
pipeline whose first stage is the `portable_timeout` *shell function*:

    portable_timeout … claude … | tee out.log | jq … | tee live.log

Bash runs a shell function at the head of a pipeline in a forked SUBSHELL, and
that subshell keeps the script's argv ($0 = ralph_loop.sh). So a single healthy
loop shows up as TWO `pgrep -af ralph_loop.sh` lines — the real loop plus its
pipeline subshell (whose PPID *is* the real loop). The old guard counted lines
and screamed "MULTIPLE loops — stop the extras", paralysing the loop for many
iterations. We now count only *independent* loops (PPID is not itself a
ralph_loop.sh) and stay silent when the only loop is this session's own ancestor.
"""

from __future__ import annotations

import os
import subprocess
import sys


def _ppid_of(pid: int) -> int:
    """Parent PID of `pid` via /proc (Linux); 0 if unknown. Robust to spaces in
    the comm field by parsing after the closing paren."""
    try:
        with open(f"/proc/{pid}/stat", "r") as fh:
            data = fh.read()
        # stat: "pid (comm) state ppid ..." — comm may contain spaces/parens.
        after = data[data.rfind(")") + 1 :].split()
        # after[0] = state, after[1] = ppid
        return int(after[1])
    except (OSError, ValueError, IndexError):
        return 0


def ralph_loop_procs() -> list[tuple[int, int, str]]:
    """(pid, ppid, cmdline) for actual `bash .../ralph_loop.sh` processes.

    Excludes shells that merely mention the string as an argument (grep/editor).
    """
    try:
        out = subprocess.run(
            ["ps", "-eo", "pid=,ppid=,args="],
            capture_output=True,
            text=True,
            timeout=5,
        )
    except (subprocess.SubprocessError, OSError):
        return []

    procs: list[tuple[int, int, str]] = []
    self_pid = os.getpid()
    for line in out.stdout.splitlines():
        line = line.rstrip()
        parts = line.split(None, 2)
        if len(parts) < 3:
            continue
        try:
            pid, ppid = int(parts[0]), int(parts[1])
        except ValueError:
            continue
        args = parts[2]
        if pid == self_pid:
            continue
        if "ralph_loop.sh" not in args:
            continue
        # Keep only genuine bash invocations of the script, not e.g. a grep whose
        # argument happens to contain "ralph_loop.sh".
        if "/bin/bash" in args or "ralph_loop.sh --" in args or args.startswith("bash"):
            procs.append((pid, ppid, line.strip()))
    return procs


def _ancestor_pids() -> set[int]:
    """Every ancestor PID of the current process (walk PPIDs up to init)."""
    seen: set[int] = set()
    pid = os.getppid()
    guard = 0
    while pid > 1 and pid not in seen and guard < 64:
        seen.add(pid)
        pid = _ppid_of(pid)
        guard += 1
    return seen


def independent_loops(procs: list[tuple[int, int, str]]) -> list[tuple[int, int, str]]:
    """Top-level loops only: drop pipeline subshells whose parent is itself a
    ralph_loop.sh process (the --live `portable_timeout | tee | jq | tee` fork)."""
    pids = {pid for pid, _, _ in procs}
    return [p for p in procs if p[1] not in pids]


def main() -> int:
    procs = ralph_loop_procs()
    if not procs:
        return 0

    independent = independent_loops(procs)
    ancestors = _ancestor_pids()
    # Loops driving *this* very session are not a hazard to it — they ARE us.
    external = [p for p in independent if p[0] not in ancestors]

    if not external:
        # Either the only loop is this session's own driver (normal autonomous
        # operation) or all detected entries were pipeline subshells. No hazard.
        return 0

    n = len(external)
    print("⚠️  CONCURRENCY GUARD: a ralph loop is already running on this tree.")
    print(f"   {n} independent ralph_loop.sh process(es) detected (excluding this session's own):")
    for _, _, line in external[:5]:
        print(f"     {line}")
    print("   Two writers on one tree corrupt each other's edits + shared .ralph state.")
    if n >= 2:
        print("   MULTIPLE independent loops are running — stop the extras before doing any work.")
    print("   If you are an interactive session: do NOT edit the working tree until the")
    print("   loop is stopped (verify with `pgrep -af ralph_loop.sh`).")
    # Always non-blocking.
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception:
        # A guard must never break SessionStart.
        sys.exit(0)
