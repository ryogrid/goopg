# M0146-0113 — SELECT DISTINCT over constant or pinned keys plans as LIMIT 1

Status: done 2026-10-08 (518c91ff3). Parent: M0146-0005.

## Problem

Regress select\_distinct has several "Ensure we get a plan with a Limit 1"
cases that differed:

```
SELECT DISTINCT four, 1, 2, 3 FROM tenk1 WHERE four = 0;
goopg:  HashAggregate  Group Key: four, 1, 2, 3  ->  …
PG:     Limit  ->  Seq Scan on tenk1  Filter: (four = 0)
```

## PG behaviour

- A distinct key whose equivalence class holds a constant is redundant
  (`pathkey_is_redundant`), and so is a constant target.
- When every key is redundant, `distinct_pathkeys` is empty.
- `create_final_distinct_paths` then plans the DISTINCT as a LIMIT 1
  over its input: every input row carries the same distinct key.

## Change

- **The test** (`distinctKeysAllPinned`, groupkeyconst.go). Every target
  must pass `parserPseudoConstant` or `orderItemPinnedByWhere`, the tests
  the ORDER BY and GROUP BY pruning already use. Grouped, windowed,
  set-operation and SRF queries keep their distinct step.
- **The plan.** The SELECT DISTINCT stage in planner.go builds `Limit{1}`
  over the input instead of a Distinct node when the test holds. A user
  LIMIT still stacks above it, as in PG.

## Verification

- **Probe** (PG 18.3 vs goopg, `max_parallel_workers_per_gather = 0`).
  Each of these is identical to PG:
  - a pinned key, alone and with another qual;
  - a pinned key plus constants;
  - ORDER BY the key;
  - a user LIMIT above it;
  - `DISTINCT 1`;
  - an OR pin and an aggregate target, which stay distinct steps;
  - result rows.
- **Test.** `TestExplainDistinctOnPinnedKeysIsLimitOne`. Disabling the
  rewrite fails three cases.
- **Regress A/B** (22 files). select\_distinct 117 → 100 diff lines.
- **TPC-DS / TPC-H.** No plan changes.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96 PASS) and ea-ratchet all PASS.

## Deferred (ledgered)

- **Partially pinned keys.** With a partially pinned key list PG drops the
  pinned keys: `DISTINCT four, two WHERE four = 0` groups on `two` only.
  goopg groups on both.
- **The parallel form.** PG plans `Limit -> Gather -> Limit -> Parallel Seq
  Scan`, with a partial LIMIT 1 in each worker. goopg's parallel version
  is a goopg-only distinct plan.
