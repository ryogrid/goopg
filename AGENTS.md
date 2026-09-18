# AGENTS.md - Codex Entry Point (Thin Wrapper)

This file exists because Codex reads `AGENTS.md`, not `AGENT.md`.
The canonical project instructions live in:

- `AGENT.md` - build, test, benchmark, and review conventions (authoritative).
- `CLAUDE.md` - agent bootstrap pointer; defers to `AGENT.md`.
- `.ralph/PROMPT.md` - Ralph loop contract, including the mandatory
  `---RALPH_STATUS---` block in the final message of every loop.

## Ralph Loop Notes for Codex Sessions

- The loop exports `RALPH_LOOP=1`. PreToolUse hooks (`.codex/hooks.json`)
  enforce the same rules as the Claude backend: no writes to reference
  clusters, no gate bypass, no edits to instruction or harness files.
- Project hook wiring (`.codex/hooks.json`) requires explicit trust review
  (`/hooks`) before it takes effect; untrusted hooks are skipped.
- Owner-only files (escalate instead of editing): `CLAUDE.md`, the
  `PLAN-PARITY-HARNESS` section of `AGENT.md`, the `## Current Priority`
  banner of `.ralph/fix_plan.md`, and harness mechanism files
  (`scripts/ralph-*guard*`, `.githooks/`, `.claude/settings.json`,
  `.codex/hook wiring`, `.ralph/PROMPT.md`, `~/.ralph/`).
- The `.githooks/pre-commit` gate is backend-independent and always applies.
