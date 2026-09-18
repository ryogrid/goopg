Task: M0141-S2b-10 — root-cause costAgg's Hashed-vs-Sorted formula gap for
  Q4/Q12 (Kind: recon). DONE and committed (`4fe2faa0c`). Selected per banner
  item 3 ("roll out fix1's success... M0141-S2b-6-resume") after confirming
  P0-E7 is `[x]` (no regressions filed) and item 2 (M0141-S2a-fix2r) already
  `[x]`.

Files (committed): `.ralph/fix_plan.md` (S2b-10 closed + S2b-11 filed),
  `docs/design/README.md` (new index row), `docs/design/0100-0149/
  m0141-s2b10-hashagg-sortagg-insertion-order.md` (new). No `.go` file
  touched — recon only.

Key symbols: `addGroupingPaths` (internal/optimizer/groupingpaths.go:379) —
  the bug site; `addPath`/`addToPathlist` (path.go:1067/1218) — the
  fuzzy-tie-break-by-insertion-order mechanism (PG-faithful, not itself
  buggy); PG's `add_paths_to_grouping_rel` (postgres/src/backend/optimizer/
  plan/planner.c:7113).

Finding (high confidence, proven by live trace + PG source read, not just
  inferred):
  - `costAgg`/`costSortRunWithWidth` are NOT the bug — term-by-term match
    against `cost_agg`/`cost_sort`, AND independently reproduced: forcing
    PG's own discarded HashAggregate alt into the open on `:65432`
    (`SET enable_sort=off`, session-scoped, no write) shows PG's OWN formula
    also prices Hashed cheaper for Q4 (-0.48%) and Q12 (-0.66%).
  - Real cause: both margins are inside PG's `STD_FUZZ_FACTOR` (1.01,
    already correctly ported as `stdFuzzFactor` in path.go). On a fuzzy tie
    with equal pathkeys, `addPath` keeps whichever candidate was inserted
    FIRST (matches PG's own documented add_path behavior). `addGroupingPaths`
    adds HASHED before SORTED (groupingpaths.go:406 then :463/:475); real PG
    adds SORTED (`can_sort` block, planner.c:7128) before HASHED (`can_hash`,
    :7286) — exactly inverted. Live trace: Sorted's `upper.ordered.input`
    entry is `verdict=dominated` right after Hashed's `upper.ordered.sort`
    was `accepted`.
  - Fix (NOT done this loop, Kind:recon discipline): swap the two blocks in
    `addGroupingPaths` (SORTED first, HASHED second). Pure reorder, no cost
    arithmetic change.

Next step: work **M0141-S2b-11** (`Kind: impl`, `Parent: M0141-S2b-10`,
  filed in fix_plan.md right after S2b-10's closing note) — the block reorder
  itself. Required gates per its own fix_plan entry: tpch-spotcheck (expect
  Q4/Q12 to flip Hashed->Sorted, matching PG — this is the desired effect,
  not a regression), tpcds-sf025 sweep + plan-shape diff (cross-check ANY
  other query that flips against a fresh PG EXPLAIN before accepting it),
  tpch-acceptance-arm digest, `go test ./internal/optimizer/...` (no
  -count=1), check-designdocs. Re-read the banner first — P0-E7 is `[x]`,
  item 2/3's other named tasks (fix2r, fix1-sweep, S2b-6-resume, M0139-0007c)
  are all `[x]`; S2b-11 is the sole remaining item-3-lineage task, so it is
  next unless the banner changed.

Gates run this loop: `python3 scripts/ralph_protected_regions.py
  check-designdocs` PASS; `python3 scripts/ralph-lineage-guard.py` PASS;
  `make ralph-state-guard` PASS; pre-commit pgbench smoke PASS (hook, on
  commit `4fe2faa0c`). No `go build`/`go test` needed (no `.go` touched).
  `scripts/tpch-estimate-audit-arm.sh` (one arm, Q4/Q12, private clone port
  5582) rc=0.

In-flight: none. No shared cluster written (`:65433` touched only via
  `pg_basebackup -X fetch` clone, never stopped; `:65432` touched only via
  read-only `EXPLAIN` + session-scoped `SET enable_sort=off`, no DDL/DML).
  Scratch artifacts left untracked (gitignored/precedent, not committed):
  `tmp/tpch-audit-m0141-s2b10-recon-20260918.server.log`,
  `analysis/leftdeep-joins/m0141-s2b10-recon-20260918*.txt`.
