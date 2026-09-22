# M0145-0021 (slice 1) — making the fire-set derivation trustworthy

Status: **slice 1 LANDED 2026-09-22.** The task's full gate template (SF1 A/B
with a mechanical no-timeout-class-increase pass condition) is NOT yet built —
this is the prerequisite it needs, and it fixes two recorded incidents.
Kind: impl
Parent: none
Movement: none — a harness fix; it reports, it does not plan.

## Why this is the first slice

M0145-0021 asks for a gate template that re-derives the current fire set **per
run** rather than hard-coding Q77/Q78. Loop #82 did that derivation by hand and
learned that the obvious route does not work: the server log the seam census
reads carries no query markers, so declines cannot be attributed to queries.
The derivation that does work is a plan A/B — capture both arms, diff, and the
queries whose plans differ ARE the fire set.

That makes the plan-diff tool the gate's load-bearing component, and it had two
defects that make a derived fire set untrustworthy.

## Defect 1 — the harness has TWO capture formats and the tool knew one

```
`===== Q1 =====`   the SF0.25 sweep's capture
`=== Q1`           scripts/jointree-parity-capture.sh's capture
```

`BLOCK_RE` matched only the first. Pointing the tool at a jointree capture —
which is what every firewall/knob A/B produces — printed

```
=== PLAN-SHAPE: queries=0 same=0 changed=0 added=0 removed=0 ===
```

a **vacuous pass indistinguishable from "the plans agree"**. So every loop that
needed to diff those captures hand-rolled its own `diff`, and two were then
bitten by noise this tool already normalises: loops #82 and #84 each "found"
Q36/Q70/Q86 changed when only the temp filename inside their psql parse errors
differed.

## Defect 2 — a capture that parses to nothing reported success

The same silence is the more dangerous half: a wrong path, a truncated file or
an unknown header all produced `changed=0` and exit 0. The tool now refuses —
`FATAL — <path> parsed 0 query blocks`, exit **2**, kept distinct from
`--strict`'s 1 — because a capture with no blocks is a broken capture, never a
clean result.

## What it buys, measured

Re-running the tool over loop #82's firewall A/B captures, which it previously
could not read at all:

```
changed (2): Q77 Q78
=== PLAN-SHAPE: queries=99 same=97 changed=2 added=0 removed=0 ===
=== JOIN-METHOD-ELECTION: moved=1 into-nestloop=1 suspects=Q77 ===
```

Both of loop #82's hand-derived findings now fall out automatically: the fire
set is exactly Q77 and Q78 with the three parse-error queries correctly
filtered, and M0145-0022's channel independently flags **Q77's move into a
nested loop** — which is the criterion-1 violation that loop had to spot by
eye, and the reason the firewall relaxation was not executed.

## What remains for M0145-0021

The gate template itself: run the A/B at SF0.25 **and** SF1 over the derived
fire set and make "no timeout-class increase" a mechanical pass condition. The
SF1 half needs an execution harness (loop #82 ran it by hand on a private
clone), which is the bulk of the work and is deliberately not bundled here.

## Execution template — loop 7

`scripts/tpcds-fireset-gate.sh` now supplies the isolated execution half. For
each requested corpus it captures baseline and candidate plans through
`jointree-parity-capture.sh`, derives the changed query ids with the canonical
plan diff, and then runs that exact set in two fresh private clone arms. The
default corpus list is SF0.25 plus SF1; an investigation can select one with
`CORPORA` while retaining the same mechanism.

The candidate is a genuine separate process environment. `BASELINE_ENV_FILE`
and `CANDIDATE_ENV_FILE` are sourced only inside their respective clone arm,
so a diagnostic GUC cannot leak into the control capture. The default arm
selector remains `BASELINE_JOINTREE=0` and `CANDIDATE_JOINTREE=1`.

The clone harness writes `Q<n> PASS`, `TIMEOUT`, or `ERROR` records and retains
the raw psql result for each query. On a timeout it stops and restarts the
private clone before continuing: killing the client alone does not establish
that the database backend stopped building its intermediate state. The status
comparator rejects an incomplete run and returns nonzero only for a newly
introduced candidate timeout; inherited timeouts remain explicit evidence.

Execution arms set `FIRESET_SKIP_CAPTURE=1`: their preceding plan A/B is the
authoritative derivation, so repeating all 99 EXPLAIN statements would add
latency without adding evidence. A direct isolated SF0.25 Q2 smoke completed
PASS in both arms and the comparator reported no introduced, unchanged, or
missing timeouts.

The full gate can be resumed safely with `FIRESET_RESUME=1` and an optional
`FIRESET_BATCH_SIZE`. It preserves completed status rows and asks the comparator
to reject every still-missing fire ID. This was necessary in a headless runner
where an accepted batch must fit a bounded foreground interval. Clone directories
use a deterministic short tag rather than the display label, avoiding Unix
control-socket path failures for the longer generated execution labels.

## Continuation evidence

The resumed SF0.25 execution processed Q8 after the prior Q2, Q5, and Q6
batches. Baseline and candidate both recorded `Q8 PASS`; the run took 25.8
seconds. The comparator reported 20 missing records from the derived 24-query
set and exited nonzero, which confirms that a partial fire-set batch cannot
produce a green gate result.

The following Q9 batch also completed `PASS` in both arms in 21.2 seconds.
Nineteen of the 24 derived IDs remain unexecuted, and the comparator again
returned nonzero for that incomplete set.

The Q10 batch likewise recorded `PASS` in both arms in 21.2 seconds. Eighteen
derived IDs remain, and the comparator remains nonzero until they are covered.

Q14 recorded `PASS` in both arms. Its candidate arm outlived the initial
foreground output yield but completed under the existing 600-second policy;
the persisted status record was read only after waiting for that same runner.
Seventeen derived IDs remain.

Q16 completed `PASS` in both arms in 20.1 seconds. Sixteen derived IDs remain,
and the comparator continues to reject the incomplete fire-set run.

Tests: `scripts/tpcds-plan-diff-test.py` gains three cases — both header
formats parse, and a zero-block capture is fatal rather than clean. Non-vacuity
checked: reverting either change fails exactly the tests that assert it.

## Acceptance evidence — both scales, 2026-09-22

The gate template now has its two-scale acceptance run. The same derived
24-ID fire set was executed on private clones in both arms at both scales,
and the comparator exited 0 with nothing missing:

```
SF0.25: FIRE-SET-TIMEOUTS: fires=Q2 Q5 Q6 Q8 Q9 Q10 Q14 Q16 Q28 Q33 Q35 Q41
        Q44 Q54 Q56 Q58 Q60 Q61 Q69 Q77 Q83 Q88 Q90 Q94
        introduced=none unchanged=none missing=none   (exit 0)
SF1:    identical fire set, identical verdict          (exit 0)
```

Evidence dirs: `tmp/m0145-0021-loop91/tpcds-sf025/` (SF0.25) and
`tmp/m0145-0021-sf1/tpcds-sf1/` (SF1) — per-query `*.result.txt`, per-arm
`*.status.txt`, the derivation `*-fires.diff.txt` and `*-fires.txt`.

Three findings from the run, each of which changes how the template is used:

- **The SF1 half was NOT the bulk of the work.** The task text assumed it
  (loop 82 had run SF1 by hand). Measured: the SF1 plan A/B capture of both
  arms, including the two 3.3 GB offline clones and the PG reference arm,
  took **89 seconds**; the 24 fires executed in three batches of 1m59s to
  3m59s. The whole SF1 half is under 15 minutes of wall clock. What made it
  look expensive was the per-query invocation pattern, not the scale.
- **Batching is the difference between minutes and a week.** The execution
  arm starts its private clone server ONCE per invocation and then walks
  every ID in `FIRESET_QUERIES`, so `FIRESET_BATCH_SIZE=8` pays one clone
  plus one startup for eight queries. Loops 92 to 99 each ran
  `FIRESET_BATCH_SIZE=1` and therefore paid a full clone-and-start per
  query, advancing one ID per loop — eight loops for what one invocation of
  `FIRESET_BATCH_SIZE=8` does in under four minutes. `FIRESET_BATCH_SIZE`
  exists to bound a headless foreground interval; bound it as large as that
  interval allows, never at 1 out of caution.
- **SF1 derived the SAME fire set as SF0.25** (all 24 IDs, byte-identical
  list). The changed-plan set is a property of the planner route, not of the
  scale — so the two-scale requirement is about the *timeout class*, which
  is scale-sensitive, and not about re-deriving a different set per scale.

The SF1 arms' own parity numbers are recorded alongside, and they are the
reason the fire set is not a parity claim: baseline `match=1 shapediff=73`,
candidate `match=0 shapediff=74` against the PG 18.3 reference. The gate
asserts "no timeout-class increase" only.

### Non-vacuity of the pass condition

The green verdict above is only meaningful because the comparator is
demonstrably able to fail. Checked directly at acceptance time:

| baseline | candidate | verdict | exit |
|---|---|---|---|
| `Q2 PASS` | `Q2 TIMEOUT` | `introduced=Q2` | 1 |
| `Q2 TIMEOUT` | `Q2 TIMEOUT` | `unchanged=Q2` | 0 |
| 24 recorded | 8 recorded | `missing=<16 IDs>` | nonzero |

So a newly introduced timeout is fatal, an inherited one stays visible as
evidence without failing the gate, and an incomplete batch can never be
mistaken for a pass. `scripts/tpcds-fireset-status.py --self-test` passes and
`scripts/tpcds-plan-diff-test.py` runs 10 cases green.

### How a task uses this template

A task touching the derived-input firewall, row estimation, or the cost
model points the candidate arm at its change and runs both corpora:

```
CANDIDATE_ENV_FILE=tmp/mytask.env \
  scripts/tpcds-fireset-gate.sh <task-id> tmp/<task-id>
```

`CORPORA` defaults to `"tpcds-sf025 tpcds-sf1"`, so the default invocation
is already the two-scale gate. Add `FIRESET_RESUME=1 FIRESET_BATCH_SIZE=N`
only to fit a bounded runner interval; the gate is not passed until the
comparator reports `missing=none` for every corpus.
