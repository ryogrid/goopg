# M0147-0049 — retire `GOOPG_INDEXKEY_HARVEST` (unnest index-key harvest switch)

Status: landed 2026-10-27.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 2 (default-ON lever, delete the
OFF arm). Lever count 3 → 2 (`scripts/lever-count.sh`). Inventory row: `analysis/m0147/m0147-0030/INVENTORY.md`.

## What was retired

goopg's sublink pull-up (`internal/optimizer/unnest.go`) decorrelates an IN/EXISTS sublink only when every outer
reference is accounted for as an equijoin. By the time it runs, goopg may already have absorbed the correlation
equality into an inner IndexScan or BitmapIndexScan probe key. `harvestIndexKeyParams` recovers those keys as
`unnestParams`, as if they had been written as Filter conjuncts. `GOOPG_INDEXKEY_HARVEST=off` skipped the harvest,
which left the correlation unaccounted, so the sublink stayed a per-row SubPlan.

PG pulls IN/EXISTS sublinks up on the parse tree (`pull_up_sublinks`, `postgres/src/backend/optimizer/prep/prepjointree.c`)
before any index choice exists. The harvest undoes an artefact of goopg's ordering, so ON is the PG-shaped arm.

## Measurement first

The entry required re-measuring reachability after M0147-0013, which confined the harvest to IN/EXISTS. It also
required re-checking the 2026-09-05 "not plan-neutral" note. The census with `GOOPG_INDEXKEY_HARVEST=off`
(`analysis/m0147/m0147-0049/`) matched TPC-H 22/22, TPC-DS SF0.25 99/99 and SF1 99/99. Its EXPLAIN text is
byte-identical to ON on all three corpora: 0 differing lines.

The 2026-09-05 note was a pinned-stats runtime A/B: Q4 and Q22 ran faster with the harvest off; Q2, Q17 and Q20 ran
faster with it on. It no longer describes any plan difference, and under banner S9 (5a) runtime is never a reason to
keep an arm.

## Removed / changed

- `unnest.go`: `indexKeyHarvestOn`, its env read in `init`, `indexKeyHarvestFromEnv`, `SetIndexKeyHarvestEnabled`,
  the gate at the top of `harvestIndexKeyParams`, and the `os` import.
  - The long flag comment, with its July and September runtime tables, is condensed to a short history pointing
    here.
- `flaglabels.go`: the resolver row is deleted, the order-list entry is marked retired, and the variable moves to
  `flagProvenanceRetired` (`retired(M0147-0049)`). The `GOOPG_EXISTS_TO_ANY` row's alignment padding is dropped now
  that it is the only one-line row. `scripts/planner-flags.env` is regenerated.
- `docs/wiki/modules/optimizer.md`: the two table rows are removed.

`deadcode ./cmd/...` was diffed before and after. `SetIndexKeyHarvestEnabled`, already test-only, left the
unreachable list; nothing was added.

## Tests

- Six ON pins in `unnest_indexkey_test.go` and two in `internal/executor/nli_semi_residual_exec_test.go` are deleted.
- `TestNLISemiResidualMatchesSubPlanPath` and `TestScalarProbeCheapPolicyResultsAgree` compare the decorrelated plan
  against the SubPlan plan, each against a hard-coded answer. The SubPlan arm now comes from the test-only
  `SetSubqueryUnnestEnabled(false)`, restored through `t.Cleanup`. That is what the harvest's OFF arm amounted to for
  these shapes.

## Inert proof (AGENT.md C6)

`git grep` over `scripts`, `ci`, `Makefile`, `.githooks`, bench shell and env files, `AGENT.md`, `CLAUDE.md` and
`docs/wiki` (after the row removal) finds no setter. Unset, the harvest was on, which is the only arm left. The
fire-set A/B and the census confirm no plan change.

## Gates

See the M0147-0049 entry in `.ralph/fix_plan.md`.
