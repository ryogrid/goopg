# Gate exceptions (OWNER-MAINTAINED)

`.githooks/commit-msg` treats a `SKIP-BLOCKED` gate stamp as a **failed gate**
unless a row below grants that exact task id that exact gate. **Only the owner
adds, edits or removes rows in this file; the loop may not edit it** — it is
whole-file protected by `scripts/ralph_protected_regions.py` (kind `harness`),
the `.githooks/pre-commit` staged check and the Ralph bash/file guards, exactly
like `CLAUDE.md`. This file exists because a one-task grant (`P0-E5`, while the
`:65433` TPC-H cluster was on hold) was re-used by the loop in 18 commits: the
hook auto-widened the exception for as long as `bench/tpch/runtime_goopg/data.HOLD`
existed, so a single owner decision silently became a standing waiver. A grant
now names one task, one gate, a hard commit budget and an expiry date, and each
use is recorded in `ci/logs/gate-exception-usage.log` (git-ignored) so the
budget cannot be exceeded.

A `SKIP-BLOCKED` stamp is accepted only when **all** of the following hold:

1. the commit message names a `task-id` listed below for that `gate`;
2. the commit body carries a `ledger:` line naming the deferral-ledger row for
   the blocker;
3. `expires` is today or later;
4. `ci/logs/gate-exception-usage.log` records fewer than `max-commits` prior
   usages for that (`task-id`, `gate`) pair.

`gate` must be exactly one of `tpch-spotcheck`, `tpch-acceptance-arm` or
`tpcds-sf025` (any other spelling makes the row inert). With **no active rows —
the current state — every `SKIP-BLOCKED` stamp is a hard commit failure.** A
blocked gate with no row is an escalation: mark the task `[!]` with an
escalation block naming the blocker.

| task-id | gate | max-commits | expires | reason |
| --- | --- | --- | --- | --- |
