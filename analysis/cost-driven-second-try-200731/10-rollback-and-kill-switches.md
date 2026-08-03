# 10 — Rollback and kill switches

## 1. Design rule: every stage must be revertible without reverting the stage below it

The stages in [09](09-staged-implementation-plan.md) are deliberately ordered so that each one
is independently revertible:

| stage | revert action | blast radius |
| --- | --- | --- |
| −1 (packer key-count guard) | revert 2 lines | restores the pre-existing silent-corruption path (so: revert only with evidence) |
| 0 (cascade slot fixes) | revert the commit | executor-only, no plan change |
| 1 (fusion scaffolding, off) | nothing to revert — the switch is off | zero |
| 2 (fusion enabled) | flip the switch | zero code change |
| 4 (MHJ default off) | flip `mhjPackingEnabled` back | plan shapes revert; snapshots must revert too |

## 2. The three independent kill switches

### KS1 — the fusion switch (Stages 1-2)

```
GOOPG_RUNTIME_JOIN_FUSION=1                   -- env, process-wide, DEFAULT off
```

Read once per `Build` into `buildEnv.fusion`; `off` ⇒ immediate `return nil, false` ⇒ the
`*planner.Join` arms of both builders execute byte-identically to pre-Stage-1 code.

Naming: this is a **goopg-private** switch and must not be mistakable for a PG one — hence the
`GOOPG_` prefix, matching `GOOPG_COST_DRIVEN_JOINORDER` (`internal/planner/bushy.go:18`). The
repository's convention that a GUC's `BootVal` must equal the PG 18 default does not bind here
because PG has no such setting.

> **Corrected after review (finding F3).** The first draft specified a **session-level GUC**
> (`goopg_runtime_join_fusion`) and justified it by "A/B a query in one psql session without
> restarting the server, which is the only way to hold server age constant". **That is not
> reachable at the decision site.** `Build(plan planner.Node)`
> (`internal/executor/executor.go:21`) and `buildRec` (`:424`) receive neither a session nor a
> `*Context` — the context only arrives at `Open`. Only a process/env-scoped switch can be
> read there.
>
> **Consequence for benchmarking, stated plainly:** a fusion A/B requires two server starts,
> so `CLAUDE.md`'s "hold server age constant" hygiene must be satisfied by *matched* fresh
> starts and matched warm-up, not by toggling inside one session. Budget for that in Stage 2.
>
> If a session GUC is judged essential, it requires either the `buildEnv` plumbing to also
> carry a session snapshot taken at parse/plan time, or moving the decision to `Open` (which
> [04 §2](04-fusion-site-and-data-structures.md) rejected and C9 depends on). Do not assume it
> is free.

### KS2 — the minimum-levels threshold

```
GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS=3        -- DEFAULT 3, process-wide (see F3 above)
```

Raising it to a large number is a soft kill (fusion never qualifies) that still exercises the
predicate code, which is useful when triaging whether a regression is in the *decision* or in
the *operator*.

### KS3 — `mhjPackingEnabled` (Stage 4)

`internal/planner/bushy.go:580`, toggled by `SetMHJPackingEnabled` (`:582-587`). Stage 4 flips
its default to `false`; reverting the default restores today's behaviour exactly, because
Stage 4 explicitly does **not** delete the node or the operator.

## 3. Diagnosis switch (not a kill switch)

```
GOOPG_FUSION_UNDER_ANALYZE=1
```

Forces fusion on even when `instrumentScope` has timing enabled ([06 §2](06-explain-and-plan-shape.md)).
Default off. Never set in a gate. When set, the top node prints an extra
`Runtime Fusion: N levels (timings are pipeline-attributed)` line so the output cannot be
mistaken for PG-shaped text.

## 4. Rollback triggers — the conditions that mandate flipping a switch

Flip KS1 to `off` immediately, without debate, on any of:

- any **DS05** row-count or checksum delta against
  `bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt`;
- any **SPOT** mismatch (Q12 ≠ 2 or Q13 ≠ 35);
- any **DIFF** failure (fused output ≠ unfused output, as ordered text);
- any new hang or OOM in a query that previously completed;
- any pg-regress diff that was not present before.

Flip KS3 back on any of:

- a TPC-H SF1 sweep regression greater than the Stage 0c measurement predicted;
- a plan diff at Stage 4 that cannot be explained as "one `Multi-Way Hash Join (N tables)`
  node expanded into N−1 `Hash Join` nodes over the same scans".

## 5. What a rollback must also do

A switch flip restores behaviour but not knowledge. Every rollback must be accompanied by:

1. the failing artefact preserved under
   `analysis/cost-driven-second-try-200731/evidence/` (the sweep report, the diff, the plan);
2. a row appended to the risk register ([08](08-risk-register.md)) naming which mitigation
   failed;
3. a decision recorded on whether the stage is retried or abandoned.

Doc 15 is the model here: it records an implemented-and-reverted design *with its measurements*
and is consequently the single most useful document in the cost-model set. A rollback that
leaves no artefact costs the project the same measurement twice.

## 6. Ordering constraint with the concurrent Ralph loop

A Ralph loop is continuously editing `internal/planner/` and `internal/executor/`. Every stage
here touches both. Implement each stage in a **git worktree off clean HEAD**, stage by explicit
pathspec, never `git add -A`, and re-run the stage's own named guard test after any rebase or
handoff — the repository's own recorded lesson is that a resumed uncommitted diff may build
but fail its own guard.
