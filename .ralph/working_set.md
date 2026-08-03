(idle — nothing in flight)

M0127-P2.3 is CLOSED and pushed. **S2 IS CLOSED (P2.1+P2.2+P2.3).**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P3.1` (`chooseHashTableSize` in a shared pkg
importable by planner AND executor; goopg-width-aware `48·c` + map
overhead). Bar: UNITS + SPOT.**

Carry-over facts a next loop should not re-derive:

- **There are now FOUR key views, do not confuse them.**
  `Join.HashKeys` = the plan's truth (every equi-pair; what EXPLAIN renders
  as `Hash Cond:`/`Merge Cond:`, filled for `JoinAlgoMerge` too).
  `Join.ExecHashKeyPlan()` = the hash executor's narrower
  `{Keys, Residual, Int64Keys}`. `Join.ExecMergeKeyPlan()` (NEW, P2.3) =
  the merge executor's; same `pairIsHashSafe` filter, `Int64Keys` always
  false (it selects the hash packing lane). `Join.Residual()` = the
  projection against the FULL list, used by nothing in the executor.
- **Merge and hash key state are separate joinOp fields ON PURPOSE.**
  `mergeKeys`/`mergeResidual` (set by `initMergeKeys`, top of
  `runMergeJoin`) vs `execKeys`/`execResidual` (set by `initExecKeys`, the
  lazy-hash Opens). One joinOp runs one algorithm; separate slots make a
  cross-read a compile error. `joinPredicateMatch` (row-based, reads the
  FULL `plan.Predicate`) is still what runNestedLoop uses — merge now uses
  `mergeResidualMatch`.
- **`pairIsHashSafe` is still the single load-bearing guard**, now for two
  callers. float4/float8 are TEXT datums; enum/toast have no `datumKey`
  arm. New for P2.3: `compareDatum`'s `KindString` arm CONTENT-SNIFFS
  (pg_lsn → uint64, UUID normalisation, `(`/`{` → element-wise numeric),
  so text equality is not strictly `=`. Ledger row `2026-08-03 M0127-P2.3`.
- **P2.3 moved no plan and no DS05 timing.** `make plan-diff
  LABEL=m0127-p21-hashkeys` = MATCH 22/22, so that baseline still stands
  for P3. DS05 timings flat vs P2.2 (Q47 30 s, Q54 145 s, Q67 231 s,
  Q78 20 s, Q88 156 s, Q97 13 s / checksum unchanged).
- **DS05 post-TIMEOUT restart hazard is still live** — the sweep dies after
  Q72 on a `systemd-run` scope-name collision. Recovery: `systemctl --user
  reset-failed`, `bench/tpcds/server.sh start sf05`, then
  `QUERIES="$(seq 73 99)" scripts/tpcds-sf05-regression.sh sweep`.
- **Do NOT `git stash`** in this tree (9 unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified.
  Tracking = `docs/design/0127-pg-shaped-join-search.md` §6 + fix_plan
  checkbox + README index status.

Gates run this loop: UNITS PASS; SPOT PASS (Q12=2 / Q13=35, 18.1 s);
PLAN MATCH 22/22; DS05 MISMATCH=0 / CKMISMATCH=0 / ERROR=0 across all 99
(Q1-Q72 `sweep-20260803-184827.txt` 68 PASS / 3 SKIP / Q72 TIMEOUT 315 s;
Q73-Q99 `sweep-20260803-192520.txt` 26 PASS / 1 SKIP / TIMEOUT=0);
SMOKE via the commit hook; `make ralph-state-guard` OK.

In-flight: none.
