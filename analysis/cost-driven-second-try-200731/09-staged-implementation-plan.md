# 09 — Staged implementation plan with verification gates

## Gate vocabulary (used by every stage)

| tag | command | notes |
| --- | --- | --- |
| **UNITS** | `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | the manual pre-commit bar (`CLAUDE.md`); scope var read at `scripts/ralph-precommit-test.sh:55` |
| **SMOKE** | the pgbench smoke run by the git hook on **every** commit | never `--no-verify` |
| **SPOT** | `scripts/tpch-spotcheck.sh` | fresh capped server; Q12=2, Q13=35 from `bench/tpch/spotcheck_expected.env`; **a "SKIPPED" line is not a pass** (R13) |
| **PLAN** | `make plan-gate` | needs `PATH` with `pg_isready` and a reachable goopg on 65433; defaults are `PLAN_DB=tpch PLAN_USER=tpch` (`Makefile:343-345`), but against a non-`tpch` cluster pass `PLAN_DB=postgres PLAN_USER=postgres`. SKIPs silently (exit 0) otherwise — **a SKIP is not a pass** |
| **DS05** | `scripts/tpcds-sf05-regression.sh sweep` | ~1 h, goopg-only, against the git-tracked oracle `bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt`; refuses to run while an SF=1 sweep is active (`guard_sf1_sweep`, `:159-165`) |
| **DIFF** | `TestFusedCascadeMatchesUnfused` (new, Stage 1) | differential fused/unfused, ordered text comparison |

Timing hygiene for any A/B in this plan (all from `CLAUDE.md` and this repo's memory):
hold server age constant; a server that just ran a timeout query sits at `GOMEMLIMIT` with
`GOGC=off` and thrashes; `timeout N psql` kills only the client — reap orphans; never
`pkill -f goopg`; always run under `GOOPG_CG_UNIT=<name> scripts/goopg-test-run.sh`.
Never pass `-count=1` to a gate's `go test`.

---

## Stage −1 — Land the packer's missing assertion (independent, do this first)

**Scope:** two lines in `internal/planner/bushy.go`, immediately before probe selection in
`collectMultiHashTables`:

```go
if len(keys) != len(scans)-1 {
    return nil, nil, 0, nil
}
```

**Why first:** R1 is a live silent-wrong-answer path that exists whether or not this proposal
proceeds. It fails closed (declines to pack), so its worst case is a performance change.

**Gates:** UNITS, SMOKE, SPOT, PLAN, DS05.
**Expected:** all green; PLAN may show a plan change for any query that was being packed
under-keyed — **if it does, that query was returning wrong rows and this is a bug fix; record
it prominently.**

---

## Stage 0 — Make the binary cascade honest (NO plan change, NO fusion)

This is the highest value-to-risk stage in the whole document set and it is **blocking**: its
measurement decides whether Stages 1-2 are worth building.

### 0a — stop re-materialising the accumulated row at every cascade level

> Revised after review (finding F2). The site differs between the two builders and both must
> be handled and measured separately — see [02 §4.1](02-premise-audit.md).

**0a-live (do this first; ~5 lines, no lifetime reasoning).** `Slot.fillFromTupleSlot`
(`internal/executor/opnode.go:129-150`) calls `ts.Row()` and then `copy(s.Cells, row)`. For a
`*VirtualSlot` source that is `acquireRow` + a column-by-column fill + a second copy, with the
pooled row dropped without `releaseRow`. Add a `*VirtualSlot` fast path that reads `v.Get(i)`
directly into `s.Cells`. This is on the **live server path**
(`joinOpKernelNext`, `opnode.go:868-876`).

**0a-legacy (only if measurement justifies it).** On the `Build` path the child `joinOp`
returns its `*VirtualSlot` directly, so `nextLazy`'s `r := slotRow(probeSlot)` is the
`acquireRow` site. The fix is to hold the child's `TupleSlot` as a source of the join's output
`VirtualSlot`.

**Hazard for 0a-legacy (finding F7):** the child does **not** return a stable slot object. A
child `joinOp` returns `o.lazyVirtualOut`, or `o.lazyOuterOnlySlot` for semi/anti, or a fresh
`*MaterializedSlot` from `Materialize()` in the FOR-UPDATE path; `rowsOp`/`spillOp` return a
fresh `asSlot(...)` per call (`spill.go:441`, `:468`). Meanwhile `ensureLazyVirtual`
(`operators_join_agg.go:1035-1068`) caches the output `VirtualSlot` once with a **fixed**
`sources` slice. So the source pointer must be **re-bound on every probe pull**, and a child
whose slot type changes mid-stream must fall back to the copy. Lifetime itself is fine: the
join serves all matches for a probe row before pulling the next, so the child's slot stays
valid — but that must be stated in a comment and covered by a fan-out test (multiple matches
per probe row).

### 0b — stop memcpy'ing a scratch key row per probe row

`nextLazy` (`operators_join_agg.go:1219-1232`) and both build loops (`:653-659`) copy the
accumulated row plus a null pad into `lazyKeyRow` purely so `evalHashKeyDatum` can resolve a
merged-space `ColumnRef.Index`.

**Change:** extract `evalHashKeyDatumSlot` alongside the existing slot-taking
`evalExprSlot`/`joinPredicateMatchSlot` (finding F11 — `evalHashKeyDatum`
(`operators_join_agg.go:960-968`) takes a `Row` today and **cannot** be "reused as-is" the way
[04 §8](04-fusion-site-and-data-structures.md) originally implied), then evaluate the key
against a `VirtualSlot` over `{realSide, nullOtherSide}` with the same index space. Zero copy,
identical resolution. This extraction is a **prerequisite of Stage 1**, not a Stage 1 task.

### 0c — measure

A/B on the TPC-H SF1 bench (65433), same server age, same GOGC/GOMEMLIMIT, with and without
0a+0b, **run with `mhjPackingEnabled` forced off** so the cascade is what is being measured.
Record `analysis/cost-driven-second-try-200731/evidence/stage0-ab.txt`.

**Derive the query set at the measurement HEAD (finding F15).** Do *not* reuse the
Q2/Q3/Q5/Q7/Q10/Q11/Q18/Q21 list — `operators_explain.go:1562-1572` records it as an
**M0054-0002 baseline** observation, not a statement about HEAD, and the planner has changed
substantially since. Run `EXPLAIN` over the TPC-H set at the measurement HEAD and take the
queries that actually emit `Multi-Way Hash Join`.

**Gates:** UNITS, SMOKE, SPOT, PLAN (must show **zero** diffs — Stage 0 is executor-only), DS05.

**Decision point:**

- If the cascade's per-row cost falls to within ~1.5× of the fused MHJ's on the packing
  queries → **stop here for performance purposes.** Skip Stages 1-2 entirely, go to Stage 4
  (retire the plan node), and the whole proposal reduces to a plan-shape cleanup with no new
  operator, no new contract, and no new bug class. This is the best available outcome.
- If a large gap remains → proceed to Stage 1, with the residual gap quantified.

---

## Stage 1 — Fusion scaffolding, decision function, and the differential harness (fusion OFF)

**Scope:**

- **`buildEnv` plumbing** ([04 §1.1](04-fusion-site-and-data-structures.md), finding F3):
  thread a per-build environment through `Build` and `buildRec`, carrying the plan root, the
  `inWorker` flag set by `newGatherOp`'s closure (`executor.go:213-219`), the
  under-instrumentation flag set by `explainOp.Open` (`operators_explain.go:57-64`), the
  resolved switch state, and the memoised Q0 result. `Build(plan)` stays as a wrapper so no
  external caller changes. **This is mechanical but touches every arm of two large switches —
  budget it explicitly; it is likely the largest single piece of Stage 1.**
- `internal/executor/fused_hash_join.go` — `tryFuseHashCascade` implementing
  [05](05-qualification-predicate.md) Q0-Q7 and `fusedHashJoinOp` per
  [04 §5-7](04-fusion-site-and-data-structures.md), including C15 re-entrant `Open`.
- Call it as the first statement of the `*planner.Join` arm in **both** `Build`
  (`executor.go`) and `buildRec` (`executor.go:535-547`).
- Env kill switch, **default off** ([10](10-rollback-and-kill-switches.md)).
- A `fusedHashJoinOp` case in `collectShareableJoins` **or** an explicit assertion that fusion
  and shared hash builds never coexist (`parallel_hash_build.go:119-150`, finding F4).
- Decline-reason counters, surfaced under a debug env var (R10).

**Prerequisite from Stage 0b:** `evalHashKeyDatumSlot` must exist (finding F11).

**Tests (new):**

| test | asserts |
| --- | --- |
| `TestJoinStructFieldCountGuard` | Q7 — `planner.Join` field count unchanged |
| `TestFusionKeyCoordinateSpace` | the Q3 caveat: `Join.LeftKey.Index` is in the merged space, for a 3-level cascade |
| `TestFusionPrefixBoundedness` | Q5 on every fused plan in the corpus |
| `TestExplainInvariantUnderFusion` | EXPLAIN text identical fused/unfused |
| `TestFusedCascadeMatchesUnfused` (**DIFF**) | for every join query in the executor test corpus, run with fusion off and on and compare **ordered** output text byte-for-byte |
| `TestFusedSchemaElementWiseIdentity` | Q6 clause 3 (finding F1) — the width check alone must not be the gate |
| `TestFusedCascadeRescan` | C15 / R3b — a fused cascade under a correlated SubPlan (`subplan.go:223-230` forces `rescanCloseOpen`) |
| `TestBothBuildersAgree` | R5 — `Build` and `BuildFastIterator` produce the same root operator kind for the same plan |
| `TestFusionDeclinesOnLockRows / OnGather / OnOuterJoin / OnLateral / OnNullAware` | Q0/Q2 fail-closed paths |

**Gates:** UNITS, SMOKE, SPOT, PLAN, DS05. With the switch off, all of these must be
**bit-identical to the pre-stage run** — Stage 1 is a no-op in production by construction.

---

## Stage 2 — Enable fusion behind the switch and measure

**Scope:** no new code; turn the switch on in the measurement environment only.

> **Ordering trap (finding F12) — read before running anything.** Q0's last clause declines
> fusion on any plan containing a `*planner.MultiHashJoin`, and `mhjPackingEnabled` still
> defaults to `true` (`bushy.go:580`) until Stage 4. So the queries that would benefit from
> fusion are **exactly** the queries that still pack, and fusion would decline on every one of
> them. Every measurement in this stage must therefore run with `mhjPackingEnabled` forced
> **off** (via `SetMHJPackingEnabled`, `bushy.go:582-587`, or
> `GOOPG_COST_DRIVEN_JOINORDER=1` if the cost-driven order is also wanted — but note that
> conflates two variables and the A/B must not).

**Verification (all with the switch ON):**

1. **DIFF** across the whole executor + planner test corpus.
2. **DS05** — the SF0.5 sweep against the git-tracked oracle. This is the strongest available
   instrument because the oracle carries a per-query **value checksum**
   (`scripts/tpcds-result-checksum.py`), so a silently NULL-padded column is caught even when
   the row count is right. Zero row/checksum deltas required.
3. **SPOT** — Q12=2, Q13=35 from a fresh capped server.
4. **Low-`work_mem` run** (R4): re-run a subset of DS05 with `work_mem` set low enough to force
   spill on at least one build side, fused and unfused; results must be identical and temp-file
   usage must be non-zero in both.
5. **TPC-H SF1 A/B**, fusion on vs off, same server age, recorded to
   `evidence/stage2-ab.txt`, plus the decline-reason histogram (R10).
6. **SMOKE** must show no OLTP regression (R11).

**Acceptance:** zero correctness deltas anywhere, and a measured win on the packing queries
that exceeds what Stage 0 already delivered. **If the incremental win over Stage 0 is small,
the honest outcome is to leave the switch off permanently and proceed to Stage 4 anyway** —
the plan-shape benefit does not depend on fusion being enabled.

---

## Stage 3 — The order dependency (external blocker, not owned by this design)

Doc 15's conclusion stands: the order is the blocker for Q5/Q9/Q21 under cost-driven join
order. Stage 0's measurement is the cheapest test of the "the executor was wrong, not the DP"
hypothesis ([07 §5](07-cost-model-interaction.md)).

Deliverable of this stage is a **decision document**, not code: re-run the SF1 A/B
(`GOOPG_COST_DRIVEN_JOINORDER=1` vs default) at post-Stage-0/2 HEAD, with the **same** timeout
on both sides this time (the 2026-07-24 pair used 600 s vs 300 s), and state whether
cost-driven order is now a net win. Nothing downstream should assume it is.

---

## Stage 4 — Retire `MultiHashJoin` as a plan node (conditional)

**Preconditions:** Stage 0 green; Stage 2 either green-and-enabled or explicitly-declined; the
packing queries no longer regress when packing is off (this is what Stage 0c measured).

**Scope:**

1. Flip `mhjPackingEnabled` default to `false` (`internal/planner/bushy.go:580`).
2. Keep `rewriteMultiWayChain`, the `MultiHashJoin` node, and `multi_hash_join.go` **in the
   tree**, reachable via `SetMHJPackingEnabled` (`bushy.go:582-587`), for at least one full
   nightly cycle. Deleting code and changing behaviour in one commit is unbisectable.
3. Follow the four-step plan-snapshot procedure in [06 §5](06-explain-and-plan-shape.md):
   PLAN green → flip → **hand-review every diff** → `make plan-snapshot-capture
   LABEL=post-mhj-retire`.
4. Build `scripts/pg-plan-shape-diff.sh` in **report mode only** ([06 §4](06-explain-and-plan-shape.md)).

**Gates:** UNITS, SMOKE, SPOT, DS05, PLAN (with hand review), and a full TPC-H SF1 sweep
compared against `sf1-r5-default-cb37d166.txt` as the historical reference.

**Only after a clean nightly cycle:** a separate commit may delete the MHJ node and operator —
and that commit is **~20 files, not one** (finding F13, inventoried in
[08 R17](08-risk-register.md)). Note in particular `generateMultiHashJoinPath`
(`internal/planner/pathgen.go:100-105`): the cost-driven path generator still *produces* MHJ
paths, which is a decision separate from the post-DP packer and must be settled explicitly.

---

## What "done" means for each stage (checklist form)

- [ ] Stage −1: `len(keys) != len(scans)-1` guard landed; 5 gates green.
- [ ] Stage 0: 0a+0b landed; PLAN shows **zero** diffs; `evidence/stage0-ab.txt` recorded;
      decision point written down.
- [ ] Stage 1: switch off; every listed test present and green; production behaviour
      bit-identical.
- [ ] Stage 2: switch on in measurement; DS05 zero deltas incl. checksums; low-`work_mem` run
      identical; decline histogram recorded; go/no-go written down.
- [ ] Stage 3: A/B re-run with symmetric timeouts; decision document.
- [ ] Stage 4: default flipped; plan diffs hand-reviewed; new baseline captured; MHJ code
      retained for one nightly cycle.

---

## Stage −1 siblings — two pre-existing defects to land before anything is measured

Stage −1 above lands the packer's missing `len(keys) == len(scans)-1` assertion (R1). Two more
pre-existing defects were found while auditing this proposal. Both are independent of it, both
fail in the safe direction once fixed, and both would **corrupt the Stage 0 measurement** if left
in place. Land each as its own commit, in this order, each with UNITS + SMOKE + SPOT + PLAN +
DS05.

### −1a — add the `*MultiHashJoin` arm to `EstimateRows` ([08 R18](08-risk-register.md))

`internal/planner/cardinality.go:38+` has no `*MultiHashJoin` case, so every packed MHJ estimates
0 rows and every ancestor's build-side decision above it is made on that zero — **in the default
configuration, today**. An A/B that flips `mhjPackingEnabled` therefore changes two things at
once, which is precisely the confound that invalidated an earlier round of this work.

**Expect `make plan-gate` diffs.** Hand-review every one: each is a build-side or algorithm
decision that was previously taken on a zero-row estimate. Some may be performance *improvements*
and some regressions; record both. Do not proceed to Stage 0 until this is baselined.

### −1b — make `VirtualSlot.Materialize()` clone arena-backed Datums ([08 R3c](08-risk-register.md))

`internal/executor/slot.go:167-169` does not `cloneRowOwned`, unlike its siblings. Latent today;
live as soon as Stage 0a-legacy or Stage 1 increases the number of consumers materialising a
composed slot. Correctness change — keep it bisectable, never folded into a performance commit.

## Gate coverage caveat that applies to every stage

**DS05 checks full row content for only 57 of its 99 queries.** The other 42 carry `ck=n/a`
(LIMIT-saturated, no stable row set) and are compared on **row count alone**
([08](08-risk-register.md), "Correction to the anchor-corpus table"). A silently NULL-padded
column in one of those 42 passes the gate green.

Therefore, for every stage that can affect join output:

- the **DIFF** harness (`TestFusedCascadeMatchesUnfused`, ordered-text comparison) is the
  *primary* correctness instrument, not a supplement;
- a green DS05 must be recorded as "57/99 content-verified, 42/99 count-only", never as
  "99/99 verified";
- when a stage's risk is specifically wrong-column or wrong-order output
  ([08 R2](08-risk-register.md), [08 R3](08-risk-register.md)), add at least one hand-written
  full-output comparison on a shape drawn from the 42.
