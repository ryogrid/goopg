#!/usr/bin/env python3
"""Stop-hook background-task guard (kaizen: bg-task-loss).

Problem this defends against
----------------------------
The Ralph loop drives Claude Code in headless ``-p`` mode. In that mode a Bash
task started with ``run_in_background: true`` is KILLED ~5 s after the agent
emits its final result, and the "re-invoke me when the task exits" notification
only fires in *interactive* sessions. So when the in-loop agent launches a test
gate (``go test`` / ``tpch-spotcheck.sh`` / ``ralph-precommit-test.sh`` /
``pgbench``) in the background and then ends its turn "waiting for the
notification", the gate dies unobserved and the next loop re-runs it from
scratch (this once burned four consecutive loops on the identical command).

What this hook does
-------------------
Registered as a ``Stop`` hook. When the agent tries to finish a turn, it checks
whether any *workload* process (a test gate / build / benchmark) is still alive
underneath the claude session. If so, it returns
``{"decision": "block", "reason": ...}`` telling the agent to wait for that PID
in the FOREGROUND and read its result before finishing — turning the "fire and
forget" mistake into a "fire and foreground-wait" that actually observes the
gate within the same turn.

Design guarantees
-----------------
* FAIL-OPEN: any exception, missing input, unknown ancestry, or exceeded block
  budget results in ``exit 0`` (stop proceeds). A guard must never be able to
  wedge a session.
* SCOPED: only sessions whose process ancestry contains ``ralph_loop.sh`` are
  ever blocked. Interactive sessions structurally lack that ancestor and take a
  sub-millisecond fast path to ``exit 0``.
* BOUNDED: a per-(session, claude-PID) counter caps the number of consecutive
  blocks at ``N`` (< Claude Code's built-in 8-block cap), so the loop can never
  hang even if the agent ignores the guidance. The counter file name embeds the
  claude PID, which changes every loop iteration, so the budget resets each loop
  despite ``--resume`` reusing one ``session_id``.

Reuses the /proc-walk patterns from the sibling ``concurrency_guard.py``.
"""

from __future__ import annotations

import json
import os
import sys
import time

# Number of consecutive Stop blocks allowed per (session_id, claude_pid) before
# we fail open. Kept below Claude Code's built-in 8-consecutive-block cap so we
# always yield first. Each block lets the agent foreground-wait up to
# BASH_MAX_TIMEOUT_MS, so 6 blocks covers a deep serial gate stack.
DEFAULT_MAX_BLOCKS = 6

STATE_DIR = "/tmp/ralph_bg_guard"
LOG_PATH = os.path.expanduser("~/.ralph/logs/bg_task_guard.log")

# Process comms that are NEVER a test/verification gate on their own — the
# agent's own tooling / interpreters. These are infra regardless of cmdline.
ALWAYS_INFRA_COMMS = {
    "node",
    "claude",
    "jq",
    "tee",
    "ps",
    "sleep",
    "tail",
    "uv",
    "uvx",
    "gopls",
}
# cmdline substrings that identify the agent's own MCP / LSP / hook
# infrastructure (a process of any comm carrying one of these is infra). Kept
# specific: a bare "mcp" would wrongly match a real gate like
# `go test ./internal/mcp/...`, so we anchor on the actual infra invocations.
INFRA_CMDLINE_SUBSTR = (
    "serena",
    "mcp-server",
    "mcp__",
    "modelcontextprotocol",
    "any-script",
    "language_server",
    "language-server",
    "shell-snapshots",
    "bg_task_guard.py",
    "concurrency_guard.py",
    "route.py",
    "gopls",
)

# cmdline substrings that positively identify a project test/verification gate.
WORKLOAD_CMDLINE_SUBSTR = (
    "tpch-spotcheck",
    "ralph-precommit-test",
    "pg-regress-runner",
    "pg-oracle-diff",
    "goopg-test-run",
    "race-gate",
    "pgbench",
)
# Process comms that are a gate on their own. NOTE: a bare `goopg` server is
# deliberately NOT here — servers are long-lived infrastructure the agent may
# intentionally keep alive across a turn (the manual-server-test workflow), not
# a result-producing gate. The gate scripts that spin up a goopg server
# (tpch-spotcheck, ralph-precommit-test) are caught by their own cmdline while
# the script itself runs.
WORKLOAD_COMMS = {"make", "pgbench"}


# --------------------------------------------------------------------------- #
# /proc helpers — every one is defensive: a PID that vanishes mid-scan must
# never raise out of these.
# --------------------------------------------------------------------------- #
def _stat_of(pid: int) -> tuple[str, str, int]:
    """Return (comm, state, ppid) for ``pid`` via /proc; ("", "", 0) on failure.

    Robust to spaces/parens in the comm field by splitting after the last ')'.
    """
    try:
        with open(f"/proc/{pid}/stat", "r") as fh:
            data = fh.read()
        lparen = data.find("(")
        rparen = data.rfind(")")
        comm = data[lparen + 1 : rparen] if lparen != -1 and rparen != -1 else ""
        after = data[rparen + 1 :].split()
        state = after[0] if len(after) > 0 else ""
        ppid = int(after[1]) if len(after) > 1 else 0
        return comm, state, ppid
    except (OSError, ValueError, IndexError):
        return "", "", 0


def _ppid_of(pid: int) -> int:
    return _stat_of(pid)[2]


def _cmdline_of(pid: int) -> str:
    """Full command line (NULs -> spaces); falls back to the comm in brackets."""
    try:
        with open(f"/proc/{pid}/cmdline", "rb") as fh:
            raw = fh.read()
        if raw:
            return raw.replace(b"\x00", b" ").decode("utf-8", "replace").strip()
    except OSError:
        pass
    comm, _, _ = _stat_of(pid)
    return f"[{comm}]" if comm else ""


def _ancestors() -> list[int]:
    """Ordered ancestor PIDs from the immediate parent up to init (max 64)."""
    chain: list[int] = []
    pid = os.getppid()
    seen: set[int] = set()
    guard = 0
    while pid > 1 and pid not in seen and guard < 64:
        chain.append(pid)
        seen.add(pid)
        pid = _ppid_of(pid)
        guard += 1
    return chain


def _argv0_base(cmdline: str) -> str:
    """Basename of argv[0] from a space-joined cmdline (best-effort)."""
    if not cmdline:
        return ""
    first = cmdline.split(" ", 1)[0]
    return os.path.basename(first)


# --------------------------------------------------------------------------- #
# Scoping: only fire under a ralph loop; locate the claude session process.
# --------------------------------------------------------------------------- #
def under_ralph_loop(ancestors: list[int]) -> bool:
    if os.environ.get("RALPH_BG_GUARD_TEST") == "1":
        return True
    for pid in ancestors:
        if "ralph_loop.sh" in _cmdline_of(pid):
            return True
    return False


def find_claude_root(ancestors: list[int]) -> int | None:
    """Nearest ancestor that is the claude CLI process.

    Matches both a bare ``claude`` argv0 and a ``node .../claude-code/cli.js``
    invocation. An explicit override (``RALPH_BG_GUARD_ROOT_PID``) supports
    testing without a real claude parent.
    """
    override = os.environ.get("RALPH_BG_GUARD_ROOT_PID")
    if override:
        try:
            return int(override)
        except ValueError:
            return None
    for pid in ancestors:
        comm, _, _ = _stat_of(pid)
        cmdline = _cmdline_of(pid)
        if comm == "claude" or _argv0_base(cmdline) == "claude":
            return pid
        if "claude-code" in cmdline and (comm == "node" or "cli.js" in cmdline):
            return pid
    return None


# --------------------------------------------------------------------------- #
# Descendant enumeration and workload classification.
# --------------------------------------------------------------------------- #
def _all_pids() -> list[int]:
    out: list[int] = []
    try:
        for name in os.listdir("/proc"):
            if name.isdigit():
                out.append(int(name))
    except OSError:
        return []
    return out


def descendants(root: int, exclude: set[int]) -> list[int]:
    """BFS over the process tree from ``root``; never descend into ``exclude``.

    ``exclude`` carries this hook's own subtree (the wrapper shell + python) so
    the guard never reports itself as a running workload.
    """
    children: dict[int, list[int]] = {}
    for pid in _all_pids():
        ppid = _ppid_of(pid)
        if ppid:
            children.setdefault(ppid, []).append(pid)

    result: list[int] = []
    stack = [c for c in children.get(root, []) if c not in exclude]
    seen: set[int] = set()
    while stack:
        pid = stack.pop()
        if pid in seen or pid in exclude:
            continue
        seen.add(pid)
        result.append(pid)
        stack.extend(children.get(pid, []))
    return result


def is_infra(comm: str, cmdline: str) -> bool:
    # An always-infra tool (node/claude/jq/gopls/...) is infra regardless of its
    # cmdline; anything (e.g. a python interpreter) whose cmdline names our own
    # MCP/LSP/hook infrastructure is infra too.
    if comm in ALWAYS_INFRA_COMMS:
        return True
    if any(s in cmdline for s in INFRA_CMDLINE_SUBSTR):
        return True
    return False


def is_workload(comm: str, cmdline: str) -> bool:
    argv0 = _argv0_base(cmdline)
    # `go test` / `go build` / `go vet`
    if argv0 == "go" or comm == "go":
        return True
    # Compiled test binaries (e.g. catalog.test)
    if comm.endswith(".test") or argv0.endswith(".test"):
        return True
    # Known gate scripts / benchmarks by cmdline
    if any(s in cmdline for s in WORKLOAD_CMDLINE_SUBSTR):
        return True
    # Servers / build driver / benchmark by comm
    if comm in WORKLOAD_COMMS:
        return True
    # Wrapper shells running a repo script
    if comm in ("bash", "sh") and "scripts/" in cmdline:
        return True
    return False


def live_workloads(root: int, exclude: set[int]) -> list[tuple[int, str]]:
    """(pid, truncated-cmdline) for each live, non-zombie workload descendant."""
    found: list[tuple[int, str]] = []
    for pid in descendants(root, exclude):
        comm, state, _ = _stat_of(pid)
        if not comm or state == "Z":  # skip vanished / zombie
            continue
        cmdline = _cmdline_of(pid)
        if is_infra(comm, cmdline):
            continue
        if is_workload(comm, cmdline):
            found.append((pid, cmdline[:80]))
    return found


# --------------------------------------------------------------------------- #
# Block-budget counter (per session_id + claude PID).
# --------------------------------------------------------------------------- #
def _counter_path(session_id: str, root: int) -> str:
    safe = "".join(c if c.isalnum() or c in "-_" else "_" for c in session_id)[:80]
    return os.path.join(STATE_DIR, f"{safe}.{root}.count")


def _prune_old_state() -> None:
    """Best-effort removal of counter files older than 24 h."""
    try:
        now = time.time()
        for name in os.listdir(STATE_DIR):
            path = os.path.join(STATE_DIR, name)
            try:
                if now - os.path.getmtime(path) > 86400:
                    os.unlink(path)
            except OSError:
                pass
    except OSError:
        pass


def _read_count(path: str) -> int:
    try:
        with open(path, "r") as fh:
            return int(fh.read().strip() or "0")
    except (OSError, ValueError):
        return 0


def _write_count(path: str, n: int) -> None:
    try:
        with open(path, "w") as fh:
            fh.write(str(n))
    except OSError:
        pass


def _log(msg: str) -> None:
    try:
        os.makedirs(os.path.dirname(LOG_PATH), exist_ok=True)
        ts = time.strftime("%Y-%m-%d %H:%M:%S")
        with open(LOG_PATH, "a") as fh:
            fh.write(f"{ts} {msg}\n")
    except OSError:
        pass


def _max_blocks() -> int:
    try:
        return int(os.environ.get("RALPH_BG_GUARD_MAX_BLOCKS", DEFAULT_MAX_BLOCKS))
    except ValueError:
        return DEFAULT_MAX_BLOCKS


# --------------------------------------------------------------------------- #
# Main
# --------------------------------------------------------------------------- #
def main() -> int:
    # 1. Parse Stop-hook stdin JSON. Bad/missing input -> fail open.
    try:
        payload = json.load(sys.stdin)
    except (json.JSONDecodeError, ValueError, OSError):
        return 0
    if not isinstance(payload, dict):
        return 0
    session_id = str(payload.get("session_id") or "unknown")

    # 2. Only ever act under a ralph loop (interactive fast path).
    ancestors = _ancestors()
    if not under_ralph_loop(ancestors):
        return 0

    # 3. Locate the claude session process.
    root = find_claude_root(ancestors)
    if root is None:
        return 0

    # 4. Build the exclusion set = this hook's own chain up to (not incl.) root,
    #    plus this process. Everything below `root` but above us is the wrapper
    #    shell that launched the hook.
    exclude: set[int] = {os.getpid(), os.getppid()}
    # Exclude the hook's own wrapper chain (everything between us and root). Only
    # do this when root is genuinely in our ancestry; with a test ROOT_PID
    # override that is NOT an ancestor, walking to the (never-hit) root would
    # wrongly exclude the entire chain — including the real session — so we
    # fall back to excluding just this process and its parent.
    if root in ancestors:
        for pid in ancestors:
            if pid == root:
                break
            exclude.add(pid)

    # 5. Any live workload under the session?
    workloads = live_workloads(root, exclude)
    counter = _counter_path(session_id, root)
    if not workloads:
        # Nothing running — clear the budget so intermittent matches don't stack.
        try:
            os.unlink(counter)
        except OSError:
            pass
        return 0

    # 6. Enforce the block budget (fail open at the cap).
    try:
        os.makedirs(STATE_DIR, mode=0o700, exist_ok=True)
    except OSError:
        return 0  # can't track budget safely -> fail open
    _prune_old_state()
    n = _read_count(counter)
    cap = _max_blocks()
    pids_desc = ", ".join(f"{p} {c}" for p, c in workloads[:6])
    if n >= cap:
        _log(f"CAP-EXCEEDED session={session_id} root={root} blocks={n}/{cap} "
             f"workloads=[{pids_desc}] -> failing open")
        sys.stderr.write(
            f"bg-task-guard: block budget {n}/{cap} exhausted; allowing stop "
            f"despite live workloads [{pids_desc}].\n"
        )
        return 0

    # 7. Block and tell the agent exactly how to recover.
    n += 1
    _write_count(counter, n)
    _log(f"BLOCK session={session_id} root={root} block={n}/{cap} "
         f"workloads=[{pids_desc}]")

    reason = (
        f"bg-task-guard: {len(workloads)} workload process(es) still running "
        f"under this session: [{pids_desc}]. In headless -p mode these are "
        f"KILLED when you finish and their results are lost (the next loop "
        f"re-runs the same gates). Before finishing: wait for them in a "
        f"FOREGROUND Bash call, e.g. `tail --pid=<PID> -f /dev/null` with a "
        f"large timeout (up to 3600000 ms), then read the gate's output/log and "
        f"act on it. Do NOT wait for background-task notifications — they never "
        f"arrive in -p mode. If you must abandon a gate, kill the PID and record "
        f"the command + log path + PID state under 'In-flight:' in "
        f".ralph/working_set.md. (block {n}/{cap})"
    )
    print(json.dumps({"decision": "block", "reason": reason}))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception:
        # A guard must never break Stop.
        sys.exit(0)
