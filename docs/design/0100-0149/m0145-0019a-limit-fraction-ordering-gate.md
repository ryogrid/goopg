# M0145-0019a — the LIMIT fraction may not hand a Sort a fast-start input

Status: **LANDED 2026-09-22.** Implements the fix M0145-0019's recon named.
Kind: impl
Parent: M0145-0019
Movement: none — TPC-DS SF0.25 `CATEGORIES-EXCL-MATCH parameterisation`
55 → 54 (one fewer divergence, inside the ±3 noise band); TPC-H categories
byte-identical. Both corpus floors held exactly.

## The defect (measured, M0145-0019)

`get_cheapest_fractional_path` answers "which path is cheapest if only a
fraction of the rows is fetched". goopg asked it at the **join search root**
(`searchCtx.finalPath`, `internal/optimizer/joinsearch.go`), before any Sort
existed. TPC-DS Q78 at SF1, firewall off, f = 100/10317:

| candidate | startup | total | fractional |
|---|---|---|---|
| `join.hash` | 16457.31 | 37659.80 | 16662.82 |
| `join.nestloop` | 5494.86 | 1147507.76 | **16564.09** |

The nested loop wins by 0.6% — and is then handed to a `Sort` that must read
every one of its rows. The 0.6% becomes 30×. Both paths were generated and
both accepted; nothing was mispriced.

## Upstream's rule, and why it cannot make this mistake

PostgreSQL resolves the fraction ONCE, on the FINAL upper rel:
`best_path = get_cheapest_fractional_path(final_rel, tuple_fraction)`
(`postgres/src/backend/optimizer/plan/planner.c:439`). By then the Sort is a
priced path whose startup cost contains its whole input's total, so a
fast-start input has nothing left to win with.

The input that gets sorted is not a free choice either:

- `create_ordered_paths` takes `input_rel->cheapest_total_path`
  (`planner.c:5314`);
- `make_ordered_path` (`planner.c:7646`+) returns NULL for any unsorted path
  that is not that one;
- an **already-sorted** path is added as is, needing no Sort, and therefore
  keeps competing at the fraction on its own merits (`planner.c:5337`+).

That asymmetry is the whole rule, and reproducing it is the fix.

## The change

`getCheapestFractionalPathOrdered(rel, tupleFraction, queryPathkeys)` replaces
the bare call at the one seam goopg has. When the statement requests an
ordering, the fraction may only displace the incumbent with a path that
**already satisfies** it; when nothing does, the answer is `CheapestTotal` —
exactly the path upstream would have sorted. With no ordering requested it is
`getCheapestFractionalPath` unchanged, so every `LIMIT`-without-`ORDER BY`
statement keeps the behaviour M0127-P5.7-b landed.

`queryPathkeys` was already carried on `searchCtx` (PG's `root->query_pathkeys`),
so the rule needed no new plumbing.

### Why here rather than at the upper rels

The literal reading of M0145-0019's deliverable was "move the call to the top
of the upper-rel lattice". That is not expressible today: `createOrderedPaths`
takes an already-lowered **Node**, not the join rel's pathlist, so there is no
pathlist at that point to choose among — which is what M0145-0007 (single
Path→Node lowering) exists to change. Reproducing upstream's *rule* at the
existing seam gets the same answer without pre-empting that task, and the seam
disappears with it.

## Verification

Q78, SF1, private `:5562` clone, knob arm, `GOOPG_DERIVED_FIREWALL=off`,
EXPLAIN-only — the exact reproduction M0145-0019 recorded:

| | before | after |
|---|---|---|
| top join | `Nested Loop Left Join (cost=5494.86..1147565.07)` | `Hash Left Join (cost=16457.31..37717.11)` |
| `Limit` | `1147784.10..1147799.43` | **`37936.15..37951.48`** |

The three equi-conditions demoted to a `Join Filter` are join conditions
again. That is M0145-0019a's stated expected movement, met.

Unit pins with a non-vacuity check: neutralising the ordering gate fails
exactly the two arms that test it and leaves the control green. The control is
load-bearing — `LIMIT` with no `ORDER BY` must still pick the fast-start path,
or the change would be "ignore the fraction whenever there is an ORDER BY",
which is a different and wrong change. A third arm pins that a path sorted the
*wrong way* does not win either, so the gate cannot be reading
`len(Pathkeys) > 0`.

## Gates

- units PASS; `tpch-spotcheck` Q12=2/Q13=33 PASS; `tpcds-sf025` sweep
  `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`,
  `verdict-changes=none runtime-moves=0 total-delta=+1.6%`; TPC-H acceptance
  arm 24 MATCH; pgbench smoke.
- **Floors held exactly**: TPC-H parallel `match=1` (Q6) — the floor
  M0144-0001 re-pinned, not AGENT.md's older `≥3` P0-E7 figure; TPC-DS SF0.25
  `match=2` (Q9, Q41).
- **Plans moved, as the task predicted.** TPC-DS SF0.25: 6 shapes changed
  (Q6, Q8, Q35, Q43, Q44, Q69), values identical. TPC-H: shapes byte-identical,
  costs slightly lower (Q1 148207.10 → 147979.67) — the same effect, expressed
  as a cheaper path at an unchanged shape.

Artefacts: `tmp/m0145-0019a-parity/` (both corpora, with and without the
change) and `tmp/m0145-0019a-q78-after-plan.txt`.
