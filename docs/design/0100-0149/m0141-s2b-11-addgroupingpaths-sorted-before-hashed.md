# M0141-S2b-11 — reorder `addGroupingPaths`: SORTED candidates before HASHED (PG's insertion order)

`Kind: impl` · `Parent: M0141-S2b-10`

Status: **landed 2026-09-18 (`1e48aca50`)** — `Movement: none` (zero
corpus plan movement; expected Q4/Q12 flip is masked by the M0129-S1
deviation — see "Finding" below; follow-up filed as M0141-S2b-12)

## Task

`internal/optimizer/groupingpaths.go`'s `addGroupingPaths` emitted the
HASHED `PathAgg` candidate before the SORTED candidate. Real PG's
`add_paths_to_grouping_rel`
(`postgres/src/backend/optimizer/plan/planner.c:7114`) emits its `can_sort`
block (`planner.c:7129`, Sorted/Plain) **before** its `can_hash` block
(`planner.c:7273`, Hashed). Because `addPath`'s tie-break on a
fuzzy-cost/equal-pathkeys collision keeps whichever candidate was inserted
FIRST (the `STD_FUZZ_FACTOR` rule both engines share — `path.go`'s
`stdFuzzFactor`, matching `pathnode.c`/`add_path`'s documented "keep the
first of two indistinguishable paths" behavior), goopg's inverted order
silently elected Hashed+explicit-Sort wherever the two strategies were
within ~1% — exactly TPC-H Q4/Q12, where real PG elects a Sort-fed
`GroupAggregate` (root-caused by M0141-S2b-10; PG's own cost formula also
prices Hashed cheaper, so no formula was touched).

The fix is a **pure block reorder — no cost arithmetic changes**: emit the
SORTED candidate(s) first, the HASHED candidate second.

## Change

`internal/optimizer/groupingpaths.go`, `addGroupingPaths` only:

- The `if aggNode.GroupingSets == nil { ... }` SORTED block now runs before
  the `if groupingHashable(...) || aggNode.GroupingSets != nil { ... }`
  HASHED block, mirroring `planner.c`'s can_sort-then-can_hash order.
- The SORTED block's two early `return`s — which in the old order meant
  "hashed is already filed, stop" — became a `haveSortedCandidate` flag +
  if/else so the same candidate *set* is produced in the new order:
  - `!presorted && groupingHasSpecialAgg(aggNode)` →
    `haveSortedCandidate = false` (decline sorted, fall through to hashed —
    identical outcome to the old early return).
  - index-ordered variant found → emit it, skip the explicit-sort variant
    (old `return`; now if/else), then hashed.
- An explanatory comment records why the order matters (insertion-order
  tie-break, citing `planner.c:7129`/`:7273` and M0141-S2b-10).

Candidate sets are identical before/after; only insertion order into
`grouped.Pathlist` changed. No cost formula, no new candidate, no removed
candidate.

## Expected effect (named movement, per S5)

TPC-H Q4/Q12 flip from Hashed+explicit-Sort to Sorted (Sort-fed
`GroupAggregate`), matching PG 18.3's own election — verified against the
parent recon's live `:65432` `EXPLAIN` evidence. Both queries' group key ==
order key, so the Sorted candidate's pathkeys satisfy the query ORDER BY
with no extra Sort — the fuzzy-tie situation M0141-S2b-10 measured
(0.17%–0.66% margins). Q5/Q21 unaffected (their Sorted candidate's
pathkeys do not satisfy ORDER BY; no tie arises). Any *other* query whose
plan shape moves must be individually cross-checked against a fresh PG
`EXPLAIN` before being accepted — a flip toward PG is the desired effect,
a flip away is a regression to file.

## Measurement

Method: two `tpch-estimate-audit-arm.sh` PLAN_ONLY arms (before = binary
built from a detached worktree at pre-change HEAD `44869f887`; after =
binary built from the working tree with the reorder staged), `PGSHAPED=1`,
private clone/port 5582, `--ref-port 65432` live PG capture, artefacts in
`analysis/m0141/m0141-s2b11-{before,after}.{txt,plans.txt,pg.plans.txt}`.

**Stats-epoch discipline mattered.** A first pair of arms ran WITHOUT a
pinned `GOOPG_ANALYZE_SEED` and produced different stats epochs
(`2df0e452`/`5d65c638`); the resulting before/after diff showed a phantom
Q3 GroupAggregate→HashAggregate "flip" that was entirely ANALYZE-sample
noise, not the reorder. Re-running both arms with
`GOOPG_ANALYZE_SEED=20260905` produced the SAME epoch (`253df6b1d97f9f5b`)
and the true result:

```
SHAPE-DELTA: queries=22 text-changed=0 shape-changed=0   (byte-identical)
PLAN-PARITY before: match=7 shapediff=13 unparsed=0 missingnode=2 error=0 timeout=0
PLAN-PARITY after:  match=7 shapediff=13 unparsed=0 missingnode=2 error=0 timeout=0
CATEGORIES            (both): join-order=13 join-method=9 scan-type=8 parameterisation=5 aggregation-strategy=9 sort-strategy=8 parallelism=0 qual-placement=4 rendering=1
CATEGORIES-EXCL-MATCH (both): join-order=13 join-method=9 scan-type=8 parameterisation=5 aggregation-strategy=9 sort-strategy=8 parallelism=0 qual-placement=4 rendering=0
```

(same-PG-reference control: both goopg captures diffed against the before
arm's own live PG capture). TPC-DS SF0.25 sweep: `PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0`, `PLAN-SHAPE: queries=99 same=99
changed=0`. **Zero plan movement on either corpus.**

## Finding — the expected Q4/Q12 flip is masked by the M0129-S1 deviation

The task expected Q4/Q12 to flip Hashed→Sorted. They did not — and the
reason is a deeper deviation the parent recon's mechanism did not reach.
`DP_TRACE=1` on the after binary (`m0141-s2b11-after-trace`) shows, for Q4:

- `groupcand strategy=1` (Sorted) → `upper.ordered.input` total=507569.94,
  `verdict=accepted` (now inserted FIRST, as intended);
- `groupcand strategy=0` (Hashed) → `upper.ordered.sort` total=506697.64 —
  0.17% cheaper, inside the 1% `STD_FUZZ_FACTOR` band — `verdict=accepted`
  anyway, and wins.

Root cause: `comparePathCostsFuzzily` (`internal/optimizer/path.go:901`)
carries the **M0129-S1 deviation** — when total and startup are fuzzily
equal it breaks the tie by EXACT cost (`path.go:943-956`) instead of
returning `costsEqual` like PG's `compare_path_costs_fuzzily`
(`pathnode.c`, which returns `COSTS_EQUAL` and lets add_path's other
dimensions / keep-first rule decide). Under M0129-S1 a strictly-cheaper
second candidate always dominates the first-inserted one, so insertion
order cannot flip any election where the costs differ at all — the
reorder is a faithful no-op under current semantics. Under true PG
semantics the second candidate would have been rejected on `COSTS_EQUAL`
(equal pathkeys, keep-first) → Sorted survives → GroupAggregate, matching
PG's actual Q4/Q12 plans. (Q12 shows the same shape: sorted 355795.87 vs
hashed 353535.20, 0.64% margin.) A second, same-origin masking exists one
level down: at the grouped rel, PG's pathkeys dimension would let the
sorted candidate (which carries group-key pathkeys) dominate the keyless
hashed one on a cost tie; M0129-S1's exact-cost direction makes the two
incomparable instead, keeping hashed alive for the ordered-rel
tournament — the precise effect M0129-S1 was designed to produce for
CTE self-joins.

Narrowing M0129-S1 is therefore the real remaining divergence, and it is
deliberately NOT done here: the deviation was added to stop pathkey-less
hash paths being evicted by fuzzily-equal-cost pathkeyed rivals (CTE
self-join nested-loop-only pathology), so removing/narrowing it needs a
blast-radius recon before any impl. Filed as **M0141-S2b-12**
(`Kind: recon`, `Parent: M0141-S2b-11`).

Sibling note for the follow-up: `partialaggupper.go`'s no-split arm emits
the same hashed-before-sorted order (hashed at :400, sorted at :412,
GatherMerge variant at :474) — same inversion class, same masking under
M0129-S1, out of this task's scope.

## Gates run

| gate | result |
|---|---|
| `go build ./...` | clean |
| `go test ./internal/optimizer/...` | PASS (cached-PASS on the exact staged code; no `-count=1`) |
| `scripts/tpch-spotcheck.sh` | PASS — Q12=2, Q13=34 canonical; stamp PASS vs staged code_tree |
| `scripts/tpcds-sf025-regression.sh sweep` | PASS=96, MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0; STATUS-DELTA verdict-changes=none; PLAN-SHAPE 99/99 identical |
| `scripts/tpch-acceptance-arm.sh` | PASS — 24/24 labels value-identical, baseline=worktree-at-HEAD binary vs after=staged binary, `PGSHAPED=1`, seed 20260905, port 5583 |
| `pg-plan-parity-diff.py` (TPC-H, same-PG control) | match=7 before AND after — floor held (noise band ±3 vs pinned 8); no category moved |
| `python3 scripts/ralph_protected_regions.py check-designdocs` | exit 0 |
| `python3 scripts/ralph-lineage-guard.py` | exit 0 |
| pre-commit pgbench smoke (hook, on `1e48aca50`) | PASS |
| `make ea-ratchet` | N/A — no estimate/selectivity/cardinality change (insertion order only; corpus plans byte-identical confirms) |
| seam-decline census | N/A — no join-enumeration seam touched |

D2 extras: stats epoch of both arms `253df6b1d97f9f5b` (pinned seed
20260905); planning route = PG-shaped search (`PGSHAPED=1`); `Movement:
none` (TPC-H match 7→7, no CATEGORIES-EXCL-MATCH category moved beyond
±3 — none moved at all; TPC-DS 99/99). No query's plan changed, so there
are no changed-query wall times to report; no query got slower.

## Cleanup

Detached worktree `/tmp/wt-s2b11-base` removed; arm binaries
`/tmp/goopg-s2b11-{base,after}` and arm outputs `/tmp/s2b11-arm-*.txt`
left in `/tmp` (ephemeral, uncommitted). Private-port servers (5582,
5583) stopped by the scripts' own cleanup traps, verified down. No
shared cluster (`:65432`/`:65433`/`:65437`/`:65438`) was started,
stopped, reset or written beyond read-only `pg_basebackup`/`EXPLAIN`.
