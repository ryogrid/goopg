# Self-improvement for the goopg Ralph harness

**Decision:** Feasible, with a small goopg-specific adaptation of Harness²'s method. Do not point the released Harness² CLI at the live Ralph checkout. Run candidate harnesses in isolated worktrees, judge them with independent goopg checks, and promote only reviewed, reproducible improvements. A lighter local controller is the recommended first implementation; a Harness² adapter is viable if its orchestration and experience library become worth the integration cost.

**Planning horizon:** The ordinary, broad PostgreSQL-compatibility Ralph loop **after** the temporary plan-parity and Executor focus ends. The current priority banner, special task ranking, and M0137–M0147 rules are observed as present constraints, not proposed as the future optimization target. This study does not change any running loop, instruction, hook, or gate.

| File | Purpose |
| --- | --- |
| [01-feasibility.md](01-feasibility.md) | What Harness² actually supplies, what goopg already has, and the integration decision. |
| [02-architecture.md](02-architecture.md) | Proposed control flow, editable surfaces, isolation, and promotion protocol. |
| [03-evaluation-and-rollout.md](03-evaluation-and-rollout.md) | Replay tasks, measurements, acceptance criteria, phased implementation, and failure handling. |

Source inspection: local `/home/ryo/work/harness2` at `1cfe254` (Google Research [repository](https://github.com/google-research/harness2/tree/1cfe254)); goopg at `483b19112a3c3c053a7120a52ffe21ff774ebe06` on `plan-parity-with-pg-take2-ralph2`, 2026-10-25. The local working tree had unrelated WIP, so observations about live policy are snapshots, not promises about its future form.
