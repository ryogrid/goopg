# IMPLEMENTATION-TODO — parallel-query, phases P0–P5

| field | value |
| --- | --- |
| status | in progress |
| started | 2026-07-21 |
| branch | `parallel-query` |
| start HEAD | `592f166a` (bundle commit) |
| scope | phases **P0–P5** of [10-roadmap.md](10-roadmap.md) — every phase that is scaffolding with **zero user-visible behaviour change**. P6 (enable Gather insertion) is reported and approved separately |
| tracking rule | a stage is DONE only when every gate line below it carries a measured result and a commit hash |

One commit + push per stage. Standing gates on every stage: units
(`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`),
`make race-gate`, `scripts/tpch-spotcheck.sh` (Q12=2 / Q13=33),
`make plan-gate`, and the pre-commit pgbench smoke — never `--no-verify`.

**Sequencing rule:** run the capped bench server and the pgbench smoke
*sequentially*, never concurrently. A concurrent bench server has been measured
degrading the smoke gate from 700 to 390 TPS and aborting a transaction.

## Stage table

| # | Stage | Scope | Status |
|---|---|---|---|
| P0 | GUC fidelity fixes | `UnitBlocks`, `min_parallel_*` units, `max_parallel_workers`, enum bool synonyms, cost ceilings | [x] (this commit) |
| P1 | Session GUC plumbing | `session*` readers + typed context fields, both protocol paths | [ ] |
| P2 | `HashAggregate` label correction | rename before `Partial `/`Finalize ` prefixes cement the misnomer | [ ] |
| P3 | Concurrency substrate | worker contexts, per-worker arenas, `Perm()` mutex, error/panic/cancel, per-worker instrumenters | [ ] |
| P4 | Parallel Seq Scan + Gather | shared block allocator, Gather operator; insertion still OFF | [ ] |
| P5 | Partial / Finalize aggregation | `AggMode`, combine rules, whitelist refusals | [ ] |

## Design amendments to fold in during these stages

Two survey findings postdate the bundle and must land in the docs alongside the
code:

- **Plan cache defeats per-session parallel planning.** `plancache.go` is
  process-wide and cross-session, keyed on `namespace-oid + normalized SQL`
  only (`planCacheKey`, `dispatch.go:1598`). A plan built under
  `max_parallel_workers_per_gather = 4` would be reused by a session that set
  it to `0`, so `SET … = 0` would silently fail to disable parallelism.
  **Resolution (user-confirmed): the Gather post-pass runs AFTER the cache
  lookup, per statement** — the cache stores serial plans only. This requires
  the post-pass to be **non-mutating**: it returns a new root wrapping shared
  children, never edits a cached node in place, or it is a data race under
  `race-gate`. Lands with P6; recorded here so it is not rediscovered.
- **The extended protocol plans before the executor context exists.**
  `dispatch_extended.go` calls `planner.Plan` at `:92`/`:103`; `ectx` is built
  at `:141`. Planning-time GUC reads therefore go through
  `sess *config.SessionRegistry`, not the context. Lands with P1.

## P0 — GUC fidelity fixes  [x]

Three of these are observable through `SHOW` today, independently of whether
parallel query is ever implemented.

- [x] **`UnitBlocks` added** (`internal/config/guc.go`), mirroring upstream's
      `GUC_UNIT_BLOCKS` with `blockSize = 8192`. Surface touched: the `Unit`
      const block, `memoryDisplayUnits`, and `bytesFamily` inside
      `convertUnit`. Deliberately **not** `unitFromSuffix` — upstream has no
      `block` input suffix either.
- [x] **The negative-multiplier branch in `FormatDisplayValue`.** PG's blocks
      row for `kB` is `-(BLCKSZ/1024)` = −8: a block is *larger* than the
      display unit, so `convert_int_from_base_unit` multiplies rather than
      divides. goopg's loop only ever divided and gated on
      `n%multiplier == 0`, so 64 blocks matched no row and would have printed
      a bare `64` instead of `512kB`. This was the one structural change; the
      rest of the unit work is table data.
- [x] **`min_parallel_table_scan_size`** → `UnitBlocks`, BootVal `8MB`
      (stored 1024 blocks). Was `UnitKB`/`8388608`, i.e. `SHOW` answered
      **8GB** where PG answers **8MB** — wrong by 1024× in both value and
      unit, because the boot value was a byte count mislabelled as kB.
- [x] **`min_parallel_index_scan_size`** → `UnitBlocks`, BootVal `512kB`
      (stored 64 blocks). Was `512MB` by the same error. This is the case that
      exercises the negative multiplier.
- [x] `MinVal`/`MaxVal` verified rather than assumed: PG uses `0 .. INT_MAX/3`
      for both, and `715827882` is correct in blocks as well — the old kB
      registration happened to carry the same number.
- [x] **`max_parallel_workers` registered** (int, boot **8** — PG's default,
      not the 2 used by `max_parallel_workers_per_gather`, which is a
      different knob). It was absent entirely.
- [x] **`debug_parallel_query` accepts PG's hidden boolean synonyms.**
      Upstream lists `true/false/yes/no/1/0` as `config_enum_entry` rows with
      `hidden = true` (`guc_tables.c:395-405`), so `SET debug_parallel_query =
      true` works there and failed here. No existing pattern to copy — the
      `TypeEnum` arm never consulted `parseBoolish`. Implemented as a fallback
      gated on the enum offering **both** `on` and `off` (`enumHasBoolPair`),
      so enums like `IntervalStyle` are unaffected, and the synonyms are NOT
      added to `EnumOptions` so `pg_settings.enumvals` and the error HINT stay
      PG-shaped. One deliberate superset, documented in code: `parseBoolish`
      also takes `t`/`f`, which upstream's hidden list omits for this GUC —
      accepting a strict superset rejects no valid PG input and avoids a
      second boolean parser.
      **This one is load-bearing**: `debug_parallel_query` is the lever the
      P4/P5 correctness gates are built on.
- [x] `parallel_setup_cost` / `parallel_tuple_cost` `MaxVal` → `DBL_MAX`
      (`math.MaxFloat64`); was `1e15`.
- [x] **`postgresql.conf.sample` updated in the same commit** —
      `TestSampleConfigCoversRegistry` (`sample_test.go:56`) asserts
      bidirectionally that every registered GUC has a sample entry and that
      each entry's literal equals the raw `BootVal`. Added
      `max_parallel_workers`, changed the two `min_parallel_*` lines and their
      unit hint comments.

Coverage: `internal/config/parallel_gucs_test.go` — display/parse round-trips
for both blocks GUCs (including the 512kB negative-multiplier case), the
`max_parallel_workers` registration and range, the full synonym table for
`debug_parallel_query` plus the assertion that synonyms do **not** leak into
`EnumOptions`, the guard that a non-on/off enum still rejects booleans, and
the cost ceilings.

- gates: units PASS; race-gate PASS (`internal/config`); spotcheck PASS
  (Q12=2 / Q13=33); **plan-gate 22/22 MATCH** — zero diffs as predicted, no
  planner code touched
- commit: _(this commit)_
