# P0-H12 — TPC-DS SF1 Q9 `scan-type` divergence: new or pre-existing?

Status: done (2026-09-18) — pre-existing, not a regression

## Context

`.ralph/fix_plan.md` banner item 1 ("Regressions found by P0-E7") named
P0-H12 as its first (only) task. P0-E7's TPC-DS full-SF1 parity capture
(`docs/design/0100-0149/p0-e7-bulk-re-measurement.md`
§"TPC-DS full-SF1 parity") was the **first-ever SF1** goopg-vs-PG
plan-parity measurement under this harness: `match=1/99` (Q41 only). Q9 —
one of the two queries the `AGENT.md` floor names (`match >= 2 (Q9, Q41)`,
set by `m0137-0004` **at SF0.25**, where Q9 does match) — is
`SHAPE-DIFF [scan-type]` at SF1. With no prior same-methodology SF1 baseline
to compare against, P0-E7 could not tell whether this divergence was
introduced by one of the 72 production commits it had just finished naming
individually (`27d4ae001`..HEAD, see that doc's "Per-commit TPC-H values
naming" section) or was always true at SF1. P0-E7 filed this task rather
than answering the question inline (Kind: recon, its own independently-sized
capture pair).

## Method

Built a second goopg binary from `27d4ae001` (2026-09-16 04:18, the oldest
commit P0-E7's per-commit table names, and the TPC-H `match=8` floor's own
"last measured" commit) in a separate git worktree, so the working tree
stayed clean throughout:

```
git worktree add --detach /tmp/wt-27d4ae001 27d4ae001
( cd /tmp/wt-27d4ae001 && go build -o /tmp/goopg-p0h12-27d4ae001-bin ./cmd/goopg )
sha256sum /tmp/goopg-p0h12-27d4ae001-bin
# e337d3bbf16f7209eab2438b4a2c2d32ce5143ce7fbf77f1eeed57615f083793
```

Served that binary against the **existing** `bench/tpcds/runtime_goopg/data`
SF1 data directory (same dataset P0-E7's HEAD capture used — a read of the
on-disk state, no reload) on the non-reference `:65436` port, through the
cgroup cap wrapper directly (not `bench/tpcds/server.sh start`, which always
rebuilds `GOOPG_BIN` from the *current* HEAD — the whole point here is to
serve an old binary):

```
GOOPG_CG_UNIT=goopg-p0h12-27d4ae001 scripts/goopg-test-run.sh \
  /tmp/goopg-p0h12-27d4ae001-bin start -D bench/tpcds/runtime_goopg/data \
  --listen 127.0.0.1:65436 --hba bench/tpcds/runtime_goopg/data/pg_hba.conf
```

Captured with the canonical tool, stamped against the old binary's sha256
(`capture_verify_serving_binary` enforces this — a stale/mismatched binary
would have failed the capture, not just recorded the wrong one):

```
CAPTURE_ENGINE=goopg GOOPG_EXPECT_BIN_SHA256=e337d3bb...83793 \
  scripts/capture-tpcds.sh 65436 postgres postgres \
  analysis/m0142/p0h12-tpcds-sf1-goopg-27d4ae001.plans.txt \
  "P0-H12 goopg SF1 (27d4ae001)" bench/tpcds/runtime_goopg/data
```

Then stopped via direct binary invocation (`/tmp/goopg-p0h12-27d4ae001-bin
stop -D bench/tpcds/runtime_goopg/data`) — same R1-safe pattern P0-E7 used,
`:65436` down before and after, no persistent state change.

PG side: re-ran the read-only reference capture against `:65438`/`tpcds`
(`EXPLAIN`-only, no `ANALYZE`, no write path — same command P0-E7 used) to
`analysis/m0142/p0h12-tpcds-sf1-pg.plans.txt`, rather than reusing P0-E7's
PG capture byte-for-byte, so this task's diff is self-contained.

Diff: `python3 scripts/pg-plan-parity-diff.py
analysis/m0142/p0h12-tpcds-sf1-goopg-27d4ae001.plans.txt
analysis/m0142/p0h12-tpcds-sf1-pg.plans.txt` ->
`analysis/m0142/p0h12-tpcds-sf1-diff.txt`.

## Result

```
PLAN-PARITY: queries=99 match=1 shapediff=73 unparsed=0 missingnode=22 error=3 timeout=0
CATEGORIES: join-order=91 join-method=71 scan-type=60 parameterisation=46 aggregation-strategy=72 sort-strategy=73 parallelism=88 qual-placement=22 rendering=23
CATEGORIES-EXCL-MATCH: join-order=91 join-method=71 scan-type=60 parameterisation=46 aggregation-strategy=72 sort-strategy=73 parallelism=88 qual-placement=22 rendering=23
```

**Identical to P0-E7's `a0e741a68` (HEAD, 2026-09-18) capture** —
`match=1/99` (Q41 only), same `CATEGORIES`/`CATEGORIES-EXCL-MATCH` counts,
digit for digit. Q9 at `27d4ae001` is `SHAPE-DIFF [scan-type]`
(`estimates(goopg cost=0.00..0.36 rows=1 width=68 | pg
cost=958966.27..958974.30 rows=1 width=160)`), the same verdict and same
category as at HEAD.

Directly diffing the two goopg-side `=== Q9` plan sections (P0-E7's HEAD
capture vs this task's `27d4ae001` capture) is **byte-identical** (`diff`
exit 0). Full-corpus diff between the two goopg captures shows only cosmetic
`EXPLAIN` alias-qualification churn in a handful of unrelated queries
(e.g. `Hash Cond: (ss_sold_date_sk = date_dim.d_date_sk)` vs `(ss_sold_date_sk
= d_date_sk)`, `cs2.cs_order_number` vs `cs_order_number` in Q80/similar) —
rendering-only, already the `rendering` category the diff tool tracks
separately, and does not touch Q9's plan at all.

**Conclusion: Q9's SF1 `scan-type` divergence pre-dates `27d4ae001` and is
unchanged across the entire 72-commit range P0-E7 measured.** This is not a
regression introduced by any commit in that range. The `match >= 2 (Q9,
Q41)` floor `m0137-0004` established is an **SF0.25-only** floor (Q9's
SF0.25 plan matches PG; its SF1 plan does not) — it never generalized to
SF1, and nothing in scope here caused that. Footnote added to
`m0137-0004-tpcds-match-reference-reconciliation.md` (the floor's origin
doc) rather than to `AGENT.md`'s harness section, which the loop does not
edit (R2).

## What was not done (scope boundary)

- **Root-causing Q9's SF1 `scan-type` choice itself** (why goopg picks a
  different scan than PG at SF1 specifically) is out of scope for this
  recon — the question asked was "new or pre-existing", not "why". It joins
  the general SF1 SHAPE-DIFF backlog (73/99 queries, tracked by the
  plan-parity harness's existing per-query categories, not a new gap this
  task discovered) rather than getting its own follow-up task: nothing
  found here is new information beyond "this specific query's specific
  divergence is old", so D1's deferral-ledger-row-plus-owning-task
  requirement does not apply — there is no newly-discovered unimplemented
  behaviour to hand off, only a dating of an already-known SHAPE-DIFF.
- **Bisecting earlier than `27d4ae001`** (whether Q9 ever matched at SF1, at
  some commit before the TPC-H floor's own reference point) was not
  attempted — `27d4ae001` is the oldest commit either P0-E7 or this task's
  parent task named, and going further back is unbounded scope with no
  named expected movement (S5), not a P0-H12 requirement.
- **No production code touched.**

## Gates run

`go build ./...` clean in both the main tree (untouched) and the
`/tmp/wt-27d4ae001` worktree (built the historic binary). No
`internal/`/`cmd/`/`go.mod`/`go.sum` file in the main tree changed this
task, so no values gate re-run and no G2 commit-msg stamp requirement
applies (D5: `docs(...)`/`analysis(...)` commit). `python3
scripts/ralph_protected_regions.py check-designdocs` exit 0.

## Movement

`Movement: none` — this is a dating/recon task (Kind: recon per its own
filing); no PG-match count changed, no `CATEGORIES-EXCL-MATCH` category
moved (the two captures compared are numerically identical — the point of
the task was confirming that, not changing it).
