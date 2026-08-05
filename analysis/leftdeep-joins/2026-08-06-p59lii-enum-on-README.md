# M0127-P5.9-l-ii — clause 6 measured on TPC-H SF=1 (2026-08-06)

The run that closes 09 §4 clause 6. Full write-up: `docs/design/leftdeep-joins/
09-verification-and-acceptance.md` §3.13.

## What was run

```
PLAN_ONLY=1 DP_TRACE=1 PGSHAPED=1 PER_Q=180s \
  scripts/tpch-estimate-audit-arm.sh 2026-08-06-p59lii-enum-on --queries 7,8,20
```

HEAD `f8d6622e` plus this loop's `--plan-only` / `CROSS-QUERY-LEVEL` changes.
Cluster: TPC-H SF=1 goopg on 65433 (`bench/tpch/runtime_goopg/data`), fresh
capped server, `max_parallel_workers_per_gather = 0`, per-session ANALYZE on all
eight tables. PG reference: the committed capture
`2026-08-05-p56giii-parity.pg.plans.txt`. Wall clock ≈ 4 min.

`PLAN_ONLY=1` means plain `EXPLAIN`: no query was executed, nothing was timed.
That is why the run was taken **while the nightly CI batch held the host** —
the arm's nightly refusal protects a timing measurement, and this run has none.
Its own §5 and §4-parity sections are omitted for the same reason: with no
actual row counts they cannot be scored, and printing them empty would end in a
clean verdict over unmeasured rows.

## Result

```
controls (goopg's OWN bushy pairings, must all be OFFERED): 2/2
controls set aside as CROSS-QUERY-LEVEL (a SubPlan boundary, not a partition): 1
candidates (PG-only bushy pairings): 2/2 offered by the goopg search
VERDICT: every PG-only bushy partition WAS enumerated — the divergence is
         cost/stats, which 09 §4's ratchet admits. Clause 6 passes.
RATCHET enum_controls=2/2 enum_controls_oos=1 enum_candidates_offered=2/2 \
        enum_candidates_crosslevel=0 enum_problems=3 enum_malformed=0
```

Both partitions §3.11 left open were offered to `makeJoinRel` at `phase=2` (the
bushy pass) with `created=false` — another pairing reached the same relset
first, so the shape was **enumerated and lost on cost**, not unreachable:

| query | PG-only bushy partition | verdict |
|---|---|---|
| Q7 | `{customer+lineitem+n2+orders} ⋈ {n1+supplier}` | `OFFERED` phase=2 lev=6 |
| Q8 | `{lineitem+orders+part} ⋈ {customer+n1+region}` | `OFFERED` phase=2 lev=6 |

The Q20 control is set aside, not failed: goopg's plan prints
`{nation+supplier} ⋈ {lineitem+part+partsupp}`, but the trace shows Q20's only
join problem is `{nation,supplier}` — the other three relations are planned
under SubPlans. A printed plan does not mark query levels; that node is a
planning boundary, not a partition any search chose.

## Files

| file | what |
|---|---|
| `2026-08-06-p59lii-enum-on.txt` | the report (spine diff + enumeration provenance) |
| `2026-08-06-p59lii-enum-on.plans.txt` | goopg's raw `EXPLAIN` output, Q7/Q8/Q20 |
| `2026-08-06-p59lii-enum-on.dptrace.txt` | the 346 `DPTRACE` lines from the arm's server log |

Re-derive the verdict offline, without a cluster:

```
go run ./cmd/estimate-audit --label recheck --plan-only \
  --from-plans analysis/leftdeep-joins/2026-08-06-p59lii-enum-on.plans.txt \
  --reference  analysis/leftdeep-joins/2026-08-05-p56giii-parity.pg.plans.txt \
  --enum-trace analysis/leftdeep-joins/2026-08-06-p59lii-enum-on.dptrace.txt
```

## Caveat carried forward

The three queries are §3.11's clause-6 candidate set, not all 22. A candidate
that only appears at another query's spine would not be in this run; the arm
takes `--queries`, and a full-22 plan-only sweep is now cheap enough to be
routine. Deferral ledger row `2026-08-06 M0127-P5.9-l-ii`.
