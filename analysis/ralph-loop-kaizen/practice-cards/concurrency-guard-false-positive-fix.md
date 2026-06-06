# Concurrency Guard — false-positive fix (2026-06-06)

- **Status:** accepted
- **Date:** 2026-06-06
- **Component:** `analysis/ralph-loop-kaizen/practice-cards/concurrency_guard.py` (SessionStart hook, kaizen T7 / R5)
- **Related:** memory `concurrent_ralph_loops_corrupt_tree`; kaizen `07-implementation-status.md` (T7 row)

## Problem

The T7 concurrency guard warns at `SessionStart` when more than one Ralph loop
is writing to the working tree (the [[concurrent_ralph_loops_corrupt_tree]]
hazard). The original implementation counted lines from
`pgrep -af ralph_loop.sh`: if `n >= 2` it printed
*"MULTIPLE loops are running — stop the extras before doing any work."*

On 2026-06-06 that alarm fired on a **single healthy loop**, paralysing seven
consecutive loop iterations (#1–#7) into reporting `BLOCKED` with no actual
second writer present.

## Root cause

`ralph_loop.sh` runs Claude in `--live` mode as a pipeline whose first stage is
the `portable_timeout` **shell function** (`ralph_loop.sh:1558`):

```bash
portable_timeout ${timeout_seconds}s "${LIVE_CMD_ARGS[@]}" \
    < /dev/null 2>"$stderr_file" | tee "$output_file" | jq --unbuffered -j "$jq_filter" | tee "$LIVE_LOG_FILE"
```

Bash runs a **shell function at the head of a pipeline in a forked subshell**,
and that subshell keeps the script's argv (`$0 = ralph_loop.sh`). So one live
loop produces **two** `pgrep -af ralph_loop.sh` lines:

```
1118056  42347    /bin/bash …/ralph_loop.sh --live --verbose   ← the real loop
1199204  1118056  /bin/bash …/ralph_loop.sh --live --verbose   ← pipeline subshell (PPID = real loop)
   └─ 1199210 timeout 86400s claude …   ← the ONE writer
```

The decisive tells that this is one loop, not two:

1. The subshell's **PPID is itself a `ralph_loop.sh`** (the real loop). A
   genuinely independent loop is parented by an interactive shell / init.
2. There is exactly **one** `timeout … claude` writer process in the whole tree.

The memory log already noted "don't trust the bare `pgrep` count — resolve ppid"
(entries #76/#106/#220), but the guard script itself never did.

## Fix

`concurrency_guard.py` now:

1. Collects `(pid, ppid, cmdline)` for every real `bash …/ralph_loop.sh`
   process via `ps -eo pid=,ppid=,args=`.
2. Keeps only **independent** loops — those whose PPID is **not** itself a
   `ralph_loop.sh` in the set. This collapses the `--live` pipeline subshell
   into its parent.
3. Walks **this session's own ancestor PIDs** (`/proc/<pid>/stat` field 4) and
   excludes the loop that is driving the current session — a loop's own Claude
   child must not be warned about itself.
4. Warns only on what remains, and says "MULTIPLE" only when `>= 2`
   independent external loops survive.

Behaviour is unchanged in the dimension that matters: it is still **warn-only**
(always exits 0; a false positive must never block legitimate work).

## Verification

- Run from inside the live loop (sole loop is this session's own ancestor):
  **no output** — false positive eliminated.
- Run with a decoy `setsid /bin/bash /tmp/ralph_loop.sh --live` whose PPID is
  not a Ralph loop and which is not an ancestor of the session: **warns**
  ("1 independent ralph_loop.sh process(es) detected …") — real protection
  intact.

## When the real hazard returns

If two genuinely independent loops run on one tree (the 2026-05-25 incident),
the guard fires correctly and the protocol in
`concurrent_ralph_loops_corrupt_tree` applies: no mutation/commit, no
`make ralph-state-guard` (its auto-repair clobbers a live peer's shared state),
no killing the peer (policy-blocked), report `BLOCKED`, and the human collapses
to one loop with `pkill -f ralph_loop.sh`.
