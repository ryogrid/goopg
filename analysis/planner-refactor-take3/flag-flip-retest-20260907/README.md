# Flag-flip re-test on current HEAD (2026-09-07), and an A/A error of mine

Motivation: four blockers went stale in one day (B-17b, E-16, C-19h, C-06), so
the remaining flag-retirement items were re-tested rather than inherited.
Binary built from `4b9ab15a2`; TPC-H SF=1 bench cluster, port 65433, db `tpch`,
`GOOPG_ANALYZE_SEED=20260905`, fresh cgroup-capped server per arm,
`estimate-audit -plan-only` against one common baseline capture.

## The error, first

My first sweep reported **0 diff lines** for both `GOOPG_INDEXKEY_HARVEST=0`
and `GOOPG_HASH_OUTER_JOIN=0`, i.e. "both flips are now byte-identical". Both
were **A/A comparisons, not A/B**:

| flag | parser | what `=0` actually did |
|---|---|---|
| `GOOPG_INDEXKEY_HARVEST` | `v != "off"` | `"0" != "off"` -> stayed **ON** |
| `GOOPG_HASH_OUTER_JOIN` | `v == "1"` | default is already **OFF**; `"0"` changed nothing |

This is the mirror image of the trap recorded as
`goopg_arm_scripts_disable_dp_search` (an arm exporting `GOOPG_PGSHAPED_DP=0`
measured a planner in which the function under test is never called, and four
capture rounds showed "no change" for that reason alone). **A flag's off-value
is per-flag and must be read from its parser, never assumed to be `0`.**

## Corrected results

| arm | value | diff vs baseline | reading |
|---|---|---:|---|
| `GOOPG_INDEXKEY_HARVEST` | `off` | **103 lines** | flip moves plans; C-20c's blocker is NOT stale, it still binds |
| `GOOPG_HASH_OUTER_JOIN` | `1` | **0 lines** | see below — this is a gate with no witness, not a neutral flip |
| `GOOPG_NLI_COSTGATE` | `legacy` | 20 lines | flip moves plans; consistent with C-20f's Q4 11.4x finding |

## `GOOPG_HASH_OUTER_JOIN`'s 0 is not evidence of neutrality

The flag governs `chooseOuterFillJoinAlgo`, which by its own comment is
"the same decision for the two join types whose preserved side the hash
executor could not fill: **RIGHT and FULL**".

**The TPC-H corpus contains ZERO RIGHT joins and ZERO FULL joins** (and exactly
one LEFT join). So a 0-line diff on TPC-H is *structurally guaranteed* and says
nothing about the flip. This is the same failure the C-19h census hit — a gate
returning a clean pass for a shape it cannot express
(`goopg_gate_with_no_witness_passes_parity_break`) — and here it applies to
C-20e's own stated gate ("byte-identical plans for the flip"), which TPC-H
cannot meaningfully adjudicate.

**Where the witness would have to come from:** TPC-DS, which is LEFT-join-rich
(C-06s moved Q5/Q40/Q75 there). Any future C-20e verdict must be measured on
that corpus, and TPC-H's agreement must not be counted as evidence.

## One observation worth filing

Q13's default plan is now a `Hash Right Join` (C-06s), yet
`GOOPG_HASH_OUTER_JOIN` is **off by default** and turning it on changes
nothing. So the right-hash Q13 is NOT reached through
`chooseOuterFillJoinAlgo`; C-06s produces it through the ordinary hash path via
the commuted direction. That suggests the flag's decision may be partly
redundant now, which is an argument to re-scope C-20e rather than to retire or
keep it on the current evidence.
