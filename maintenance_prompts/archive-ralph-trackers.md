# Maintenance: Archive completed work out of the Ralph tracker files

**Audience:** a Claude Code agent (Opus-4.8 class) asked to shrink the Ralph-loop tracker
files when they have grown too large for the loop to carry in context.

**What this does:** moves *completed* work out of the two live tracker files into
git-tracked archive directories, leaving only what a subsequent Ralph loop still needs
(open tasks + standing rules), and keeps the archives small enough to render on GitHub.

- `.ralph/fix_plan.md` — completed `- [x]` subtasks → `completed_milestones/completed_fix_plan_NNN.md`
- `.ralph/deferral_ledger.md` — `status = resolved` rows → `completed_defferral/completed_deferral_NNN.md`

Everything removed stays in git history, so trimming is safe. When in doubt, keep the item
(the user's standing preference here is **conservative**: never drop an open task or a
standing rule).

---

## Token-frugality mandate (READ FIRST)

`fix_plan.md` is ~800 KB and `deferral_ledger.md` is ~2.5 MB. **Never load either file whole
into your own context.** Do all bulk work with scripts (the two under
`maintenance_prompts/`) and inspect only counts, headers, and small samples. Delegate any
semantic reading to a cheap subagent (Haiku/Sonnet) rather than reading megabytes yourself.
During the reference run this whole task cost the main agent only small `grep`/`wc` outputs.

---

## Step 0 — Stop the Ralph loop first (mandatory)

The loop rewrites `fix_plan.md` mid-run; editing it while the loop is live corrupts both.
The loop is hosted under a **screen respawner** — killing only `ralph_loop.sh` lets the
respawner relaunch it. Kill the respawner first.

1. Map the tree (PIDs churn — re-derive every time):
   ```bash
   pgrep -af ralph_loop.sh
   pgrep -af mem_guard.py
   pgrep -af 'timeout 86400s claude'
   # for a loop PID, walk ppid up to its `screen` ancestor:
   ps -o pid=,ppid=,comm= -p <loop_pid>   # → bash → screen
   ```
   You will find: a **loop process group** (pgid = the main `ralph_loop.sh` pid; contains
   the loop, its subshell, `mem_guard.py`, and `tee`/`jq` helpers) and a separate **worker
   process group** (pgid = the `timeout 86400s claude` pid; contains the worker `claude`
   plus its MCP servers — serena, any-script, gopls).

2. ⚠️ **Identify and protect any *other* `claude`/`screen` session.** In the reference run a
   second, unrelated interactive `claude` lived under a different `screen`. Confirm its
   screen pid and claude pid, and **never** `pkill claude` or blanket-kill by pgid — a
   transient bash of the other session can match `pgrep -f 'ralph_loop.sh'` (it self-matches
   your own inspection command too). Kill only the two verified-pure ralph groups + the
   ralph screen, by explicit pid/pgid.

3. Kill order (SIGKILL), respawner first:
   ```bash
   kill -9 <ralph-screen-pids> <ralph-screen-child-bash>   # respawner
   kill -9 -<loop_pgid>                                     # loop group
   kill -9 -<worker_pgid>                                   # worker group + its MCP servers
   ```

4. Verify (exclude your own shell `$$`, which self-matches):
   ```bash
   ps -eo pid,args | grep -F '/home/ryo/.ralph/ralph_loop.sh' | grep -v grep | grep -vw $$
   pgrep -af mem_guard.py
   # confirm the OTHER claude session is still ALIVE
   ```
   `.ralph/status.json` should stop updating. **The user restarts the loop themselves** —
   do not relaunch it.

(See project memory `stopping_ralph_loop_screen_respawner`,
`interactive_vs_ralph_stop_stash_restore`, `goopg_manual_server_test_workflow`.)

Then confirm the working tree wasn't left mid-write:
`git status --porcelain .ralph/fix_plan.md .ralph/deferral_ledger.md` (usually clean — the
loop's churn is in other files; leave that WIP alone).

---

## Step 1 — Split the deferral ledger (pure script, 0 LLM tokens)

Run `maintenance_prompts/split_ledger.py` (deterministic; back up first). It:

- Keeps the header block (title/intro + `| status | … |` + `| :-- | … |` separator).
- Reads each `|`-delimited data row; **field 2 trimmed** is the status. Moves **exactly**
  `resolved` rows; keeps everything else (`-`, `open`, and edge statuses like `PARTIAL`,
  `ATTEMPTED, REVERTED`, `SUPERSEDED`) in `.ralph/deferral_ledger.md` (single file — the
  live append target for the loop; do NOT chunk it unless asked).
- Writes resolved rows to `completed_defferral/completed_deferral_NNN.md`, chunked to
  **≤ ~350 KB each** (GitHub renders markdown up to ~512 KB; stay under it), each a
  standalone table (its own H1 + note + header + separator). Rows are copied **verbatim** —
  do not unescape entity-escaped `<table>`/`<col>` (memory
  `deferral_ledger_raw_html_tag_nesting`).
- Writes `completed_defferral/README.md` index (part, rows, date range, size).
- Prints a status histogram + conservation check (`moved + kept == original data rows`).

```bash
cp .ralph/deferral_ledger.md /tmp/ledger.orig   # safety
python3 maintenance_prompts/split_ledger.py
```

Verify independently (don't trust only the script):
```bash
# 0 resolved rows left in the live ledger:
awk -F'|' 'NR>14&&/^\|/{s=$2;gsub(/ /,"",s);if(s=="resolved")c++}END{print c+0}' .ralph/deferral_ledger.md
# every chunk row is resolved, and total == moved count:
grep -c '^| resolved ' completed_defferral/completed_deferral_00*.md
```

---

## Step 2 — Archive completed fix_plan subtasks (script + count-based verify)

Run `maintenance_prompts/split_fixplan.py` (back up first). Rule — **conservative, top-level
only**:

- A movable unit is a **top-level (column-0) `- [x]` block**: its `- [x]` line through
  everything (including nested `  - […]` children) up to the next top-level `- ` bullet /
  `#` header / `---`.
- Move those blocks to `completed_milestones/completed_fix_plan_NNN.md` (grouped by their
  `##` milestone header, original order). Pick the next free `NNN`.
- **Keep** all prose, all standing rules (M-NIGHTLY's `<!-- … -->` comment, M0122's
  verify-before-implement / per-task-rule paragraphs), every open `- [ ]` block, and the
  entire **active-branch** milestone (in the reference run: `M0123` — excluded via a
  `KEEP_ENTIRE` list; update this to whatever the current branch milestone is).
- Where a milestone loses completed items, the script drops in one pointer note:
  `_(completed [x] subtasks archived → …)_`.

**Why top-level only:** checkboxes nest. A top-level `[x]` may contain nested `[x]` children
(move together — fine) but must never contain an open `[ ]` child (that would strand open
work). Guard for it:
```bash
grep -cE '^\s*- \[ \]' completed_milestones/completed_fix_plan_NNN.md   # MUST be 0
```
If it prints > 0, a nested open item got archived — open that block, move it back to the
live file, and re-check.

Verify (counts only — no full read):
```bash
grep -cE '^- \[[xX]\]' .ralph/fix_plan.md   # remaining top-level done == only the kept active milestone's
grep -cE '^- \[ \]'    .ralph/fix_plan.md   # open count UNCHANGED vs before
grep -cE '^## '        .ralph/fix_plan.md   # every milestone header still present
# conservation: original top-level checkbox count == kept + archived
```
Also diff the active milestone section before/after — it must be byte-identical.

If the archive file exceeds ~1 MB, split it into `_NNN.md` + `_NNN+1.md` (existing archives
already reach ~1 MB, so a single file is acceptable below that).

---

## Step 3 — Finish

- Update this doc's `KEEP_ENTIRE` note and the pointer-note text if conventions change.
- Report before/after sizes (`wc -l`, `du -h`) and the conservation numbers.
- Leave the edits **uncommitted** for the user unless they ask you to commit. If you do
  commit, do it **before** the loop is restarted (the loop rewrites `fix_plan.md`; an
  uncommitted restructure is fragile — memory `ralph_fixplan_driver_churn_defeats_edit`).
  Commit the moved-out archive dirs together with the trimmed live files.

## Recovery

Everything is in git. To restore a wrongly-moved block:
`git show HEAD:.ralph/fix_plan.md` / `git show HEAD:.ralph/deferral_ledger.md`, or restore
the pre-run backups you made in Step 1/2.

## Reference scripts

- `maintenance_prompts/split_ledger.py` — deferral-ledger splitter (Step 1).
- `maintenance_prompts/split_fixplan.py` — fix_plan archiver (Step 2).

Both take no arguments, hard-code the repo path at the top (edit if the checkout moves),
overwrite the live file in place, and print a verification summary. Read them before running.
