# 0126-0003 — live-path de-materialisation and the slot-taking hash-key evaluator

| field | value |
| --- | --- |
| status | draft |
| date | 2026-07-31 |
| task | M0126-0003 |
| milestone | `docs/milestones/0126-cost-driven-planning-production-viability.md` |
| design of record | `analysis/cost-driven-second-try-200731/` **09** Stage 0a-live + 0b, **02** §2/§4.1, findings F2/F11 — read them first; this doc does not restate them |
| depends on | `0126-0001` (the `Materialize()` clone), `0126-0002` (baseline) |

## 1. Scope

Remove the two per-row copies at the join seam that the bundle identifies as the
real cost MHJ avoids — **without any plan change and without fusion**:

- **0a-live**: `Slot.fillFromTupleSlot` (`internal/executor/opnode.go:129-150`)
  does `ts.Row()` (for a `*VirtualSlot`: `acquireRow` + zeroing + per-column
  48-byte copy, pooled row then dropped without `releaseRow`) followed by a
  second `copy(s.Cells, row)`. Add a `*VirtualSlot` fast path reading `v.Get(i)`
  directly into `s.Cells` (~5 lines, no lifetime reasoning). This is the live
  server path (`joinOpKernelNext`, `opnode.go:868-876`).
- **0b**: extract `evalHashKeyDatumSlot` alongside the existing slot-taking
  `evalExprSlot`/`joinPredicateMatchSlot`, and evaluate hash keys against a
  `VirtualSlot` over `{realSide, nullOtherSide}` — deleting the per-probe-row
  full-width `lazyKeyRow` memcpy. `evalHashKeyDatum`
  (`operators_join_agg.go:960-968`) takes a `Row` and cannot be reused as-is
  (F11). **The extraction is a hard prerequisite of -0006, not -0006 work.**

## 2. Files and symbols touched

| file | symbol | change |
|---|---|---|
| `internal/executor/opnode.go:129-150` | `Slot.fillFromTupleSlot` | `*VirtualSlot` fast path; no `acquireRow`, no second copy |
| `internal/executor/operators_join_agg.go:960-968` | `evalHashKeyDatum` | extract `evalHashKeyDatumSlot` (shared body, slot-taking) |
| `internal/executor/operators_join_agg.go:653-659, 1219-1232` | build loops + `nextLazy` | evaluate keys via the slot variant; delete the `lazyKeyRow` copies |

Sibling-path audit (Hard-won Rule #2): the build loops and the probe path are
the twins here — both must switch to the slot evaluator in the same commit, or
one path hashes different bytes than the other.

## 3. Commit split

1. 0a-live (`fillFromTupleSlot` fast path).
2. 0b (`evalHashKeyDatumSlot` + call-site switch).
Never folded with any cost-model change (bundle 07 §3: perf fix + cost retune in
one commit is unbisectable).

## 4. Gates

Per commit: UNITS, SMOKE, SPOT, **PLAN — ZERO diffs required** (executor-only by
construction), DS05.

## 5. Stop / decision conditions

Unconditional. **Any PLAN diff is a failure**: the change was not executor-only —
revert and re-scope. A DS05 row/checksum delta is an immediate stop (silent
row-count regression class).

## 6. Rollback

Plain commit revert; executor-only, no plan change, no snapshot movement
(bundle 10 §1 Stage 0 row). Preserve failing artefacts under `evidence/`.

## 7. What this doc deliberately does not decide

Whether the legacy `Build` path needs the same treatment (that is -0004's
trigger, decided by -0005's measurement), and how much of the 117 µs/row gap
this closes (that is -0005's job — the bundle's magnitude figure is arithmetic,
not measurement).
