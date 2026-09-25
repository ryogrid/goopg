# M0144-0002 — First-divergence census

Status: done — instrument landed (`scripts/pg-plan-first-divergence.py`),
ranked tables committed under `analysis/m0144/` (see
`m0144-0002-first-divergence-census.md` there).

## Why

`pg-plan-parity-diff.py`'s nine headline categories tag each divergent query
4–7 ways, non-exclusively — they answer "how many queries touch this class"
but not "what is the *first* wrong node". 03-forward-plan §1 (owner-adopted
as M0144-0002) prescribes the fix: align the two normalised trees
node-by-node in PG's plan-order and record one mutually-exclusive record per
divergent query — `(parent kind, PG child kind, goopg child kind, depth)` —
so the table ranks *decision points*, the thing a fix actually targets.

## Instrument

`scripts/pg-plan-first-divergence.py` imports the sibling differ wholesale
(`importlib` on the hyphenated filename — parser, N1–N7 `normalise_tree`,
`reassign_aux`, `collect_tables`, `leaves`, `scan_key`, `children_match`,
`qual_signature`, `key_category`/`cond_category`/`mismatch_category`/
`extra_category`/`presence_category` vocabulary) and adds a stop-at-first
aligner:

- Children pair positionally; a 2-child join whose straight pairing fails
  `children_match` but whose swapped pairing matches is a
  `swapped-children` (join-order) record at the join — not descended, since
  pairing below a reorder is positional luck.
- Same-kind divergence checks run in `cmp_same`'s order: `Parallel` flag /
  `Workers Planned`, `scan_key`, parameterised-inner, join type, join
  leaf-set (SubPlan involvement flips it to parameterisation), cond-prop
  signatures, `Sort Key`/`Group Key` text. KEY_PROPS diffs are the
  verdict-neutral `rendering` category in the differ, so they do not count
  as census divergences — a rendering-only query is a census MATCH.
- Extra children and one-sided aux (SubPlan/InitPlan, paired by name after
  `reassign_aux`) are presence records; error/timeout/unparseable/
  missing-section queries get verdict-class records.
- Two deliberate attribution refinements over the sibling's fallbacks (the
  census's job is decision points, so these matter): `Partial`/`Finalize`
  phased-agg kinds are mapped back to `AGG_KINDS` before category lookup,
  so `HashAggregate` vs `Finalize HashAggregate` reads
  aggregation-strategy, not the sibling's join-order fallback; the same
  base-kind map is applied to `extra_category`.
- Both section-header spellings (`=== Qn`, `===== Qn =====`) parse, so
  sweep `plans-*.txt` files and `estimate-audit` captures are both valid
  inputs.

## Results (committed captures, 2026-09-20)

| corpus | divergent | census MATCH | agrees with differ floor? |
|---|---|---|---|
| TPC-H parallel (`analysis/m0144/m0144-0001-*-parallel.*`) | 21/22 | Q6 | yes — same 1/22 as the parity run |
| TPC-DS SF0.25 (latest sweep + `m0142-0012verify` PG ref) | 97/99 | Q9, Q41 | yes — exactly the committed floor pair |
| TPC-DS SF1 (P0-E7's pair) | 98/99 | Q41 | — |

Top first-divergence clusters: `Limit → PG GroupAggregate vs goopg Sort`
dominates TPC-DS (14 at SF0.25, 6 at SF1; the wider ordered-agg-under-LIMIT
family is ~25/~20 records); TPC-H leads with aggregation-strategy at the
root/Sort boundary (7). Full ranked tables and per-query records:
`analysis/m0144/m0144-0002-*`.

## Forward use

M0144-0003 cites these clusters when checking route order; M0144-0007 takes
each cluster head and forces PG's shape to read the cost margin; M0144-0011
picks the vertical-slice target from this table.
