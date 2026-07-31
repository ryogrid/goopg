# 08 — Risk register

Ranked by expected damage. This project's most expensive failure mode, by its own record, is
a **silent row-count regression**: `scripts/tpch-spotcheck.sh:5-9` states it outright —
"silent row-count regressions from executor/planner changes are this project's most expensive
failure mode (m0071 Stage-B et al.)".

The anchor corpus that would catch such a regression is smaller than one might assume, and
its size should shape the plan:

| corpus | size | file |
| --- | --- | --- |
| TPC-H spotcheck | 2 anchors (Q12=2, Q13=35) | `bench/tpch/spotcheck_expected.env` |
| TPC-H nightly row anchors | 12 rows | `ci/batch/tpch-row-anchors.csv` |
| TPC-DS SF0.5 oracle | 112 lines, `q\|status\|rows\|ck\|secs`, with a **value checksum** per query | `bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt` |
| pg-regress suite | via `scripts/pg-regress-runner.sh` | — |

The TPC-DS oracle is the strongest instrument available, because it carries a
`scripts/tpcds-result-checksum.py` **checksum over row content**, not just a count. A join
that silently NULL-pads a column changes the checksum even when the row count is unchanged.
**Every stage in [09](09-staged-implementation-plan.md) that can affect join output runs the
SF0.5 sweep.**

---

## R1 — SEV-1 (pre-existing) — the MHJ packer can emit an under-keyed plan

**Not introduced by this proposal; found while auditing it.**

`collectMultiHashTables` (`internal/planner/bushy.go:1519-1645`) resolves join keys by column
name (`findScanByColName`, `:1731-1751`), silently skips a key when either side fails to
resolve (`if li >= 0 && ri >= 0`, `:1596-1604`), and validates only that every table's degree
is `<= 2` (`:1620-1633`). It **never asserts `len(keys) == len(scans)-1`**.

`multiHashJoinOp.Open`'s `keySteps` BFS then `break`s when no further progress is possible
(`internal/executor/multi_hash_join.go:158-163`). Tables never reached keep
`tableSlots[i].row == o.nulls[i]` (`:~275`) for the whole execution: their columns emit NULL,
and the join predicate that should have constrained them is gone. Cost-model doc 15 §2
clause 4 already names this behaviour — "the executor *silently corrupts* non-tree input
(drops keys / null-pads unreached dims)" — but the guard it prescribes lives in the abandoned
v2 DP code, not in the live packer.

**Mitigation, independent of this proposal:** add to `collectMultiHashTables`, immediately
before the probe selection:

```go
if len(keys) != len(scans)-1 { return nil, nil, 0, nil }
```

This is a two-line, fail-closed change whose worst case is declining to pack a plan that would
have packed. It should be landed on its own, with its own spotcheck + SF0.5 run, regardless of
whether the rest of this design proceeds. **Recommended as the very first commit of this line
of work.**

## R2 — SEV-1 — fused output column mapping is wrong (silent wrong answers)

The historical instances: doc 13's layout desync; the M0125-0008 stale-permutation bug
documented at `internal/planner/plan.go:869-886`, where an `EXISTS … AND NOT EXISTS …` pair
returned **more** rows than either conjunct alone because an ancestor key had been re-resolved
against a phantom layout.

**Mitigations:** the **element-wise** schema identity asserted per level
([04 §4](04-fusion-site-and-data-structures.md), [05 Q6 clause 3](05-qualification-predicate.md));
no name-based *resolution* anywhere in the fusion path; Q3's index-range checks; and — the real
defence — the differential test in [09](09-staged-implementation-plan.md) Stage 1 that runs
every join query twice (fused / unfused) and compares output **as ordered text**, not as a row
count.

> **Review correction (F1):** the first draft's mitigation was the *width* identity alone.
> That is void: `Join.Output()` returns a cached schema (`internal/planner/plan.go:889-897`)
> that the comment at `:869-886` says can be "a stale *permutation*" — and a permutation has
> the same width. If clause 3 is dropped during implementation, **this risk has no
> mitigation at all** except the differential test, which only covers the shapes it exercises.

## R3 — SEV-1 — row ORDER changes (looks like a wrong answer to every gate)

An odometer whose level order differs from the cascade's nesting order produces the same
multiset in a different sequence. The TPC-DS oracle checksum is computed over rows; TPC-H
anchors compare counts but the regress suite compares text.

**Mitigation:** contract C1; level 0 = innermost; bucket iteration in append order; and the
differential test compares **ordered** output.

## R3b — SEV-1 (new, from review F10) — a fused operator that is not re-Open-safe

`internal/executor/subplan.go:223-230` classifies a `*planner.Join` inside a SubPlan as
`rescanCloseOpen`: Close + Open per outer row. Cursors/portals, `RecursiveUnion`/
`WorkTableScan`, `Memoize` and prepared-statement reuse are further re-Open sources. A fused
operator that carries odometer state (`active`, per-level `cursor`/`matches`, bound slots)
across an `Open` emits rows belonging to the **previous outer row**.

**Mitigation:** contract C15; `TestFusedCascadeRescan` in Stage 1; and `Close` must be safe
after a partially failed `Open`.

## R4 — SEV-1 — spill silently disappears

MHJ's build path has no budget and no spill ([02 §7](02-premise-audit.md)). A fused operator
that copies it turns a query that previously spilled 2 GB into one that OOMs.

**Mitigation:** contract C8 — `drainRowsBounded(child, ctx.WorkMem)` per level, mandatory;
Stage 2 verification includes a deliberately low `work_mem` run that must produce identical
results fused and unfused.

## R5 — SEV-2 — the two builders diverge

`Build` and `BuildFast`/`buildRec` both construct joins
([04 §1](04-fusion-site-and-data-structures.md)); `parallel_hash_build.go:104-109` already
documents a three-way duplication of the probe-side rule as a live hazard.

**Mitigation:** one `tryFuseHashCascade`, called from both arms, plus a test that builds the
same plan through both entry points and asserts the same operator kind at the root.

## R6 — SEV-2 — parallel shared hash builds

`ctx.SharedHashBuilds[*planner.Join]` (`parallel_hash_build.go:95-100`) is populated by the
leader before fan-out. A fused operator that ignores it rebuilds the hash per worker (slow but
correct); one that half-adopts it can put a parallel scan on a build side, which
`parallel_hash_build.go:104-109` says "would silently lose rows".

**Mitigation:** Q0 declines on any `Gather`/`GatherMerge` in the plan. Revisit only as separate,
later work.

## R7 — SEV-2 — FOR UPDATE loses its ctid

`preserveCTIDRel` is set by `lockRowsOp.Open` *after* the operator tree is built
(`operators_join_agg.go:105-118`), so a build-time fusion decision cannot see it.

**Mitigation:** Q0 declines on any `*planner.LockRows` in the plan.

## R8 — SEV-2 — EXPLAIN ANALYZE becomes misleading

Under the chosen rule ([06 §2](06-explain-and-plan-shape.md)) EXPLAIN ANALYZE runs unfused, so
its timings are an upper bound for fused plans. A future engineer chases a phantom regression.

**Mitigation:** the caveat goes in the operator doc comment, in `docs/`, and in the top-node
line printed under `GOOPG_FUSION_UNDER_ANALYZE=1`.

## R9 — SEV-2 — cancellation regression on a deep odometer

A probe row with large fan-out at shallow levels and no match at the deepest can spin many
odometer steps without emitting. The cascade's per-`Next()` check would not fire either
(the cascade also spins inside its own loops), so this is at worst a preserved defect — but it
is worth fixing.

**Mitigation:** contract C7 clause (c): `ctx.Ctx.Err()` every 4096 odometer steps.

## R10 — SEV-3 — a `BuildLeft` level truncates the chain unexpectedly

Q2 ends the chain at any `BuildLeft` level. If the planner sets `BuildLeft` more often than
expected, fusion quietly never fires and the whole project shows no benefit.

**Mitigation:** Stage 1 ships a counter/`DEBUG` log of decline reasons; the Stage 2 report must
include a histogram of decline reasons over the TPC-H and TPC-DS query sets. A design that
never fires is a failure that must be visible, not silent.

## R11 — SEV-3 — regression on the *non*-fusing majority

`tryFuseHashCascade` runs on every `*planner.Join` build. On OLTP plans that is a wasted walk
per join per plan build, on the pgbench hot path that the pre-commit hook measures on every
commit.

**Mitigation:** cheap early-out — `if !fusionEnabled || p.Type != JoinTypeInner || p.Algo !=
JoinAlgoHash { return nil, false }` as the first line, before any tree walk; and the Q0 root
walk memoised once per `Build`, not once per join. Verify with the pgbench smoke that the
pre-commit hook already runs.

## R12 — SEV-3 — plan-snapshot noise masks a real regression at Stage 4

Flipping `mhjPackingEnabled` changes EXPLAIN for every packing query, so `make plan-gate`
produces a large expected diff.

**Mitigation:** the four-step procedure in [06 §5](06-explain-and-plan-shape.md) — green
before, hand-review every diff, re-baseline after.

## R13 — SEV-3 — a `plan-gate` or spotcheck SKIP recorded as a pass

`make plan-gate` exits 0 with "SKIPPED" when there is no baseline or no reachable server
(`Makefile:377-385`); `scripts/tpch-spotcheck.sh:15-17` exits 0 with a "loud SKIPPED message"
when no populated data dir exists.

**Mitigation:** every stage's verification record must quote the gate's **final line**, not its
exit code. A stage whose gate SKIPped is not complete.

## R15 — SEV-2 (new, from review F5) — "optimising away" the build-side row copy

`drainRowsBounded` deep-copies every retained row (`internal/executor/spill.go:388-399`).
That copy is a correctness requirement: `seqScanOp` reuses and releases `o.scanRow`
(`internal/executor/operators_storage.go:1361`), so a hash entry that aliased it would be
silently overwritten mid-execution (the M0097-0058 class). An implementer optimising the
fused build for memory is very likely to try removing it.

**Mitigation:** contract C13 now says the opposite of what the first draft said, in bold, with
the reason. A code comment at the fused build loop must repeat it.

## R16 — SEV-2 (new, from review F8) — `instrumentScope` is a racy global

`var instrumentScope *instrumenter` (`internal/executor/instrument.go:215`) is mutated and
restored by `withInstrumentation` (`:225-233`) without a lock. Gating fusion on it makes plan
building depend on what *other sessions* are doing, and introduces a read that `make race-gate`
can flag.

**Mitigation:** per-build flag on `buildEnv`, never a read of the global
([05 Q0](05-qualification-predicate.md), [06 §2](06-explain-and-plan-shape.md)).

## R17 — SEV-2 (new, from review F13) — "delete the MHJ node" is a ~20-file change

Live (non-test) references to `MultiHashJoin` span `internal/planner/`
(`view_privilege.go:62`, `subplan_lower_walk.go:114`, `inner_join_qual_pushdown.go:94`,
`pushdown.go:70`, `nl_index_join.go:175`/`:497`, `subplan_cost.go:74`, `cost_funcs.go:169`,
`pathgen.go:100-105`, `exists_to_any.go:134`, `unnest.go` ×8, `bushy.go` ×several,
`plan.go:1149-1171`) and `internal/executor/` (`executor.go:275`, `subplan.go:229`,
`multi_hash_join.go`, `operators_explain.go:1386`/`:1562`). Note also
`generateMultiHashJoinPath` (`pathgen.go:100-105`) — the cost-driven path generator still
*produces* MHJ paths, which is a separate decision from the packer.

**Mitigation:** Stage 4 carries this inventory; deletion is a separate commit after a clean
nightly cycle, and `generateMultiHashJoinPath` is called out as its own decision.

## R14 — SEV-4 — concurrent-loop tree corruption during implementation

A Ralph loop is editing `internal/planner/` and `internal/executor/` continuously.

**Mitigation:** implement each stage in a git worktree off clean HEAD; stage by explicit
pathspec; never `git add -A`. (`CLAUDE.md`, Git section.)

---

## R3c — SEV-1 (new, from the second design pass; verified) — `VirtualSlot.Materialize()` does not clone arena-backed Datums

`internal/executor/slot.go:167-169`:

```go
func (s *VirtualSlot) Materialize() *MaterializedSlot {
	return &MaterializedSlot{schema: s.schema, row: s.Row()}
}
```

`s.Row()` (`:159-164`) does `acquireRow` + a per-column `s.Get(i)` — it copies `Datum` **structs**
but does **not** call `cloneRowOwned`. An arena-backed `Datum` keeps its `ArenaID`, so after the
producer's next `arena.Reset` the materialised row holds dangling references. Contrast
`drainRowsBounded` (`spill.go:384-402`), which does exactly that clone and documents why
(M0073-0004 retention boundary).

This is a **pre-existing** defect, not one this proposal introduces — but any slot-chaining work
(Stage 0a-legacy, Stage 1) multiplies the number of consumers that legitimately call
`Materialize()` on a composed slot, so it turns a latent hazard into a live one.

**Mitigation:** fix `VirtualSlot.Materialize()` to clone arena-backed Datums, as its own
prerequisite commit with its own gates, *before* any chaining work. Do not fold it into a
performance commit — it is a correctness change and must be bisectable on its own.

## R18 — SEV-1 (new, from the second design pass; verified and upgraded) — `EstimateRows` has no `*MultiHashJoin` arm

`EstimateRows` (`internal/planner/cardinality.go:38+`) switches over `SeqScan, IndexScan, Values,
Filter, Limit, Sort, Project, Distinct, WindowAgg, Join, OrdinalityWrap, Aggregate, Insert,
Update/Delete/DDL/…, Explain`. There is **no `*MultiHashJoin` case**, so every packed MHJ
estimates **0 rows**.

The second design pass surfaced this as a *measurement confound*: an A/B that flips
`mhjPackingEnabled` also changes every `BuildLeft` / algorithm decision **above** the packed
chain, because those decisions read `EstimateRows` and get 0 on one arm and a real number on the
other. That is exactly the confound doc 15's round-5 work was criticised for, and it would
silently corrupt Stage 0c and Stage 2.

**The synthesis pass upgrades the severity.** `mhjPackingEnabled` defaults to `true`
(`bushy.go:580`), so this is not only a measurement artefact — **it is a live defect in the
default configuration today.** Every ancestor of every packed MHJ is choosing its build side and
its join algorithm from a zero-row estimate. Given that `buildLeft = lRows > 0 && rRows > 0 &&
lRows < rRows` (`bushy.go:1382`) requires *both* sides to be `> 0`, a zero on one side means the
size comparison silently defaults to "build on the right" regardless of the true sizes.

**Mitigation:** add the `*MultiHashJoin` arm to `EstimateRows` as **its own commit, before
Stage 0**, with UNITS + SMOKE + SPOT + PLAN + DS05. Expect `make plan-gate` to show diffs — every
one must be hand-reviewed, because each is a build-side decision that was previously made on a
zero. Recorded in [09](09-staged-implementation-plan.md) as a Stage −1 sibling.

---

## Correction to the anchor-corpus table above — the SF0.5 oracle is weaker than stated

The header of this chapter calls the TPC-DS SF0.5 oracle "the strongest instrument available"
because it carries a per-query **value checksum**. That is true but **it does not cover the whole
suite**, and the plan must not treat a green DS05 as full content coverage.

Measured on `bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt` (format
`q|status|rows|ck|secs`):

| | count |
| --- | ---: |
| oracle rows | 99 |
| rows carrying a real checksum | **57** |
| rows with `ck=n/a` — **row count only** | **42** |
| status `OK` / `SKIP_QUERYGEN` / `TIMEOUT` | 95 / 3 / 1 |

`ck=n/a` marks the LIMIT-saturated queries, where a saturated window has no stable row set. So
for **42 of 99 queries the gate detects a row-count change and nothing else** — a silently
NULL-padded column or a permuted output would pass green on those.

**Consequence for the plan:** DS05 is necessary but not sufficient as R1/R2's detector. The
differential harness (`TestFusedCascadeMatchesUnfused`, ordered-text comparison, Stage 1) is the
only instrument in this design that compares full content on arbitrary shapes, and it must
therefore be treated as the primary correctness gate, not as a supplement.
