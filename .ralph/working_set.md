(idle — nothing in flight)

# Loop #82 — M0145-0018's FRESH E1 re-run: blocker GONE, one criterion still fails

Banner: the GO says 0018's fresh E1 re-runs once 0019 lands. 0019/0019a landed,
so this is that re-run. **Measurement only — `problemPairsOuterWithDerived`
untouched, zero production diff (C1).**
Design: `docs/design/0100-0149/m0145-0018-firewall-relaxation-no-go.md`
§"Fresh E1 re-verification (2026-09-22)".

## Fire set RE-DERIVED (the task insists it drifts)
SF0.25 knob-arm census: `outer-over-derived` **3**; with the firewall off the
class vanishes (`declines` 17→14). Affected queries: **Q77, Q78** — unchanged.
**PLAN-DIFF TRAP**: a naive A/B diff reports FIVE changed queries
(Q36, Q70, Q77, Q78, Q86). Q36/Q70/Q86 are the capture's pre-existing parse
`error=3` queries whose only difference is the temp FILENAME in the psql error
text. Do not chase them.

## The 2026-09-21 no-go is GONE — M0145-0019a fixed it
Q78 SF1 firewall-off was `Nested Loop Left Join (cost=5494.86..1147565.07)`,
>1800 s. Now: `Hash Left Join (cost=16457.31..37717.11)`.

## SF1 execution A/B (private clone, fresh server per arm)
| query | firewall ON | OFF | checksum |
|---|---|---|---|
| Q77 | 5492 ms, 44 rows | **6373 ms**, 44 rows | `e2f12e6ef310f604` both |
| Q78 | 29608 ms, 100 rows | 29140 ms, 100 rows | `5e12c7e6baa093e8` both |
Values byte-identical; Q78 holds its ~29 s class; no timeout; no C-04a class.

## WHY NOT EXECUTED — criterion 1 fails on Q77
"no NL-epsilon election": with the firewall off, at BOTH scales, a `rows=1`
`Append` branch goes `Hash Left Join (cost=0.00..0.03)` →
`Nested Loop Left Join (cost=0.00..0.06)`, degenerate `s_store_sk = s_store_sk`
demoted to a `Join Filter`; top cost rises (SF0.25 `Limit` 10.65→11.58);
+881 ms / +16% at SF1. Benign, but literally what the criterion names.
Executing DELETES production code (R3 non-reversible) under a GO conditioned
on passing, and 0018's option choice is ALREADY awaiting a re-take (loop #79).

## OWNER DECISION NEEDED (this is the loop's deliverable)
Waive criterion 1 for the Q77 shape and execute the relaxation — or keep the
firewall and close 0018 as its own option 1 (permanent cost-model backstop).
Evidence: `tmp/m0145-0018-e1/` (both SF0.25 captures + four SF1 plans).

## Gates
Measurement only, zero production diff → no value gates (C1). state guard OK;
pgbench smoke via the commit hook. SF1 clone removed, port 5563 free.

## Next loop
0018 is owner-blocked. Per the banner the flow chain **0004 → 0005 → 0007 →
0008** resumes "after that, still under the firewall constraint until 0018
executes it" — so **M0145-0004** (UNION ALL → appendrel jointree entry) is the
next selectable item. Harness 0021/0022/0023 may run any time.

## Owner escalations — four open
partition_aggregate inventory row; template1 collision (A vs B); M0145-0018
(option re-take AND now the criterion-1 waive); M0145-0012 re-sequencing
behind M0145-0020a.
