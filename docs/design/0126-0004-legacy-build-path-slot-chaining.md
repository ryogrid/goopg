# 0126-0004 — legacy `Build`-path slot chaining

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-31 |
| task | M0126-0004 — **CONDITIONAL** |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` |
| design of record | `analysis/cost-driven-second-try-200731/` **09** Stage 0a-legacy + hazard F7, **02** §2.1/§9 — read them first; this doc does not restate them |
| depends on | `0126-0003` (both its commits), and its trigger below |

## 1. Scope

On the legacy `Build` path the probe child returns its `*VirtualSlot` directly,
so `nextLazy`'s `r := slotRow(probeSlot)` (`operators_join_agg.go:1214`) is the
`acquireRow`-and-copy site. Hold the child's `TupleSlot` as a **source** of the
join's output `VirtualSlot` instead of materialising it. This matters because
`buildRec` migrates no `Aggregate` (bundle 02 §9) — **every aggregate-topped
TPC-H star query runs its whole join subtree under legacy `Build` today**, so
this path, not the slab, governs the analytic workload.

## 2. Files and symbols touched

| file | symbol | change |
|---|---|---|
| `internal/executor/operators_join_agg.go:1207-1215` | `nextLazy` probe pull | keep the child's slot as a source; no `slotRow` materialisation |
| `internal/executor/operators_join_agg.go:1035-1068` | `ensureLazyVirtual` | the cached output `VirtualSlot` has a **fixed** `sources` slice — re-bind the probe source pointer **on every probe pull** |

**The F7 re-binding contract (the centre of this doc):** the child does NOT
return a stable slot object — it may return `o.lazyVirtualOut`,
`o.lazyOuterOnlySlot` (semi/anti), a fresh `*MaterializedSlot` from
`Materialize()` (FOR UPDATE), or a fresh `asSlot(...)` per call
(`rowsOp`/`spillOp`, `spill.go:441`, `:468`). Therefore: re-bind per pull, and
**fall back to the copy** whenever the child's slot type changes mid-stream.
Lifetime is safe by control flow — `nextLazy` pulls a new probe row only after
draining all matches for the current one — state that in a comment and promote
it with a debug assertion, per bundle 02 §2.4's honesty note (nothing enforces
the contract today).

## 3. Commit split

One commit, plus a mandatory **fan-out test** (multiple matches per probe row)
in the same commit.

## 4. Gates

UNITS, SMOKE, SPOT, PLAN (zero diffs), DS05.

## 5. Stop / decision conditions

**TRIGGER:** -0003's interim A/B (its own gate run, before -0005) shows the
legacy `Build` path still carries measured traffic on a bench query set. (The
decider is -0003 alone — -0005 runs *after* this task and cannot gate it.) If the slab serves every
benchmarked query by then, close as ***measured-unnecessary*** with a
`.ralph/deferral_ledger.md` row — do **not** do the lifetime reasoning
speculatively.

Stop: any DIFF/DS05 delta, or the fan-out test failing under any child slot
type, reverts the commit.

## 6. Rollback

Plain revert; executor-only. Preserve artefacts (10 §5).

## 7. What this doc deliberately does not decide

Migrating `Aggregate` to the slab (which would obsolete this task) — that is a
separate line of work the bundle names but does not scope.
