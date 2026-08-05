# M0127-P5.9-l — the join-spine (pairing) channel, and its first measurement

2026-08-06. Design: `docs/design/leftdeep-joins/09-verification-and-acceptance.md`
§3.11 (result) and §4 (where the instrument landed).

## What this is

Clause 6 of the S5 acceptance bar asks whether goopg's join search can produce
the bushy tree shapes PG 18.3 chooses. 09 §4 specified the check as "verified
through the §4 parity gate's spine diff"; four acceptance runs scored it from a
proxy because no such diff existed. This is that diff.

The unit is the **pairing**, not the joinrel. `estimateaudit.Parity` keys a
joinrel by the SET of base relations underneath it (upstream's
`RelOptInfo.relids`) — correct for comparing *estimates*, blind to *order*. On
Q7 both engines build the same six-relation top joinrel and the parity channel
calls it `matched`; they partition it completely differently.

## How to re-derive it (no server, no re-run)

```bash
go build -o tmp/estimate-audit ./cmd/estimate-audit
./tmp/estimate-audit --label 2026-08-06-p59l-spine-on \
  --from-plans analysis/leftdeep-joins/2026-08-05-p59run4-audit-on.plans.txt \
  --reference  analysis/leftdeep-joins/2026-08-05-p56giii-parity.pg.plans.txt \
  --fail-on-violation=false
```

`…-audit-off.plans.txt` for the OFF arm. A live arm gets the same section for
free — `scripts/tpch-estimate-audit-arm.sh` needed no change, because the
section renders whenever a `--reference` is present.

## Result (run 4's committed plans, TPC-H SF=1, 22 queries)

| | flag OFF | flag ON |
|---|---|---|
| pairings matched | 13 | **24** |
| PG-only pairings | 44 | **33** |
| goopg-only pairings | 45 | **32** |
| bushy spine chosen by goopg | 2 (Q5, Q20) | **6** (Q2, Q7, Q8, Q9, Q10, Q20) |
| bushy spine chosen by PG | 3 (Q7, Q8, Q20) | 3 (Q7, Q8, Q20) |
| clause-6 candidates | 2 (Q7, Q8) | 2 (Q7, Q8) |

1. **goopg's search expresses and WINS a real bushy TPC-H partition.** Q20's
   top pairing is `{nation+supplier} ⋈ {lineitem+part+partsupp}` on both
   engines. 09 §3.10 recorded the opposite ("goopg produces no bushy spine on
   any of the 22, in either arm") from a manual reading; that reading is
   superseded. The PG side of it (bushy on exactly Q7, Q8, Q20) stands, as does
   the Q7 partition it quoted.
2. **The flag moves every spine number toward PG.** Matched pairings nearly
   double; both one-sided counts fall ~25 %.
3. **Two candidates remain**: PG's bushy top on Q7
   (`{customer+lineitem+n2+orders} ⋈ {n1+supplier}`) and Q8
   (`{lineitem+orders+part} ⋈ {customer+n1+region}`).

## What it does NOT settle

For Q7 and Q8, "enumerated by the DP and lost on cost" and "never enumerated"
predict the identical observable — a chosen plan without that pairing. This
channel reads chosen plans on both sides, so it names the question and cannot
answer it. → **M0127-P5.9-l-ii**: record the pairings `makeJoinRel` was
actually offered, with their phase, and test membership directly.

Ambiguous pairings (a relation scanned twice without an alias: Q2, Q8, Q17,
Q18, Q22 on the ON arm) are printed but excluded from the candidate list — the
same rendering gap 09 §4.1 records as making `shape_mismatches` an upper bound.
Q8 still yields a candidate because it comes from the reference side, which
deduplicates relation names (`select_rtable_names`, ruleutils.c).

## Files

- `2026-08-06-p59l-spine-on.txt` / `-off.txt` — the audit artifacts, whose
  third section is `=== JOIN SPINE PAIRINGS vs PG 18.3 (09 §4 clause 6)`.
  The §5 and §4 sections above it are re-derivations of run 4's own numbers and
  agree with `2026-08-05-p59run4-audit-{on,off}.txt`.
