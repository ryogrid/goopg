# AGENTS.md - Codex / Devin Entry Point (Thin Wrapper)

This file exists because Codex and Devin CLI read `AGENTS.md`, not `AGENT.md`.
The canonical project instructions live in:

- `AGENT.md` - build, test, benchmark, and review conventions (authoritative).
- `CLAUDE.md` - agent bootstrap pointer; defers to `AGENT.md`.
- `.ralph/PROMPT.md` - Ralph loop contract, including the mandatory
  `---RALPH_STATUS---` block in the final message of every loop.

## Ralph Loop Notes for Codex and Devin Sessions

- The loop exports `RALPH_LOOP=1`. PreToolUse hooks enforce the same rules as
  the Claude backend: no writes to reference clusters, no gate bypass, no
  edits to instruction or harness files.
  - Codex: `.codex/hooks.json`. Project hook wiring requires explicit trust
    review (`/hooks`) before it takes effect; untrusted hooks are skipped.
  - Devin: `.devin/hooks.v1.json` routes Devin tool calls (`exec`,
    `write_to_process`, `edit`, `write`, `notebook_edit`, `mcp_call_tool`)
    through `scripts/ralph-devin-guard-adapter.py` to the same guards. The
    loop runs Devin with `--permission-mode dangerous` and no sandbox, so a
    denied tool call is final: do not retry it by other means.
- Owner-only files (escalate instead of editing): `CLAUDE.md`, the
  `PLAN-PARITY-HARNESS` section of `AGENT.md`, the `## Current Priority`
  banner of `.ralph/fix_plan.md`, and harness mechanism files
  (`scripts/ralph-*guard*`, `.githooks/`, `.claude/settings.json`,
  `.claude/settings.local.json`, `.codex/` hook wiring, `.devin/hooks.v1.json`,
  `.devin/config.json`, `.devin/config.local.json`, `.ralph/PROMPT.md`,
  `~/.ralph/`).
- The `.githooks/pre-commit` gate is backend-independent and always applies.
