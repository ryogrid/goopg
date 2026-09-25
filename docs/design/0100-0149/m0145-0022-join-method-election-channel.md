# M0145-0022 — the join-method election channel on the SF0.25 sweep

Status: **LANDED report-only 2026-09-22.** Promotion to gate-deciding is the
task's own deferred call — see "Promotion".
Kind: impl
Parent: none
Movement: none — a harness channel; it reports, it does not plan.

## Why this exists

M0145-0012 and M0145-0018 both produced plans whose **values were verified
byte-identical at SF0.25** while the shape and the clock regressed — Q74 16.6x
at that scale, and at SF1 Q78 did not finish at all, so no value comparison
existed there. Every value gate in the harness is blind to that class. The
sweep's plan-shape channel already reported THAT a plan moved; nothing reported
WHICH WAY, and the direction is what separates a neutral re-shape from the
C-04a failure.

## Half of the task already existed

The task asks for two things. The **wall-clock threshold report is already
implemented**: `scripts/tpcds-sweep-diff.py` emits `STATUS-DELTA … verdict-changes
… runtime-moves=N … total-delta=±X%` with a ≥2.0x per-query threshold, a 5 s
floor and TIMEOUT readings excluded. Checking before building is the whole of
what this section records; the remaining work was the election classification.

## What landed

`scripts/tpcds-plan-diff.py` gains a join-method channel over the queries the
shape diff already found changed:

```
# join-method: Q6 hash+4 nestloop-4
=== JOIN-METHOD-ELECTION: moved=4 into-nestloop=0 ===
```

- Join nodes are counted per family (nestloop / hash / merge) and only the
  **delta** is printed, so a query whose methods did not move is not listed —
  the shape channel already covers cost-only movement.
- A move **into** Nested Loop is named a suspect, because that is C-04a's
  signature: an equi-join demoted to a `Join Filter` on a nested loop priced
  off an epsilon row estimate. Moves out of it, and merge/hash exchanges, are
  reported without judgement — a plan may elect a nested loop for good reasons,
  and this channel exists to make a human look, not to decide.
- **Report-only by construction.** Nothing in it touches the exit status, which
  remains `--strict`'s alone.

## It retro-validates M0145-0019a

Run over the capture pair that brackets the LIMIT-fraction ordering gate:

```
changed (6): Q6 Q8 Q35 Q43 Q44 Q69
# join-method: Q6 hash+4 nestloop-4
# join-method: Q8 hash+1 nestloop-1
# join-method: Q43 hash+1 nestloop-1
# join-method: Q44 hash+1 nestloop-1
=== JOIN-METHOD-ELECTION: moved=4 into-nestloop=0 ===
```

Four queries moved OUT of Nested Loop and none moved in — exactly the direction
that fix should produce, confirmed independently of the reasoning that produced
it. Against an 11-day-old baseline the same channel reports `moved=43
into-nestloop=25`, so it is not simply reporting zero.

## Tests, and the trap the first draft fell into

`scripts/tpcds-plan-diff-test.py` covers the node-name matching (`Parallel Hash
Join` must count once as hash and not also as the `Parallel Hash` build node;
`Nested Loop Left Join` is still a nested loop), both directions of the
suspect call, and that a cost-only change is not listed.

The first draft wrote its fixtures with `=== Q1` headers where captures use
`===== Q1 =====`. The tool parsed **zero blocks**, and every assertion about
absence passed vacuously — three of seven tests were green for no reason. This
repository has been bitten by that exact off-by-two before (an awk range using
`=== Q5` against a `===== Q5 =====` capture, recorded under the set-op
common-type work). The fixture writer now carries a comment saying so.

Non-vacuity checked: neutralising the suspect-direction call fails exactly the
two tests that assert it and leaves the rest green.

## Promotion

The task says to start report-only and promote after one clean corpus cycle.
This is that start. Promoting means deciding what a suspect should DO — fail
the gate, or require an explicit acceptance — and that is a policy call with a
false-positive cost, since a move into Nested Loop is legitimate whenever the
inner really is tiny. Ledgered rather than taken here.
