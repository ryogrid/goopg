#!/usr/bin/env python3
"""ralph-devin-guard-adapter.py - Devin CLI PreToolUse hook for the Ralph guards.

Devin CLI reads Claude-style hooks, but its tool names differ (exec, edit,
write, notebook_edit, write_to_process, mcp_call_tool), so the Claude matchers
in .claude/settings.json (Bash, Edit|Write|..., mcp__serena__.*) never fire
under Devin. This adapter is wired from .devin/hooks.v1.json and rebuilds a
Claude-shaped payload for the existing guards:

  exec              -> Bash{command}          -> ralph-bash-guard.sh
  write_to_process  -> denied (typed text) / allowed (control keys only)
  edit              -> Edit{...}              -> ralph-file-guard.sh
  write             -> Write{...}             -> ralph-file-guard.sh
  notebook_edit     -> NotebookEdit{...}      -> ralph-file-guard.sh
  mcp_call_tool     -> mcp__serena__<tool>    -> ralph-file-guard.sh
                       (serena servers only; the arguments are defined by the
                       MCP server, so they are identical across hosts)

The guard's deny JSON (hookSpecificOutput.permissionDecision = "deny") is
printed unchanged; Devin honors that format. Active only when RALPH_LOOP=1.

Devin-specific rules enforced here (no Claude equivalent):
  - exec.workdir inside a harness directory (.git, .githooks, .claude, .codex,
    .devin, .ralph, scripts/lib, ~/.ralph) is denied: the bash guard matches
    paths textually, so a bare file name run from such a directory would
    slip past it.
  - write_to_process text is denied (text typed into a running shell can be
    split into fragments that each look harmless); only control keys in
    angle-bracket notation (e.g. <C-c>, <CR>) are allowed.
  - Guard infrastructure failures (guard missing, crash, timeout) and serena
    edit calls whose arguments cannot be read are denied: Devin runs with
    --permission-mode dangerous, so this hook is the only pre-execution check.
Tool calls outside the policed set are allowed; the git-layer
.githooks/pre-commit checks remain the final defense.
"""

import datetime
import json
import os
import re
import shlex
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
BASH_GUARD = os.path.join(HERE, "ralph-bash-guard.sh")
FILE_GUARD = os.path.join(HERE, "ralph-file-guard.sh")
# Devin's hook timeout is 10s (.devin/hooks.v1.json); stay below it.
GUARD_TIMEOUT_SEC = 9

# Repo-relative directories an exec workdir may not point into.
HARNESS_DIRS = (".git", ".githooks", ".claude", ".codex", ".devin", ".ralph",
                "scripts/lib")

# write_to_process bytes_input made only of angle-bracket key names.
CONTROL_KEYS_RE = re.compile(r"^(\s*<[A-Za-z0-9_+-]+>\s*)+$")

# Serena tools that write files (mirrors SERENA_EDIT in ralph_protected_regions.py).
SERENA_EDIT = {
    "replace_content", "create_text_file", "replace_symbol_body",
    "insert_after_symbol", "insert_before_symbol", "rename_symbol",
    "safe_delete_symbol", "replace_in_files", "replace_lines", "delete_lines",
    "insert_at_line", "execute_shell_command",
}

REMEDIATION = ("If this step is really needed, write an escalation block into "
               "the task and mark it [!] instead of working around the guard.")


class Deny(Exception):
    """Raised by translate() for a denial decided by the adapter itself."""


def project_root():
    for var in ("DEVIN_PROJECT_DIR", "CLAUDE_PROJECT_DIR"):
        val = os.environ.get(var, "")
        if val and os.path.isdir(val):
            return val
    try:
        out = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                             capture_output=True, text=True, timeout=5)
        if out.returncode == 0 and out.stdout.strip():
            return out.stdout.strip()
    except Exception:
        pass
    return os.getcwd()


def resolve(path, cwd):
    if not isinstance(path, str) or not path:
        return ""
    if os.path.isabs(path) or path.startswith("~"):
        return path
    return os.path.normpath(os.path.join(cwd, path))


def _inside(path, base):
    try:
        return os.path.commonpath([path, base]) == base
    except ValueError:
        return False


def check_workdir(workdir, root):
    """Return the resolved workdir, or raise Deny if it is a harness dir."""
    if not isinstance(workdir, str) or not workdir:
        return root
    path = workdir
    if path.startswith("~"):
        path = os.path.expanduser(path)
    if not os.path.isabs(path):
        path = os.path.join(root, path)
    real = os.path.realpath(path)
    real_root = os.path.realpath(root)
    home_ralph = os.path.realpath(os.path.expanduser("~/.ralph"))
    bases = [home_ralph] + [os.path.join(real_root, d) for d in HARNESS_DIRS]
    for base in bases:
        if _inside(real, base):
            raise Deny("RALPH_LOOP guard (Devin): exec workdir %s is inside a "
                       "harness directory (%s). Run commands from the project "
                       "root and name paths explicitly. %s"
                       % (workdir, base, REMEDIATION))
    return real


def env_prefix(env):
    """Render exec's env object as shell assignments so the bash guard sees
    them exactly as it would see 'VAR=value cmd' (e.g. RALPH_LOOP=,
    GIT_CONFIG_* overrides)."""
    if not isinstance(env, dict):
        return ""
    parts = []
    for key, val in env.items():
        parts.append("%s=%s" % (key, shlex.quote("" if val is None else str(val))))
    return " ".join(parts) + " " if parts else ""


def translate(data):
    """Return (guard_script, claude_payload) or None when not evaluable."""
    tool = data.get("tool_name") or ""
    ti = data.get("tool_input") or {}
    if not isinstance(ti, dict):
        return None
    root = project_root()

    if tool == "exec":
        command = ti.get("command")
        if not isinstance(command, str):
            return None
        cwd = check_workdir(ti.get("workdir"), root)
        return BASH_GUARD, {
            "tool_name": "Bash",
            "tool_input": {"command": env_prefix(ti.get("env")) + command},
            "cwd": cwd,
        }

    if tool == "write_to_process":
        # Typed text can be split into fragments that each pass the bash
        # guard, so it is not evaluated piecemeal: run commands with exec
        # (optionally with shell_id) instead. Control keys stay allowed so a
        # hung process can still be interrupted.
        text = ti.get("text_input")
        raw = ti.get("bytes_input")
        if isinstance(text, str) and text != "":
            raise Deny("RALPH_LOOP guard (Devin): write_to_process text input "
                       "is not allowed in the loop. Run the command with the "
                       "exec tool instead (pass shell_id to reuse a shell).")
        if isinstance(raw, str) and raw != "" and not CONTROL_KEYS_RE.match(raw):
            raise Deny("RALPH_LOOP guard (Devin): write_to_process bytes_input "
                       "may only contain control keys such as <C-c> or <CR>. "
                       "Run commands with the exec tool instead.")
        return None

    if tool == "edit":
        path = resolve(ti.get("file_path"), root)
        if not path:
            return None
        return FILE_GUARD, {
            "tool_name": "Edit",
            "tool_input": {
                "file_path": path,
                "old_string": ti.get("old_string", ""),
                "new_string": ti.get("new_string", ""),
                "replace_all": bool(ti.get("replace_all")),
            },
            "cwd": root,
        }

    if tool == "write":
        path = resolve(ti.get("file_path"), root)
        if not path:
            return None
        return FILE_GUARD, {
            "tool_name": "Write",
            "tool_input": {"file_path": path, "content": ti.get("content", "")},
            "cwd": root,
        }

    if tool == "notebook_edit":
        path = resolve(ti.get("notebook_path"), root)
        if not path:
            return None
        return FILE_GUARD, {
            "tool_name": "NotebookEdit",
            "tool_input": {"notebook_path": path},
            "cwd": root,
        }

    if tool == "mcp_call_tool":
        server = ti.get("server_name") or ""
        name = ti.get("tool_name") or ""
        if "serena" not in str(server).lower() or not isinstance(name, str) or not name:
            return None
        args = ti.get("arguments")
        if args is None:
            args = {}
        elif isinstance(args, str):
            try:
                args = json.loads(args) if args.strip() else {}
            except ValueError:
                args = None  # unreadable: handled as a non-object below
        if not isinstance(args, dict):
            if name in SERENA_EDIT:
                raise Deny("RALPH_LOOP guard (Devin): serena %s arguments could "
                           "not be read, so the edit cannot be checked. Retry "
                           "with an arguments object." % name)
            return None
        return FILE_GUARD, {
            "tool_name": "mcp__serena__" + name,
            "tool_input": args,
            "cwd": root,
        }

    return None


def deny(reason, tool, subject):
    print(json.dumps({"hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "permissionDecision": "deny",
        "permissionDecisionReason": reason,
    }}, separators=(",", ":")))
    audit(tool, subject)


def audit(tool, subject):
    """Best-effort denial audit line, same file as the Claude guards."""
    try:
        log_dir = os.path.join(project_root(), "ci", "logs")
        os.makedirs(log_dir, exist_ok=True)
        line = "%s tool=devin-adapter rule=%s subject=%s\n" % (
            datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
            tool, " ".join(str(subject).split())[:200])
        with open(os.path.join(log_dir, "ralph-guard-denials.log"), "a") as f:
            f.write(line)
    except Exception:
        pass


def main():
    if os.environ.get("RALPH_LOOP") != "1":
        return 0
    try:
        data = json.load(sys.stdin)
    except Exception:
        return 0
    if not isinstance(data, dict):
        return 0
    tool = str(data.get("tool_name") or "")
    ti = data.get("tool_input")
    try:
        job = translate(data)
    except Deny as exc:
        deny(str(exc), tool, json.dumps(ti)[:200])
        return 0
    if job is None:
        return 0
    script, payload = job
    try:
        proc = subprocess.run([script], input=json.dumps(payload),
                              capture_output=True, text=True,
                              timeout=GUARD_TIMEOUT_SEC)
    except Exception as exc:
        # Devin has no other pre-execution check (dangerous mode), so a guard
        # that cannot run blocks the call instead of silently allowing it.
        deny("RALPH_LOOP guard (Devin): guard %s could not run (%s); the "
             "call is blocked. Retry once; if it persists, escalate."
             % (os.path.basename(script), type(exc).__name__),
             tool, json.dumps(ti)[:200])
        return 0
    out = (proc.stdout or "").strip()
    if '"deny"' in out:
        print(out)
    elif proc.returncode != 0:
        deny("RALPH_LOOP guard (Devin): guard %s failed (exit %d); the call "
             "is blocked. Retry once; if it persists, escalate."
             % (os.path.basename(script), proc.returncode),
             tool, json.dumps(ti)[:200])
    return 0


if __name__ == "__main__":
    sys.exit(main())
