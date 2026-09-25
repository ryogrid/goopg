# M0144-0001 — Canonicalise parallel-mode TPC-H parity

Status: done — flag flipped, caller audit clean, floor re-pinned by the
fresh parallel capture (see §4).

## Context

Owner decision 2026-09-20 (filed as milestone `M0144`, banner item 2,
milestone doc `docs/milestones/0144-measurement-first-parity.md`): TPC-H
plan parity is measured in **parallel mode** — both engines plan with
`max_parallel_workers_per_gather` enabled — because that is the protocol
the product actually serves (`:65433` runs `max_parallel_workers_per_gather=4`)
and what `make plan-gate` has always compared. Serial mode stays a
diagnostic variant.

`cmd/estimate-audit`'s `-serial` flag still defaulted `true`, so the
canonical measurement tool measured the non-canonical protocol unless every
invocation remembered `-serial=false`. This task flips the default.

## Change

- `cmd/estimate-audit/main.go` — `-serial` default `true` → `false`; the
  package comment's "`--serial (default)`" phrasing updated to describe the
  split: `-serial=true` remains for the EXECUTED estimate audit (goopg does
  not propagate ANALYZE instrumentation out of parallel workers, so nodes
  under a `Gather` report no actual rows), while parallel is the default for
  `-plan-only` parity captures.
- `scripts/tpch-estimate-audit-arm.sh` — already pinned `-serial=true`
  explicitly in the same owner change that filed this task (its contract is
  an executed serial run); comment updated to past tense.

## Caller audit (required by the task)

Every invocation site of the `estimate-audit` binary:

| site | mode needed | state |
|---|---|---|
| `scripts/tpch-estimate-audit-arm.sh:195` | serial (executed audit) | pinned `-serial=true` before this task |
| `scripts/estimate-parity-gate.sh` / `make ea-ratchet` | n/a | TPC-DS-only; never invokes the binary |
| `scripts/seam-decline-census.py`, `qual-placement-census.py` | n/a | read `<label>.plans.txt` artefacts only |
| `scripts/capture-tpch.sh`, `check-stats-epoch.sh`, `tpch-private-clone.sh`, `ralph-bash-guard*.sh` | n/a | comments only |

No invocation site relied on the old default. No test pins it
(`cmd/estimate-audit/main_test.go` covers `selectQueries`, the stats-epoch
hash, and offline-replay degradation — none touch `flags.serial`).

## Floor re-pin — measured result (§4)

G3 capture 2026-09-20 on HEAD `2a2ebc3f0`: goopg half via
`PLAN_ONLY=1 scripts/tpch-estimate-audit-arm.sh m0144-0001-goopg-parallel
-serial=false -out analysis/m0144` (private clone `:5582`, online
`pg_basebackup` from `:65433`, served-binary sha256
`cf3bae66…` verified against `/proc/<pid>/exe`); PG half via a second bare
`estimate-audit -plan-only -serial=false -warm-stats=false -port 65432`
invocation (R1: never `-ref-port`, which would ANALYZE the reference —
recipe corrected in `m0137-0003` §2 in this same loop).

Result (`pg-plan-parity-diff.py` on
`analysis/m0144/m0144-0001-{goopg,pg}-parallel.plans.txt`):

```
PLAN-PARITY: queries=22 match=1 shapediff=21 unparsed=0 missingnode=0 error=0 timeout=0
CATEGORIES: join-order=18 join-method=11 scan-type=13 parameterisation=7 aggregation-strategy=6 sort-strategy=11 parallelism=20 qual-placement=5 rendering=1
```

**match=1/22 (Q6)** — reported to the owner for the `AGENT.md` §Goal floor
re-pin (the harness section is owner-edited; the loop records the number,
it does not write the floor). The P0-E7 provisional `>=3` was measured on
the pre-reload stats epoch: both epochs differ here (goopg
`7e63d8f62dbc2bd9` vs `55ec1f3ffbd9ef2c`; PG `1d792464639c67da` vs
`82281b5e012d9acc`) because the 2026-09-20 8-FK reload reloaded both
clusters between the two captures, so this is a new-baseline measurement,
not a same-data regression. Of the two lost matches, Q13's PG plan is
byte-identical in shape across epochs while goopg's flipped
serial→Gather+Partial-HashAggregate on the new stats; Q11's goopg InitPlan
flipped NL+index → Hash-Join chain for the same reason. No production
commit between the two captures touches aggregation or join-order costing
(the only one, the IN-unnest SJInfo producer, has verified zero corpus
reachability), so the move is attributable to the stats epoch, not code.


## Non-goals

- No estimate or costing change: the flag only decides whether the audit
  session issues `SET max_parallel_workers_per_gather = 0`. Parity
  categories are unchanged by construction (`PARITY: N/A — measurement-tool
  flag default` on the code commit).
- The executed-audit arm (`tpch-estimate-audit-arm.sh`) is untouched
  semantically — it pinned `-serial=true` already.
