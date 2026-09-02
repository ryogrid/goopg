# P0-B — the plan-parity instrument and oracle hygiene

Implementation design for TODO items **P0-05, P0-06, P0-07, P0-08, P0-09,
P0-10, P0-11**. Parent design: [08 §3](../08-target-design.md),
[09 §3](../09-verification-and-acceptance.md).

Depends on [P0-A](P0-A-explain-instrument.md) for a trustworthy renderer, but
the tool itself can be built and unit-tested against recorded plan text before
P0-A lands.

Revision 2 — corrected after agent review. Six claims inherited from the parent
bundle were wrong; each correction is marked **[R2]** and is propagated back
into 07/09/TODO.md by the commit that lands this file.

---

## 1. What exists, and why it cannot answer the question

| tool | what it compares | why it is not a corpus parity instrument |
|---|---|---|
| `cmd/plan-snapshot` | goopg EXPLAIN today vs a committed goopg capture | **goopg-vs-goopg.** TPC-H only (`main.go:56` imports `internal/testutil/tpch`). |
| `cmd/estimate-audit` | **[R2]** goopg vs PG — it *does* have a PG side (`main.go:288` `--reference`, `:289` `--ref-port` defaulting to 65432) and it *does* compare shape: `internal/testutil/estimateaudit/spine.go` compares how each engine **partitioned** a relset, and `parity.go` classes PG-only joinrels as divergence. | TPC-H only, and it compares **join pairings on the chosen spine**, not whole plan trees. No TPC-DS, no scan/aggregate/sort/parallel dimension. |
| `scripts/tpcds-plan-diff.py` | ad-hoc | not wired to any gate |
| `make plan-gate` | `plan-snapshot diff` against `ls -t plan_snapshots/*.txt \| head -1` | **[R2]** That selects `warm-stats-base.txt` (Aug 2026), **not** `m0077-final`. SKIPs silently (exit 0) when no baseline exists or the server is down (`Makefile:419-426`). |

**[R2]** The parent bundle's claim that the acceptance criterion "has never been
measured" is overstated. It *has* been measured, on the TPC-H join spine, by
`estimate-audit --reference`. What has never been measured is a **corpus-wide
tree diff across both suites** covering scan type, aggregation strategy, sort
strategy and parallelism. That — not "no PG comparison exists" — is the gap
P0-05/06 fill, and 07 §2's framing is corrected accordingly.

---

## 2. Measured properties of the two cluster pairs

Captured 2026-09-02 against the live clusters, because a parity fixture is
unattributable without them.

### 2.1 The TPC-H clusters do not hold identical data **[new finding]**

| table | PG :65432 | goopg :65433 | |
|---|---|---|---|
| `lineitem` | 5 998 835 | 6 001 255 | **differ, +0.040 %** |
| `orders` | 1 500 000 | 1 500 000 | same |
| `customer` | 150 000 | 150 000 | same |
| `part` | 200 000 | 200 000 | same |
| `partsupp` | 800 000 | 800 000 | same |
| `supplier` | 10 000 | 10 000 | same |
| `nation` / `region` | 25 / 5 | 25 / 5 | same |

The two clusters were loaded by separate HammerDB runs, and HammerDB generates
1–7 line items per order at random, so only `lineitem` diverges. 0.04 % cannot
change a plan shape, but it means **row counts are not comparable between the
engines** and any future value-level check across the pair is invalid. Recorded
in the fixture header; a `MISSING-NODE`/`SHAPE-DIFF` verdict is unaffected.

### 2.2 The clusters are configured differently — wider than P0-12 records

| GUC | PG :65432 | goopg :65433 | |
|---|---|---|---|
| `work_mem` | 64MB | 512MB | **8× to goopg** (P0-12 records this) |
| `effective_cache_size` | 2GB | 4GB | **2× to goopg** (P0-12 records this) |
| `shared_buffers` | 512MB | 2GB | **4× to goopg** — **[new finding]**, not in P0-12 |
| `random_page_cost`, `seq_page_cost`, `cpu_tuple_cost`, `hash_mem_multiplier`, `from_collapse_limit`, `join_collapse_limit`, `geqo_threshold`, `max_parallel_workers_per_gather`, `enable_hashagg` | identical | identical | |

`shared_buffers` does not feed PG's cost model (`effective_cache_size` does), so
it cannot change a *plan*; it changes *runtime* substantially, so it belongs in
P0-12's alignment along with the other two. P0-12's text is extended.

### 2.3 The TPC-DS query corpus is not git-tracked **[R2, new finding]**

`bench/tpcds/runtime_goopg/tpcds-data/queries/query*.sql` — 100 files,
0 tracked (`.gitignore:109` excludes `bench/tpcds/runtime_goopg/tpcds-data/`).

So a committed PG plan fixture for TPC-DS is keyed to inputs that are **not in
the repository**. The fixture header must therefore carry a **SHA-256 digest of
the concatenated query files**, and `parity diff` must refuse to run when the
local corpus digest does not match the fixture's. Without that the fixture
silently compares against different SQL after any dsqgen re-run. This also
means `--suite tpcds` needs new corpus plumbing that reads the untracked bench
directory — it cannot reuse `internal/testutil/tpch`, which has no TPC-DS
counterpart.

---

## 3. P0-05 / P0-06 — `plan-parity`

### Placement

A new subcommand in `cmd/plan-snapshot` (dispatch is a plain
`switch os.Args[1]` at `main.go:61-73`; no structural obstacle), with the logic
in a testable library `internal/testutil/planparity`. That location follows the
`estimateaudit` precedent: an analysis library under `internal/testutil/`,
imported by a `cmd/` binary.

```
plan-snapshot parity capture --engine goopg|pg --suite tpch|tpcds --out <file>
plan-snapshot parity diff    --goopg <file> --pg <file> [--budget <n>]
```

### The parse target

Input is `EXPLAIN` **text**, not JSON, so the PG side comes from a stock `psql`
and the goopg side exercises the renderer a human reads — a JSON path would let
the text renderer rot, which is exactly the P0-04b defect. The parser recovers
the tree from the `->` indent discipline both engines share
(`internal/executor/operators_explain.go:1523` cites `explain.c:1616-1635`).

```go
type Node struct {
    Type     string   // "Hash Join", "Seq Scan", "Parallel Index Only Scan"
    Relation string   // "on <rel>" target, alias-stripped
    Index    string   // "using <index>"
    JoinType string   // Inner/Left/Semi/Anti/Full
    Strategy string   // aggregate strategy, sort method
    Costs    Costs    // startup, total, rows, width — reported, never compared
    Children []*Node
}
```

### Normalisation policy

Declared here, printed in every report header, one unit test per rule. **An
undeclared normalisation turns a real divergence into a silent MATCH**, which is
the failure mode that makes an instrument worse than none.

| # | rule | justification |
|---|---|---|
| N1 | Strip PostgreSQL's standalone `Hash` nodes | Verified on both sides: PG emits one (`postgres/src/backend/commands/explain.c:1429`, `pname = "Hash"`), goopg has no Hash node — the build lives inside `joinOp` (`operators_explain.go:1600-1602`). Executor structure, not a planning choice. |
| N2 | `Materialize`: **counted, not stripped** | Materialize *is* a planning choice (P2-06). Reported in its own column; folding it away would hide the item that fixes it. |
| N3 | Alias-strip relation names to the base relation | Revisited once P0-04 aligns suffix numbering. |
| N4 | **[R2]** `Subquery Scan`: **counted as `MISSING-NODE`, not stripped** | goopg emits `Subquery Scan` nowhere in the tree (the only occurrence is a test fixture string in `parity_test.go:56`). A PG-only node silently stripped is precisely the class the instrument exists to count. The original "strip on both sides" rule was one-sided and wrong. |
| N5 | Costs, rows, widths, times, buffers, loops excluded from shape equality | They are the other column of the report. |
| N6 | `never executed` subtrees kept, not pruned | An unexecuted branch is still a planned branch. |

N2 and N4 print per-query counters even when the verdict is MATCH.

### Verdicts and taxonomy

Per query: `MATCH`, `SHAPE-DIFF`, `MISSING-NODE`, `ERROR`, `TIMEOUT`.
Roll-up line — **[R2]** this *extends* 09 §3.1's specified line (which is
`queries/match/shape-diff/missing-node`) with the suite and the two failure
counts:

```
PLAN-PARITY: suite=tpch queries=22 match=N shape-diff=N missing-node=N error=N timeout=N
```

Each `SHAPE-DIFF` is classified into the nine 09 §3.1 categories
(`join-order`, `join-method`, `scan-type`, `parameterisation`,
`aggregation-strategy`, `sort-strategy`, `parallelism`, `qual-placement`,
`rendering`), by first divergence in a top-down aligned walk; a query may carry
several. `rendering` is the residual bucket and **its size is itself a metric** —
a large count means the instrument, not the planner, needs work (09 §1 R3).

### Committed fixtures

`bench/tpch/plans-pg/<date>.txt` (from :65432 db `tpch`) and
`bench/tpcds/plans-pg/<date>.txt` (from :65438 db `tpcds05`, user `ryo` per
`env_tpcds.sh:63`). Neither directory exists yet.

Header records, and `parity diff` verifies where it can:

- PG version string, `date -Iseconds`, git commit
- **the query-corpus SHA-256** (load-bearing for TPC-DS, §2.3)
- base-table row counts for the capture cluster (§2.1)
- `work_mem`, `effective_cache_size`, `shared_buffers`, `random_page_cost`,
  `max_parallel_workers_per_gather` (§2.2)

### Mode

Report-only, with a **pinned mismatch budget that must not grow**. A hard
match-all bar is declined while cost constants and statistics fidelity still
differ — the same call `leftdeep-joins/09` §4 made.

---

## 4. P0-07 — the baseline roll-up

Run capture + diff for both suites, commit the roll-up, write the numbers into
09 §7.1 as the starting points for bars A1/A2. The number is expected to be bad;
**it is recorded as found**, with no tuning between measuring and recording.

---

## 5. P0-08 — re-pin the stale baseline **[R2 — rewritten]**

The parent bundle described one defect; there are two distinct paths and the
original description fitted neither.

### 5.1 What the nightly actually does

`ci/batch/stages/stage-tpch.sh:234`:

```sh
# Pinned plan diff (informational; ls -t mtime-tie gotcha — ci/design/05 §C).
make -C "${REPO_ROOT}" plan-diff LABEL=m0077-final MODE=structural ... || true
```

It calls **`plan-diff`**, not `plan-gate`. `plan-diff` (`Makefile:403-409`) has
no SKIP path — it requires `LABEL` and returns the differ's exit status. The
`m0077-final` pin is deliberate: the comment records that `plan-gate`'s `ls -t`
selection was rejected for the nightly because of an mtime-tie hazard. `|| true`
is deliberate too — `summarize.py:687-689` consumes the result as an
informational `plan-drift` note.

So the nightly **does not** silently pass a gate that never ran. It runs, it
fails against a May 2026 baseline, and it reports drift by design. The defect is
narrower: **the pinned label is four months stale**, so the note is noise.

Adding `PLAN_GATE_REQUIRE=1` "set by ci/batch" — the parent proposal — would
never fire, because ci/batch never invokes that target. That proposal is
withdrawn; if the interactive `plan-gate`'s double SKIP is worth hardening it
must be argued on its own footing, and it is **not** part of this item.

### 5.2 The re-pin, which is three coordinated edits

1. Capture and commit `plan_snapshots/take2-p0-<date>.txt` against the current
   build (goopg :65433, up).
2. `ci/batch/stages/stage-tpch.sh:234` — `LABEL=take2-p0-<date>`.
3. `ci/batch/lib/summarize.py:689` — the note text hardcodes `m0077-final`.

Miss any one and the nightly keeps diffing against May 2026 or reports the wrong
baseline name. They land in one commit.

**[R2]** Separately: `make plan-gate` today selects `warm-stats-base.txt`
(Aug 2026), not `m0077-final`. The parent bundle's "22/22 diverge nightly, no
signal" attributed the nightly's behaviour to `plan-gate`'s selection rule; the
two are unrelated. 07 and 09 §3.4 are corrected.

---

## 6. P0-09 — TPC-DS oracle time resolution **[R2 — rescoped to near-zero]**

### The facts, corrected

- The timer is `scripts/tpcds-sf05-regression.sh:498-500` (`start=$SECONDS` …
  `secs=$((SECONDS - start))`), not `:502`.
- **54** of the 95 `OK` rows read `secs=0`, not 83 of 95. (The "83 of 95" figure
  in 09 §3.4 and TODO P0-09 is wrong; all three are corrected.)
- **Nothing reads the column.** The compare path
  (`scripts/tpcds-sf05-regression.sh:796-801`) takes field 2 (status), field 3
  (rows) and field 4 (ck). Field 5 is referenced nowhere in the script, in
  `ci/batch/`, or in `scripts/`.
- The fixture's own header says so: `secs are machine-specific; rows and ck are
  the fixture` (`:477`).

### Decision

A standalone re-capture would spend ~20 minutes of PG time and **truncate a
git-tracked fixture** (`cmd_oracle` truncates by design) to improve a column
with **zero readers**. That is net-negative risk for no measurable gain.

**Rescoped:** change the timer to `EPOCHREALTIME`-based millisecond arithmetic
and widen the printed column to three decimals — a two-line change that costs
nothing — but do **not** trigger a re-capture for it. The improved resolution
lands on the next capture required for another reason (a dataset or query-file
change), under design D5's existing acceptance rule that re-captured `rows` and
`ck` must equal the pinned ones query-for-query.

TODO P0-09 is rewritten to this scope, and 09 §3.4's claim that no ratio can be
formed is corrected: `secs` was never a ratio input.

---

## 7. P0-10 — already fixed; close it

TODO P0-10 and 09 §3.4 state that `ci/batch/lib/summarize.py` reads `r["rows"]`
while the TPC-DS CSV column is `expected_rows`, making every anchor inert.
**Stale.** `summarize.py:651-656` reads `r["expected_rows"]`, with a comment at
`:652` explaining the difference from the TPC-H path (which correctly reads
`r["rows"]` at `:576-580`).

Fixed by `63056c544` (2026-07-30), whose message names the defect verbatim —
a month before this bundle was written.

Action: mark `[-]` dropped-already-fixed with that commit as evidence; correct
09 §3.4 and 07's measurement-gap list; record in REVIEW.md that a `git log -S`
would have refuted the claim before it was written. No code change.

---

## 8. P0-11 — path provenance **[R2 — rescoped to extend, not create]**

09 §1 R4: verify both candidates were generated before concluding a cost bug.

**A `DPTRACE` channel behind `GOOPG_PGSHAPED_DP_TRACE=1` already exists** —
`internal/optimizer/joinsearchtrace.go` (`offer()` `:162`, `decline()` `:178`,
`traceTag = "DPTRACE"` `:196`, `emit()` `:237`), parsed by
`internal/testutil/estimateaudit/enumtrace.go` and consumed by
`cmd/estimate-audit --enum-trace` (`main.go:295`).

It answers **half** of R4 — "was this *partition* enumerated" — at
`makeJoinRel` granularity. It does not answer "which producer offered this
*path*, and did it survive dominance". P0-11 adds the second half.

### Design

A third `DPTRACE` line type, `path`, emitted from `addPath`
(`internal/optimizer/path.go:555`) **and from `addPartialPath` (`:562`)** —
**[R2]** the partial-path list was omitted from the parent design, yet
`parallelism` is one of the nine divergence classes, so a trace that ignores it
cannot answer whether a partial path was ever offered.

Record per call:

```
producer, relids, kind, required_outer, rows, startup, total,
disabled_nodes, pathkeys, verdict(accepted|dominated|rejected-fuzzy), dominated_by
```

`producer` is a **caller-supplied string**, not recovered from the stack —
`runtime.Stack` in a hot path is the `perf-optimize2` regression this repository
has already paid for once. 12 in-tree `addPath` call sites
(`pathgen.go:29,90,124`; `pathparamindex.go:389`; `pathindexonly.go:111`;
`joinpathsnli.go:252`; `pathindexordered.go:181`; `pathbitmap.go:75,82,495`;
`joinpathsmerge.go:369`; `joinsearch.go:394`) plus one `addPartialPath` caller
(`pathgen.go:40`) — a small mechanical change.

### The emitter and the parser must land together

**[R2]** `enumtrace.go` counts `DPTRACE`-tagged lines that fail to parse as
`Malformed`, deliberately, so a silent drop cannot understate enumeration.
Shipping a new tagged record type without the matching `EnumPath` arm would make
every `estimate-audit --enum-trace` run report a large `Malformed` count and
look like a regression in the existing instrument. **One commit.**

*Gate: units — a recorded enumeration for a two-relation join asserts every
offered path appears exactly once with a verdict, and `Malformed` stays 0.*

---

## 9. Order

| # | item | needs a server? |
|---|---|---|
| 1 | P0-10 (close; docs only) | no |
| 2 | P0-09 (timer only, no re-capture) | no |
| 3 | P0-11 (`addPath` + `addPartialPath` trace **and** `enumtrace.go` arm) | no |
| 4 | P0-06 (diff library + unit tests over recorded pairs) | no |
| 5 | P0-05 PG side, both suites | PG :65432, :65438 — up |
| 6 | P0-05 goopg side | goopg :65433 up; **:65437 must be started** |
| 7 | P0-07 (baseline roll-up) | both |
| 8 | P0-08 (re-pin: snapshot + stage-tpch.sh + summarize.py) | goopg :65433 |

Items 1–5 need no goopg TPC-DS cluster, so they proceed while :65437 comes up.

## 10. Risks

| risk | mitigation |
|---|---|
| The text parser mis-reads a nested subplan or CTE and reports a false SHAPE-DIFF. | Unit tests over recorded plan pairs including `InitPlan`/`SubPlan`/`CTE` before any corpus claim. |
| The normalisation list grows quietly until everything matches. | Every rule is a numbered row here, printed per report, one unit test each; N2/N4 print counters even on MATCH. |
| P0-11 lands the emitter without the parser arm and inflates `Malformed`. | Same commit; the gate asserts `Malformed == 0`. |
| **[R2]** The TPC-DS fixture silently compares against re-generated SQL. | The corpus SHA-256 is in the fixture header and `parity diff` refuses to run on a mismatch (§2.3). |
| **[R2]** The capture measures a half-loaded or differently-loaded database. | Base-table row counts go in the fixture header and are compared between the two captures; §2.1's `lineitem` gap is the reason this is not hypothetical. The parent design's "counts must match the recorded load" was unimplementable — no such artefact exists (`spotcheck_expected.env` pins *result* rows, not cardinalities) — so the fixture becomes its own record. |
