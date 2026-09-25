# Handover: opencode → Claude Code (2026-09-10, plan-parity workstream)

Reader: a coding agent continuing the `plan-parity-with-pg-take2`
program. Goal: goopg generates PG18.3-identical plans for all
executable TPC-H + TPC-DS queries (identical stats/costing/logic;
never forced shapes; slower time is NOT a regression).

## 1. Session state (read this first)

- Branch: `plan-parity-with-pg-take2`. HEAD `4b0c97e52` (R47 rev-3
  design, APPROVED-WITH-NOTES).
- **Uncommitted WIP (yours to finish): R47 slice 1 implementation
  + TDD tests.** `git status` shows exactly three code files as
  `M` from this session:
  - `internal/optimizer/groupingpaths.go` (per-candidate Agg
    spec clones, 3 sites)
  - `internal/optimizer/partialaggupper.go` (same, 5 sites)
  - `internal/optimizer/upperordered_test.go` (2 appended
    dominance tests, both PASSING)
  - Slice-1 gate PASSED: byte-identical A/B both corpora
    (TPC-DS: only Q36/70/86 PID-noise; TPC-H: zero changes).
  - DO NOT stage/commit: `.claude/settings.local.json`,
    `opencode.json`, `.gitignore`, `.claude.json`
    (host-harness/other-worker artifacts, not mine).
- Servers (all mine, all clean `r46` binary unless noted):
  `:5552` TPC-H clone (`/tmp/pp2/tpch`, oc-base binary),
  `:5553` TPC-DS SF0.5 clone (`/tmp/pp2/ds05oc`),
  `:5554` TPC-H clone (`/tmp/pp2/tpch-oc`).
  **Never touch `:5545`** (foreign lane). PG references
  `:65432`/`:65438` were DOWN on arrival; I started `:65432`
  for measurements, then STOPPED it — restart via
  `bench/tpch/setup_pg.sh` (no `--reset`!) when needed.
- Program scoreboard (canonical data, pinned env
  `work_mem='64MB'`, `max_parallel_workers_per_gather=4`):
  TPC-H `match=1 shapediff=19`; TPC-DS `match=1 (Q9)
  shapediff=69`. Captures: `/tmp/pp2/oc-tpch-r46c.txt`,
  `/tmp/pp2/oc-ds05-r46c.txt`; PG fixtures:
  `bench/tpch/plans-pg/`, `bench/tpcds/plans-pg/`.

## 2. R46 recap (shipped `de85a10a7`; context for R47)

Cost the legacy funnel's index-vs-seq choice (`costIndexScan`
vs `costSeqscan` at `planIndexScanFromWhereShape` equality arm
+ the absorber `eqKey` rewrite in `scan_input_rewrite.go` —
the second producer was found live: funnel declined yet EXPLAIN
still indexed). TPC-DS Q9 → MATCH (program's first).
Carve-out: reg*-identifier arrays skip the gate (sequential
reg*[] comparison broken twice over — filed as **K100**).
5 new unit pins; 5 IOS/executor fixtures re-baselined to 2000
distinct rows + ANALYZE in vacuum-then-ANALYZE order (ANALYZE
in the seeding txn is a no-op; TOAST compresses width-pads —
scale via row counts).

## 3. R47 status: rev-3 design APPROVED-WITH-NOTES, slice 1
##    done+gated, slice 2 NOT implemented

Design: `docs/design/not_ralph/plan_parity_fix_take2/
r47-q4-upper-rel/DESIGN.md` (+ `pg-flips/` with five live PG
captures). Target: TPC-H Q4
(`[aggregation-strategy, sort-strategy, qual-placement]`, no
join-order). Rev 1 REJECTED (11 findings) and rev 2 REJECTED
(narrow) — history in take2 `TODO.md` under R47; do not
relitigate, the verdicts are recorded.

### Step-0 findings (all measured, traces removed, tree clean)

- Q4's semi is a legacy `tryBuildNLI`
  `*NestedLoopIndexJoin`, UNSTAMPED (`CostSet=false`; bypasses
  the `createPlanNode` stamp funnel). Search builds zero
  nested-loop paths for Q4 (traced `addNLIPaths`/
  `addNestLoopPath` silent).
- `1141.32 = Derive(Filter_wrapper[NLI], 57066)` =
  NLI_derive (570.66 — no children arm in
  `legacyDisplayChildren`) + perRow (570.66). The render reads
  the collapsed Filter wrapper. HashAgg startup 1283.98 =
  1141.32 + 0.0025×57066, exact.
- Width flip (448 vs 64, same 9-col Output): per-column type
  widths differ per winning parent. Open mechanism,
  verdict-neutral (N1).
- Semi selectivity 1.0 acknowledged, not owned.
- Firing micro-rule UNIDENTIFIED after exhaustive analysis —
  the design is plumbing+measurement, no predicted flip.

### PG flip experiments (re-measured live 2026-09-10, archived)

With pinned GUCs on `:65432`, Q4: default→sorted (70122.64),
sort-off→hashed (69911.66), hashagg-off→sorted,
no-ORDER-BY→sorted, LIMIT-5→sorted. Work_mem sweep
(1MB/4MB/256MB) identical. Fixture Q4 is stale-serial; live is
parallel (Gather Merge — needs the K96/K97 execution program,
out of scope). Round targets fixture-serial shape; K9-compliant
framing (fixture = movement detector, NO verdict claim).

### Slice 2 recipe (mapped, not coded — highest value next step)

All facts verified from code this session; implement in this order:

1. **Translation helper** (new): grouping emission order →
   output-coordinate pathkeys. Guard mirrors the executor
   exactly (`Strategy==Sorted && GroupingSets==nil &&
   len(GroupExprs)>0 && Mode==Simple`,
   `operators_join_agg.go:2222`). Child must be `*Sort`;
   emit the leading run of child Sort keys that are bare group
   keys (membership via `exprEqual` — same input space, sound),
   mapped to output positions by (Name, SourceTableIdx) with
   uniqueness required (self-join ambiguity → decline);
   direction/nulls from child keys. Non-ColumnRef keys,
   index/presorted variants → nil (fail-closed, today's
   behavior). Rationale: `ColumnRef` identity is positional
   (`col:{Index}`, `exprwalk.go:668`) so input-coord group keys
   NEVER match output-coord ORDER BY keys without translation —
   this is THE load-bearing fact; no-sort cannot fire without it.
2. **Ordered loop**: at planSelect's normal ORDER BY arm only
   (not SRF post-sort), when `agg != nil`, the ordered input
   node IS `agg.node` (pointer equality — fail-closed
   otherwise), and the grouped rel holds ≥2 `PathAgg` survivors
   with ZERO `PathFinalizeAgg` (parallel-split guard):
   for each survivor, shallow-copy the Path with translated
   Pathkeys and call existing `addOrderedPaths` on the ORDERED
   rel; `setCheapest` + `getCheapestFractionalPath` elect (no
   new comparator); build winner via `createPlanNode`.
3. **Copy-back update**: descend final winner through `*Sort`
   only to the topmost `*Aggregate`; `*agg.node = *found` +
   re-run `stampAggregateInputTarget(agg.node, nil)` (B-01c
   keep depends on Child — the winner's Child differs by
   strategy; skipping this ships wrong narrowings).
   Non-Sort-wrapped or missing → leave grouping state (safe).
4. **Dominance pins first (TDD, already passing):**
   `TestOrderedDropsHashedSortForPresortedGrouping` (translated
   pathkeys → hashed+Sort dropped) and
   `TestOrderedStacksSortWithoutTranslatedPathkeys`
   (untranslated → hashed wins, documents the gap) in
   `upperordered_test.go`. Both green on current tree.
5. **Gates:** per-query census with ZERO EXTRA flips (no
   previously-matching query newly diverges); shape-delta
   alongside; units + suites + TPC-H spotcheck + SF0.5 sweep
   `MISMATCH=0`; byte-guard A/B both corpora. Final-selection
   rule is cheapest-total (no LIMIT → tupleFraction 0 —
   stated in design); `ConsiderStartup=false` on upper rels
   (no LIMIT) is what lets COSTS_EQUAL fall through to
   pathkeys — do NOT "fix" that.
6. If nothing flips: the census (which candidates survive
   where) DIAGNOSES the real rule for the follow-up —
   candidates are native-vs-sort pathkey semantics,
   grouping-level pruning, `can_hash`-adjacent gating.
   Either outcome passes; inventing a rule fails.

### Review notes already applied to rev 3

Cite fixes (`internal/` prefixes, line numbers), PG inner
sort gate (`planner.c:~5355-5365`) + broader-offer deviation
declared, work_mem claim softened (no artefact file),
§3.3 sharpened (census must eliminate ≥1 hypothesis),
"no Aggregate arm is added" wording. Open review threads to
honor: M0129-S1 tie-break declaration, sort-disabled stamping,
Memoize census on Q4, estimate re-baseline IF Q4 moves,
inode-verified serving binary (K91).

## 4. Traps and hygiene (learned this session)

- **Build hygiene:** one `go build` produced a binary with
  corpus-wide width shifts (cause undetermined); `go clean
  -cache` + rebuild reproduced HEAD-like behavior. Always
  verify SERVING behavior (probe a known plan), not just the
  inode — K91's check is necessary but not sufficient.
- **Unexplained transient (on record):**
  `TestIOS_CompositeInt4Int4` passed 4× at 3 rows mid-session,
  then failed 20/20 on the same tree (suspected stale test
  binary in a stash-pop window). Current state deterministic.
- **Servers:** never share a data dir between two servers
  (launcher stops any server on the dir — I once killed `:5553`
  by probing `:5556` on the same dir; relaunched clean).
  `launch-verified.sh` verifies listener inode; unique cgroup
  scope per launch; foreground execution for long runs.
- **Tests:** never `git commit --no-verify`; never `-count=1`
  in gates (one-off probes only). Unit/component gate:
  `RALPH_PRECOMMIT_SCOPE=units
  scripts/ralph-precommit-test.sh`. Planner/executor changes
  additionally need `scripts/tpch-spotcheck.sh` + the SF0.5
  sweep (`scripts/tpcds-sf05-regression.sh sweep`, ~1h).
- **Git:** `area(scope): summary — detail` style; stage by
  explicit pathspec (concurrent WIP may be present — never
  `git add -A`); take2 TODO.md is the progress ledger.
- **Cadence per round:** TODO entry → Design Doc → agent
  review → `commit -n` + push → implement → report in same
  dir → `commit -n` + push. `match` count is NOT a success
  criterion (conjunction rule — every non-match differs in
  4–7 categories; judge by CATEGORY movement + zero EXTRA).
- **K9 binding:** never use `bench/tpch/plans-pg/` as a parity
  target (stale serial); owner waiver for fixture re-capture
  still pending — do not presuppose it.
- **R48 (independent, either order):** semi JoinQual placement
  + stray `Filter: (true)` drop (probe construction, no shared
  code with R47).
- **K100 (executor, open):** sequential reg*[] comparison
  (scalar-cast leak drops IsArray; OID-vs-name compare without
  catalog). R46 carve-out dies with it. Corpus impact zero.

## 5. Suggested order of work

1. Commit slice-1 as part of the R47 implementation commit
   (already gated byte-identical) — or land slice 2 first
   and commit once per cadence; your call, but do NOT lose
   the uncommitted slice-1 edits.
2. Implement slice 2 per §3 above (translation helper +
   ordered loop + copy-back update), TDD pins already green.
3. Gates (units, suites, spotcheck, SF0.5 sweep, A/B both
   corpora + census), REPORT.md, commit + push.
4. If Q4 flips: stretch achieved. If not: hypothesis-
   eliminating census → follow-up round (pathkey semantics
   is prime suspect).
5. Open threads if blocked: R48, K100, fixture re-capture
   waiver (owner = Ryo — ASK rather than assume).
