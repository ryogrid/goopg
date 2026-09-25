# M0144-0005 — Instrumented PG 18.3: `debug_plan_candidates` trace GUC

Status: landed 2026-09-20 (GUC + emitters + captures + distiller).
Analysis record: `analysis/m0144/m0144-0005-debug-plan-candidates.md`.

## Purpose

Instrument 2 of M0144's Phase A (0004 `OPTIMIZER_DEBUG` survivors →
**0005 per-candidate trace** → 0006 `-finstrument-functions` call graph).
0004 shows what PG *kept* per relation; this instrument shows what PG was
*offered* and *why each candidate lost* — the per-candidate artefact that
goopg's `GOOPG_PGSHAPED_DP_TRACE`/`DPPATH` output is diffed against to split
"join-order divergence" into candidate gap / costing gap / tie-break gap.

## Design decisions

1. **Same scratch tree, small source patch.** Built on the 0004 tree
   (`tmp/pg18-optdebug/src`, `62d6c7d3df6`, `-DOPTIMIZER_DEBUG` retained).
   The oracle `./postgres/` is untouched; the patch lives only in the
   git-ignored scratch checkout.
2. **`bool debug_plan_candidates` GUC**, `PGC_USERSET`/`DEVELOPER_OPTIONS`,
   default `false` — session-scoped, `SET` per diagnostic session.
3. **Emit to backend stdout** (`fprintf`+`fflush`), the same channel
   `OPTIMIZER_DEBUG`'s `pprint()` uses, so `pg_ctl -l server.log` slices
   capture both instruments at once. Not `elog` — keeps records out of the
   client-visible log stream and byte-interleaved with pprint dumps.
4. **One `PLANCAND` line per event.** Verbs: `add`/`padd` (candidate
   offered to `add_path`/`add_partial_path`), `ok`/`pok` (accepted,
   `removed=N` incumbents evicted), `rej`/`prej` (rejected — emitting the
   first dominating incumbent and the comparator vector that decided it),
   `preskip`/`ppreskip` (precheck rejection before a Path exists),
   `win` (`set_cheapest` survivors: cheapest total/startup/parameterized +
   pathlist/partial-pathlist counts).
5. **Path descriptor** `{k=<pathtype> rows=.. cost=s..t dis=n par=(b..)|-
   pk=n pw=n psafe=0|1 [jt=n out=(b..) in=(b..)]}` — `par` is
   `PATH_REQ_OUTER`; `pk` is the *effective* pathkey count (NIL for
   parameterized paths, matching `add_path` policy); join paths carry
   `jointype` + outer/inner input relsets.
6. **Dominator tracking, not just verdicts.** In `add_path` the six
   `accept_new = false` sites each tag the deciding dimension
   (`keys`/`psafe`/`rows`/`tie`/`outer`/`cost`) via a `PC_DOM` macro that
   snapshots the incumbent + cost/keys/outer comparator results at the
   moment of rejection; `add_partial_path` does the same for its four
   sites (`dis`/`cost`/`keys`/`tie`) with a keys-only comparator.
7. **Emit before free.** Verdict lines print *before* the rejected path's
   `pfree` — the first draft emitted after the recycle (use-after-free).
8. **Non-invasive.** All emission is `if (debug_plan_candidates)`-gated;
   comparator state is read, never written. Verified: off-mode emits zero
   lines; on-mode Q7 keeps `Limit→GroupAggregate→Gather Merge` and Q5
   keeps `Parallel Append` — identical to the 0004/reference shapes.

## Files touched (scratch tree only)

- `src/backend/optimizer/util/pathnode.c` — GUC var, comparator→string
  helpers, descriptor/emitters, hooks in `set_cheapest`, `add_path`,
  `add_path_precheck`, `add_partial_path`, `add_partial_path_precheck`.
- `src/include/optimizer/pathnode.h` — `extern bool debug_plan_candidates`.
- `src/backend/utils/misc/guc_tables.c` — GUC registration + include.

## Diffing protocol (downstream use)

Per rel, pair a `PLANCAND` block with goopg's `DPPATH` block:

- **candidate gap** — a kind/parameterization present in PG `add` lines but
  absent from goopg's offered set (or vice versa).
- **costing gap** — same candidate offered by both, different
  `cost=`/`rows=`.
- **tie-break gap** — same candidate + same cost fields, different verdict
  (`via=`/`cmp=` records exactly which comparator dimension decided).
- `preskip`/`ppreskip` marks candidates PG killed at precheck — if goopg
  builds and carries them, that's an ordering/admission divergence, not a
  cost one.

Distiller: `scripts/pg-plancand-distill.py` (raw PLANCAND slice → per-rel
candidate tables + reject-via histograms). Captures:
`analysis/m0144/optdebug-0005/`, raw slices `tmp/pg18-optdebug/plancand/`.
