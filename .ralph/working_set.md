(idle — nothing in flight)

# Loop #80 result — M0145-0019a LANDED (the LIMIT-fraction ordering gate)

Banner: OWNER GO sequence 0009 [x] → 0019 [x] → **0019a [x] this loop** →
M0145-0020 next. Then 0018's fresh E1 re-verification re-runs.
Design: `docs/design/0100-0149/m0145-0019a-limit-fraction-ordering-gate.md`.

## The change
`getCheapestFractionalPathOrdered` at `searchCtx.finalPath`: with an ordering
requested, the LIMIT fraction may only be won by a path that ALREADY satisfies
it; otherwise the answer is `CheapestTotal` — the path `create_ordered_paths`
would sort. No ordering requested → old behaviour unchanged.
Upstream: `planner.c:439` (fraction on `final_rel`), `:5314`
(`cheapest_total_path`), `:7646`+ (refuses other unsorted inputs), `:5337`+
(already-sorted path competes as is). `queryPathkeys` was already on
`searchCtx` — no new plumbing.

## Expected movement MET
Q78 SF1 (private `:5562`, knob arm, firewall off):
`Nested Loop Left Join (…1147565.07)` → `Hash Left Join (…37717.11)`;
`Limit` `1147784.10` → **`37936.15`**; the Join-Filter demotion is gone.

## The literal deliverable is NOT expressible yet — ledgered
"Move the call to the upper-rel lattice" can't be done: `createOrderedPaths`
takes an already-lowered **Node**, not a pathlist. **M0145-0007** (single
Path→Node lowering) is what makes it expressible; the ordering gate is deleted
then. Faking it over Nodes would add a second selection mechanism (G8 forbids).

## Gates — ALL PASS
units; tpch-spotcheck Q12=2/Q13=33; tpcds-sf025 `PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0`, `verdict-changes=none runtime-moves=0
total-delta=+1.6%`; acceptance arm 24 MATCH; pgbench smoke; state guard.
Plans moved as predicted: SF0.25 6 shapes (Q6 Q8 Q35 Q43 Q44 Q69), values
identical; TPC-H shapes byte-identical, costs lower (Q1 148207.10→147979.67).
Both parity captures taken WITH and WITHOUT the change (G7).

## ⚠ FLOOR FIGURE — read before reading AGENT.md's number
TPC-H parallel floor is **match=1 (Q6)**, re-pinned by **M0144-0001**; AGENT.md
still prints the older `>= 3` P0-E7 figure. Checking the re-pin is what stopped
this loop filing a false floor-breach escalation. SF0.25 floor match=2 (Q9,Q41).
Both held exactly.

## Next loop
**M0145-0020** — port `examine_simple_variable`'s non-recursive CTE arm
(0012's named prerequisite). After that the banner re-runs M0145-0018's fresh
E1 at SF0.25 AND SF1 and executes the relaxation only if it passes both.

## Owner escalations — two open + one to re-take
partition_aggregate's inventory row; template1 namespace collision (A vs B).
M0145-0018's option choice still needs re-taking on loop #79's evidence
(options (b) and (c) are both measured as wrong targets).
