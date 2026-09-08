# R13 — the mechanical three, and a Gather the knob does not control

*Round 13 of `../TODO.md`. Findings round: nothing committed as code,
for a reason stated in §3.*

## 1. The generated artefact: solved, but not committable yet

`TestFlagProvenanceEnvIsGenerated` needs
`go run ./cmd/gen-planner-flag-labels > scripts/planner-flags.env`. Run
under the flip it produces exactly one line of change:

```
-GOOPG_PLANNER_FLAG_LABEL_GOOPG_GATHER_PATHS='unset(off)'
+GOOPG_PLANNER_FLAG_LABEL_GOOPG_GATHER_PATHS='unset(all)'
```

**It must land WITH the flip, not before it.** The file records what the
default *is*; committing `unset(all)` while the default is still `off`
would make the provenance stamp lie — and this stamp is what bench
reports cite to say which planner configuration produced a number.
Recorded here so R14 does it in the right order rather than discovering
the coupling.

## 2. The finding: `SetGatherPathsMode("off")` did not produce a serial plan

`TestC19fPathModelGatherExecutesAsAParallelHashJoin` fails on:

> *"mode off produced a Gather; the control arm is not serial and proves
> nothing"*

The control arm is explicit — `optimizer.SetGatherPathsMode("off")`,
which resolves through `gatherPathModeFromEnv("off")` to
`gatherPathsOff` under the new resolver as well as the old one (checked;
"off" is an explicit case, not the default arm). So the knob **was** off
and a `Gather` appeared anyway.

The only other producer of a Gather is the **post-pass**
(`stampParallelScan` / `MaybeAddGather`), which walks an already-built
tree and is not gated by `GOOPG_GATHER_PATHS` at all. That is consistent
with K16: goopg has two independent parallelism mechanisms, and this
test's control arm assumes the knob disables *parallelism*, when it only
disables *partial paths*.

**Why this is not filed as "just update the test":** the assertion is
load-bearing. Its comment says *"Any row difference is a wrong answer,
not a slow plan"* — it exists to give the parallel arm a serial baseline
to be compared against. If no setting produces a serial plan for this
fixture, the test cannot do its job, and the right fix may be to give
the post-pass its own off switch rather than to weaken the assertion.
That is a design question, not an edit.

## 3. Why nothing was committed

The provenance change is correct only in the presence of the flip (§1),
and the flip is still reverted. The control-arm question (§2) needs a
decision about the post-pass, not a test edit. Committing either half
alone would leave the tree stating something untrue.

So: tree green at the shipping default, nothing half-applied, and the
two items are now understood rather than merely listed.

## 4. Status of R10's failing set

15 (R10) → 8 (R11) → 7 (R12) → **7**, with the composition now fully
characterised:

| # | class | disposition |
|---|---|---|
| 1 | `TestFlagProvenanceEnvIsGenerated` | solved; lands with the flip (§1) |
| 2 | `TestC19fPathModelGatherExecutesAsAParallelHashJoin` | needs a post-pass decision (§2) |
| 3 | `TestPartialPathIsNeverTheFinalPath` | superseded stage pin; mechanical |
| 4–7 | Slice3 ×2, `TestSplitEqualityForHashMultiKey`, `TestOwnedBuildPoisonPrebuiltBoundary` | **real plan movement**; adjudicate against PG |

## 5. Filed

- **R14 — adjudicate items 4–7 against PG**, decide item 2's post-pass
  question, then land flip + provenance + stage pin together in one
  commit, and run R10 `DESIGN.md` §5's gates.
