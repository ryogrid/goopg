# M0144-0005 — `debug_plan_candidates` trace: per-candidate PG records

Instrument-2 capture. The 0004 scratch build gained a `debug_plan_candidates`
GUC emitting `PLANCAND` records for every path candidate offered to
`add_path`/`add_partial_path`, every precheck rejection, and every
`set_cheapest` winner. Design doc:
`docs/design/0100-0149/m0144-0005-debug-plan-candidates.md`.

## Record vocabulary

```
PLANCAND add|padd   rel=(b ...) cand={k=<type> rows=.. cost=s..t dis=n
                                     par=(b..)|- pk=n pw=n psafe=0|1
                                     [jt=<jt> out=(b..) in=(b..)]}
PLANCAND ok|pok     ... cand={...} removed=N          # N incumbents evicted
PLANCAND rej|prej   ... cand={...} by={dominator} via=<dim> cmp=cost:X keys:Y outer:Z
PLANCAND preskip|ppreskip rel=.. dis=n cost=s..t pk=n par=.. by={dominator}
PLANCAND win        rel=.. npath=N npart=N total={} startup={} param={}
```

`via=` names the deciding `add_path` dimension: `cost`, `keys`, `tie`,
`rows`, `outer`, `psafe` (partial: `dis`, `cost`, `keys`, `tie`). `by=` is
the first incumbent that dominated the candidate.

## Verification

- `SET debug_plan_candidates=on` emits; `off` (default) emits nothing —
  log line count identical across an off-mode EXPLAIN.
- Plan shapes unchanged with tracing on: Q7 still plans
  `Limit → GroupAggregate → Gather Merge` (the reference shape goopg
  diverges from with `Limit → Sort`); Q5 keeps `Parallel Append`.
- Zero compiler warnings on the patched objects; `--enable-cassert` build.

## Captures (12 queries, every 0002 census category n≥2)

Raw slices `tmp/pg18-optdebug/plancand/q*.log`; distilled per-rel tables
`analysis/m0144/optdebug-0005/q*-plancand.txt` via
`scripts/pg-plancand-distill.py`. Same query set as 0004 so survivor
dumps and candidate traces can be read side by side.

| query | candidates (add+padd) | rej+prej | preskip+ppreskip | win |
|---|---|---|---|---|
| q3   | 142  | 46   | 98   | 10 |
| q5   | 250  | 108  | 199  | 48 |
| q7   | 930  | 479  | 1107 | 24 |
| q10  | 348  | 192  | 116  | 36 |
| q12  | 132  | 45   | 118  | 11 |
| q18  | 3991 | 2190 | 5445 | 52 |
| q23a | 454  | 194  | 483  | 66 |
| q23b | 923  | 452  | 1248 | 87 |
| q34  | 219  | 95   | 182  | 20 |
| q40  | 1337 | 557  | 919  | 24 |
| q42  | 147  | 56   | 107  | 10 |
| q47  | 280  | 126  | 345  | 25 |
| q56  | 1067 | 571  | 1885 | 70 |

(`win` counts include upper-rel and per-subquery `set_cheapest` calls —
one per relation the planner finished, not one per query.)

## First reads

- **Precheck kills dominate.** `preskip`/`ppreskip` ≈ half of all
  rejections on every query (e.g. q7: 1107 preskip vs 479 post-build
  rejections; q18: 5445 vs 2190). Most "candidates" never become Path
  objects — `add_path_precheck`'s cheap dominance screen removes them on
  cost alone. Any goopg-side enumeration that builds and traces every
  candidate will show more `DPPATH` lines than PG `PLANCAND` for the same
  rel *without* that being a candidate-set gap.
- **`via=cost` decides nearly everything post-build.** Across all 12
  captures: cost 3243, preskip 9892, tie 811, keys 44 — and **zero**
  `via=rows`/`outer`/`psafe`/`dis`. The fine-grained dominance dimensions
  (row-count, outer-subset, parallel-safety) exist in the comparator but
  never cast the deciding vote on this corpus: if a goopg divergence
  persists after costs match, `keys`/`tie` are the only realistic
  suspects.
- **Parameterized paths are visibly distinguished.** `par=(b 2)`
  IndexScans survive beside unparameterized SeqScans in every base-rel
  `win` (e.g. q12 rel `(b 1)`: `param=[IndexScan rows=11
  cost=0.42..4.60204]`) — the NL-inner candidate set is explicit, exactly
  what Q7's `Gather Merge` route needs for the diff.
- **Join candidates carry full shape.** `jt=INNER out=(b 1) in=(b 2)` on
  every join `add`/`ok`/`rej` line — join-method and join-order
  divergences are adjudicable per candidate without reconstructing input
  relsets.

## Resume points

- Raw slices + distilled tables are the PG-side input for pairing against
  `GOOPG_PGSHAPED_DP_TRACE=1` DPPATH captures (needs a goopg server
  started with the env var — read at process start,
  `joinsearchtrace.go:49`).
- Scratch cluster persists on `:5560` (`tmp/pg18-optdebug/`); the patch is
  `git diff` in `tmp/pg18-optdebug/src` (not committed anywhere — it lives
  in the scratch checkout only).
- 0006's `-finstrument-functions` build should use a *separate* scratch
  configure (`--without`-instrument binary coexistence is simplest) — or
  rebuild this tree with the flag added.
