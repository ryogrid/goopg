# How plan parity is measured and driven — methodology

*Audience: the owner, and any coding agent picking this workstream up.
This file explains **how we measure and why we count that way**. Round
status lives in `TODO.md` (authoritative); root causes in
`plan-parity-root-causes.md`; sequencing in `ROADMAP-to-all-match.md`.
Working copies of every script named here are in `methodology/`.*

---

## 1. The goal, restated precisely

Every currently-executable TPC-H and TPC-DS query must produce the
**same plan as PG 18.3** — reached by the same statistics, the same cost
computation and the same planning logic. Two consequences that differ
from every earlier workstream in this repo:

- **A slower plan that matches PG is NOT a regression.** Execution time
  is reported, never adjudicated.
- **Forcing shapes is forbidden.** A plan that matches because it was
  special-cased does not count.

Current state: **TPC-H 2/22, TPC-DS 0/99.**

---

## 2. Why we count CATEGORIES, not matches

This is the single most important thing to understand before reading any
round report.

**All-match is a conjunction, not a sequence.** No non-matching query
differs from PG in only one way — every one differs in **4–7 categories
at once**, and *zero* queries are blocked by join-order alone. So:

> **No single fix flips any query to MATCH.** Each query needs *all* of
> its categories closed before its verdict changes.

That is arithmetic, not pessimism, and it explains a pattern that would
otherwise read as failure: R1 (qpqual currency), R3 (hashagg spill), R6
(window Sort) and R21 slice 2b were each correct and PG-faithful, and
**each moved the match count by zero**.

**Therefore: judge a round by its category, and state the expected
category movement in the design BEFORE measuring.** A round whose
success test is "match count rises" is mis-specified at this stage.

Queries blocked per category (TPC-DS of 99 / TPC-H of 22):

| category | TPC-DS | TPC-H |
|---|---|---|
| **join-order** | **95** | **17** |
| parallelism | 89 | 16 |
| aggregation-strategy | 80 | 10 |
| sort-strategy | 80 | 13 |
| join-method | 73 | 12 |
| scan-type | 70 | 14 |
| parameterisation | 41 | 6 |
| rendering | 33 | 7 |
| qual-placement | 13 | 7 |

---

## 3. The second axis: is the query even ELIGIBLE?

Category counts rank work by breadth. They miss a harder distinction.

A query the PG-shaped join search **declines** falls to the legacy
planner and **cannot converge on PG's plan by any amount of costing
work** (K27). Those queries are a hard floor, not a tall wall.

```bash
GOOPG_PGSHAPED_DP_TRACE=1 <server>              # enable the channel
grep -oP "seam-decline reason=\K\S+" <log> | sort | uniq -c
grep -oP "DPTRACE problem nrels=\d+ rels=\S+" <log> | sort -u
```

Current: **TPC-H 0 declines** (so its 2/22 is *entirely* costing and
candidate generation — seam work cannot help it) and **TPC-DS 9**, down
from 13.

Attribute a decline to a query by running queries one at a time and
watching the log grow — the trace has no query id.

---

## 4. The measurement pipeline

### 4.1 Capture (both engines, identical script)

`methodology/capture-tpch.sh <port> <db> <user> <out> <hdr>`
`methodology/capture-tpcds.sh <port> <out> <hdr>`

Non-obvious requirements, each of which has burned a round:

- **GUCs are pinned IN SESSION** on whichever engine is addressed:
  `SET work_mem='64MB'`, `SET max_parallel_workers_per_gather=4`. A
  cluster's ambient config must never decide a comparison (K10: the
  TPC-DS pair once ran 512MB vs 4MB — that measured configuration, not
  planning).
- **`SET` command tags are stripped** (`grep -vx SET`) — they parse as a
  plan node named `SET`.
- **The temp SQL filename is FIXED**, never `mktemp` (K18): the path
  appears in psql ERROR text for the unplannable queries, so a random
  name makes two byte-identical captures diff.
- TPC-H sections are `=== Qn`, with `Q15` replaced by `Q15a-VIEWBODY`
  (the CREATE VIEW has no plan shape); TPC-DS sections are
  `===== Qn =====` and multi-statement files need the per-statement
  EXPLAIN split. Q36/70/86 fail to parse on *both* engines.

### 4.2 The PG reference must be captured LIVE

**Never diff against `bench/tpch/plans-pg/`** (K9). That fixture is
stale and SERIAL; live PG at the configured GUCs plans TPC-H in
parallel. Diffing it made all nine TPC-H `MISSING-NODE` verdicts
artefacts — the true count is 0, and Q6 had been matching PG all along
while filed as a divergence.

### 4.3 Compare

```bash
python3 scripts/pg-plan-parity-diff.py <goopg.sections> <pg.sections>
```

Verdicts: `MATCH / SHAPE-DIFF / UNPARSED / MISSING-NODE / ERROR /
TIMEOUT`.

- **`UNPARSED` must be 0** for any parity claim — it means the tool
  declined to answer, not that the plans differ. It was split out of
  `MISSING-NODE` in R2 precisely because conflating them asserted
  something much stronger than the evidence supported.
- `MISSING-NODE` means a PG node kind goopg provably cannot emit.
- TPC-DS needs section normalisation first:
  `sed -E 's/^=====[[:space:]]*(Q[0-9]+)[[:space:]]*=====$/=== \1/'`

`scripts/tpcds-plan-diff.py` is **byte** equality — a goopg-vs-goopg
movement detector only. It can never be a parity criterion, because
costs differ between engines.

---

## 5. The gates every round must pass

Parity is the subject; **values are the bar.** A plan change that
returns wrong rows is not a parity improvement.

| gate | command | bar |
|---|---|---|
| suites | `go test ./internal/optimizer/ ./internal/executor/ -count=1` | green |
| TPC-H values | `methodology/values-digest-tpch.sh <port> <out>`, diff vs baseline | byte-identical |
| TPC-H tripwires | Q12/Q13 row counts in that digest | same as the baseline arm |
| TPC-DS values | `GOOPG_BIN=<bin> SF05_NO_BUILD=1 scripts/tpcds-sf05-regression.sh sweep` | `MISMATCH=0 CKMISMATCH=0 ERROR=0` |
| parity | §4.3, both corpora | category movement, adjudicated |

`-count=1` is required for correctness re-runs here (the result cache
goes stale across test file add/remove). The repo's never-`-count=1`
rule is about *their* gate scripts, not these.

**A `TIMEOUT` in the sweep is a real loss of values coverage**, not an
accepted cost — it means that query's rows were never checked.

---

## 6. Running servers (the traps are not optional)

`methodology/launch-verified.sh <bin> <datadir> <port> <log> <scope> [gogc] [gomemlimit]`

- **`pg_isready` READY is necessary, not sufficient** (K5). A surviving
  older server answers the port while your instance never binds. The
  launcher verifies `/proc/<pid>/exe` inode == the binary you built.
  This cost a full pair of captures once: an arm silently measured the
  *previous* binary and looked entirely normal.
- **cgroup scope names are single-use.** Reuse fails with "already
  loaded" and the start silently never happens — use `$$` or similar.
- **Backgrounding is banned on the command line.** Put `&` *inside* a
  script file and wait for readiness in the foreground.
- **Never pattern-kill.** `pkill -f goopg` self-matches the invoking
  shell. Stop with `<bin> stop -D <dir>`.
- Use a **private clone and a private port** (55xx). `:65432` (PG TPC-H)
  and `:65438` (PG TPC-DS) are live references — verify, never restart.
- Set `GOOPG_ANALYZE_SEED=20260905` at server start.

---

## 7. How to diagnose, in order

The single most repeated lesson in this workstream, stated as a
procedure. Roughly a dozen diagnoses were wrong until measured, and the
trace channels answered each in one run.

1. **Run the cheap decisive check first.** Alarming messages have twice
   come from *test-side walkers*, not the planner — four separate
   walkers could not descend a `Gather`, producing
   `searched tree has 0 joins` and `an ON qual was dropped, which is a
   cross product`. TPC-H values were byte-identical the whole time. A
   cross product that returns identical rows is not a cross product.
2. **Instrument the input before theorising about the logic.** Confirm
   the candidate is *generated* before blaming the cost model
   (`GOOPG_PGSHAPED_DP_TRACE=1`, `tracePath`, `traceSeamDecline`).
3. **Check callers, not just the file.** A function can read correct and
   be dead (`generateScanPaths` is test-only), and a comment can
   describe code that does not exist (`addPartialHashJoinPath`'s
   "declines SEMI/ANTI" filter never existed).
4. **Ask the oracle rather than reasoning.** PG is running on `:65432`
   and `:65438`. When goopg and a test disagree about what is correct,
   the test can be wrong — three tests pinned a row-dropping outer-join
   demotion until PG was asked directly.
5. **Re-read your own headline against your own caveats before
   committing** (K22). Twice the evidence on the page was right and the
   conclusion drawn from it was not.

---

## 8. Ledger conventions

- `TODO.md` holds `K1…Kn` — the durable knowledge. **Corrections are
  struck inline, never rewritten**, so a superseded claim stays legible
  next to what replaced it. Several K-items are corrections of earlier
  K-items.
- One round = design doc → commit `-n` + push → implement → English
  report in the same directory → commit + push.
- Stage by **explicit pathspec** (foreign WIP is often present); never
  `git add -A`; never `git clean -fdx` (2 GB of benchmark data).
- Name diff outputs `*.txt` — `*.out` is gitignored.

---

## 9. What "done" will look like

`UNPARSED = 0` and `MATCH = 22` on TPC-H, `MATCH = 96` on TPC-DS
(Q36/70/86 are unplannable on both engines and out of scope), against
**live** PG references at pinned GUCs, with both values gates all-zero —
and with each round's category movement recorded so the path is
auditable rather than asserted.

## What the parity verdict is BLIND to (verified from the tool's source)

`scripts/pg-plan-parity-diff.py:113` parses
`(cost=..  rows=..  width=..)` with a single regex and N1 moves **all
three** into a side column before comparison. N5 strips `::type`
renderings; N6 compares Filter / Index Cond / Hash Cond by (referenced
columns, operator multiset) and NOT by literal values.

So a change to an estimate, a cost, a WIDTH, a cast rendering or a
literal registers **only insofar as it changes plan STRUCTURE**.

Measured consequences, all from this workstream:

| round | changed | shape-changed | category movement |
|---|---|---|---|
| R30 correlation | 99 plans | 6 | scan-type −1, join-method +2 |
| R31b heap density | 96 plans | 10 | qual-placement −1 |
| R34 cast folding | 18 plans | **0** | none |
| R36 baserel selectivity | 44 plans | 24 | net −2 |

R34 is the instructive one: it corrected a 580x cardinality error and
measured exactly zero, because it changed no plan's structure.

**Therefore:** run `methodology/shape-delta.sh OLD.norm NEW.norm` every
round and report its counts ALONGSIDE the categories. A round with
`shape-changed=0` moved no plan at all and its category zero is
trivial; a round with shape changes and no category movement moved
plans SIDEWAYS. Conflating the two produced a wrong conclusion once
already (R35's withdrawn "the metric can never see an estimate change").
