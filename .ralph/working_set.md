Task: M0138-0008 — bisect the TPC-DS category shift M0138 caused. **DONE
this loop** (`bcaf86757`, committed). Banner's item 3 (M0138 0007-0009 /
M0140-0006) is now down to one task: **M0140-0006** (TPC-DS parallelism
partial-Append producer, deferred by M0140-0004 without an owner). A new
follow-up, **M0137-0020** (pin `GOOPG_ANALYZE_SEED` in
`scripts/capture-tpcds.sh`), was filed by this loop and sits in item 2's
milestone section even though item 2 itself was already marked fully closed
— it is new, unowned work discovered mid-task, not re-opened harness debt,
so it does not inherit item 2's priority (same reasoning M0137-0019 was
given). Re-read the banner fresh; as last read, remaining candidates at
roughly equal priority are M0140-0006, M0137-0020, and M0137-0018/0019.

Files: `docs/design/0100-0149/m0138-0008-category-shift-bisect.md` (new),
`docs/design/README.md` (+1 row), `.ralph/fix_plan.md` (M0138-0008 `[x]`,
M0137-0020 filed `[ ]`), `.ralph/deferral_ledger.md` (`m0138-0008-category-
shift-bisect` row). No production code touched (measurement-only task).

Key symbols/tools: `scripts/pg-plan-parity-diff.py --verbose` (per-query
category tags), `scripts/capture-tpcds.sh` (TPC-DS EXPLAIN capture — does
**not** pin `GOOPG_ANALYZE_SEED`), `GOOPG_ANALYZE_SEED` env var
(`internal/executor/operators_analyze.go:704-748`, seeds the block/reservoir
sampler; already pinned by `scripts/tpch-acceptance-arm.sh` /
`scripts/estimate-parity-gate.sh` but not by the TPC-DS path).

Findings: bisected M0138-0005's unexplained TPC-DS `join-order` 89->90 /
`qual-placement` 16->17 shift by building goopg at `0e97c94b3` (pre-M0138-
0002) and `44085c324` (M0138-0004), loading a **private** SF0.25 dataset
with each on port 5533 (shared `:65437` cluster never touched), diffing
against the live PG `:65438` reference. **Unpinned trials showed the
category counts are noisy at a FIXED commit** (ANALYZE reservoir-seed
variance — same mechanism as M0138-0009's correlation finding, but now shown
to flip plan-parity verdict CATEGORY tags, the milestone's own success
metric): `Q26`, `Q45`, `Q48`, `Q51`, `Q97` each flip a category tag between
same-commit trials; the naive before/after read even came out backwards
(90->89) from noise alone. Pinning `GOOPG_ANALYZE_SEED=20260905` (already
used elsewhere, just never by the TPC-DS capture path) made two fresh
same-commit captures byte-identical. With the seed pinned identically on
both commits, the shift **is real and reproducible** in M0138-0005's
original direction, and narrows to exactly **Q21** (join-order — a
cardinality-estimate-driven join-spine change moving `warehouse` out of
goopg's inner join tree) and **Q48** (qual-placement — a `store`/
`store_sales` access-path change). Both are genuine consequences of
M0138-0002..-0004's improved statistics, not defects. Filed **M0137-0020**
to pin the seed in the harness itself, since the milestone review's own
"TPC-DS categories net worse 525->540" headline was measured unpinned and
therefore carries an unknown amount of this same noise — re-measuring that
headline with the seed pinned is separate follow-on work, NOT done this
loop (scoped out, named in the design doc's "what was not done").

In-flight: none. Two git worktrees (`/tmp/m0138-0008-wt-{before,after}`),
their private binaries, private data dirs (port 5533,
`/tmp/m0138-0008-data-*`), and cgroup scopes were all torn down before the
status block (`git worktree remove --force` x2, verified `port 5533 clear`,
`no leftover cgroup scopes`). Shared TPC-DS bench cluster confirmed UP and
untouched (`bench/tpcds/server.sh status`: sf025 :65437 UP, postgres :65438
UP) after the loop's work.

Next step: re-read the `## Current Priority` banner fresh. Per this loop's
read, select **M0140-0006** (TPC-DS parallelism partial-Append producer —
resume point is in its own fix_plan entry, deferred by M0140-0004 without an
owner) unless the banner has moved M0137-0020 or M0137-0018/0019 ahead of it
by then.

Gates run: `go build ./...` clean (no production code changed this loop).
`make ralph-state-guard`: one self-repair (the same recurring benign
stale-clean-exit-marker pattern several prior loops have noted), clean after
repair. Pre-commit hook's pgbench smoke: PASS (tps ~43-150, 0 failed).
No `go test`/`tpch-spotcheck.sh`/`tpcds` sweep run this loop — measurement-
only task, no executor/planner code touched, matching M0138-0005's own
verification-scope precedent (see that design doc's "Verification" section).
