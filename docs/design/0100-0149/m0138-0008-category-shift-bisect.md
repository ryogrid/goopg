# M0138-0008 — bisecting the TPC-DS category shift M0138 caused

Status: accepted (landed 2026-09-15)

## Task

M0138-0005's corpus re-measure found TPC-DS `join-order` moved 89->90 and
`qual-placement` moved 16->17 between the milestone-filing baseline and the
post-M0138-0002/-0003/-0004 epoch, with no verdict-tuple change (still
`match=2 shapediff=69 missingnode=25 error=3`). The specific query was never
identified — deferred as a recon-budget cut, resume point "re-capture at the
pre-M0138-0002 commit and bisect". This task does that bisection.

## Method — four unpinned trials surfaced a second question before the first could be answered

Two git worktrees, built to standalone binaries:

- BEFORE = `0e97c94b3` (tip of M0138-0001, immediately before M0138-0002)
- AFTER = `44085c324` (M0138-0004, the last production-code commit of M0138
  before M0140 work landed — the same epoch M0138-0005 measured)

Only `internal/executor/operators_analyze.go` and the new
`analyze_block_sampler.go` differ between these two commits (`git show
--stat`) — no storage/catalog format change, so a private SF0.25 dataset
(freshly loaded from the shared, git-tracked `tpcds-data-sf025/` TSVs, on a
private port/datadir so the shared `:65437` cluster was never touched) could
be re-used for both binaries. PG reference plans came from the live `:65438`
oracle (byte-identical to the frozen `analysis/m0138/m0138-0005-epoch1-tpcds-pg.txt`
capture modulo filename-embedded diagnostics, ruling out PG-side drift as a
confound). Diffs via `scripts/pg-plan-parity-diff.py --verbose`.

**First result (unpinned, wall-clock-seeded ANALYZE, the default): the
direction reversed.** BEFORE measured `join-order=90 qual-placement=16`; AFTER
measured `join-order=89 qual-placement=15` — the *opposite* sign from
M0138-0005's 89->90/16->17. A second trial at each commit (fresh load,
same binary, same code) then showed `join-order` and `qual-placement`
disagreeing with the *first* trial at the identical commit: BEFORE's own two
trials read 90 and 90 with `qual-placement` 16 and 15; AFTER's two trials
read 89 twice but `qual-placement` 15 and 14. Per-query tag comparison
(`join -1 1 -2 1 <tags-before> <tags-before2>`, comparing which categories
tag each `Qn` line) named the cause directly: **`Q45` and `Q26` flip their
`join-order`/`scan-type`/`qual-placement` tags between the two BEFORE trials
alone — same commit, same binary, different ANALYZE run.** `Q48` flips
`join-method`/`qual-placement` between *both* same-commit trial pairs
(BEFORE-vs-BEFORE2 **and** AFTER-vs-AFTER2). `Q51` flips `aggregation-strategy`
and `Q97` flips `parallelism`, each within a same-commit pair. This is the
same mechanism M0138-0009 characterised for the scalar `correlation`
statistic (reservoir-sampler seed variance), now shown to also flip
**plan-parity category verdict tags** — the exact metric this milestone group
uses as its success criterion.

**The fix that was already sitting in the tree: `GOOPG_ANALYZE_SEED`.**
`operators_analyze.go:704-748` reads it once to seed the block/reservoir
sampler; `scripts/tpch-acceptance-arm.sh` and `scripts/estimate-parity-gate.sh`
already pin it (`GOOPG_ANALYZE_SEED=20260905`) for exactly this reason, but
**`scripts/capture-tpcds.sh` and the M0137-0003 canonical TPC-DS
baseline-capture procedure never set it.** Two fresh AFTER-commit trials with
`GOOPG_ANALYZE_SEED=20260905` pinned produced byte-identical captures
(`diff` shows only PID/filename lines) and identical `CATEGORIES:` lines —
confirming the knob eliminates the noise.

**With the seed pinned identically on both commits, the shift is real and
reproducible**, and matches M0138-0005's original direction (modulo a
different absolute baseline, since 0005's unpinned baseline predates this
task's pin): BEFORE = `join-order=89 qual-placement=15`, AFTER =
`join-order=90 qual-placement=16` (also `parallelism` 86->87, invisible in
0005's report). Per-query tag diff of the two *pinned* captures narrows this
to exactly three queries, and no others:

- **`Q21`** gains `join-order`+`join-method` (the `join-order`+1). BEFORE,
  goopg's plan already matched PG's leaf-set `{date_dim, inventory, item,
  warehouse}` at the relevant Hash Join and only aggregation-strategy/
  sort-strategy/parallelism diverged. AFTER, goopg restructures the join
  spine — `warehouse` moves out of the inner join tree PG keeps it in
  (`Partial HashAggregate/c-extra-pg/Seq Scan … present only on PG side: Seq
  Scan on warehouse`) — i.e. the improved block-sampler/reservoir statistics
  changed a cardinality estimate enough to flip which join order goopg's
  cost-based search selects.
- **`Q48`** loses `join-method` (BEFORE: Hash Join where PG uses Nested Loop;
  AFTER: goopg also picks Nested Loop, closing that tag) but gains
  `qual-placement` (+1) — a `Filter` that lands on PG's side only at a
  different `Index Scan` node, and the `store` table's access path moves
  from inside a Hash Join subtree to a separate `Index Scan store_pkey`. Net:
  one tag lost, one gained, both attributable to the same improved
  selectivity estimate for `store`/`store_sales`.
- **`Q97`** gains `parallelism` only (not `join-order`/`qual-placement` — the
  `parallelism` 86->87 M0138-0005 did not report because it was not a
  category the review table tracked at the time).

## Conclusions

1. **M0138-0008's own question is answered**: `Q21` (join-order) and `Q48`
   (qual-placement) are the specific queries M0138-0002..-0004's statistics
   changes moved, and the mechanism is a genuine cardinality-estimate-driven
   join-order/access-path change on two dimension-heavy joins, not a defect.
2. **A more consequential finding surfaced first**: the TPC-DS corpus-capture
   procedure (`scripts/capture-tpcds.sh`, cited as canonical by
   `docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md` §3) does
   not pin `GOOPG_ANALYZE_SEED`, so **any unpinned before/after category-count
   comparison over this corpus carries several categories of ±1..2 noise**
   from reservoir-sampler seed variance alone (`join-order`, `qual-placement`,
   `join-method`, `scan-type`, `aggregation-strategy`, `parallelism` all
   observed to flip on at least one query across four unpinned trials, from
   five distinct queries: Q21, Q26, Q45, Q48, Q51, Q97 — note Q21 did NOT
   flip in the unpinned trials' same-commit pairs, so its shift reads as
   commit-caused even unpinned; Q26/Q45/Q48/Q51/Q97 are the noise set). This
   directly touches the 2026-09-15 progress review's headline "TPC-DS
   categories net worse, 525->540" — that comparison was also taken unpinned,
   so some unknown fraction of the 15-category movement it reports is
   measurement noise rather than a real regression from the 36 tasks it
   reviewed. This task does not re-litigate that number (out of scope — a
   different, larger corpus-wide re-measure); it only establishes that the
   instrument used to take it needs the same fix applied here.
3. Follow-up task filed: **M0137-0020**, pin `GOOPG_ANALYZE_SEED` in
   `scripts/capture-tpcds.sh` (or the M0137-0003 procedure doc, whichever the
   task determines is the right layer) so future TPC-DS category-count deltas
   are trustworthy at the ±1 level.

## What was not done (scope boundary)

- **The 525->540 headline was not re-measured with a pinned seed.** Named as
  the reason M0137-0020 exists, not attempted here (recon-sized task, and
  the fix needs to land before a trustworthy re-measure is possible).
- **`Q26`/`Q45`/`Q48` (unpinned)/`Q51`/`Q97`'s underlying plans were not
  further triaged** beyond identifying them as seed-noise-sensitive — that is
  a property of the *statistics estimate landing near a cost tie*, which is
  exactly M0142's territory (join-order costing), not this task's.

## Verification

No production code changed (measurement-only task, matching M0138-0005's own
scope boundary). Raw captures under `/tmp/m0138-0008-*` (scratch, not
committed, same precedent as M0138-0001/-0005's `/tmp` census captures —
conclusions live in this doc, not the raw artefacts). Two worktrees and their
private binaries were removed after use (`git worktree remove`). `go build
./...` unaffected. `make ralph-state-guard` run before the status block.
