# EX0-03c Design — Minimal sortOp method/space counters + per-worker lines

Item: `TODO_EXECUTOR.md` EX0-03c (gate: golden test, pin `changed=0`,
no timing claim). Status: design for review. Smallest of the three
splits: no analogue exists today (no `SortStats` type, no `Sort Method:`
line), so this adds the counters, the fold, the render, and the test in
one commit — one variable (sort observability), EX-P3-safe.

## 1. Counters (source: existing `Open` accounting)

- `sortOp` gains its plan node (`newSortOp` takes `*optimizer.Sort` but
  keeps only `Keys` today) + one field: `peakBytes` (max of the
  `chunkBytes` accumulator already maintained in `Open`, sampled at each
  `flushChunk` and at the tail). `peakBytes` MUST reset to 0 at `Open`
  start (the Stage-9 Close+Open rescan contract; `Open` currently resets
  no sortOp state — follow the `distinctOp`/`limitOp` reset-at-Open
  precedent, or the accumulator carries a stale max across rescans).
  Spilled-ness is `len(spillFiles) > 0` sampled at end of `Open` (the
  only valid point — `Close` clears it).
- At end of successful `Open`, publish one `SortStat{Method, SpaceKB,
  SpaceType}` into a lazily-allocated ctx map keyed by plan node:
  Method = `quicksort` / `external merge`; SpaceType = `Memory` /
  `Disk`; SpaceKB = peak mem KiB, or the on-disk sum (best-effort
  `os.Stat` over `spillFiles`, errors → 0, never fail the query) when
  spilled. PG text form verbatim: `Sort Method: %s  %s: %dkB`
  (`postgres/.../explain.c:3105`).

## 2. Fold (worker order preserved, leader separate)

- Worker ctx maps stay private (same reason as hash stats pre-EX0-03).
  `MergeWorkerContext` appends each worker entry to a NEW leader carrier
  `map[*optimizer.Sort][]SortStat` with an EXPLICIT worker index (same
  rule as EX0-03b's `foldGatherWorkerStats` — never bare call order;
  render-side sorts by index). The leader's own `SortStats` single-map
  entry is NEVER merged into the list: it renders the main line (PG's
  leader line), workers render the per-worker lines.
- Leader non-participation: with participation off and n>0 the leader
  never Opens its Sort, so no main-line entry exists. Follow PG
  (`explain.c:3116-3124`): promote worker 0's stats to the main line in
  exactly this case. Failed Opens publish nothing (nil-map read renders
  no lines); prebuild throwaway trees run under nil scope per EX0-03b
  (a Sort on a hash build side would otherwise publish a phantom entry —
  harmless since the real Open overwrites it, but the rule stays nil).
- Method vocabulary is the PG subset goopg can produce (`quicksort`,
  `external merge`; `Memory`/`Disk`) — `top-N heapsort` et al never
  occur here.
- No concurrency: fold runs post-`Wait`, same edge as EX0-03/03b.

## 3. Render (ANALYZE-only, text only)

- Sort node in the ANALYZE walk: main line from `ctx.SortStats[node]`
  when present, else worker-0 promotion when the carrier is non-empty
  (§2); then one `Worker i:  Sort Method: …` line per carrier entry
  (flat `Worker N:` prefix matches EX0-03b's lines; PG nests them in
  worker blocks — noted divergence). Sort is blocking (`Open` fully
  drains the child), so LIMIT above cannot skip its execution — only
  never-executed subtrees (plain EXPLAIN, unexecuted InitPlans) miss
  entries and render no lines. No entry → no lines, never a nil-map
  crash (publish lazy-allocates).
- JSON twin stays out (same escape clause as EX0-03/03b).

## 4. Verification (gate)

- Golden: serial sort ANALYZE → main line, no worker lines; forced
  parallel sort (small fixture; sorts are per-worker by construction —
  the only prebuilds are hash and bitmap, so no prebuild to defeat) →
  main + N worker lines, count == launched; rerun tolerates
  distribution change (no exact per-worker assertions).
- Unit: `peakBytes` rescan reset (Open→Open accumulates nothing stale);
  worker-0 promotion (leader absent + workers present → main line ==
  worker 0); empty-carrier + non-ANALYZE byte-identical pin extended to
  the Sort arm.
- Suite green incl. `-race` on the worker tests (EX0-03b rule); serial
  goldens byte-identical; `changed=0` by the same construction argument
  as EX0-03b (ANALYZE-only path, empty carrier → unreachable) with the
  empty-carrier unit test as its checkable form. No timing claim.
