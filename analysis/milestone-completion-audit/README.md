# Milestone completion audit

Tooling to audit whether milestones / tasks / subtasks the Ralph loop has marked
**complete** (or archived to `completed_milestones/`) are *actually* complete — i.e.
to surface work recorded as **DEFERRED / delegated / only PARTIAL** yet managed as
if it were done.

## `milestones_to_json.py` — preprocessing (this is the first step)

Converts the progress markdown into one hierarchical, machine-readable JSON so a
downstream detector (a separate, still-pending step) can reason over it.

```bash
source ../../venv/bin/activate        # repo-root venv; stdlib only, no pip installs
python milestones_to_json.py          # writes ./milestones_hierarchy.json
# options: --root <repo> (default: auto-detected two levels up)
#          --output <path> (default: ./milestones_hierarchy.json)
#          --no-ledger
```

### Inputs → output sections

| input | JSON section |
|-------|--------------|
| `.ralph/fix_plan.md` | `active` |
| `completed_milestones/*.md` (8 archives + `m0100-0005-progress-log.md`) | `completed` |
| `.ralph/deferral_ledger.md` | `deferral_ledger` |

### Model (hybrid tree + per-node annotations)

The structural tree goes only as deep as the markdown actually nests:
**milestone (H2 `## …`) → task (top-level checkbox) → subtask/slice (indented
checkbox, e.g. `M0020-S01`)**, sorted by the numeric key parsed from the id
(`M0118-0008` → `[118,8]`), falling back to document order for id-less prose tasks.

The agent-invented notions that exist only as *prose* — `loop` / `perm` /
`Part A/B/C` / date-stamped sub-releases — are **not** fabricated into tree levels.
They are captured as per-node annotation fields so nothing is lost:

- `status_flags` — raw signals: `complete` (checkbox `[x]` only), `completed_note`,
  `partial`, `deferred`, `delegated`, `promoted`, `blocked`, `follow_up`, `superseded`.
- `tags` — `loops` / `perms` / `slices` / `steps` extracted from the text.
- `inline_segments` — the `Part X` / date-stamped sub-releases split out of a task body.
- `dates`, `design_docs`, and the verbatim `raw_text`.

The deferral ledger is parsed losslessly (cells may contain literal `|`); `date` and
`task_id` are extracted as columns and the remainder kept verbatim in `rest`/`raw`.

The script prints a summary plus a preview of **checkbox-checked-but-also
deferred/delegated/partial** nodes — the candidate signal the downstream audit refines.

`milestones_hierarchy.json` is a generated artifact; re-run the script to refresh it.
