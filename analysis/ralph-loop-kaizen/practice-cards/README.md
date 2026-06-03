# Practice cards (sample artifacts)

These are **proposals**, not yet wired into the harness. They demonstrate the
task-conditional practice-loading design in
[`../04-rules-and-practices.md`](../04-rules-and-practices.md).

- Each `*.md` card is a short, enforced-as-checklist distillation of knowledge
  that currently lives as always-on memory or milestone prose.
- `route.py` is the cheap, deterministic classifier: given the current top task
  and `git diff`, it prints only the relevant cards. It makes **no LLM call**,
  so per-loop overhead is ~zero.

## Try the router (read-only)

```bash
# auto-detect task type from the top open fix_plan task + staged/working diff
python3 analysis/ralph-loop-kaizen/practice-cards/route.py

# force a task type (useful for testing)
python3 analysis/ralph-loop-kaizen/practice-cards/route.py --task "executor planner Q9 join"
python3 analysis/ralph-loop-kaizen/practice-cards/route.py --explain
```

## Proposed wiring (NOT applied — `.ralph/` is protected, but settings.local.json is editable)

Add a `UserPromptSubmit` hook to `.claude/settings.local.json` so each loop gets
its task-relevant cards injected as additional context. See §4.2 of the parent
doc for the snippet and the validation caveat.
