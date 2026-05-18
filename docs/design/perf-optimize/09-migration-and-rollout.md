# 09 — Migration and Rollout

This chapter sequences the implementation into four phases, defines
the smoke tests and acceptance bands that gate each phase, lists the
existing design docs that must be marked SUPERSEDED, and enumerates
risks with mitigations. It is the integration plan that ties the
preceding eight chapters together.

Cross-references: all preceding chapters [[01-memory-context]] through
[[08-runtime-internals]].

## 1. Why phased

The refactor touches every internal package: `mctx` is foundational
for everything; `Datum` reformat cascades through ~225 call sites;
the executor refactor changes the operator pipeline; MVCC + activity
+ bufpool + WAL each replace load-bearing concurrency primitives. A
single-commit rollout would be unreviewable, untestable, and
unrevertible.

The plan defines **four phases**, each shippable independently as one
or more milestones (M0107.A, M0107.B, ...), each gated by a smoke
test and a pgbench-suite acceptance band.

## 2. Phase A — `mctx` substrate

**Scope:**
- Land `internal/mctx` package per [[01-memory-context]].
- Delete `internal/executor/arena.go` and `internal/executor/arena_registry.go`.
- Port the existing arena callers in `internal/executor/operators_storage.go`
  (`seqScanOp`, `indexScanOp`, others) to use `mctx.Context` instead.
- Wire `sessionCtx` / `txnCtx` / `stmtCtx` lifecycle through
  `internal/server/server.go::serveConn` and `internal/server/dispatch.go::executeOneSimpleStmt`.

**Out of scope for Phase A:**
- `Datum` reformat (Phase B).
- Operator-interface deletion (Phase C).
- MVCC / activity / bufpool / WAL contention fixes (Phase D).

**Phase A is foundational and self-contained.** All it does is replace
the existing per-operator `Arena` with hierarchical mctx; the rest of
the system continues to look the same to callers.

**Smoke tests:**
- `go test ./...` green.
- TPC-H q1..q22 regression (run via existing `bench/tpch/` harness)
  — wall-clock within ±5 % of pre-refactor (no GC behaviour change
  expected at this phase).
- One pgbench c=10 SO run completes; TPS within ±5 % of pre-refactor
  baseline (no contention or GC change expected; this confirms no
  functional break).

**Acceptance band:** no regression vs `ab1b955` baseline. Phase A
delivers infrastructure, not measured performance lift.

**Rollback:** revert the single PR. The chunks-pooled allocator can
be reverted cleanly because the API surface (`mctx.Acquire/Alloc/
Release`) is new and callers haven't yet depended on its lifetime
semantics beyond what `Arena` already offered.

## 3. Phase B — Pointer-free `Datum`

**Scope:**
- Land the new `Datum` layout per [[02-datum-pointer-free]] behind a
  `//go:build datumv2` tag.
- Add the new accessors alongside the old ones.
- Migrate `internal/executor/` (~80 call sites) to new accessors
  under the new tag.
- Migrate `internal/wal/`, `internal/access/heap/`, `internal/planner/`,
  `internal/initdb/`, `internal/protocol/`, `internal/server/`
  (~145 call sites total).
- Once all packages compile + test green under `datumv2`, drop the
  tag; delete the `Buf`, `Big`, `arena` fields.
- Add the `unsafe.Sizeof(Datum{}) == 24` compile-time assertion.

**Smoke tests:**
- `go test ./...` green.
- TPC-H regression — numeric-heavy queries (q1, q4, q5) within ±10 %
  of pre-refactor wall-clock. **If a numeric query regresses > 10 %**,
  do not proceed to Phase C; land the packed-numeric-arithmetic
  kernel sub-task first (see §risk-register).
- pgbench c=10 SO TPS target: **≥ 5 000** (vs 2 307 baseline). This
  is the first measurable lift from the refactor — primarily from
  GC scan reduction on Datum + plan-tree allocation moving to mctx.
- pgbench c=10/c=50 simple-update + standard within ±10 % of
  pre-refactor (Phase B is not yet expected to lift writes).
- GC profile — `gcBgMarkWorker` cum% at c=10 SO drops to **< 35 %**
  (from 63 %).

**Acceptance band:** c=10 SO TPS ≥ 5 000; GC scan reduction visible
in pprof; no numeric regression > 10 %.

**Rollback:** the build-tag-staged migration makes Phase B revertible
in segments. The final "delete the old fields" commit is atomic; if
problems emerge after it lands, revert that one PR and the system is
back to the dual-layout state (working but slower).

## 4. Phase C — Concrete-type executor

**Scope:**
- Land the concrete `OpNode` sum-type per [[03-executor-concrete]].
- Land the concrete `Slot` struct (deleting `TupleSlot` interface).
- Land the `PlanNode` / `ExprNode` sum-types (deleting plan-node
  interfaces).
- Migrate hot-path operators (scan/filter/project/limit/sort/join/
  insert/update/delete) to concrete types.
- Keep cold-path operators (vacuum/cluster/analyze/ddl/explain/...)
  on the legacy `Operator` interface behind an `opAdapter` shim.
- Migrate parser AST to mctx (delete `tokenSlicePool` / `parserPool`).

**Smoke tests:**
- `go test ./...` green.
- TPC-H regression — all queries within ±10 %. q5 and q9 (the M0077-
  unlocked OLAP-heavy ones) get extra attention.
- pgbench c=10 SO TPS target: **≥ 8 000** (the headline target).
- pgbench c=50 SO TPS target: **≥ 18 000**.
- GC profile — `gcBgMarkWorker` cum% at c=10 SO drops to **< 15 %**
  (the [[01-memory-context]] verification target).
- CPU profile — `dispatchSimpleQueryViaExecutor` cum% drops to
  **< 10 %**; `runtime.itabHashFunc` falls out of top-40.
- Heap profile — `inuse_space` stable within ±5 % over a 1-hour run.

**Acceptance band:** c=10 SO TPS ≥ 8 000; GC + dispatch CPU below
targets above; no TPC-H regression > 10 %.

**Rollback:** Phase C's PRs are larger than B's; revertibility is
preserved by splitting C into:
- C.1 — `OpNode` sum-type lands; hot-path operators migrated; cold
  path still on adapter.
- C.2 — `Slot` struct replaces interface; consumers updated.
- C.3 — `PlanNode` / `ExprNode` sum-types land; parser migrated.

Each sub-phase is independently revertible. If C.3 breaks but C.1-2
were good, revert C.3 only.

## 5. Phase D — Contention fixes

**Scope (five sub-milestones, mostly independent):**
- D1: MVCC ProcArray per [[04-mvcc-procarray]].
- D2: Activity per-backend per [[05-activity-perbackend]].
- D3: Bufpool lock-free per [[06-bufpool-lockfree]]; retire
  M0098-0003 + M0099-0002.
- D4: WAL striping + FSM-distributed inserts per [[07-wal-fsm-insert]].
- D5: Runtime linkname patterns per [[08-runtime-internals]] —
  per-P xid cache + nanotime + slot semaphore.

D1 and D2 share the `procNum` identity; they should land together or
in close succession. D3 is independent. D4 depends on D3 (consults
bufmap). D5 is mostly independent but the slot semaphore (`SemaAcquire`/
`Release`) is consumed by D3's bufpool slow path; lands together
or D5-first.

**Smoke tests (per-sub-milestone):**

| Sub-milestone | Targeted bottleneck                  | Acceptance band                                          |
|---------------|--------------------------------------|----------------------------------------------------------|
| D1 (mvcc)     | `Manager.mu` 92 % write delay        | c=50 SU TPS ≥ 2 000 (vs 347); `mvcc.*` out of mutex top-20 |
| D2 (activity) | `Registry.mu` 95 % c=100 SO delay    | c=100 SO TPS ≥ 10 000 (vs 6 400); `activity.*` out of mutex top-20 |
| D3 (bufpool)  | `partition.mu` (128 mutexes) + scan  | `runtime.futex` cum% < 8 % at c=100 SO; `bufferPartition.*` out of mutex top-20 |
| D4 (wal+fsm)  | c=100 SU/standard livelock           | c=100 SU TPS ≥ 500 (was SKIPPED); c=100 standard TPS ≥ 500 |
| D5 (runtime)  | per-P xid contention; semaphore wait | Combined `runtime.futex` drop with D3; nanotime bench ~5 ns |

**Final integrated acceptance band (after all of D1..D5).**

Pre-refactor numbers are sourced from
`analysis/perf-optimize/runs/20260518_115032/results_summary.tsv` (the
run captured at commit `ab1b955`); the post-refactor numbers are
re-measured against the same script and pgbench parameters so the
comparison is apples-to-apples. Both this table and the overview
table in [[00-overview]] §8 draw from this same TSV.

| Metric                           | Pre-refactor (`ab1b955`) | Post-refactor target          |
|----------------------------------|--------------------------|-------------------------------|
| c=10 select-only TPS             | 2 307                    | ≥ 8 000                       |
| c=10 simple-update TPS           | 410                      | ≥ 1 500                       |
| c=10 standard TPS                | 349                      | ≥ 1 200                       |
| c=50 select-only TPS             | 5 034                    | ≥ 18 000                      |
| c=50 simple-update TPS           | 347                      | ≥ 2 000                       |
| c=50 standard TPS                | 339                      | ≥ 1 800                       |
| c=100 select-only TPS            | 6 400                    | ≥ 12 000                      |
| c=100 simple-update TPS          | DEADLOCK / SKIPPED       | ≥ 500 (measured)              |
| c=100 standard TPS               | DEADLOCK / SKIPPED       | ≥ 500 (measured)              |
| `gcBgMarkWorker` cum% (c=10 SO)  | 63.3 %                   | < 15 %                        |
| `runtime.futex` cum% (c=100 SO)  | 23.0 %                   | < 8 %                         |
| `mvcc.Manager.*` mutex top-20    | dominant                 | absent                        |
| `activity.Registry.*` mutex top-20 | dominant               | absent                        |
| `bufferPartition.mu` mutex top-20 | dominant                | absent                        |
| Datum sizeof                     | 64 B                     | 24 B (no GC-traced fields)    |

**Rollback:** each sub-milestone is independently revertible. The
trickiest is D3 (bufpool) because the on-disk page format is shared
with all readers; the rollback test is to revert D3, restart, and
confirm the database reads back without WAL replay needing the post-
refactor code.

## 6. Existing design docs marked SUPERSEDED

The refactor obsoletes several extant design docs. They get a
`Status: SUPERSEDED-BY: docs/design/perf-optimize/<chapter>` header in
the same PR that lands the replacement:

| Existing doc                                            | Superseded by                                              |
|---------------------------------------------------------|------------------------------------------------------------|
| `docs/design/0068-0003-batch-string-arena.md`           | `docs/design/perf-optimize/01-memory-context.md`           |
| `docs/design/0073-0001-datum-arena-field.md`            | `docs/design/perf-optimize/02-datum-pointer-free.md`       |
| `docs/design/0074-0003-arena-registry-forward-compat.md`| `docs/design/perf-optimize/01-memory-context.md`           |
| `docs/design/0098-0003-bufpool-partitioning.md`         | `docs/design/perf-optimize/06-bufpool-lockfree.md`         |
| `docs/design/0099-0002-pin-fastpath.md`                 | `docs/design/perf-optimize/06-bufpool-lockfree.md`         |

The M0098-0002 (WAL group commit) doc is **not** superseded; group
commit semantics are preserved by [[07-wal-fsm-insert]]. Its design
is layered with the 8-stripe insert locks rather than replaced.

The M0091-0001 (activity goroutine cache) doc is superseded; the
goroutine→PID indirection it implements is deleted per
[[05-activity-perbackend]] §7.

## 7. Verification commands

The same commands `analysis/perf-optimize/` uses. Reused unchanged so
post-refactor numbers are directly comparable:

```bash
# Full pgbench suite (≈ 60 min wall-clock)
bash analysis/perf-optimize/scripts/run_perf_suite.sh

# Post-process the most recent run; generates pprof_top + results TSV
bash analysis/perf-optimize/scripts/analyze.sh \
  "$(ls -t analysis/perf-optimize/runs/ | head -1)"

# Compare against the pre-refactor baseline (commit ab1b955):
diff -u analysis/perf-optimize/runs/20260518_115032/results_summary.tsv \
       analysis/perf-optimize/runs/<NEW_RUN_ID>/results_summary.tsv
```

A `make perf-suite` target wraps the three commands above for
convenience; each phase's PR description includes the diff output
inline.

## 8. Risk register

| Risk | Severity | Mitigation |
|------|----------|------------|
| Pointer-free `Datum` makes `*big.Int` decoding more expensive on numeric-heavy workloads | Medium | Keep int64 mantissa fast path; profile TPC-H q1 / q4 / q5 in Phase B; if regression > 10 %, land packed-numeric kernels (sum/avg over (Lo, Hi) pairs) before Phase C. |
| `mctx` lifetime bugs (use-after-Reset) escape to production | High | `gen`-counter weak refs in debug builds; comprehensive `-race` test runs; `mctx.Probe(ptr)` helper that asserts the gen in test/dev builds; concentrated review of `internal/server/dispatch.go` lifecycle wiring. |
| `//go:linkname` site breaks on Go upgrade | Low | Build-tag-gated fallbacks for every linkname site (per [[08-runtime-internals]]); per-Go-minor CI smoke test; bump procedure documented. |
| ProcArray slot exhaustion at high `max_connections` | Low | Slot array sized at server start with 2× over-provision; documented bound (`max_connections` GUC limit); explicit panic on exhaustion (better than silent corruption). |
| Lock-free `bufmap` race on insert | Medium | Robin-Hood with version words; CAS retry loop bounded; `-race` stress tests with 1 000 concurrent goroutines doing Pin/Unpin/evict for 30 s. |
| WAL stripe LSN reservation races with segment rotation | Medium | The atomic `nextLSN.Add` reserves; `rotateMu sync.Mutex` (declared on `wal.Writer` in [[07-wal-fsm-insert]] §2; rare — once per `wal_segment_size`) serializes segment rotation. Mirrors PG. |
| Phase D landing in fragments breaks pgbench mid-suite | Medium | Each sub-milestone's smoke test is the full pgbench c=10/c=50 suite (excluding c=100 SU/standard until D3+D4 land together). c=100 SU/standard are part of the integrated D3+D4 gate. |
| Reviewer subagent misses subtle issues | Low | Two-pass review: re-run reviewer after addressing findings; user is the final reviewer before any implementation milestone opens; CI gates each phase via the smoke tests above. |
| Cold-path operator adapter (Phase C) hides bugs in vacuum/cluster paths | Low | A `go vet`-style lint asserts that no hot-path call site reaches the adapter at runtime (a runtime flag panics if hot-path code paths hit the adapter); cold paths are exercised by the existing vacuum + analyze + DDL tests. |
| Phase B's build-tag-staged migration leaves dual accessors in production | Medium | The final "drop the build tag" commit is atomic and short; CI runs the entire test matrix under both `datumv2` and pre-`datumv2` for the duration of the migration; the tag is removed in one PR within the same week as the last package's migration. |
| Per-P xid cache visibility — a P that crashes loses its cached xids | Low | Lost xids are not a correctness issue (CLOG has no entry, so they're treated as in-doubt → aborted). The CLOG bank is grown on demand. |

## 9. Rollback rules

For each phase:

1. **If any unit test regresses**, do not merge the phase.
2. **If TPC-H regresses > 10 % on any query**, do not merge; root-cause
   and either fix in-phase or escalate to user.
3. **If pgbench TPS regresses on the suite that was previously
   working**, do not merge.
4. **If the acceptance band is not hit but no regression**, merge is
   still permitted at user discretion (continued progress is more
   valuable than a stalled refactor); the missed target is noted in
   the milestone retrospective and addressed in a follow-up.
5. **If a phase merges and a regression is discovered later**, revert
   the phase's PR. The phase is then re-attempted as a separate
   milestone after the regression is understood and fixed.

## 10. Reviewer-subagent step

After all ten chapter files (`00-overview.md` through `09-migration-
and-rollout.md`) are written, but before any implementation milestone
opens, spawn a design reviewer:

```
Agent(
    subagent_type = "general-purpose",
    description   = "Review perf-optimize design docs",
    prompt        = "
        Read docs/design/perf-optimize/00-overview.md through 09-migration
        -and-rollout.md (all 10 files). For each chapter, verify:

        - At least one goopg file:line reference is cited.
        - At least one PG postgres/src/... reference is cited.
        - Concrete Go signatures (not pseudocode) for proposed types
          and methods.
        - A verification subsection sized against an analysis/perf-
          optimize/ chapter.

        Flag any of the following as findings:
        - Missing or fabricated file:line references.
        - Missing PG citations.
        - Pseudocode where Go is expected.
        - Missing verification sections.
        - Internal contradictions across chapters (e.g., chapter 4
          says procNum is uint16, chapter 5 says int32).
        - Pointer-free claims that are not actually pointer-free
          (e.g., a struct that includes an interface or *T field
          claimed to be pointer-free).
        - Unsafe type-pun without runtime.KeepAlive discussion.
        - Concurrency design that uses CAS without an ABA argument
          where ABA is possible.
        - API surface that still passes interfaces in the per-row
          hot path.
        - Anything that breaks the PG on-disk format invariants
          (heap page, tuple, WAL record, control file, CLOG bank,
          FSM bytes, btree page).

        Output: numbered punch list with each item citing the design-
        doc file:line and a one-sentence description. Group items by
        severity (blocker / important / nit).
    "
)
```

Findings are addressed in a revision pass; the reviewer is re-run
at least once after the revision. Iterate until the reviewer returns
"no blocker findings" (nits and importants may be deferred to the
user's discretion).

## 11. Done criteria for the design pass

The design pass is **complete** when **all** of the following hold:

- `docs/design/perf-optimize/00-overview.md … 09-migration-and-rollout.md`
  exist with the structure documented in [[00-overview]] §4.
- Every chapter cites at least one goopg `file:line` reference and
  at least one PG `postgres/src/...` reference.
- Every chapter contains concrete Go signatures for proposed types
  and methods.
- Every chapter ends with a verification subsection sized against an
  `analysis/perf-optimize/` chapter.
- `grep -RIn 'TODO\|FIXME\|TBD' docs/design/perf-optimize/*.md` is
  empty.
- The reviewer subagent has been run at least once and its blocker
  findings addressed.
- No `.ralph/` file is touched; no `analysis/perf-optimize/` artifact
  is modified; the Ralph autonomous loop continues to operate.

The implementation pass — actually writing the code — is gated
separately, phase by phase, per §2..§5 above.
