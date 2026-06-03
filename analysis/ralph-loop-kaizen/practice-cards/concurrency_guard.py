#!/usr/bin/env python3
"""SessionStart concurrency guard (kaizen T7 / R5).

Encodes the [[concurrent_ralph_loops_corrupt_tree]] lesson as a guard instead of
a hope: two writers on one working tree clobber each other's edits and shared
.ralph state. At SessionStart this checks for running ralph loops and, if found,
prints a prominent warning that Claude Code injects as session context.

WARN-ONLY by design: it always exits 0 and never blocks a session — a false
positive must not be able to stop legitimate work. It only surfaces the risk so
the operator/agent can yield.

This is exactly the situation that caused trouble before: an interactive session
(or a second loop) starting while a ralph loop is live.
"""

from __future__ import annotations

import subprocess
import sys


def running_ralph_loops() -> list[str]:
    """PIDs+cmdlines of live ralph_loop.sh processes (pgrep can't self-match us:
    this process is python, not ralph_loop.sh)."""
    try:
        out = subprocess.run(
            ["pgrep", "-af", "ralph_loop.sh"], capture_output=True, text=True, timeout=5
        )
    except (subprocess.SubprocessError, OSError):
        return []
    lines = []
    for line in out.stdout.splitlines():
        # Exclude any shell that merely contains the string as an argument (e.g.
        # a grep/editor), keep only actual `bash .../ralph_loop.sh` invocations.
        if "ralph_loop.sh" in line and ("/bin/bash" in line or line.strip().split()[1:2] == []):
            lines.append(line.strip())
        elif "ralph_loop.sh --" in line:
            lines.append(line.strip())
    return lines


def main() -> int:
    loops = running_ralph_loops()
    if loops:
        n = len(loops)
        print("⚠️  CONCURRENCY GUARD: a ralph loop is already running on this tree.")
        print(f"   {n} ralph_loop.sh process(es) detected:")
        for line in loops[:5]:
            print(f"     {line}")
        print("   Two writers on one tree corrupt each other's edits + shared .ralph state.")
        if n >= 2:
            print("   MULTIPLE loops are running — stop the extras before doing any work.")
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
